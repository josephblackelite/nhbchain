package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

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

// RolePOSRegistryAdmin lets its holder change the POS registry for any
// merchant and device (TxTypePOSRegistry). Without it, only the account that
// owns a merchant address may change that merchant's entry and the entries of
// the devices bound to it. Granted like the other roles, through genesis or a
// governance role grant.
const RolePOSRegistryAdmin = "ROLE_POS_REGISTRY_ADMIN"

var (
	// ErrPOSRegistryInvalidPayload marks a registry transaction whose payload
	// cannot be executed as it stands: it does not decode, names no known
	// registry message, or carries an authority that is not the account that
	// signed it. It is a pure function of the transaction's own bytes, so the
	// block builder prunes it (see classifyProposalError).
	ErrPOSRegistryInvalidPayload = errors.New("pos: invalid registry payload")
	// ErrPOSRegistryUnauthorized marks a registry transaction whose signer is
	// neither the owner of the merchant it changes nor a holder of
	// RolePOSRegistryAdmin. A role can be granted later, so the block builder
	// skips it rather than pruning it.
	ErrPOSRegistryUnauthorized = errors.New("pos: signer may not change this registry entry")
)

// decodePOSRegistryAddress reads an address written as bech32 (nhb1..., the
// form the RPC layer prints) or as 0x-prefixed hex. It reports false for
// anything else, such as the free-form merchant labels the registry also
// accepts.
func decodePOSRegistryAddress(value string) ([]byte, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, false
	}
	if decoded, err := crypto.DecodeAddress(trimmed); err == nil {
		return decoded.Bytes(), true
	}
	if common.IsHexAddress(trimmed) {
		return common.HexToAddress(trimmed).Bytes(), true
	}
	return nil, false
}

// requirePOSRegistryAuthority accepts signer if it holds RolePOSRegistryAdmin
// or owns the merchant address: the merchant a registry entry belongs to is
// its owner's own address.
func requirePOSRegistryAuthority(manager *nhbstate.Manager, signer []byte, merchant string) error {
	if manager.HasRole(RolePOSRegistryAdmin, signer) {
		return nil
	}
	if owner, ok := decodePOSRegistryAddress(merchant); ok && bytes.Equal(owner, signer) {
		return nil
	}
	return fmt.Errorf("%w: merchant %q", ErrPOSRegistryUnauthorized, strings.TrimSpace(merchant))
}

// requirePOSRegistrySignerIsAuthority refuses a message whose authority field
// names an account other than the one that signed the transaction. The field
// is never the source of authority (the signature is), but a message that
// claims another account's authority is forged or confused, and executing it
// would hide that.
func requirePOSRegistrySignerIsAuthority(signer []byte, authority string) error {
	if strings.TrimSpace(authority) == "" {
		return nil
	}
	claimed, ok := decodePOSRegistryAddress(authority)
	if !ok || !bytes.Equal(claimed, signer) {
		return fmt.Errorf("%w: authority %q is not the account that signed the transaction", ErrPOSRegistryInvalidPayload, strings.TrimSpace(authority))
	}
	return nil
}

