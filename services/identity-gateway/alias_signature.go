package identitygateway

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	escrow "nhbchain/native/escrow"
)

// NHB-AUDIT-S6b: handleBindAlias's single-use bind token (NHB-AUDIT-S6,
// server.go/store.go) only ever proves the caller controls the target
// inbox -- it never proved the caller also controls the alias itself, so
// anyone holding a bind token for a verified email could bind that email
// to ANY syntactically-valid alias string, including one they do not own
// or control on-chain. This file adds the alias-side half of the proof,
// following the exact delegated-signature convention
// native/escrow/engine_milestone_signature.go already established for the
// same class of problem (a client-supplied identifier is never trusted on
// its own -- only a cryptographically recovered signer is): the alias's
// own controlling on-chain key must sign a canonical envelope, and the
// recovered address is checked against that alias's actual on-chain owner
// (chain_client.go's AliasOwnerLookup) -- never a client-supplied address.

// AliasBindEnvelope is the canonical payload the alias's own controlling
// on-chain key signs to authorize binding a specific verified email to
// that alias. BindToken pins this signature to the exact same single-use,
// time-bound token /identity/email/verify just issued (store.go's
// BindTokenDigest/BindTokenExpires) -- reusing that existing single-use
// and expiry mechanism as this envelope's replay-protection nonce, rather
// than standing up a second one, so a captured alias signature is exactly
// as short-lived and single-use as the bind token it travels with: once
// store.BindAlias consumes the token (atomically, on a successful bind),
// the very same signature can never be replayed for a second bind either.
type AliasBindEnvelope struct {
	AliasID   string `json:"aliasId"`
	EmailHash string `json:"emailHash"`
	BindToken string `json:"bindToken"`
}

func buildAliasBindEnvelope(aliasID, emailHash, bindToken string) ([]byte, error) {
	envelope := AliasBindEnvelope{AliasID: aliasID, EmailHash: emailHash, BindToken: bindToken}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("identity-gateway: build alias bind envelope: %w", err)
	}
	return payload, nil
}

// RecoverAliasBindSigner reconstructs the canonical AliasBindEnvelope for
// (aliasID, emailHash, bindToken) and recovers the address that produced
// signature over it, via the same Keccak256+secp256k1 recovery
// native/escrow.RecoverSigner already implements for milestone escrow's
// delegated actions. handleBindAlias must treat the RETURNED address as
// the sole source of truth for who is asserting alias ownership -- never
// a client-supplied address field -- and then check it against the
// alias's real on-chain owner before trusting it.
func RecoverAliasBindSigner(aliasID, emailHash, bindToken string, signature []byte) ([20]byte, error) {
	payload, err := buildAliasBindEnvelope(aliasID, emailHash, bindToken)
	if err != nil {
		return [20]byte{}, err
	}
	digestHash := ethcrypto.Keccak256Hash(payload)
	var digest [32]byte
	copy(digest[:], digestHash[:])
	signer, err := escrow.RecoverSigner(digest, signature)
	if err != nil {
		return [20]byte{}, fmt.Errorf("identity-gateway: recover alias bind signer: %w", err)
	}
	return signer, nil
}

// SignAliasBindEnvelope signs the canonical AliasBindEnvelope for
// (aliasID, emailHash, bindToken) with priv. Exported for tests and
// reference Go clients constructing the wire signature -- the actual
// authorization check (RecoverAliasBindSigner) never calls this. Mirrors
// native/escrow's SignMilestoneActionEnvelope/SignMilestoneCreateEnvelope
// convention.
func SignAliasBindEnvelope(aliasID, emailHash, bindToken string, priv *ecdsa.PrivateKey) ([]byte, error) {
	if priv == nil {
		return nil, fmt.Errorf("identity-gateway: signing key required")
	}
	payload, err := buildAliasBindEnvelope(aliasID, emailHash, bindToken)
	if err != nil {
		return nil, err
	}
	digestHash := ethcrypto.Keccak256Hash(payload)
	return ethcrypto.Sign(digestHash[:], priv)
}
