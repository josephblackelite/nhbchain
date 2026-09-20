package core

import (
	"errors"
	"math/big"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	posv1 "nhbchain/proto/pos"
)

// registryFixture is a processor with four accounts: a registry admin (holder
// of RolePOSRegistryAdmin), two merchants that each own their own address, and
// an outsider with no role. Every registry message an authority sends carries
// the next registry nonce for that authority.
type registryFixture struct {
	t        *testing.T
	sp       *StateProcessor
	manager  *nhbstate.Manager
	keys     map[string]*crypto.PrivateKey
	addrs    map[string][20]byte
	txNonce  map[string]uint64
	regNonce map[string]uint64
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	fx := &registryFixture{
		t:        t,
		sp:       sp,
		manager:  nhbstate.NewManager(sp.Trie),
		keys:     map[string]*crypto.PrivateKey{},
		addrs:    map[string][20]byte{},
		txNonce:  map[string]uint64{},
		regNonce: map[string]uint64{},
	}
	for _, name := range []string{"admin", "merchantA", "merchantB", "outsider"} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate %s key: %v", name, err)
		}
		var addr [20]byte
		copy(addr[:], key.PubKey().Address().Bytes())
		fx.keys[name], fx.addrs[name] = key, addr
		writeAccount(t, sp, addr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}
	admin := fx.addrs["admin"]
	if err := fx.manager.SetRole(RolePOSRegistryAdmin, admin[:]); err != nil {
		t.Fatalf("grant registry admin role: %v", err)
	}
	return fx
}

// addr is the bech32 form of a named actor, the form a merchant address takes.
func (fx *registryFixture) addr(name string) string {
	a := fx.addrs[name]
	return crypto.MustNewAddress(crypto.NHBPrefix, a[:]).String()
}

// send packs msg the way consensus/codec does (a serialized Any), signs it as
// who and applies it as the next transaction of that account.
func (fx *registryFixture) send(who string, msg proto.Message) error {
	fx.t.Helper()
	packed, err := anypb.New(msg)
	if err != nil {
		fx.t.Fatalf("pack message: %v", err)
	}
	return fx.sendData(who, mustMarshal(fx.t, packed))
}

func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func (fx *registryFixture) sendData(who string, data []byte) error {
	fx.t.Helper()
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypePOSRegistry, Nonce: fx.txNonce[who], Data: data, GasLimit: 100_000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(fx.keys[who].PrivateKey); err != nil {
		fx.t.Fatalf("sign registry tx: %v", err)
	}
	err := fx.sp.ApplyTransaction(tx)
	if err == nil {
		fx.txNonce[who]++
	}
	return err
}

// next returns the registry nonce the next message of who must carry.
func (fx *registryFixture) next(who string) uint64 {
	fx.regNonce[who]++
	return fx.regNonce[who]
}

func (fx *registryFixture) accountNonce(who string) uint64 {
	fx.t.Helper()
	a := fx.addrs[who]
	account, err := fx.sp.getAccount(a[:])
	if err != nil {
		fx.t.Fatalf("load %s: %v", who, err)
	}
	return account.Nonce
}

func (fx *registryFixture) merchant(addr string) *pos.Merchant {
	fx.t.Helper()
	record, ok, err := fx.manager.POSGetMerchant(addr)
	if err != nil {
		fx.t.Fatalf("load merchant: %v", err)
	}
	if !ok {
		return nil
	}
	return record
}

func (fx *registryFixture) device(id string) *pos.Device {
	fx.t.Helper()
	record, ok, err := fx.manager.POSGetDevice(id)
	if err != nil {
		fx.t.Fatalf("load device: %v", err)
	}
	if !ok {
		return nil
	}
	return record
}

