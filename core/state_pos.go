package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	posv1 "nhbchain/proto/pos"
)

// applyPOSAuthorize handles the authorization of a payment intent. The payer
// field is trust-on-verify, not trust-on-read: it must match the account
// that actually signed this transaction, exactly as with every other
// value-moving transaction on this chain, otherwise anyone could lock funds
// out of an arbitrary victim account by simply naming them as msg.Payer.
func (sp *StateProcessor) applyPOSAuthorize(tx *types.Transaction) error {
	var msg posv1.MsgAuthorizePayment
	if err := proto.Unmarshal(tx.Data, &msg); err != nil {
		return fmt.Errorf("pos: decode authorize msg: %w", err)
	}
	signer, err := tx.From()
	if err != nil {
		return fmt.Errorf("pos: recover signer: %w", err)
	}
	payerDecoded, err := crypto.DecodeAddress(msg.GetPayer())
	if err != nil {
		return fmt.Errorf("pos: invalid payer: %w", err)
	}
	if !bytes.Equal(payerDecoded.Bytes(), signer) {
		return fmt.Errorf("pos: payer must match the transaction signer")
	}
	merchantDecoded, err := crypto.DecodeAddress(msg.GetMerchant())
	if err != nil {
		return fmt.Errorf("pos: invalid merchant: %w", err)
	}
	amount, ok := new(big.Int).SetString(msg.GetAmount(), 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("pos: invalid amount")
	}

	manager := nhbstate.NewManager(sp.Trie)
	lifecycle := pos.NewLifecycle(manager)
	lifecycle.SetEmitter(stateProcessorEmitter{sp: sp})
	lifecycle.SetNowFunc(func() time.Time { return sp.blockTimestamp().UTC() })

	var payer, merchant [20]byte
	copy(payer[:], payerDecoded.Bytes())
	copy(merchant[:], merchantDecoded.Bytes())

	if _, err := lifecycle.Authorize(payer, merchant, amount, msg.GetExpiry(), msg.GetIntentRef()); err != nil {
		return err
	}
	// Every other transaction type advances the signer's account nonce on
	// success (see incrementNativeAccountNonce's other callers); this one
	// didn't, so validateSenderAccount's exact-match nonce check let the
	// identical signed bytes be resubmitted indefinitely -- each resubmission
	// locks amount again under a fresh, unrelated authorization ID (Authorize
	// derives that ID from its own internal per-payer counter, not the
	// transaction nonce), draining the payer's balance into pending holds one
	// resubmission at a time. Bumping the nonce here closes that replay and
	// also stops this account's nonce from permanently desyncing from what
	// any normal wallet would compute next.
	return sp.incrementNativeAccountNonce(signer)
}

// ErrPOSInvalidAuthorizationID marks a capture or void whose authorization id
// is not a 32-byte hex string. It is a pure function of the transaction's own
// payload, so the block builder prunes it (see classifyProposalError).
var ErrPOSInvalidAuthorizationID = errors.New("pos: invalid authorization id")

// decodePOSAuthorizationID decodes the authorization id carried by a capture
// or void message. Authorization ids are 32-byte hashes, and every place the
// chain reports one (the pos_getAuthorization result, the payment event
// attributes) writes it as lowercase hex, with a 0x prefix in the RPC result
// and without one in event attributes. Capture and void therefore accept
// exactly that hex text, optionally 0x/0X prefixed, and nothing else: a raw
// 32-byte id cannot travel in a proto3 string field, and anything that does
// not decode to exactly 32 bytes could only ever address a different record.
func decodePOSAuthorizationID(raw string) ([32]byte, error) {
	var id [32]byte
	trimmed := raw
	if len(trimmed) >= 2 && trimmed[0] == '0' && (trimmed[1] == 'x' || trimmed[1] == 'X') {
		trimmed = trimmed[2:]
	}
	if len(trimmed) != 2*len(id) {
		return id, fmt.Errorf("%w: expected 64 hex characters, optionally prefixed with 0x", ErrPOSInvalidAuthorizationID)
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return id, fmt.Errorf("%w: not valid hex", ErrPOSInvalidAuthorizationID)
	}
	copy(id[:], decoded)
	return id, nil
}

// applyPOSCapture claims funds against a pending authorization. Only the
// merchant the authorization was created for may capture it -- enforced by
// passing the transaction's recovered signer, not any payload-supplied
// field, down to Lifecycle.Capture.
func (sp *StateProcessor) applyPOSCapture(tx *types.Transaction) error {
	var msg posv1.MsgCapturePayment
	if err := proto.Unmarshal(tx.Data, &msg); err != nil {
		return fmt.Errorf("pos: decode capture msg: %w", err)
	}
	signer, err := tx.From()
	if err != nil {
		return fmt.Errorf("pos: recover signer: %w", err)
	}
	var caller [20]byte
	copy(caller[:], signer)

	amount, ok := new(big.Int).SetString(msg.GetAmount(), 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("pos: invalid amount")
	}
	manager := nhbstate.NewManager(sp.Trie)
	lifecycle := pos.NewLifecycle(manager)
	lifecycle.SetEmitter(stateProcessorEmitter{sp: sp})
	lifecycle.SetNowFunc(func() time.Time { return sp.blockTimestamp().UTC() })

	authID, err := decodePOSAuthorizationID(msg.GetAuthorizationId())
	if err != nil {
		return err
	}

	if _, err := lifecycle.Capture(authID, amount, caller); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(signer)
}

