package sync

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// GovernanceVerifier validates the optional governance anchor included with a snapshot manifest.
type GovernanceVerifier interface {
	Verify(anchor *GovernanceAnchor, digest []byte) error
}

// GovernanceKeyVerifier checks governance anchors using a single ECDSA public key.
type GovernanceKeyVerifier struct {
	Key *ecdsa.PublicKey
}

// Verify implements GovernanceVerifier.
func (g GovernanceKeyVerifier) Verify(anchor *GovernanceAnchor, digest []byte) error {
	if anchor == nil {
		return fmt.Errorf("governance anchor missing")
	}
	if g.Key == nil {
		return fmt.Errorf("governance verifier not configured")
	}
	payloadHash := sha256.Sum256(anchor.Payload)
	if !bytes.Equal(payloadHash[:], digest) {
		return fmt.Errorf("governance payload does not match manifest digest")
	}
	if len(anchor.Signature) != 65 {
		return fmt.Errorf("invalid governance signature length")
	}
	if !ethcrypto.VerifySignature(ethcrypto.FromECDSAPub(g.Key), digest, anchor.Signature[:64]) {
		return fmt.Errorf("governance signature invalid")
	}
	return nil
}

// VerifyManifest ensures the manifest is correctly signed by the validator set or the governance anchor when provided.
func VerifyManifest(manifest *SnapshotManifest, set *ValidatorSet, governance GovernanceVerifier) error {
	if manifest == nil {
		return fmt.Errorf("nil manifest")
	}
	digest, err := manifest.Digest()
	if err != nil {
		return fmt.Errorf("manifest digest: %w", err)
	}
	if len(manifest.Signatures) == 0 {
		if governance == nil {
			return fmt.Errorf("snapshot manifest missing validator signatures")
		}
		return governance.Verify(manifest.Governance, digest)
	}
	if set == nil {
		return fmt.Errorf("validator set is required for signature verification")
	}
	converted := make([]BlockSignature, 0, len(manifest.Signatures))
	for _, sig := range manifest.Signatures {
		converted = append(converted, BlockSignature{Address: sig.Address, Signature: sig.Signature})
	}
	if err := set.VerifyQuorum(digest, converted); err != nil {
		return err
	}
	return nil
}
