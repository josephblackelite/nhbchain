package loyalty_test

import (
	"errors"
	"testing"

	loyalty "nhbchain/native/loyalty"
)

// PL-R5T-15: the standing paymaster-consent rule at the registry.

type consentFixture struct {
	registry *loyalty.Registry
	id       loyalty.BusinessID
	owner    [20]byte
	admin    [20]byte
	wallet   [20]byte // a third wallet that is neither owner nor admin
}

func newConsentFixture(t *testing.T, standing bool) *consentFixture {
	t.Helper()
	registry, manager := newTestRegistry(t)
	registry.SetPaymasterStandingConsent(standing)
	f := &consentFixture{registry: registry}
	f.owner[0] = 0xA1
	f.admin[0] = 0xA2
	f.wallet[0] = 0xA3
	id, err := registry.RegisterBusiness(f.owner, "Acme")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	f.id = id
	if err := manager.SetRole("ROLE_LOYALTY_ADMIN", f.admin[:]); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	return f
}

// assign makes wallet the business's paymaster the way the live flow does: the
// wallet opts in, then a loyalty admin names it.
func (f *consentFixture) assign(t *testing.T, wallet [20]byte) {
	t.Helper()
	if err := f.registry.SetPaymaster(f.id, wallet, wallet); err != nil {
		t.Fatalf("opt-in: %v", err)
	}
	if err := f.registry.SetPaymaster(f.id, f.admin, wallet); err != nil {
		t.Fatalf("admin assign: %v", err)
	}
}

func (f *consentFixture) recorded(t *testing.T, wallet [20]byte) bool {
	t.Helper()
	ok, err := f.registry.PaymasterAssignmentRecorded(f.id, wallet)
	if err != nil {
		t.Fatalf("recorded: %v", err)
	}
	return ok
}

func TestStandingConsentOffWritesNoRecordAndAllowsNoWithdrawal(t *testing.T) {
	f := newConsentFixture(t, false)
	f.assign(t, f.wallet)
	if f.recorded(t, f.wallet) {
		t.Fatalf("with the rule off an assignment must not write a standing record")
	}
	// The paymaster cannot withdraw itself while the rule is off.
	if err := f.registry.SetPaymaster(f.id, f.wallet, [20]byte{}); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("withdrawal with the rule off: err = %v, want ErrUnauthorized", err)
	}
	if pm, ok := f.registry.PrimaryPaymaster(f.owner); !ok || pm != f.wallet {
		t.Fatalf("the paymaster changed")
	}
}

func TestStandingConsentOnRecordsAnAdminAssignmentAndClearsItOnRotation(t *testing.T) {
	f := newConsentFixture(t, true)
	f.assign(t, f.wallet)
	if !f.recorded(t, f.wallet) {
		t.Fatalf("an assignment made with consent must leave a standing record")
	}
	// Rotation to the owner's own wallet clears the old record.
	if err := f.registry.SetPaymaster(f.id, f.owner, f.owner); err != nil {
		t.Fatalf("owner takes over: %v", err)
	}
	if f.recorded(t, f.wallet) {
		t.Fatalf("the record of a wallet that is no longer the paymaster must be cleared")
	}
	// The owner's own wallet never needs a record.
	if f.recorded(t, f.owner) {
		t.Fatalf("the owner's own wallet must not need a record")
	}
}

func TestStandingConsentOnRecordsAnAdminAssigningItself(t *testing.T) {
	f := newConsentFixture(t, true)
	if err := f.registry.SetPaymaster(f.id, f.admin, f.admin); err != nil {
		t.Fatalf("admin self-assignment: %v", err)
	}
	if !f.recorded(t, f.admin) {
		t.Fatalf("an admin naming its own wallet must leave a standing record")
	}
}