// applyPOSVoid cancels a pending authorization. Either the payer or the
// merchant on that authorization may void it; enforced against the
// transaction's recovered signer, same as applyPOSCapture.
func (sp *StateProcessor) applyPOSVoid(tx *types.Transaction) error {
	var msg posv1.MsgVoidPayment
	if err := proto.Unmarshal(tx.Data, &msg); err != nil {
		return fmt.Errorf("pos: decode void msg: %w", err)
	}
	signer, err := tx.From()
	if err != nil {
		return fmt.Errorf("pos: recover signer: %w", err)
	}
	var caller [20]byte
	copy(caller[:], signer)

	manager := nhbstate.NewManager(sp.Trie)
	lifecycle := pos.NewLifecycle(manager)
	lifecycle.SetEmitter(stateProcessorEmitter{sp: sp})
	lifecycle.SetNowFunc(func() time.Time { return sp.blockTimestamp().UTC() })

	authID, err := decodePOSAuthorizationID(msg.GetAuthorizationId())
	if err != nil {
		return err
	}

	if _, err := lifecycle.Void(authID, msg.GetReason(), caller); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(signer)
}

// GetPOSAuthorization returns the authorization record for the given ID, if
// one exists. Read-only: safe to call outside of transaction application.
func (sp *StateProcessor) GetPOSAuthorization(id [32]byte) (*pos.Authorization, error) {
	if sp == nil {
		return nil, fmt.Errorf("state processor unavailable")
	}
	if sp.Trie == nil {
		return nil, fmt.Errorf("state trie unavailable")
	}
	manager := nhbstate.NewManager(sp.Trie)
	lifecycle := pos.NewLifecycle(manager)
	return lifecycle.Get(id)
}

// GetPOSAuthorizationByIntentRef resolves the authorization created for the
// given client-supplied intent reference, if any. This is the only way a
// caller that only knows the IntentRef it embedded in an Authorize
// transaction (typically a merchant, not the payer) can discover the
// resulting authorization ID needed for a later Capture or Void.
func (sp *StateProcessor) GetPOSAuthorizationByIntentRef(intentRef []byte) (*pos.Authorization, error) {
	if sp == nil {
		return nil, fmt.Errorf("state processor unavailable")
	}
	if sp.Trie == nil {
		return nil, fmt.Errorf("state trie unavailable")
	}
	manager := nhbstate.NewManager(sp.Trie)
	lifecycle := pos.NewLifecycle(manager)
	return lifecycle.FindByIntentRef(intentRef)
}

func (sp *StateProcessor) applyPOSRegistry(tx *types.Transaction) error {
	// For registry commands, the Gateway acts as Authority. We extract it from Tx From.
	authority, err := tx.From()
	if err != nil {
		return err
	}
	authorityAddr := common.BytesToAddress(authority).Hex()
	manager := nhbstate.NewManager(sp.Trie)
	registry := pos.NewRegistry(manager)

	// Since we wrap all registry messages under TxTypePOSRegistry, we can check the inner type
	// by trying to unmarshal sequentially.
	var msgMerchant posv1.MsgRegisterMerchant
	if err := proto.Unmarshal(tx.Data, &msgMerchant); err == nil && msgMerchant.MerchantAddr != "" {
		_, err = registry.RegisterDevice(authorityAddr, "unknown", msgMerchant.MerchantAddr, msgMerchant.Nonce, msgMerchant.ExpiresAt, msgMerchant.ChainId)
		// Assuming RegisterMerchant is actually UpsertMerchant in pos registry.
		if _, err = registry.UpsertMerchant(authorityAddr, msgMerchant.MerchantAddr, msgMerchant.Nonce, msgMerchant.ExpiresAt, msgMerchant.ChainId); err != nil {
			return err
		}
		return sp.incrementNativeAccountNonce(authority)
	}

	var msgDevice posv1.MsgRegisterDevice
	if err := proto.Unmarshal(tx.Data, &msgDevice); err == nil && msgDevice.DeviceId != "" {
		if _, err := registry.RegisterDevice(authorityAddr, msgDevice.DeviceId, msgDevice.MerchantAddr, msgDevice.Nonce, msgDevice.ExpiresAt, msgDevice.ChainId); err != nil {
			return err
		}
		return sp.incrementNativeAccountNonce(authority)
	}

	var msgPause posv1.MsgPauseMerchant
	if err := proto.Unmarshal(tx.Data, &msgPause); err == nil && msgPause.MerchantAddr != "" {
		if _, err := registry.PauseMerchant(authorityAddr, msgPause.MerchantAddr, msgPause.Nonce, msgPause.ExpiresAt, msgPause.ChainId); err != nil {
			return err
		}
		return sp.incrementNativeAccountNonce(authority)
	}

	// We implement a fallthrough strategy for out of scope messages. Nothing
	// mutated, so the nonce still advances (this transaction was still
	// included and "succeeded") to avoid leaving the account's nonce stuck
	// for whatever it sends next -- same reasoning as the three branches
	// above and applyPOSAuthorize/Capture/Void.
	return sp.incrementNativeAccountNonce(authority)
}
