package loyalty

import (
	"errors"
	"testing"
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
	if err := registry.SetPaymaster(businessID, outsider, paymaster); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected unauthorized error, got %v", err)
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
	if err := registry.SetPaymaster(firstID, owner, firstPaymaster); err != nil {
		t.Fatalf("set first paymaster: %v", err)
	}
	var secondPaymaster [20]byte
	secondPaymaster[0] = 0x22
	if err := registry.SetPaymaster(secondID, owner, secondPaymaster); !errors.Is(err, ErrPaymasterConflict) {
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
