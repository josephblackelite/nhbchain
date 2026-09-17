package types

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// VoteType distinguishes a BFT prevote from a precommit. This is a separate
// definition from consensus/bft's own VoteType, deliberately kept in sync
// (not aliased) so this package -- which core/node.go's untrusted-peer
// block verification lives in -- never needs to import consensus/bft (that
// would risk a package cycle, since consensus/bft already imports
// core/types). consensus/bft/vote_payload_parity_test.go asserts these two
// independently-defined types produce byte-identical signed payloads for
// the same logical vote, so any future drift between them is caught by a
// test, not discovered as a silent quorum-certificate verification failure
// in production.
type VoteType byte

const (
	VoteTypePrevote   VoteType = 0x01
	VoteTypePrecommit VoteType = 0x02
)

// PrecommitVote is the exact payload a validator's consensus key signs when
// voting on a block during a BFT round. Field names, JSON tags, and types
// must exactly match consensus/bft.Vote -- this is the payload a
// QuorumCert's signatures were produced over, and verification only works
// if this struct marshals identically to the one that was actually signed.
type PrecommitVote struct {
	BlockHash []byte   `json:"blockHash"`
	Round     int      `json:"round"`
	Type      VoteType `json:"type"`
	Height    uint64   `json:"height"`
}

// Bytes returns the canonical signed payload for this vote.
func (v *PrecommitVote) Bytes() []byte {
	b, _ := json.Marshal(v)
	return b
}

// Digest returns the sha256 digest actually signed/verified for this vote,
// matching consensus/bft's sha256.Sum256(vote.bytes()) convention exactly.
func (v *PrecommitVote) Digest() [32]byte {
	return sha256.Sum256(v.Bytes())
}