func TestStandingConsentOnLetsThePaymasterWithdrawItself(t *testing.T) {
	f := newConsentFixture(t, true)
	f.assign(t, f.wallet)
	if err := f.registry.SetPaymaster(f.id, f.wallet, [20]byte{}); err != nil {
		t.Fatalf("paymaster withdrawal: %v", err)
	}
	if _, ok := f.registry.PrimaryPaymaster(f.owner); ok {
		t.Fatalf("the business still has a paymaster after the withdrawal")
	}
	if f.recorded(t, f.wallet) {
		t.Fatalf("the record must be cleared by the withdrawal")
	}
}

func TestStandingConsentOnWithdrawalIsOnlyForTheCurrentPaymaster(t *testing.T) {
	f := newConsentFixture(t, true)
	f.assign(t, f.wallet)
	var stranger [20]byte
	stranger[0] = 0xEE
	if err := f.registry.SetPaymaster(f.id, stranger, [20]byte{}); !errors.Is(err, loyalty.ErrUnauthorized) {
		t.Fatalf("a stranger clearing the paymaster: err = %v, want ErrUnauthorized", err)
	}
	// A withdrawal that names another wallet is not a withdrawal.
	if err := f.registry.SetPaymaster(f.id, f.wallet, f.admin); !errors.Is(err, loyalty.ErrUnauthorized) && !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("the paymaster naming a different wallet: err = %v", err)
	}
	if pm, ok := f.registry.PrimaryPaymaster(f.owner); !ok || pm != f.wallet {
		t.Fatalf("the paymaster changed")
	}
}

func TestStandingConsentOnReaffirmingALegacyAssignmentNeedsTheWalletsConsentAgain(t *testing.T) {
	f := newConsentFixture(t, false)
	f.assign(t, f.wallet) // made before the rule: no record
	f.registry.SetPaymasterStandingConsent(true)
	if f.recorded(t, f.wallet) {
		t.Fatalf("a legacy assignment has no record")
	}
	// The admin re-affirms the same wallet: its consent was consumed by the first
	// assignment, so without a new opt-in nothing is recorded.
	if err := f.registry.SetPaymaster(f.id, f.admin, f.wallet); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("re-affirm without a new opt-in: err = %v, want ErrPaymasterConsent", err)
	}
	if f.recorded(t, f.wallet) {
		t.Fatalf("a refused re-affirmation must not write a record")
	}
	if err := f.registry.SetPaymaster(f.id, f.wallet, f.wallet); err != nil {
		t.Fatalf("new opt-in: %v", err)
	}
	if err := f.registry.SetPaymaster(f.id, f.admin, f.wallet); err != nil {
		t.Fatalf("re-affirm with the opt-in: %v", err)
	}
	if !f.recorded(t, f.wallet) {
		t.Fatalf("the re-affirmation must record the consent")
	}
	// Once recorded, a further re-affirmation is a quiet no-op.
	if err := f.registry.SetPaymaster(f.id, f.admin, f.wallet); err != nil {
		t.Fatalf("second re-affirm: %v", err)
	}
	// The opt-in the re-affirmation used is spent: after the owner takes the
	// business back, naming the wallet again needs a new one.
	if err := f.registry.SetPaymaster(f.id, f.owner, f.owner); err != nil {
		t.Fatalf("owner takes over: %v", err)
	}
	if err := f.registry.SetPaymaster(f.id, f.admin, f.wallet); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("naming the wallet again without a new opt-in: err = %v, want ErrPaymasterConsent", err)
	}
}

func TestStandingConsentOnOwnersOwnWalletNeverNeedsARecord(t *testing.T) {
	f := newConsentFixture(t, true)
	if err := f.registry.SetPaymaster(f.id, f.owner, f.owner); err != nil {
		t.Fatalf("owner assigns itself: %v", err)
	}
	if f.recorded(t, f.owner) {
		t.Fatalf("no record is written for the owner's own wallet")
	}
	// re-affirming the owner is a no-op, not an error, and writes nothing
	if err := f.registry.SetPaymaster(f.id, f.owner, f.owner); err != nil {
		t.Fatalf("owner re-affirms itself: %v", err)
	}
	if f.recorded(t, f.owner) {
		t.Fatalf("re-affirming the owner's own wallet wrote a record")
	}
}
