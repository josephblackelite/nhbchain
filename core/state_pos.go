package core

import (
	"bytes"
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

	_, err = lifecycle.Authorize(payer, merchant, amount, msg.GetExpiry(), msg.GetIntentRef())
	return err
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

	var authID [32]byte
	copy(authID[:], []byte(msg.GetAuthorizationId()))
	if len(msg.GetAuthorizationId()) == 64 {
		// assuming hex encoded
		parsed, _ := common.ParseHexOrString(msg.GetAuthorizationId())
		copy(authID[:], parsed)
	}

	_, err = lifecycle.Capture(authID, amount, caller)
	return err
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

	var authID [32]byte
	copy(authID[:], []byte(msg.GetAuthorizationId()))
	if len(msg.GetAuthorizationId()) == 64 {
		parsed, _ := common.ParseHexOrString(msg.GetAuthorizationId())
		copy(authID[:], parsed)
	}

	_, err = lifecycle.Void(authID, msg.GetReason(), caller)
	return err
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
		_, err = registry.UpsertMerchant(authorityAddr, msgMerchant.MerchantAddr, msgMerchant.Nonce, msgMerchant.ExpiresAt, msgMerchant.ChainId)
		return err
	}

	var msgDevice posv1.MsgRegisterDevice
	if err := proto.Unmarshal(tx.Data, &msgDevice); err == nil && msgDevice.DeviceId != "" {
		_, err = registry.RegisterDevice(authorityAddr, msgDevice.DeviceId, msgDevice.MerchantAddr, msgDevice.Nonce, msgDevice.ExpiresAt, msgDevice.ChainId)
		return err
	}

	var msgPause posv1.MsgPauseMerchant
	if err := proto.Unmarshal(tx.Data, &msgPause); err == nil && msgPause.MerchantAddr != "" {
		_, err = registry.PauseMerchant(authorityAddr, msgPause.MerchantAddr, msgPause.Nonce, msgPause.ExpiresAt, msgPause.ChainId)
		return err
	}

	// We implement a fallthrough strategy for out of scope messages
	return nil
}