// Sign produces a QuorumSignature over this vote using the given
// secp256k1 private key, via the same go-ethereum recoverable-signature
// convention used throughout this codebase (core/types/transaction.go's
// Transaction.Sign, consensus/bft's vote/proposal signing, etc.).
func (v *PrecommitVote) Sign(priv *ecdsa.PrivateKey) (*QuorumSignature, error) {
	if priv == nil {
		return nil, fmt.Errorf("quorum vote: nil private key")
	}
	digest := v.Digest()
	sig, err := ethcrypto.Sign(digest[:], priv)
	if err != nil {
		return nil, fmt.Errorf("quorum vote: sign: %w", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey)
	return &QuorumSignature{Validator: addr.Bytes(), Signature: sig}, nil
}

// QuorumSignature is one validator's precommit signature contributing to a
// QuorumCert.
type QuorumSignature struct {
	Validator []byte `json:"validator"`
	Signature []byte `json:"signature"`
}

// QuorumCert bundles the set of validator precommit signatures that
// certified a block's commit under BFT quorum (>=2/3 voting power),
// reusing the exact signatures each validator already produced during the
// live consensus round -- not a separate attestation requiring extra
// signing. Attached to Block (not BlockHeader) so its presence, absence, or
// content never changes a block's hash/identity (BlockHeader.Hash() only
// ever marshals *BlockHeader) -- this keeps the fix backward-compatible
// with every already-committed block, which simply has no QuorumCert.
//
// NHB-TRIAGE-C1: the P2P block-sync path (core/node.go's
// commitSyncedBlock) used to accept and commit any structurally-valid
// block with no check at all that it was ever actually voted on by the
// real validator set -- Header.Validator is just a claimed proposer
// address, not a proof. QuorumCert closes that gap: a synced block must
// now carry proof that real validators, controlling real voting power,
// actually precommitted to this exact (height, round, block hash).
type QuorumCert struct {
	Height     uint64            `json:"height"`
	Round      int               `json:"round"`
	BlockHash  []byte            `json:"blockHash"`
	Signatures []QuorumSignature `json:"signatures"`
}

// Verify checks that qc's signatures collectively represent >=2/3 of the
// supplied validator set's voting power, over the vote payload
// (height, round, blockHash, type=precommit) qc itself claims to certify,
// checked against headerHash (the block's actual, independently-recomputed
// header hash -- never trust qc.BlockHash alone, it's attacker-controlled
// input on the untrusted sync path).
//
// validatorPower is a map[string(address bytes)]*big.Int, matching the
// shape core.Node.GetValidatorSet() / consensus/bft.Engine.validatorSet
// already use. Callers should pass the validator set that was actually
// active AT the height being verified, not necessarily "whatever the
// verifier's current/latest set happens to be" -- core.Node.commitBlock's
// caller does this via validatorSetAtHeight, which reconstructs it from
// the parent block's own historical state (this codebase's trie storage
// never prunes old nodes, so any previously-committed height's state
// remains fully readable). This function itself is agnostic to how the
// caller sourced the map; it just verifies against whatever it's given.
func (qc *QuorumCert) Verify(headerHash []byte, validatorPower map[string]*big.Int) error {
	if qc == nil {
		return fmt.Errorf("quorum certificate missing")
	}
	if len(headerHash) == 0 {
		return fmt.Errorf("quorum certificate: empty header hash")
	}
	if !bytes.Equal(qc.BlockHash, headerHash) {
		return fmt.Errorf("quorum certificate: block hash mismatch")
	}
	if len(qc.Signatures) == 0 {
		return fmt.Errorf("quorum certificate: no signatures")
	}
	if len(validatorPower) == 0 {
		return fmt.Errorf("quorum certificate: validator set unavailable")
	}

	vote := &PrecommitVote{BlockHash: headerHash, Round: qc.Round, Type: VoteTypePrecommit, Height: qc.Height}
	digest := vote.Digest()

	totalPower := big.NewInt(0)
	for _, power := range validatorPower {
		if power != nil {
			totalPower.Add(totalPower, power)
		}
	}
	if totalPower.Sign() <= 0 {
		return fmt.Errorf("quorum certificate: validator set has zero total power")
	}

	seen := make(map[string]struct{}, len(qc.Signatures))
	signedPower := big.NewInt(0)
	for _, sig := range qc.Signatures {
		key := string(sig.Validator)
		if _, dup := seen[key]; dup {
			continue // duplicate signer -- do not double-count, no error either (defensive, matches core/sync.VerifyQuorum's convention)
		}
		power, isValidator := validatorPower[key]
		if !isValidator || power == nil || power.Sign() <= 0 {
			return fmt.Errorf("quorum certificate: signature from non-validator %x", sig.Validator)
		}
		if len(sig.Signature) != 65 {
			return fmt.Errorf("quorum certificate: invalid signature length for %x", sig.Validator)
		}
		pub, err := ethcrypto.SigToPub(digest[:], sig.Signature)
		if err != nil {
			return fmt.Errorf("quorum certificate: recover signer %x: %w", sig.Validator, err)
		}
		recovered := ethcrypto.PubkeyToAddress(*pub)
		if !bytes.Equal(recovered.Bytes(), sig.Validator) {
			return fmt.Errorf("quorum certificate: signature does not match claimed validator %x", sig.Validator)
		}
		seen[key] = struct{}{}
		signedPower.Add(signedPower, power)
	}

	// threshold = ceil(2*total/3), identical formula to
	// consensus/bft.Engine.hasTwoThirdsPowerLocked so a QuorumCert is held
	// to exactly the same quorum bar the live BFT round already enforced
	// when these signatures were originally collected.
	threshold := new(big.Int).Mul(big.NewInt(2), totalPower)
	threshold.Add(threshold, big.NewInt(2))
	threshold.Div(threshold, big.NewInt(3))
	if signedPower.Cmp(threshold) < 0 {
		return fmt.Errorf("quorum certificate: insufficient voting power: signed=%s threshold=%s total=%s", signedPower, threshold, totalPower)
	}
	return nil
}
