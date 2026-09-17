package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
)

// BlockSignature is a validator signature over a block header.
type BlockSignature struct {
	Address   []byte `json:"address"`
	Signature []byte `json:"signature"`
}

// BlockProof couples a block header with validator attestations.
type BlockProof struct {
	Header     *types.BlockHeader `json:"header"`
	Signatures []BlockSignature   `json:"signatures"`
}

// Validator models the consensus voting power and public key metadata for a validator.
type Validator struct {
	Address []byte
	Power   uint64
}

// ValidatorSet verifies quorum signatures.
type ValidatorSet struct {
	validators map[string]Validator
	totalPower uint64
}

// NewValidatorSet constructs a deterministic validator set keyed by normalized address bytes.
func NewValidatorSet(list []Validator) *ValidatorSet {
	set := &ValidatorSet{validators: make(map[string]Validator)}
	for _, v := range list {
		key := normalizeAddress(v.Address)
		if key == "" {
			continue
		}
		set.validators[key] = v
		set.totalPower += v.Power
	}
	return set
}

// TotalPower returns the cumulative voting power of the set.
func (s *ValidatorSet) TotalPower() uint64 {
	if s == nil {
		return 0
	}
	return s.totalPower
}

// VerifyQuorum ensures the provided signatures collectively represent >= 2/3 of the voting power.
func (s *ValidatorSet) VerifyQuorum(digest []byte, signatures []BlockSignature) error {
	if s == nil {
		return fmt.Errorf("validator set not configured")
	}
	if len(signatures) == 0 {
		return fmt.Errorf("no signatures provided")
	}
	seen := make(map[string]struct{})
	var signed uint64
	for _, sig := range signatures {
		key := normalizeAddress(sig.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		validator, ok := s.validators[key]
		if !ok {
			return fmt.Errorf("signature from unknown validator %x", sig.Address)
		}
		if len(sig.Signature) != 65 {
			return fmt.Errorf("invalid signature length for %x", sig.Address)
		}
		pub, err := ethcrypto.SigToPub(digest, sig.Signature)
		if err != nil {
			return fmt.Errorf("recover signature for %x: %w", sig.Address, err)
		}
		derived := ethcrypto.PubkeyToAddress(*pub)
		if !bytes.Equal(derived.Bytes(), validator.Address) {
			return fmt.Errorf("signature address mismatch: expected %x got %x", validator.Address, derived.Bytes())
		}
		signed += validator.Power
		seen[key] = struct{}{}
	}
	total := s.totalPower
	if total == 0 {
		return fmt.Errorf("validator set has zero total power")
	}
	if signed*3 < total*2 {
		return fmt.Errorf("insufficient voting power: signed=%d total=%d", signed, total)
	}
	return nil
}

func blockProofDigest(chainID uint64, height uint64, headerHash []byte) ([]byte, error) {
	if len(headerHash) == 0 {
		return nil, fmt.Errorf("empty header hash")
	}
	buf := make([]byte, 8+8+len(headerHash))
	binary.BigEndian.PutUint64(buf[:8], chainID)
	binary.BigEndian.PutUint64(buf[8:16], height)
	copy(buf[16:], headerHash)
	sum := sha256.Sum256(buf)
	return sum[:], nil
}

func normalizeAddress(address []byte) string {
	if len(address) == 0 {
		return ""
	}
	buf := make([]byte, hex.EncodedLen(len(address)))
	hex.Encode(buf, address)
	return strings.ToLower(string(buf))
}

// ProofFetcher yields block proofs sequentially.
type ProofFetcher interface {
	Next(ctx context.Context, fromHeight uint64) (*BlockProof, error)
}

// HeaderApplier persists verified headers to storage.
type HeaderApplier interface {
	Apply(ctx context.Context, header *types.BlockHeader) error
}

// RangeSyncer verifies proofs from a checkpoint until the fetcher signals completion.
type RangeSyncer struct {
	chainID uint64
	set     *ValidatorSet
	applier HeaderApplier
}

// NewRangeSyncer constructs a fast-sync range processor.
func NewRangeSyncer(chainID uint64, set *ValidatorSet, applier HeaderApplier) *RangeSyncer {
	return &RangeSyncer{chainID: chainID, set: set, applier: applier}
}

// Sync consumes proofs until the fetcher returns io.EOF. The final validated header is returned.
func (s *RangeSyncer) Sync(ctx context.Context, checkpoint *types.BlockHeader, fetcher ProofFetcher) (*types.BlockHeader, error) {
	if s == nil {
		return nil, fmt.Errorf("range syncer not configured")
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("missing checkpoint header")
	}
	if fetcher == nil {
		return nil, fmt.Errorf("nil proof fetcher")
	}
	prevHash, err := checkpoint.Hash()
	if err != nil {
		return nil, fmt.Errorf("hash checkpoint: %w", err)
	}
	expectedHeight := checkpoint.Height
	current := checkpoint
	for {
		proof, err := fetcher.Next(ctx, expectedHeight+1)
		if err != nil {
			if err == io.EOF {
				return current, nil
			}
			return nil, err
		}
		if proof == nil || proof.Header == nil {
			return nil, fmt.Errorf("fetcher returned empty proof")
		}
		header := proof.Header
		if header.Height != expectedHeight+1 {
			return nil, fmt.Errorf("non-sequential proof: have %d expected %d", header.Height, expectedHeight+1)
		}
		if !bytes.Equal(header.PrevHash, prevHash) {
			return nil, fmt.Errorf("header %d predecessor mismatch", header.Height)
		}
		headerHash, err := header.Hash()
		if err != nil {
			return nil, fmt.Errorf("hash header %d: %w", header.Height, err)
		}
		digest, err := blockProofDigest(s.chainID, header.Height, headerHash)
		if err != nil {
			return nil, err
		}
		if err := s.set.VerifyQuorum(digest, proof.Signatures); err != nil {
			return nil, fmt.Errorf("quorum check failed at height %d: %w", header.Height, err)
		}
		if s.applier != nil {
			if err := s.applier.Apply(ctx, header); err != nil {
				return nil, fmt.Errorf("apply header %d: %w", header.Height, err)
			}
		}
		prevHash = headerHash
		expectedHeight = header.Height
		current = header
	}
}

// SliceProofFetcher implements ProofFetcher over an in-memory slice.
type SliceProofFetcher struct {
	proofs []*BlockProof
}

// Next returns the next proof or io.EOF once exhausted.
func (f *SliceProofFetcher) Next(_ context.Context, fromHeight uint64) (*BlockProof, error) {
	if len(f.proofs) == 0 {
		return nil, io.EOF
	}
	proof := f.proofs[0]
	if proof == nil || proof.Header == nil {
		return nil, fmt.Errorf("empty proof in slice")
	}
	if proof.Header.Height != fromHeight {
		return nil, fmt.Errorf("slice fetcher height mismatch: want %d got %d", fromHeight, proof.Header.Height)
	}
	f.proofs = f.proofs[1:]
	return proof, nil
}

// StaticHeaderApplier collects applied headers into a slice, useful for tests.
type StaticHeaderApplier struct {
	Headers []*types.BlockHeader
}

// Apply stores the header in memory.
func (a *StaticHeaderApplier) Apply(_ context.Context, header *types.BlockHeader) error {
	if header == nil {
		return fmt.Errorf("nil header")
	}
	a.Headers = append(a.Headers, header)
	return nil
}