// applyPOSRegistry executes one merchant or device registry change. tx.Data is
// the serialized google.protobuf.Any consensus/codec builds for these
// messages, so the Any's type URL is the explicit message type and the payload
// is never decoded by trial. Whoever signs the transaction is the authority:
// the owner of the merchant being changed, or a holder of RolePOSRegistryAdmin.
func (sp *StateProcessor) applyPOSRegistry(tx *types.Transaction) error {
	signer, err := tx.From()
	if err != nil {
		return fmt.Errorf("pos: recover signer: %w", err)
	}
	if len(tx.Data) == 0 {
		return fmt.Errorf("%w: payload required", ErrPOSRegistryInvalidPayload)
	}
	var packed anypb.Any
	if err := proto.Unmarshal(tx.Data, &packed); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrPOSRegistryInvalidPayload, err)
	}
	unpack := func(msg proto.Message) error {
		if err := packed.UnmarshalTo(msg); err != nil {
			return fmt.Errorf("%w: decode %s: %v", ErrPOSRegistryInvalidPayload, packed.GetTypeUrl(), err)
		}
		return nil
	}

	manager := nhbstate.NewManager(sp.Trie)
	registry := pos.NewRegistry(manager)
	// The registry keeps one nonce per authority, named by the signer.
	authority := common.BytesToAddress(signer).Hex()

	switch {
	case packed.MessageIs(&posv1.MsgRegisterMerchant{}):
		var msg posv1.MsgRegisterMerchant
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryAuthority(manager, signer, msg.GetMerchantAddr()); err != nil {
			return err
		}
		if _, err := registry.UpsertMerchant(authority, msg.GetMerchantAddr(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	case packed.MessageIs(&posv1.MsgPauseMerchant{}):
		var msg posv1.MsgPauseMerchant
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryAuthority(manager, signer, msg.GetMerchantAddr()); err != nil {
			return err
		}
		if _, err := registry.PauseMerchant(authority, msg.GetMerchantAddr(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	case packed.MessageIs(&posv1.MsgResumeMerchant{}):
		var msg posv1.MsgResumeMerchant
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryAuthority(manager, signer, msg.GetMerchantAddr()); err != nil {
			return err
		}
		if _, err := registry.ResumeMerchant(authority, msg.GetMerchantAddr(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	case packed.MessageIs(&posv1.MsgRegisterDevice{}):
		var msg posv1.MsgRegisterDevice
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryAuthority(manager, signer, msg.GetMerchantAddr()); err != nil {
			return err
		}
		// Registering an existing device again moves it to the merchant named
		// here, so the merchant it is bound to now has to agree as well: a
		// merchant cannot take over another merchant's device.
		if existing, ok, err := registry.Device(msg.GetDeviceId()); err != nil {
			return err
		} else if ok && existing != nil {
			if err := requirePOSRegistryAuthority(manager, signer, existing.Merchant); err != nil {
				return err
			}
		}
		if _, err := registry.RegisterDevice(authority, msg.GetDeviceId(), msg.GetMerchantAddr(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	case packed.MessageIs(&posv1.MsgRevokeDevice{}):
		var msg posv1.MsgRevokeDevice
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryDeviceAuthority(registry, manager, signer, msg.GetDeviceId(), msg.GetMerchantAddr()); err != nil {
			return err
		}
		if _, err := registry.RevokeDevice(authority, msg.GetDeviceId(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	case packed.MessageIs(&posv1.MsgRestoreDevice{}):
		var msg posv1.MsgRestoreDevice
		if err := unpack(&msg); err != nil {
			return err
		}
		if err := requirePOSRegistrySignerIsAuthority(signer, msg.GetAuthority()); err != nil {
			return err
		}
		if err := requirePOSRegistryDeviceAuthority(registry, manager, signer, msg.GetDeviceId(), msg.GetMerchantAddr()); err != nil {
			return err
		}
		if _, err := registry.RestoreDevice(authority, msg.GetDeviceId(), msg.GetNonce(), msg.GetExpiresAt(), msg.GetChainId()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported message %q", ErrPOSRegistryInvalidPayload, packed.GetTypeUrl())
	}
	return sp.incrementNativeAccountNonce(signer)
}

// requirePOSRegistryDeviceAuthority applies the authority rule to a change of
// an existing device: the merchant that matters is the one the device is bound
// to. A merchant named in the message must be that merchant.
func requirePOSRegistryDeviceAuthority(registry *pos.Registry, manager *nhbstate.Manager, signer []byte, deviceID, namedMerchant string) error {
	device, ok, err := registry.Device(deviceID)
	if err != nil {
		return err
	}
	if !ok || device == nil {
		return fmt.Errorf("%w: %s", pos.ErrDeviceNotRegistered, strings.TrimSpace(deviceID))
	}
	if named := strings.ToLower(strings.TrimSpace(namedMerchant)); named != "" && named != device.Merchant {
		return fmt.Errorf("%w: device %s is bound to a different merchant than %q", ErrPOSRegistryUnauthorized, strings.TrimSpace(deviceID), strings.TrimSpace(namedMerchant))
	}
	return requirePOSRegistryAuthority(manager, signer, device.Merchant)
}