// A merchant runs its own registry entry: it registers its address, pauses
// itself and resumes. Each message is told apart by its type alone -- pause and
// resume have exactly the fields of a merchant registration, so a handler that
// guessed the type from the bytes would take them all for a registration.
func TestPOSRegistryMerchantOwnerRegistersPausesAndResumesItself(t *testing.T) {
	fx := newRegistryFixture(t)
	merchant := fx.addr("merchantA")

	if err := fx.send("merchantA", &posv1.MsgRegisterMerchant{MerchantAddr: merchant, Nonce: fx.next("merchantA"), ChainId: "1"}); err != nil {
		t.Fatalf("register merchant: %v", err)
	}
	if rec := fx.merchant(merchant); rec == nil || rec.Paused {
		t.Fatalf("expected an active merchant record after registration, got %+v", rec)
	}
	if err := fx.send("merchantA", &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: fx.next("merchantA"), ChainId: "1"}); err != nil {
		t.Fatalf("pause merchant: %v", err)
	}
	if rec := fx.merchant(merchant); rec == nil || !rec.Paused {
		t.Fatalf("expected the merchant to be paused after MsgPauseMerchant, got %+v", rec)
	}
	if err := fx.send("merchantA", &posv1.MsgResumeMerchant{MerchantAddr: merchant, Nonce: fx.next("merchantA"), ChainId: "1"}); err != nil {
		t.Fatalf("resume merchant: %v", err)
	}
	if rec := fx.merchant(merchant); rec == nil || rec.Paused {
		t.Fatalf("expected the merchant to be active again after MsgResumeMerchant, got %+v", rec)
	}
	if got := fx.accountNonce("merchantA"); got != 3 {
		t.Fatalf("merchant account nonce = %d, want 3 (one per registry transaction)", got)
	}
	// Registering a merchant does not register a device as a side effect.
	if dev := fx.device("unknown"); dev != nil {
		t.Fatalf("registering a merchant created a device record: %+v", dev)
	}
}

