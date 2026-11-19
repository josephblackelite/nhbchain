package core

import (
	"errors"
	"testing"

	posv1 "nhbchain/proto/pos"
)

// PL-R5T-66: device ids are one global first-come namespace. Any account can register
// an unused id under its own address, the real merchant is then refused, and only a
// ROLE_POS_REGISTRY_ADMIN holder can evict. These tests state that as the live rule so
// that any change to it (per-merchant ids, an id-prefix rule) has to be made behind an
// activation height and updates them on purpose.
func TestPOSRegistryDeviceIDsAreFirstComeAndOnlyAnAdminEvicts(t *testing.T) {
	fx := newRegistryFixture(t)
	a, outsider := fx.addr("merchantA"), fx.addr("outsider")

	// First come: an account with no merchant record, role or balance takes the id.
	if err := fx.send("outsider", &posv1.MsgRegisterDevice{MerchantAddr: outsider, DeviceId: "till-1", Nonce: fx.next("outsider")}); err != nil {
		t.Fatalf("squatter registers an unused id under its own address: %v", err)
	}
	// The real merchant is refused, whichever address it names.
	for _, named := range []string{a, outsider} {
		err := fx.send("merchantA", &posv1.MsgRegisterDevice{MerchantAddr: named, DeviceId: "till-1", Nonce: fx.next("merchantA")})
		if !errors.Is(err, ErrPOSRegistryUnauthorized) {
			t.Fatalf("real merchant registering the squatted id under %q: got %v, want ErrPOSRegistryUnauthorized", named, err)
		}
	}
	// The squatter holds the id and can revoke it.
	if err := fx.send("outsider", &posv1.MsgRevokeDevice{DeviceId: "till-1", Nonce: fx.next("outsider")}); err != nil {
		t.Fatalf("squatter revokes the id: %v", err)
	}
	if dev := fx.device("till-1"); dev == nil || dev.Merchant != outsider || !dev.Revoked {
		t.Fatalf("the squatted id: %+v", dev)
	}

	// Nobody but the admin can evict: not the real merchant, not another outsider's role-less call.
	if err := fx.send("merchantB", &posv1.MsgRegisterDevice{MerchantAddr: fx.addr("merchantB"), DeviceId: "till-1", Nonce: fx.next("merchantB")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("a second merchant taking the squatted id: got %v", err)
	}

	// The admin moves the id to the real merchant. The revoked flag carries over, so the
	// admin restores it, and from then on the real merchant runs it.
	if err := fx.send("admin", &posv1.MsgRegisterDevice{MerchantAddr: a, DeviceId: "till-1", Nonce: fx.next("admin")}); err != nil {
		t.Fatalf("admin evicts the squatter by re-registering the id: %v", err)
	}
	if dev := fx.device("till-1"); dev == nil || dev.Merchant != a || !dev.Revoked {
		t.Fatalf("after the eviction: %+v, want bound to the real merchant and still revoked", dev)
	}
	if err := fx.send("admin", &posv1.MsgRestoreDevice{DeviceId: "till-1", Nonce: fx.next("admin")}); err != nil {
		t.Fatalf("admin restores the id: %v", err)
	}
	if dev := fx.device("till-1"); dev == nil || dev.Merchant != a || dev.Revoked {
		t.Fatalf("after the restore: %+v", dev)
	}
	if err := fx.send("merchantA", &posv1.MsgRevokeDevice{DeviceId: "till-1", Nonce: fx.next("merchantA")}); err != nil {
		t.Fatalf("the real merchant now runs its device: %v", err)
	}
	// And the squatter no longer can.
	if err := fx.send("outsider", &posv1.MsgRestoreDevice{DeviceId: "till-1", Nonce: fx.next("outsider")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("the evicted squatter restoring the id: got %v", err)
	}
}

// An admin can register the real merchants' ids before anyone else does.
func TestPOSRegistryAdminCanPreRegisterDeviceIDs(t *testing.T) {
	fx := newRegistryFixture(t)
	a := fx.addr("merchantA")
	if err := fx.send("admin", &posv1.MsgRegisterDevice{MerchantAddr: a, DeviceId: "till-7", Nonce: fx.next("admin")}); err != nil {
		t.Fatalf("admin pre-registers: %v", err)
	}
	if err := fx.send("outsider", &posv1.MsgRegisterDevice{MerchantAddr: fx.addr("outsider"), DeviceId: "till-7", Nonce: fx.next("outsider")}); !errors.Is(err, ErrPOSRegistryUnauthorized) {
		t.Fatalf("a squatter taking a pre-registered id: got %v", err)
	}
}
