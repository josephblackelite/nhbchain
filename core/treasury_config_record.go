package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"nhbchain/crypto"
)

// The genesis hash (and so the chain id) does not cover the admin wallet or
// the buyback signer set and threshold, yet every start re-reads them from the
// genesis file, and NHB_MASTER_TREASURY can replace the admin wallet. All of
// them change what blocks do, so a node whose configuration differs from the
// one it first ran with computes different state from the network. This file
// keeps a small record of that configuration in the node's own database and
// checks it on every start.
//
// The record lives under its own key in the same store as the chain data, not
// in the state trie: it is never hashed into a state root or a block hash, and
// it changes nothing about how a block executes. A binary that does not know
// the key ignores it.

// treasuryConfigKey is the database key of the record. It is not a block hash,
// a trie node or any other prefix used in this package.
var treasuryConfigKey = []byte("treasuryconfig:v1")

// treasuryConfigRerecordEnv, when set to a true value, makes the node replace
// the stored record with the configuration it is starting with instead of
// refusing to start on a difference. It is for a change that was meant to be
// made on every validator; leave it unset otherwise.
const treasuryConfigRerecordEnv = "NHB_RERECORD_TREASURY_CONFIG"

// treasuryConfigRecordVersion is the first byte of the stored record.
const treasuryConfigRecordVersion = 1

// ErrTreasuryConfigMismatch is returned (wrapped) by NewNode when the treasury
// configuration it would run with is not the one recorded for the database.
var ErrTreasuryConfigMismatch = errors.New("treasury configuration differs from the one recorded for this database")

// treasuryConfig is the part of a node's startup configuration that the
// genesis hash does not commit and that blocks depend on: the admin/treasury
// wallet as the node will actually use it (genesis file or the environment
// override) and the buyback signer quorum.
type treasuryConfig struct {
	hasAdmin   bool
	admin      [20]byte
	hasSigners bool
	threshold  uint32
	signers    [][20]byte // sorted, used as a set
}

func newTreasuryConfig(admin [20]byte, hasAdmin bool, signers [][20]byte, threshold uint32, hasSigners bool) treasuryConfig {
	var cfg treasuryConfig
	if hasAdmin {
		cfg.hasAdmin = true
		cfg.admin = admin
	}
	if hasSigners {
		cfg.hasSigners = true
		cfg.threshold = threshold
		cfg.signers = append([][20]byte(nil), signers...)
		sort.Slice(cfg.signers, func(i, j int) bool {
			return bytes.Compare(cfg.signers[i][:], cfg.signers[j][:]) < 0
		})
	}
	return cfg
}

// encode returns the canonical bytes of the record: version, flags, admin,
// threshold, signer count, then the sorted signers.
func (c treasuryConfig) encode() []byte {
	var flags byte
	if c.hasAdmin {
		flags |= 1
	}
	if c.hasSigners {
		flags |= 2
	}
	buf := make([]byte, 0, 2+20+4+4+20*len(c.signers))
	buf = append(buf, treasuryConfigRecordVersion, flags)
	buf = append(buf, c.admin[:]...)
	buf = binary.BigEndian.AppendUint32(buf, c.threshold)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(c.signers)))
	for _, signer := range c.signers {
		buf = append(buf, signer[:]...)
	}
	return buf
}

// digest identifies the configuration; operators can compare it across
// validators.
func (c treasuryConfig) digest() string {
	sum := sha256.Sum256(c.encode())
	return hex.EncodeToString(sum[:])
}

func decodeTreasuryConfig(raw []byte) (treasuryConfig, error) {
	const fixed = 2 + 20 + 4 + 4
	if len(raw) < fixed {
		return treasuryConfig{}, fmt.Errorf("treasury configuration record is %d bytes, too short", len(raw))
	}
	if raw[0] != treasuryConfigRecordVersion {
		return treasuryConfig{}, fmt.Errorf("treasury configuration record has version %d, this build reads version %d", raw[0], treasuryConfigRecordVersion)
	}
	flags := raw[1]
	if flags&^3 != 0 {
		return treasuryConfig{}, fmt.Errorf("treasury configuration record has unknown flags %#x", flags)
	}
	var admin [20]byte
	copy(admin[:], raw[2:22])
	threshold := binary.BigEndian.Uint32(raw[22:26])
	count := binary.BigEndian.Uint32(raw[26:30])
	body := raw[fixed:]
	if uint64(len(body)) != uint64(count)*20 {
		return treasuryConfig{}, fmt.Errorf("treasury configuration record names %d signers but holds %d bytes of them", count, len(body))
	}
	signers := make([][20]byte, count)
	for i := range signers {
		copy(signers[i][:], body[i*20:(i+1)*20])
	}
	cfg := newTreasuryConfig(admin, flags&1 != 0, signers, threshold, flags&2 != 0)
	// Only a record this code wrote (sorted, unused fields zero) is accepted.
	if !bytes.Equal(cfg.encode(), raw) {
		return treasuryConfig{}, errors.New("treasury configuration record is not in its canonical form")
	}
	return cfg, nil
}

