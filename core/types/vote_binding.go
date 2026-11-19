package types

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// VoteBindingDomain separates the digest a vote is signed over from the genesis hash
// onwards from every other digest in the system.
const VoteBindingDomain = "nhb-vote-binding-v1"

// ErrVoteBindingGenesisMissing is returned when a vote must be signed or checked over the
// genesis hash and the hash is not known.
var ErrVoteBindingGenesisMissing = errors.New("vote binding: genesis hash unavailable")

// VoteBinding says which digest a consensus vote is signed over (ledger PL-R5T-31).
//
// A vote's payload is {blockHash, round, type, height}; its legacy digest is the sha256
// of that payload and carries no network identity, so two votes cast by one key on two
// networks at the same height, round and type look like an equivocation, and a quorum
// certificate or a vote of one network verifies on another. From FromHeight (zero = never,
// the live value) the digest also covers the network's genesis hash. The rule is read from
// the height of the vote itself: a vote at or above FromHeight verifies only against the
// bound digest, a vote below it only against the legacy digest, so every committed block
// and every proof about an earlier height are judged as before.
type VoteBinding struct {
	Genesis    []byte
	FromHeight uint64
}

// Active reports whether a vote at height is signed over the genesis hash.
func (b VoteBinding) Active(height uint64) bool {
	return b.FromHeight != 0 && height >= b.FromHeight
}

// BoundVoteDigest is sha256(domain | 0x00 | uint16(len(genesis)) | genesis | payload).
func BoundVoteDigest(genesis, payload []byte) [32]byte {
	buf := make([]byte, 0, len(VoteBindingDomain)+3+len(genesis)+len(payload))
	buf = append(buf, VoteBindingDomain...)
	buf = append(buf, 0)
	var n [2]byte
	binary.BigEndian.PutUint16(n[:], uint16(len(genesis)))
	buf = append(buf, n[:]...)
	buf = append(buf, genesis...)
	buf = append(buf, payload...)
	return sha256.Sum256(buf)
}

// Digest returns the digest a vote at height with the given payload is signed over. It
// fails, rather than falling back to the legacy digest, when the binding is active and the
// genesis hash is unknown.
func (b VoteBinding) Digest(height uint64, payload []byte) ([32]byte, error) {
	if !b.Active(height) {
		return sha256.Sum256(payload), nil
	}
	if len(b.Genesis) == 0 {
		return [32]byte{}, ErrVoteBindingGenesisMissing
	}
	return BoundVoteDigest(b.Genesis, payload), nil
}
