package loyalty_test

import (
	"errors"
	"testing"

	"nhbchain/core/events"
	loyalty "nhbchain/native/loyalty"
)

func TestSetPaymasterRequiresAuthorization(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0xAA
	businessID, err := registry.RegisterBusiness(owner, "Acme Corp")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	var paymaster [20]byte
	paymaster[0] = 0xBB
	var outsider [20]byte
	outsider[0] = 0xCC
	if err := registry.SetPaymaster(businessID, outsider, paymaster); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}
	// The owner may commit only its own wallet: naming another one would let
	// it spend that wallet's funds without its say.
	if err := registry.SetPaymaster(businessID, owner, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("expected paymaster consent error, got %v", err)
	}
	if _, ok := registry.PrimaryPaymaster(owner); ok {
		t.Fatalf("a rejected paymaster assignment must leave the business without one")
	}
	if err := registry.SetPaymaster(businessID, owner, owner); err != nil {
		t.Fatalf("owner set its own wallet as paymaster: %v", err)
	}
	stored, ok := registry.PrimaryPaymaster(owner)
	if !ok {
		t.Fatalf("expected paymaster to be registered")
	}
	if stored != owner {
		t.Fatalf("unexpected paymaster returned")
	}
	var admin [20]byte
	admin[0] = 0xDD
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	var newPaymaster [20]byte
	newPaymaster[0] = 0xEE
	if err := registry.SetPaymaster(businessID, admin, newPaymaster); err != nil {
		t.Fatalf("admin set paymaster: %v", err)
	}
	stored, ok = registry.PrimaryPaymaster(owner)
	if !ok || stored != newPaymaster {
		t.Fatalf("primary paymaster mismatch")
	}
}

func TestSetPaymasterSingleActivePerOwner(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x11
	var admin [20]byte
	admin[0] = 0x12
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	firstID, err := registry.RegisterBusiness(owner, "First")
	if err != nil {
		t.Fatalf("register first business: %v", err)
	}
	secondID, err := registry.RegisterBusiness(owner, "Second")
	if err != nil {
		t.Fatalf("register second business: %v", err)
	}
	var firstPaymaster [20]byte
	firstPaymaster[0] = 0x21
	if err := registry.SetPaymaster(firstID, admin, firstPaymaster); err != nil {
		t.Fatalf("set first paymaster: %v", err)
	}
	var secondPaymaster [20]byte
	secondPaymaster[0] = 0x22
	if err := registry.SetPaymaster(secondID, admin, secondPaymaster); !errors.Is(err, loyalty.ErrPaymasterConflict) {
		t.Fatalf("expected paymaster conflict, got %v", err)
	}
	var zeroAddr [20]byte
	if err := registry.SetPaymaster(firstID, owner, zeroAddr); err != nil {
		t.Fatalf("clear first paymaster: %v", err)
	}
	if err := registry.SetPaymaster(secondID, admin, secondPaymaster); err != nil {
		t.Fatalf("set second paymaster: %v", err)
	}
	stored, ok := registry.PrimaryPaymaster(owner)
	if !ok || stored != secondPaymaster {
		t.Fatalf("expected primary paymaster to be second")
	}
}

func TestSetPaymasterEmitsRotationEvent(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x21
	var admin [20]byte
	admin[0] = 0x22
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	businessID, err := registry.RegisterBusiness(owner, "Example")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	var first [20]byte
	first[0] = 0x31
	emitter := &capturingEmitter{}
	registry.SetEmitter(emitter)

	if err := registry.SetPaymaster(businessID, admin, first); err != nil {
		t.Fatalf("set initial paymaster: %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	evt, ok := emitter.events[0].(events.LoyaltyPaymasterRotated)
	if !ok {
		t.Fatalf("unexpected event type %T", emitter.events[0])
	}
	if evt.NewPaymaster != first {
		t.Fatalf("expected new paymaster recorded in event")
	}

	var second [20]byte
	second[0] = 0x32
	if err := registry.SetPaymaster(businessID, admin, second); err != nil {
		t.Fatalf("rotate paymaster: %v", err)
	}
	if len(emitter.events) != 2 {
		t.Fatalf("expected two events, got %d", len(emitter.events))
	}
	evt, ok = emitter.events[1].(events.LoyaltyPaymasterRotated)
	if !ok {
		t.Fatalf("unexpected event type %T", emitter.events[1])
	}
	if evt.OldPaymaster != first || evt.NewPaymaster != second {
		t.Fatalf("unexpected event payload %#v", evt)
	}
}

// TestSetPaymasterConsent pins down who may name which wallet as a business's
// paymaster. Rewards are debited from the paymaster's own balance and the
// named wallet never signs the call, so an owner may commit only its own
// wallet (or clear the paymaster); a loyalty admin, the role trusted with
// assigning wallets, may name any. An outsider is refused whatever it names.
func TestSetPaymasterConsent(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var owner, other, admin, outsider [20]byte
	owner[0], other[0], admin[0], outsider[0] = 0x61, 0x62, 0x63, 0x64
	businessID, err := registry.RegisterBusiness(owner, "Consent Corp")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	paymaster := func() ([20]byte, bool) { return registry.PrimaryPaymaster(owner) }

	if err := registry.SetPaymaster(businessID, owner, other); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("owner naming another wallet: err = %v, want ErrPaymasterConsentRequired", err)
	}
	if err := registry.SetPaymaster(businessID, outsider, outsider); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("outsider naming itself: err = %v, want ErrUnauthorized", err)
	}
	if _, ok := paymaster(); ok {
		t.Fatalf("a refused assignment left a paymaster behind")
	}

	if err := registry.SetPaymaster(businessID, owner, owner); err != nil {
		t.Fatalf("owner naming its own wallet: %v", err)
	}
	if got, ok := paymaster(); !ok || got != owner {
		t.Fatalf("paymaster = %x (set=%v), want the owner's own wallet", got, ok)
	}
	// Re-affirming the current paymaster is still a request about that wallet.
	if err := registry.SetPaymaster(businessID, owner, other); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("owner replacing its own wallet with another: err = %v, want ErrPaymasterConsentRequired", err)
	}
	if err := registry.SetPaymaster(businessID, owner, [20]byte{}); err != nil {
		t.Fatalf("owner clearing the paymaster: %v", err)
	}
	if _, ok := paymaster(); ok {
		t.Fatalf("paymaster still set after the owner cleared it")
	}

	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	if err := registry.SetPaymaster(businessID, admin, other); err != nil {
		t.Fatalf("loyalty admin naming a wallet: %v", err)
	}
	if got, ok := paymaster(); !ok || got != other {
		t.Fatalf("paymaster = %x (set=%v), want the wallet the admin named", got, ok)
	}

	// An owner that also holds the role may name any wallet as well.
	if err := manager.SetRole(roleLoyaltyAdmin, owner[:]); err != nil {
		t.Fatalf("assign owner the admin role: %v", err)
	}
	if err := registry.SetPaymaster(businessID, owner, admin); err != nil {
		t.Fatalf("owner holding the admin role naming a wallet: %v", err)
	}
	if got, ok := paymaster(); !ok || got != admin {
		t.Fatalf("paymaster = %x (set=%v), want the wallet the admin-role owner named", got, ok)
	}
}
