package consensus

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	gethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"nhbchain/crypto"
	consensusv1 "nhbchain/proto/consensus/v1"
)

// ErrMissingPayload indicates that no module message was supplied when building a transaction envelope.
var ErrMissingPayload = errors.New("consensus sdk: payload required")

// ErrMissingKey indicates that a signing key was not supplied.
var ErrMissingKey = errors.New("consensus sdk: signing key required")

// ErrInvalidChainID reports that the chain identifier provided when constructing the
// envelope was empty.
var ErrInvalidChainID = errors.New("consensus sdk: chain id required")

// NewTx constructs a transaction envelope that wraps the provided module payload. The
// supplied chain identifier and nonce are embedded to provide replay protection. The
// fee arguments are optional; omitting the amount or denom will result in the fee being
// excluded from the envelope.
func NewTx(payload proto.Message, nonce uint64, chainID, feeAmount, feeDenom, feePayer, memo string) (*consensusv1.TxEnvelope, error) {
	if payload == nil {
		return nil, ErrMissingPayload
	}
	trimmedChainID := strings.TrimSpace(chainID)
	if trimmedChainID == "" {
		return nil, ErrInvalidChainID
	}
	anyPayload, err := anypb.New(payload)
	if err != nil {
		return nil, fmt.Errorf("pack payload: %w", err)
	}
	envelope := &consensusv1.TxEnvelope{
		Payload: anyPayload,
		Nonce:   nonce,
		ChainId: trimmedChainID,
	}
	trimmedMemo := strings.TrimSpace(memo)
	if trimmedMemo != "" {
		envelope.Memo = trimmedMemo
	}
	amount := strings.TrimSpace(feeAmount)
	denom := strings.TrimSpace(feeDenom)
	payer := strings.TrimSpace(feePayer)
	if amount != "" || denom != "" || payer != "" {
		envelope.Fee = &consensusv1.Fee{
			Amount: amount,
			Denom:  denom,
			Payer:  payer,
		}
	}
	return envelope, nil
}

// Sign produces an authenticated transaction envelope by hashing the serialized
// envelope and signing it with the supplied private key. The returned structure can be
// submitted directly via the consensus client.
func Sign(envelope *consensusv1.TxEnvelope, key *crypto.PrivateKey) (*consensusv1.SignedTxEnvelope, error) {
	if envelope == nil {
		return nil, ErrMissingPayload
	}
	if key == nil || key.PrivateKey == nil {
		return nil, ErrMissingKey
	}
	body, err := proto.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	digest := sha256.Sum256(body)
	sig, err := gethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign envelope: %w", err)
	}
	pubKey := key.PubKey()
	if pubKey == nil || pubKey.PublicKey == nil {
		return nil, fmt.Errorf("derive public key: %w", ErrMissingKey)
	}
	signature := &consensusv1.TxSignature{
		PublicKey: gethcrypto.FromECDSAPub(pubKey.PublicKey),
		Signature: sig,
	}
	return &consensusv1.SignedTxEnvelope{Envelope: envelope, Signature: signature}, nil
}

// Submit signs the provided envelope if necessary and pushes it to the remote
// consensus service. When a SignedTxEnvelope is supplied the function forwards the
// transaction as-is. If only a raw envelope is provided a signing key must also be
// passed.
func Submit(ctx context.Context, client *Client, envelope *consensusv1.TxEnvelope, signed *consensusv1.SignedTxEnvelope, key *crypto.PrivateKey) (*consensusv1.SignedTxEnvelope, error) {
	if client == nil {
		return nil, fmt.Errorf("consensus sdk: client required")
	}
	var err error
	switch {
	case signed != nil:
		if envelope == nil {
			envelope = signed.GetEnvelope()
		}
	case envelope != nil:
		signed, err = Sign(envelope, key)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrMissingPayload
	}
	if err := client.SubmitEnvelope(ctx, signed); err != nil {
		return nil, err
	}
	return signed, nil
}
