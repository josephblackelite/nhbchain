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
	// The wallet's own opt-in assigns nothing, and does not let the owner name
	// it either: only a loyalty admin may assign a wallet other than the
	// owner's own.
	if err := registry.SetPaymaster(businessID, paymaster, paymaster); err != nil {
		t.Fatalf("paymaster opt-in: %v", err)
	}
	if _, ok := registry.PrimaryPaymaster(owner); ok {
		t.Fatalf("an opt-in must not assign the paymaster")
	}
	if err := registry.SetPaymaster(businessID, owner, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("owner naming a wallet that opted in: err = %v, want ErrPaymasterConsentRequired", err)
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
	// Even a loyalty admin needs the named wallet's own opt-in.
	if err := registry.SetPaymaster(businessID, admin, newPaymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("admin naming a wallet without its opt-in: err = %v, want ErrPaymasterConsent", err)
	}
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
	if err := registry.SetPaymaster(firstID, firstPaymaster, firstPaymaster); err != nil {
		t.Fatalf("first paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(firstID, admin, firstPaymaster); err != nil {
		t.Fatalf("set first paymaster: %v", err)
	}
	var secondPaymaster [20]byte
	secondPaymaster[0] = 0x22
	if err := registry.SetPaymaster(secondID, secondPaymaster, secondPaymaster); err != nil {
		t.Fatalf("second paymaster opt-in: %v", err)
	}
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

// TestSetPaymasterConsentIsPerBusinessAndSingleUse covers the opt-in a wallet
// records for one business: it is per business, assigns nothing by itself and
// is consumed by the assignment it authorizes. A loyalty admin is the caller
// that assigns a wallet other than the owner's own.
func TestSetPaymasterConsentIsPerBusinessAndSingleUse(t *testing.T) {
	registry, manager := newTestRegistry(t)
	var ownerA, ownerB, paymaster, admin [20]byte
	ownerA[0], ownerB[0], paymaster[0], admin[0] = 0x51, 0x52, 0x53, 0x54
	if err := manager.SetRole(roleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
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
	if err := registry.SetPaymaster(idB, admin, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("other business must not use the opt-in, got %v", err)
	}
	// An opt-in alone assigns nothing.
	if _, ok := registry.PrimaryPaymaster(ownerA); ok {
		t.Fatalf("opt-in must not assign the paymaster")
	}
	if err := registry.SetPaymaster(idA, admin, paymaster); err != nil {
		t.Fatalf("assign consenting paymaster: %v", err)
	}

	// The opt-in is consumed: after rotating away, naming it again needs a
	// fresh opt-in.
	var other [20]byte
	other[0] = 0x55
	if err := registry.SetPaymaster(idA, other, other); err != nil {
		t.Fatalf("opt-in for other: %v", err)
	}
	if err := registry.SetPaymaster(idA, admin, other); err != nil {
		t.Fatalf("rotate to other: %v", err)
	}
	if err := registry.SetPaymaster(idA, admin, paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("consumed opt-in must not be reusable, got %v", err)
	}

	// The owner and the calling admin never need a separate opt-in for their
	// own wallets, and an admin may name the business's owner.
	if err := registry.SetPaymaster(idA, ownerA, ownerA); err != nil {
		t.Fatalf("owner as its own paymaster: %v", err)
	}
	if err := registry.SetPaymaster(idA, admin, admin); err != nil {
		t.Fatalf("admin as paymaster: %v", err)
	}
	if err := registry.SetPaymaster(idA, admin, ownerA); err != nil {
		t.Fatalf("admin naming the business owner: %v", err)
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

// TestSetPaymasterConsent pins down who may name which wallet as a business's
// paymaster. Rewards are debited from the paymaster's own balance and the
// named wallet never signs the call, so an owner may commit only its own
// wallet (or clear the paymaster). A loyalty admin, the role trusted with
// assigning wallets, may name another one only after that wallet has recorded
// its own opt-in. An outsider can assign nothing whatever it names.
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
	// An outsider naming itself only records the opt-in; nothing is assigned.
	if err := registry.SetPaymaster(businessID, outsider, outsider); err != nil {
		t.Fatalf("outsider recording its opt-in: %v", err)
	}
	if _, ok := paymaster(); ok {
		t.Fatalf("a refused assignment or an opt-in left a paymaster behind")
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
	if err := registry.SetPaymaster(businessID, admin, other); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("loyalty admin naming a wallet without its opt-in: err = %v, want ErrPaymasterConsent", err)
	}
	if _, ok := paymaster(); ok {
		t.Fatalf("a refused admin assignment left a paymaster behind")
	}
	if err := registry.SetPaymaster(businessID, other, other); err != nil {
		t.Fatalf("other recording its opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, admin, other); err != nil {
		t.Fatalf("loyalty admin naming a wallet that opted in: %v", err)
	}
	if got, ok := paymaster(); !ok || got != other {
		t.Fatalf("paymaster = %x (set=%v), want the wallet the admin named", got, ok)
	}

	// An owner that also holds the role names another wallet as an admin
	// does, with that wallet's opt-in.
	var third [20]byte
	third[0] = 0x65
	if err := manager.SetRole(roleLoyaltyAdmin, owner[:]); err != nil {
		t.Fatalf("assign owner the admin role: %v", err)
	}
	if err := registry.SetPaymaster(businessID, owner, third); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("owner holding the admin role naming a wallet without its opt-in: err = %v, want ErrPaymasterConsent", err)
	}
	if err := registry.SetPaymaster(businessID, third, third); err != nil {
		t.Fatalf("third recording its opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, owner, third); err != nil {
		t.Fatalf("owner holding the admin role naming a wallet that opted in: %v", err)
	}
	if got, ok := paymaster(); !ok || got != third {
		t.Fatalf("paymaster = %x (set=%v), want the wallet the admin-role owner named", got, ok)
	}
}