// Nobody but the merchant's owner or a registry admin may pause a merchant,
// revoke its devices or bind a device to it: an outsider used to be able to
// switch off any merchant's sponsorship.
func TestPOSRegistryRefusesAnOutsider(t *testing.T) {
	fx := newRegistryFixture(t)
	merchant := fx.addr("merchantA")
	if err := fx.send("merchantA", &posv1.MsgRegisterMerchant{MerchantAddr: merchant, Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("register merchant: %v", err)
	}
	if err := fx.send("merchantA", &posv1.MsgRegisterDevice{MerchantAddr: merchant, DeviceId: "terminal-1", Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("register device: %v", err)
	}

	attempts := map[string]proto.Message{
		"pause":            &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: fx.next("outsider")},
		"resume":           &posv1.MsgResumeMerchant{MerchantAddr: merchant, Nonce: fx.next("outsider")},
		"register":         &posv1.MsgRegisterMerchant{MerchantAddr: merchant, Nonce: fx.next("outsider")},
		"bind a device":    &posv1.MsgRegisterDevice{MerchantAddr: merchant, DeviceId: "terminal-2", Nonce: fx.next("outsider")},
		"revoke a device":  &posv1.MsgRevokeDevice{MerchantAddr: merchant, DeviceId: "terminal-1", Nonce: fx.next("outsider")},
		"restore a device": &posv1.MsgRestoreDevice{MerchantAddr: merchant, DeviceId: "terminal-1", Nonce: fx.next("outsider")},
	}
	for name, msg := range attempts {
		if err := fx.send("outsider", msg); !errors.Is(err, ErrPOSRegistryUnauthorized) {
			t.Fatalf("outsider %s: got %v, want ErrPOSRegistryUnauthorized", name, err)
		}
	}
	if rec := fx.merchant(merchant); rec == nil || rec.Paused {
		t.Fatalf("the outsider changed the merchant: %+v", rec)
	}
	if dev := fx.device("terminal-1"); dev == nil || dev.Revoked {
		t.Fatalf("the outsider changed the device: %+v", dev)
	}
	if dev := fx.device("terminal-2"); dev != nil {
		t.Fatalf("the outsider bound a device to the merchant: %+v", dev)
	}
	if got := fx.accountNonce("outsider"); got != 0 {
		t.Fatalf("refused transactions advanced the outsider's nonce to %d", got)
	}
}

// The authority field of a message is not a way to borrow another account's
// authority: a message naming the registry admin as its authority but signed
// by someone else is refused as forged, whatever it asks for, and so is one
// that names another merchant's owner. The same field naming the true signer
// is accepted.
func TestPOSRegistryForgedAuthorityIsRefused(t *testing.T) {
	fx := newRegistryFixture(t)
	victim := fx.addr("merchantA")
	own := fx.addr("outsider")

	forged := map[string]proto.Message{
		"admin as authority, victim merchant": &posv1.MsgPauseMerchant{Authority: fx.addr("admin"), MerchantAddr: victim, Nonce: fx.next("outsider")},
		"admin as authority, own merchant":    &posv1.MsgRegisterMerchant{Authority: fx.addr("admin"), MerchantAddr: own, Nonce: fx.next("outsider")},
		"victim as authority, victim device":  &posv1.MsgRegisterDevice{Authority: victim, MerchantAddr: victim, DeviceId: "d", Nonce: fx.next("outsider")},
		"authority that is not an address":    &posv1.MsgPauseMerchant{Authority: "admin", MerchantAddr: own, Nonce: fx.next("outsider")},
	}
	for name, msg := range forged {
		if err := fx.send("outsider", msg); !errors.Is(err, ErrPOSRegistryInvalidPayload) {
			t.Fatalf("%s: got %v, want ErrPOSRegistryInvalidPayload", name, err)
		}
	}
	if rec := fx.merchant(victim); rec != nil {
		t.Fatalf("a forged message reached the victim merchant: %+v", rec)
	}
	if rec := fx.merchant(own); rec != nil {
		t.Fatalf("a forged message registered a merchant: %+v", rec)
	}

	if err := fx.send("outsider", &posv1.MsgRegisterMerchant{Authority: own, MerchantAddr: own, Nonce: fx.next("outsider")}); err != nil {
		t.Fatalf("a message whose authority is its true signer must be accepted: %v", err)
	}
}

// A registry admin may change any merchant and any device.
func TestPOSRegistryAdminMayChangeAnyEntry(t *testing.T) {
	fx := newRegistryFixture(t)
	merchant := fx.addr("merchantB")

	steps := []struct {
		name string
		msg  proto.Message
		then func()
	}{
		{"register merchant", &posv1.MsgRegisterMerchant{MerchantAddr: merchant, Nonce: fx.next("admin")}, func() {
			if fx.merchant(merchant) == nil {
				t.Fatalf("merchant not registered")
			}
		}},
		{"pause merchant", &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: fx.next("admin")}, func() {
			if rec := fx.merchant(merchant); rec == nil || !rec.Paused {
				t.Fatalf("merchant not paused: %+v", rec)
			}
		}},
		{"resume merchant", &posv1.MsgResumeMerchant{MerchantAddr: merchant, Nonce: fx.next("admin")}, func() {
			if rec := fx.merchant(merchant); rec == nil || rec.Paused {
				t.Fatalf("merchant not resumed: %+v", rec)
			}
		}},
		{"register device", &posv1.MsgRegisterDevice{MerchantAddr: merchant, DeviceId: "till-9", Nonce: fx.next("admin")}, func() {
			if dev := fx.device("till-9"); dev == nil || dev.Merchant != merchant {
				t.Fatalf("device not bound to the merchant: %+v", dev)
			}
		}},
		{"revoke device", &posv1.MsgRevokeDevice{DeviceId: "till-9", Nonce: fx.next("admin")}, func() {
			if dev := fx.device("till-9"); dev == nil || !dev.Revoked {
				t.Fatalf("device not revoked: %+v", dev)
			}
		}},
		{"restore device", &posv1.MsgRestoreDevice{DeviceId: "till-9", Nonce: fx.next("admin")}, func() {
			if dev := fx.device("till-9"); dev == nil || dev.Revoked {
				t.Fatalf("device not restored: %+v", dev)
			}
		}},
	}
	for _, step := range steps {
		if err := fx.send("admin", step.msg); err != nil {
			t.Fatalf("admin %s: %v", step.name, err)
		}
		step.then()
	}
}

