package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	manifestVersion = 1
	toolVersion     = "1"
	archiveFormat   = "tar+gzip"

	// maxManifestBytes bounds how much of a manifest file is read.
	maxManifestBytes = 4 << 20

	// A manifest may not describe a snapshot larger than these. The live
	// chain's is a few hundred megabytes; the bounds leave room for years of
	// growth and stop a manifest (which nobody signs) from asking a consumer
	// for an absurd download or unpack. A consumer applies its own, lower,
	// limit as well (deployvalidator.sh does).
	maxArchiveBytes  int64 = 64 << 30
	maxUnpackedBytes       = defaultMaxBytes
	// maxExpansion is how many times larger than the archive a snapshot may
	// unpack. A chain database gzips to between half and all of its size (the
	// chains built for the tests and the local network's snapshot unpack to 1.2
	// to 2.2 times their archive), so an archive announcing more than this is a
	// decompression bomb.
	maxExpansion int64 = 64

	// maxTipFutureSkew is how far ahead of this host's clock a snapshot's
	// newest block may be dated. A block cannot be in a snapshot before it
	// exists, so more than clock error means the snapshot was made up.
	maxTipFutureSkew = 5 * time.Minute
)

// A snapshot holds only the files of the chain database. Nothing else that
// lives in a node's data directory (p2p identity and peer list, consensus
// lock and sign state, keystores, logs, lock files) is ever named here, packed
// or extracted.
var (
	fileNamePattern = regexp.MustCompile(`^(CURRENT|MANIFEST-[0-9]{6,}|[0-9]{6,}\.(log|ldb|sst))$`)
	// housekeepingFiles are LevelDB bookkeeping files that are skipped, not
	// packed: the lock file every open creates and the human-readable logs.
	housekeepingFiles = map[string]bool{"LOCK": true, "LOG": true, "LOG.old": true}

	archiveNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.tar\.gz$`)
	hex32Pattern       = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	decimalPattern     = regexp.MustCompile(`^[0-9]{1,20}$`)
	// shortText bounds free-text producer fields so they stay one printable line.
	shortTextPattern = regexp.MustCompile(`^[A-Za-z0-9._:+/@ -]{0,200}$`)
)

func allowedFileName(name string) bool { return fileNamePattern.MatchString(name) }

type producerInfo struct {
	BinaryVersion string `json:"binaryVersion"`
	BinaryCommit  string `json:"binaryCommit"`
	BinarySha256  string `json:"binarySha256"`
	SnapshotTool  string `json:"snapshotTool"`
}

type fileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

type archiveInfo struct {
	Name             string      `json:"name"`
	Format           string      `json:"format"`
	Size             int64       `json:"size"`
	Sha256           string      `json:"sha256"`
	UncompressedSize int64       `json:"uncompressedSize"`
	Files            []fileEntry `json:"files"`
}

// manifest describes one snapshot archive: which chain, which block, and
// exactly which bytes.
type manifest struct {
	ManifestVersion int          `json:"manifestVersion"`
	ChainID         string       `json:"chainId"`
	GenesisHash     string       `json:"genesisHash"`
	Height          uint64       `json:"height"`
	TipHash         string       `json:"tipHash"`
	StateRoot       string       `json:"stateRoot"`
	TipTimestamp    int64        `json:"tipTimestamp"`
	CreatedAt       string       `json:"createdAt"`
	Producer        producerInfo `json:"producer"`
	Archive         archiveInfo  `json:"archive"`
}

func hex0x(b []byte) string { return "0x" + hex.EncodeToString(b) }

func parseHex32(s string) ([]byte, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "0x") {
		s = "0x" + s
	}
	if !hex32Pattern.MatchString(s) {
		return nil, fmt.Errorf("%q is not a 32-byte hex value", s)
	}
	return hex.DecodeString(s[2:])
}

func parseSha256(s string) ([]byte, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return nil, fmt.Errorf("%q is not a sha256 (64 hex digits)", s)
	}
	return hex.DecodeString(s)
}

// validate checks the manifest is well formed. It says nothing about whether
// its claims are true; verifyManifestAgainst and the archive checks do that.
func (m *manifest) validate() error {
	if m.ManifestVersion != manifestVersion {
		return fmt.Errorf("manifest version %d is not supported (this tool reads version %d)", m.ManifestVersion, manifestVersion)
	}
	if !decimalPattern.MatchString(m.ChainID) {
		return fmt.Errorf("manifest chainId %q is not a decimal number", m.ChainID)
	}
	if _, err := strconv.ParseUint(m.ChainID, 10, 64); err != nil {
		return fmt.Errorf("manifest chainId %q: %w", m.ChainID, err)
	}
	for name, value := range map[string]string{"genesisHash": m.GenesisHash, "tipHash": m.TipHash, "stateRoot": m.StateRoot} {
		if !hex32Pattern.MatchString(value) {
			return fmt.Errorf("manifest %s %q is not a lower-case 0x-prefixed 32-byte hex value", name, value)
		}
	}
	genesis, _ := hex.DecodeString(m.GenesisHash[2:])
	derived := uint64(0)
	for _, b := range genesis[:8] {
		derived = derived<<8 | uint64(b)
	}
	if strconv.FormatUint(derived, 10) != m.ChainID {
		return fmt.Errorf("manifest chainId %s is not the first 8 bytes of genesisHash %s (%d)", m.ChainID, m.GenesisHash, derived)
	}
	if _, err := time.Parse(time.RFC3339, m.CreatedAt); err != nil {
		return fmt.Errorf("manifest createdAt %q: %w", m.CreatedAt, err)
	}
	if m.TipTimestamp < 0 {
		return fmt.Errorf("manifest tipTimestamp %d is negative", m.TipTimestamp)
	}
	for name, value := range map[string]string{"binaryVersion": m.Producer.BinaryVersion, "binaryCommit": m.Producer.BinaryCommit, "snapshotTool": m.Producer.SnapshotTool} {
		if !shortTextPattern.MatchString(value) {
			return fmt.Errorf("manifest producer.%s %q holds characters that are not allowed", name, value)
		}
	}
	if m.Producer.BinarySha256 != "" {
		if _, err := parseSha256(m.Producer.BinarySha256); err != nil {
			return fmt.Errorf("manifest producer.binarySha256: %w", err)
		}
	}
	a := m.Archive
	if !archiveNamePattern.MatchString(a.Name) {
		return fmt.Errorf("manifest archive name %q is not a plain .tar.gz file name", a.Name)
	}
	if a.Format != archiveFormat {
		return fmt.Errorf("manifest archive format %q is not %q", a.Format, archiveFormat)
	}
	if a.Size <= 0 {
		return fmt.Errorf("manifest archive size %d is not positive", a.Size)
	}
	if a.Size > maxArchiveBytes {
		return fmt.Errorf("manifest archive size %d is above the %d bytes a snapshot may have", a.Size, maxArchiveBytes)
	}
	if _, err := parseSha256(a.Sha256); err != nil {
		return fmt.Errorf("manifest archive sha256: %w", err)
	}
	if len(a.Files) == 0 {
		return fmt.Errorf("manifest lists no files")
	}
	var total int64
	seen := make(map[string]bool, len(a.Files))
	names := make([]string, 0, len(a.Files))
	haveCurrent, haveManifest := false, false
	for _, f := range a.Files {
		if !allowedFileName(f.Name) {
			return fmt.Errorf("manifest lists %q, which is not a chain database file", f.Name)
		}
		if seen[f.Name] {
			return fmt.Errorf("manifest lists %q twice", f.Name)
		}
		seen[f.Name] = true
		names = append(names, f.Name)
		if f.Size < 0 {
			return fmt.Errorf("manifest file %s has a negative size", f.Name)
		}
		if _, err := parseSha256(f.Sha256); err != nil {
			return fmt.Errorf("manifest file %s: %w", f.Name, err)
		}
		total += f.Size
		if total < 0 {
			return fmt.Errorf("manifest file sizes overflow")
		}
		haveCurrent = haveCurrent || f.Name == "CURRENT"
		haveManifest = haveManifest || strings.HasPrefix(f.Name, "MANIFEST-")
	}
	if !sort.StringsAreSorted(names) {
		return fmt.Errorf("manifest files are not sorted by name")
	}
	if !haveCurrent || !haveManifest {
		return fmt.Errorf("manifest lacks CURRENT or a MANIFEST file")
	}
	if total != a.UncompressedSize {
		return fmt.Errorf("manifest file sizes add up to %d but uncompressedSize is %d", total, a.UncompressedSize)
	}
	if total > maxUnpackedBytes {
		return fmt.Errorf("manifest files add up to %d bytes, above the %d bytes a snapshot may unpack to", total, maxUnpackedBytes)
	}
	if total > a.Size*maxExpansion {
		return fmt.Errorf("manifest says the %d byte archive unpacks to %d bytes, more than %d times its size: a chain database does not compress that far, so this is not a snapshot but a decompression bomb", a.Size, total, maxExpansion)
	}
	return nil
}

func (m *manifest) chainIDValue() uint64 {
	v, _ := strconv.ParseUint(m.ChainID, 10, 64)
	return v
}

func (m *manifest) file(name string) (fileEntry, bool) {
	for _, f := range m.Archive.Files {
		if f.Name == name {
			return f, true
		}
	}
	return fileEntry{}, false
}

func readManifestFile(path string) (*manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(raw) > maxManifestBytes {
		return nil, fmt.Errorf("manifest is larger than %d bytes", maxManifestBytes)
	}
	return parseManifest(raw)
}

func parseManifest(raw []byte) (*manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("parse manifest: data after the manifest object")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *manifest) marshal() ([]byte, error) {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// expectations are what the operator pins before trusting a manifest.
type expectations struct {
	ChainID     uint64
	GenesisHash []byte
	MinHeight   uint64
	MaxAge      time.Duration
	TipHash     []byte
	StateRoot   []byte
	// RejectValidators are addresses that must not be validators in the
	// snapshot's state.
	RejectValidators []string
}

// checkManifest compares the manifest with the pinned expectations.
func checkManifest(m *manifest, e expectations, now time.Time) error {
	if e.GenesisHash == nil {
		return fmt.Errorf("no genesis hash was pinned: pass --genesis-hash (or set NHB_SNAPSHOT_GENESIS_HASH)")
	}
	if got := m.chainIDValue(); got != e.ChainID {
		return fmt.Errorf("the snapshot is for chain id %d, expected %d", got, e.ChainID)
	}
	if m.GenesisHash != hex0x(e.GenesisHash) {
		return fmt.Errorf("the snapshot is for genesis hash %s, expected %s", m.GenesisHash, hex0x(e.GenesisHash))
	}
	if m.Height < e.MinHeight {
		return fmt.Errorf("the snapshot is at height %d, below the required minimum %d", m.Height, e.MinHeight)
	}
	if !now.IsZero() {
		if ahead := time.Unix(m.TipTimestamp, 0).Sub(now); ahead > maxTipFutureSkew {
			return fmt.Errorf("the snapshot's newest block is dated %s ahead of this host's clock: a block cannot be in a snapshot before it exists, so the snapshot was made up (or this host's clock is wrong)", ahead.Round(time.Second))
		}
	}
	if e.MaxAge > 0 {
		created, _ := time.Parse(time.RFC3339, m.CreatedAt)
		if age := now.Sub(created); age > e.MaxAge {
			return fmt.Errorf("the snapshot was created %s ago, older than the allowed %s", age.Round(time.Second), e.MaxAge)
		}
		// createdAt is free text that nobody signs, so it cannot be what the
		// limit rests on: the age of the newest block, which extract checks
		// against the unpacked database, is.
		if err := checkTipAge(m.TipTimestamp, e.MaxAge, now); err != nil {
			return err
		}
	}
	if e.TipHash != nil && m.TipHash != hex0x(e.TipHash) {
		return fmt.Errorf("the snapshot tip hash is %s, expected %s", m.TipHash, hex0x(e.TipHash))
	}
	if e.StateRoot != nil && m.StateRoot != hex0x(e.StateRoot) {
		return fmt.Errorf("the snapshot state root is %s, expected %s", m.StateRoot, hex0x(e.StateRoot))
	}
	return nil
}

// checkTipAge refuses a snapshot whose newest block is older than maxAge (any
// age is accepted when maxAge is not positive). The age of a snapshot is the age
// of its newest block: it is how many blocks a follower has to catch up on, and,
// unlike the manifest's creation time, it is a value extract compares with the
// database it unpacked, so a manifest cannot claim a fresher snapshot than the
// one it holds.
func checkTipAge(tipTimestamp int64, maxAge time.Duration, now time.Time) error {
	if maxAge <= 0 {
		return nil
	}
	if age := now.Sub(time.Unix(tipTimestamp, 0)); age > maxAge {
		return fmt.Errorf("the snapshot's newest block is dated %s ago, older than the allowed %s: the snapshot is stale (its manifest may claim it was made more recently than that)", age.Round(time.Second), maxAge)
	}
	return nil
}

// manifestField returns one field of the manifest as text, for scripts.
func manifestField(m *manifest, name string) (string, error) {
	switch name {
	case "chainId":
		return m.ChainID, nil
	case "genesisHash":
		return m.GenesisHash, nil
	case "height":
		return strconv.FormatUint(m.Height, 10), nil
	case "tipHash":
		return m.TipHash, nil
	case "stateRoot":
		return m.StateRoot, nil
	case "tipTimestamp":
		return strconv.FormatInt(m.TipTimestamp, 10), nil
	case "createdAt":
		return m.CreatedAt, nil
	case "producer.binaryVersion":
		return m.Producer.BinaryVersion, nil
	case "producer.binaryCommit":
		return m.Producer.BinaryCommit, nil
	case "producer.binarySha256":
		return m.Producer.BinarySha256, nil
	case "archive.name":
		return m.Archive.Name, nil
	case "archive.size":
		return strconv.FormatInt(m.Archive.Size, 10), nil
	case "archive.uncompressedSize":
		return strconv.FormatInt(m.Archive.UncompressedSize, 10), nil
	case "archive.sha256":
		return m.Archive.Sha256, nil
	}
	return "", fmt.Errorf("unknown manifest field %q", name)
}
