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
	if err := registry.SetPaymaster(businessID, owner, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("expected missing consent error, got %v", err)
	}
	if err := registry.SetPaymaster(businessID, paymaster, paymaster); err != nil {
		t.Fatalf("paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, owner, paymaster); err != nil {
		t.Fatalf("owner set paymaster: %v", err)
	}
	stored, ok := registry.PrimaryPaymaster(owner)
	if !ok {
		t.Fatalf("expected paymaster to be registered")
	}
	if stored != paymaster {
		t.Fatalf("unexpected paymaster returned")
	}
	var admin [20]byte
	admin[0] = 0xDD
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	var newPaymaster [20]byte
	newPaymaster[0] = 0xEE
	if err := registry.SetPaymaster(businessID, newPaymaster, newPaymaster); err != nil {
		t.Fatalf("new paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, admin, newPaymaster); err != nil {
		t.Fatalf("admin set paymaster: %v", err)
	}
	stored, ok = registry.PrimaryPaymaster(owner)
	if !ok || stored != newPaymaster {
		t.Fatalf("primary paymaster mismatch")
	}
}

func TestSetPaymasterSingleActivePerOwner(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x11
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
	if err := registry.SetPaymaster(firstID, firstPaymaster, firstPaymaster); err != nil {
		t.Fatalf("first paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(firstID, owner, firstPaymaster); err != nil {
		t.Fatalf("set first paymaster: %v", err)
	}
	var secondPaymaster [20]byte
	secondPaymaster[0] = 0x22
	if err := registry.SetPaymaster(secondID, secondPaymaster, secondPaymaster); err != nil {
		t.Fatalf("second paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(secondID, owner, secondPaymaster); !errors.Is(err, loyalty.ErrPaymasterConflict) {
		t.Fatalf("expected paymaster conflict, got %v", err)
	}
	var zeroAddr [20]byte
	if err := registry.SetPaymaster(firstID, owner, zeroAddr); err != nil {
		t.Fatalf("clear first paymaster: %v", err)
	}
	if err := registry.SetPaymaster(secondID, owner, secondPaymaster); err != nil {
		t.Fatalf("set second paymaster: %v", err)
	}
	stored, ok := registry.PrimaryPaymaster(owner)
	if !ok || stored != secondPaymaster {
		t.Fatalf("expected primary paymaster to be second")
	}
}

func TestSetPaymasterEmitsRotationEvent(t *testing.T) {
	registry, _ := newTestRegistry(t)
	var owner [20]byte
	owner[0] = 0x21
	businessID, err := registry.RegisterBusiness(owner, "Example")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	var first [20]byte
	first[0] = 0x31
	var second [20]byte
	second[0] = 0x32
	if err := registry.SetPaymaster(businessID, first, first); err != nil {
		t.Fatalf("first paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, second, second); err != nil {
		t.Fatalf("second paymaster opt-in: %v", err)
	}
	emitter := &capturingEmitter{}
	registry.SetEmitter(emitter)

	if err := registry.SetPaymaster(businessID, owner, first); err != nil {
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

	if err := registry.SetPaymaster(businessID, owner, second); err != nil {
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

func TestSetPaymasterConsentIsPerBusinessAndSingleUse(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var ownerA, ownerB, paymaster, admin [20]byte
	ownerA[0], ownerB[0], paymaster[0], admin[0] = 0x51, 0x52, 0x53, 0x54
	idA, err := registry.RegisterBusiness(ownerA, "A")
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	idB, err := registry.RegisterBusiness(ownerB, "B")
	if err != nil {
		t.Fatalf("register B: %v", err)
	}

	// The opt-in for one business does not let another name the same address.
	if err := registry.SetPaymaster(idA, paymaster, paymaster); err != nil {
		t.Fatalf("opt-in for A: %v", err)
	}
	if err := registry.SetPaymaster(idB, ownerB, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("other business must not use the opt-in, got %v", err)
	}
	// An opt-in alone assigns nothing.
	if _, ok := registry.PrimaryPaymaster(ownerA); ok {
		t.Fatalf("opt-in must not assign the paymaster")
	}
	if err := registry.SetPaymaster(idA, ownerA, paymaster); err != nil {
		t.Fatalf("assign consenting paymaster: %v", err)
	}

	// The opt-in is consumed: after rotating away, naming it again needs a
	// fresh opt-in.
	var other [20]byte
	other[0] = 0x55
	if err := registry.SetPaymaster(idA, other, other); err != nil {
		t.Fatalf("opt-in for other: %v", err)
	}
	if err := registry.SetPaymaster(idA, ownerA, other); err != nil {
		t.Fatalf("rotate to other: %v", err)
	}
	if err := registry.SetPaymaster(idA, ownerA, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("consumed opt-in must not be reusable, got %v", err)
	}

	// The owner and the calling admin never need a separate opt-in.
	if err := registry.SetPaymaster(idA, ownerA, ownerA); err != nil {
		t.Fatalf("owner as its own paymaster: %v", err)
	}
	if err := manager.SetRole("ROLE_LOYALTY_ADMIN", admin[:]); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	if err := registry.SetPaymaster(idA, admin, admin); err != nil {
		t.Fatalf("admin as paymaster: %v", err)
	}
	// Clearing the paymaster never needs consent.
	var zero [20]byte
	if err := registry.SetPaymaster(idA, ownerA, zero); err != nil {
		t.Fatalf("clear paymaster: %v", err)
	}
	// An outsider can only opt itself in, never name someone else, and cannot
	// clear the paymaster.
	var outsider [20]byte
	outsider[0] = 0x56
	if err := registry.SetPaymaster(idA, outsider, paymaster); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("outsider naming another address: want unauthorized, got %v", err)
	}
	if err := registry.SetPaymaster(idA, outsider, zero); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("outsider clearing: want unauthorized, got %v", err)
	}
	// An opt-in needs a business to exist.
	var missing loyalty.BusinessID
	missing[0] = 0xFF
	if err := registry.SetPaymaster(missing, outsider, outsider); !errors.Is(err, loyalty.ErrBusinessNotFound) {
		t.Fatalf("opt-in for unknown business: want not found, got %v", err)
	}
}