func treasuryAddressString(addr [20]byte) string {
	if a, err := crypto.NewAddress(crypto.NHBPrefix, addr[:]); err == nil {
		return a.String()
	}
	return hex.EncodeToString(addr[:])
}

// summary renders the configuration for a log line.
func (c treasuryConfig) summary() string {
	admin := "admin wallet unset"
	if c.hasAdmin {
		admin = "admin wallet " + treasuryAddressString(c.admin)
	}
	if !c.hasSigners {
		return admin + ", no buyback signers"
	}
	return fmt.Sprintf("%s, %d buyback signers with threshold %d", admin, len(c.signers), c.threshold)
}

// differences names every part of other that is not the same as c, with
// c as the recorded side and other as the current one.
func (c treasuryConfig) differences(other treasuryConfig) []string {
	var out []string
	switch {
	case c.hasAdmin && !other.hasAdmin:
		out = append(out, fmt.Sprintf("admin wallet was %s and is now unset", treasuryAddressString(c.admin)))
	case !c.hasAdmin && other.hasAdmin:
		out = append(out, fmt.Sprintf("admin wallet was unset and is now %s", treasuryAddressString(other.admin)))
	case c.hasAdmin && c.admin != other.admin:
		out = append(out, fmt.Sprintf("admin wallet was %s and is now %s", treasuryAddressString(c.admin), treasuryAddressString(other.admin)))
	}
	switch {
	case c.hasSigners && !other.hasSigners:
		out = append(out, fmt.Sprintf("buyback signers were configured (%d, threshold %d) and are now unset", len(c.signers), c.threshold))
	case !c.hasSigners && other.hasSigners:
		out = append(out, fmt.Sprintf("buyback signers were unset and are now configured (%d, threshold %d)", len(other.signers), other.threshold))
	case c.hasSigners:
		if !sameSigners(c.signers, other.signers) {
			out = append(out, fmt.Sprintf("buyback signer set differs (%d recorded, %d now)", len(c.signers), len(other.signers)))
		}
		if c.threshold != other.threshold {
			out = append(out, fmt.Sprintf("buyback signer threshold was %d and is now %d", c.threshold, other.threshold))
		}
	}
	return out
}

func sameSigners(a, b [][20]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// treasuryConfigRerecordRequested reports whether the operator asked, through
// the environment, to replace the stored record.
func treasuryConfigRerecordRequested(getenv func(string) string) bool {
	v := strings.TrimSpace(getenv(treasuryConfigRerecordEnv))
	if v == "" {
		return false
	}
	on, err := strconv.ParseBool(v)
	return err == nil && on
}

// verifyOrRecordTreasuryConfig compares cfg with the record in the database.
//
//   - No record (every database made before this check existed, one made by a
//     tool that does not start a node, or a snapshot from such a node): cfg is
//     recorded and the start goes on.
//   - A record equal to cfg: the start goes on.
//   - A record that differs, or cannot be read: the start is refused with an
//     error naming what differs, unless replace is set, in which case the
//     record is replaced with cfg and the change is logged.
//
// A read error on the key is treated as "no record", as every other lookup in
// this package does; it can only make the check weaker than intended, never
// stop a node that has nothing to compare.
func (bc *Blockchain) verifyOrRecordTreasuryConfig(cfg treasuryConfig, replace bool) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	store := func(action string) error {
		if err := bc.db.Put(treasuryConfigKey, cfg.encode()); err != nil {
			return fmt.Errorf("store treasury configuration record: %w", err)
		}
		fmt.Printf("Treasury configuration %s for this database: %s digest=%s\n", action, cfg.summary(), cfg.digest())
		return nil
	}

	raw, getErr := bc.db.Get(treasuryConfigKey)
	if getErr != nil {
		return store("recorded")
	}
	recorded, decodeErr := decodeTreasuryConfig(raw)
	if decodeErr != nil {
		if replace {
			fmt.Printf("Warning: %s is set and the stored treasury configuration record is unreadable (%v); replacing it.\n", treasuryConfigRerecordEnv, decodeErr)
			return store("replaced")
		}
		return fmt.Errorf("%w: the stored record cannot be read: %v; if the database and its configuration are known to be right, start once with %s=1 to write a new record", ErrTreasuryConfigMismatch, decodeErr, treasuryConfigRerecordEnv)
	}
	diffs := recorded.differences(cfg)
	if len(diffs) == 0 {
		fmt.Printf("Treasury configuration matches the record: %s digest=%s\n", cfg.summary(), cfg.digest())
		return nil
	}
	if replace {
		fmt.Printf("Warning: %s is set; replacing the stored treasury configuration (%s; digest=%s) because %s.\n", treasuryConfigRerecordEnv, recorded.summary(), recorded.digest(), strings.Join(diffs, "; "))
		return store("replaced")
	}
	return fmt.Errorf("%w: %s. Check the genesis file this node is given and NHB_MASTER_TREASURY in its environment. If the change is intended and is being made on every validator, start once with %s=1 to record the new values and then remove it", ErrTreasuryConfigMismatch, strings.Join(diffs, "; "), treasuryConfigRerecordEnv)
}