// A merchant runs its own devices and cannot touch another merchant's: it may
// not bind a device that belongs to someone else, nor revoke or restore it.
func TestPOSRegistryDevicesBelongToTheirMerchant(t *testing.T) {
	fx := newRegistryFixture(t)
	a, b := fx.addr("merchantA"), fx.addr("merchantB")

	if err := fx.send("merchantA", &posv1.MsgRegisterDevice{MerchantAddr: a, DeviceId: "shared-id", Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("merchant A registers its device: %v", err)
	}
	if err := fx.send("merchantB", &posv1.MsgRegisterDevice{MerchantAddr: b, DeviceId: "shared-id", Nonce: fx.next("merchantB")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("merchant B taking over A's device: got %v, want ErrPOSRegistryUnauthorized", err)
	}
	if err := fx.send("merchantB", &posv1.MsgRevokeDevice{DeviceId: "shared-id", Nonce: fx.next("merchantB")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("merchant B revoking A's device: got %v, want ErrPOSRegistryUnauthorized", err)
	}
	if err := fx.send("merchantA", &posv1.MsgRevokeDevice{MerchantAddr: b, DeviceId: "shared-id", Nonce: fx.next("merchantA")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("revoking a device under another merchant's name: got %v, want ErrPOSRegistryUnauthorized", err)
	}
	if dev := fx.device("shared-id"); dev == nil || dev.Merchant != a || dev.Revoked {
		t.Fatalf("device changed by the wrong merchant: %+v", dev)
	}

	if err := fx.send("merchantA", &posv1.MsgRevokeDevice{MerchantAddr: a, DeviceId: "shared-id", Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("merchant A revokes its own device: %v", err)
	}
	if dev := fx.device("shared-id"); dev == nil || !dev.Revoked {
		t.Fatalf("device not revoked: %+v", dev)
	}
	if err := fx.send("merchantA", &posv1.MsgRestoreDevice{DeviceId: "shared-id", Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("merchant A restores its own device: %v", err)
	}
	if err := fx.send("merchantA", &posv1.MsgRevokeDevice{DeviceId: "never-registered", Nonce: fx.next("merchantA")}); !errors.Is(err, pos.ErrDeviceNotRegistered) {
		t.Fatalf("revoking an unknown device: got %v, want pos.ErrDeviceNotRegistered", err)
	}
}

// The transaction names its message explicitly. Bytes that are not a packed
// registry message, an empty payload and a packed message of another module
// are refused as invalid rather than guessed at.
func TestPOSRegistryPayloadMustNameARegistryMessage(t *testing.T) {
	fx := newRegistryFixture(t)
	merchant := fx.addr("merchantA")

	// A bare registry message, without the Any that names its type, used to be
	// decoded by trial. It is no longer a valid payload.
	bare := mustMarshal(t, &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: 1})
	other, err := anypb.New(&posv1.MsgAuthorizePayment{Payer: merchant, Merchant: merchant, Amount: "1"})
	if err != nil {
		t.Fatalf("pack other message: %v", err)
	}
	for name, data := range map[string][]byte{
		"empty":                    nil,
		"garbage":                  {0xff, 0xff, 0xff, 0x01},
		"bare registry message":    bare,
		"another module's message": mustMarshal(t, other),
	} {
		if err := fx.sendData("merchantA", data); !errors.Is(err, ErrPOSRegistryInvalidPayload) {
			t.Fatalf("%s payload: got %v, want ErrPOSRegistryInvalidPayload", name, err)
		}
	}
	if rec := fx.merchant(merchant); rec != nil {
		t.Fatalf("an invalid payload changed the registry: %+v", rec)
	}
}

// A registry nonce at or below the authority's last is stale, and a refused
// message does not consume it.
func TestPOSRegistryNonceOnlyMovesForward(t *testing.T) {
	fx := newRegistryFixture(t)
	merchant := fx.addr("merchantA")
	if err := fx.send("merchantA", &posv1.MsgRegisterMerchant{MerchantAddr: merchant, Nonce: 5}); err != nil {
		t.Fatalf("register merchant: %v", err)
	}
	if err := fx.send("merchantA", &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: 5}); !errors.Is(err, pos.ErrStaleNonce) {
		t.Fatalf("reusing a registry nonce: got %v, want pos.ErrStaleNonce", err)
	}
	if err := fx.send("merchantA", &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: 0}); !errors.Is(err, pos.ErrInvalidRequest) {
		t.Fatalf("a zero registry nonce: got %v, want pos.ErrInvalidRequest", err)
	}
	if err := fx.send("merchantA", &posv1.MsgPauseMerchant{MerchantAddr: merchant, Nonce: 6}); err != nil {
		t.Fatalf("pause at the next nonce: %v", err)
	}
}
