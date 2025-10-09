package state

import (
	"errors"
	"testing"

	"nhbchain/core/identity"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() {
		db.Close()
	})
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return NewManager(tr)
}

func TestIdentitySetAliasAndResolve(t *testing.T) {
	manager := newTestManager(t)
	var addr [20]byte
	addr[19] = 1

	if err := manager.IdentitySetAlias(addr[:], "FrankRocks"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	resolved, ok := manager.IdentityResolve("frankrocks")
	if !ok || resolved == nil {
		t.Fatalf("expected alias to resolve")
	}
	if resolved.Primary != addr {
		t.Fatalf("resolved address mismatch: got %x want %x", resolved.Primary, addr)
	}
	if resolved.Alias != "frankrocks" {
		t.Fatalf("unexpected alias stored: %s", resolved.Alias)
	}
	if resolved.CreatedAt == 0 || resolved.UpdatedAt == 0 {
		t.Fatalf("expected timestamps to be recorded, got created=%d updated=%d", resolved.CreatedAt, resolved.UpdatedAt)
	}
	if len(resolved.Addresses) == 0 || resolved.Addresses[0] != addr {
		t.Fatalf("expected primary address in addresses list")
	}
	alias, ok := manager.IdentityReverse(addr[:])
	if !ok {
		t.Fatalf("expected reverse lookup to succeed")
	}
	if alias != "frankrocks" {
		t.Fatalf("unexpected alias: %s", alias)
	}
}

func TestIdentitySetAliasRename(t *testing.T) {
	manager := newTestManager(t)
	var addr [20]byte
	addr[19] = 2

	if err := manager.IdentitySetAlias(addr[:], "alpha"); err != nil {
		t.Fatalf("set alias alpha: %v", err)
	}
	if err := manager.IdentitySetAlias(addr[:], "bravo"); err != nil {
		t.Fatalf("rename alias: %v", err)
	}
	if _, ok := manager.IdentityResolve("alpha"); ok {
		t.Fatalf("expected old alias to be removed")
	}
	resolved, ok := manager.IdentityResolve("bravo")
	if !ok || resolved == nil {
		t.Fatalf("expected new alias to resolve")
	}
	if resolved.Primary != addr {
		t.Fatalf("resolved address mismatch after rename")
	}
	alias, ok := manager.IdentityReverse(addr[:])
	if !ok || alias != "bravo" {
		t.Fatalf("expected reverse alias to be bravo, got %q", alias)
	}
}

func TestIdentitySetAliasDuplicateRejected(t *testing.T) {
	manager := newTestManager(t)
	var first [20]byte
	var second [20]byte
	first[19] = 3
	second[19] = 4

	if err := manager.IdentitySetAlias(first[:], "shared"); err != nil {
		t.Fatalf("set alias for first: %v", err)
	}
	err := manager.IdentitySetAlias(second[:], "shared")
	if !errors.Is(err, identity.ErrAliasTaken) {
		t.Fatalf("expected ErrAliasTaken, got %v", err)
	}
}

func TestIdentitySetAliasValidation(t *testing.T) {
	manager := newTestManager(t)
	var addr [20]byte
	addr[19] = 5

	cases := []string{"ab", "contains space", "UPPERCASE_TOO_LONG................................"}
	for _, alias := range cases {
		if err := manager.IdentitySetAlias(addr[:], alias); !errors.Is(err, identity.ErrInvalidAlias) {
			t.Fatalf("alias %q: expected ErrInvalidAlias, got %v", alias, err)
		}
	}
	if _, ok := manager.IdentityResolve("zz"); ok {
		t.Fatalf("unexpected resolve success for invalid alias")
	}
}

func TestIdentitySetAvatarUpdatesRecord(t *testing.T) {
	manager := newTestManager(t)
	var addr [20]byte
	addr[19] = 6

	if err := manager.IdentitySetAlias(addr[:], "avataruser"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	initial, ok := manager.IdentityResolve("avataruser")
	if !ok || initial == nil {
		t.Fatalf("expected alias to resolve after registration")
	}
	if initial.AvatarRef != "" {
		t.Fatalf("expected empty avatar, got %q", initial.AvatarRef)
	}

	future := initial.UpdatedAt + 100
	updated, err := manager.IdentitySetAvatar("avataruser", "https://cdn.nhb/example.png", future)
	if err != nil {
		t.Fatalf("set avatar: %v", err)
	}
	if updated == nil {
		t.Fatalf("expected updated record")
	}
	if updated.AvatarRef != "https://cdn.nhb/example.png" {
		t.Fatalf("unexpected avatar stored: %q", updated.AvatarRef)
	}
	if updated.CreatedAt != initial.CreatedAt {
		t.Fatalf("createdAt mutated: got %d want %d", updated.CreatedAt, initial.CreatedAt)
	}
	if updated.UpdatedAt != future {
		t.Fatalf("expected UpdatedAt %d, got %d", future, updated.UpdatedAt)
	}

	resolved, ok := manager.IdentityResolve("avataruser")
	if !ok || resolved == nil {
		t.Fatalf("expected resolve to succeed after avatar update")
	}
	if resolved.AvatarRef != "https://cdn.nhb/example.png" {
		t.Fatalf("resolved avatar mismatch: %q", resolved.AvatarRef)
	}
	if resolved.UpdatedAt != future {
		t.Fatalf("expected UpdatedAt to persist, got %d", resolved.UpdatedAt)
	}
}

func TestIdentityAddAndRemoveAddress(t *testing.T) {
	manager := newTestManager(t)
	var primary [20]byte
	primary[19] = 7
	var secondary [20]byte
	secondary[18] = 1

	if err := manager.IdentitySetAlias(primary[:], "frankrocks"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	record, err := manager.IdentityAddAddress("frankrocks", secondary[:], 1000)
	if err != nil {
		t.Fatalf("add address: %v", err)
	}
	if len(record.Addresses) != 2 {
		t.Fatalf("expected two addresses, got %d", len(record.Addresses))
	}
	if record.Addresses[0] != primary {
		t.Fatalf("primary should remain first entry")
	}
	if record.Addresses[1] != secondary {
		t.Fatalf("expected secondary address to be appended")
	}
	if record.UpdatedAt != 1000 {
		t.Fatalf("expected updated timestamp to be set")
	}
	alias, ok := manager.IdentityReverse(secondary[:])
	if !ok || alias != "frankrocks" {
		t.Fatalf("expected reverse lookup for secondary, got %q", alias)
	}

	updated, err := manager.IdentityRemoveAddress("frankrocks", secondary[:], 2000)
	if err != nil {
		t.Fatalf("remove address: %v", err)
	}
	if len(updated.Addresses) != 1 || updated.Addresses[0] != primary {
		t.Fatalf("expected only primary address to remain")
	}
	if updated.UpdatedAt != 2000 {
		t.Fatalf("expected updated timestamp after removal")
	}
	if _, ok := manager.IdentityReverse(secondary[:]); ok {
		t.Fatalf("expected reverse mapping to be cleared")
	}

	if _, err := manager.IdentityRemoveAddress("frankrocks", secondary[:], 0); !errors.Is(err, identity.ErrAddressNotLinked) {
		t.Fatalf("expected ErrAddressNotLinked, got %v", err)
	}
	if _, err := manager.IdentityRemoveAddress("frankrocks", primary[:], 0); !errors.Is(err, identity.ErrPrimaryAddressRequired) {
		t.Fatalf("expected ErrPrimaryAddressRequired, got %v", err)
	}
}

func TestIdentityAddressConflicts(t *testing.T) {
	manager := newTestManager(t)
	var ownerA, ownerB, shared [20]byte
	ownerA[19] = 8
	ownerB[19] = 9
	shared[10] = 1

	if err := manager.IdentitySetAlias(ownerA[:], "alpha"); err != nil {
		t.Fatalf("set alias alpha: %v", err)
	}
	if err := manager.IdentitySetAlias(ownerB[:], "bravo"); err != nil {
		t.Fatalf("set alias bravo: %v", err)
	}
	if _, err := manager.IdentityAddAddress("alpha", shared[:], 123); err != nil {
		t.Fatalf("add shared address: %v", err)
	}
	if _, err := manager.IdentityAddAddress("bravo", shared[:], 456); !errors.Is(err, identity.ErrAddressLinked) {
		t.Fatalf("expected ErrAddressLinked, got %v", err)
	}
}

func TestIdentitySetPrimaryPromotesAddress(t *testing.T) {
	manager := newTestManager(t)
	var primary [20]byte
	primary[19] = 10
	var secondary [20]byte
	secondary[18] = 2
	var tertiary [20]byte
	tertiary[17] = 3

	if err := manager.IdentitySetAlias(primary[:], "gamma"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	if _, err := manager.IdentityAddAddress("gamma", secondary[:], 100); err != nil {
		t.Fatalf("add secondary: %v", err)
	}
	updated, err := manager.IdentitySetPrimary("gamma", secondary[:], 200)
	if err != nil {
		t.Fatalf("set primary: %v", err)
	}
	if updated.Primary != secondary {
		t.Fatalf("expected secondary to become primary")
	}
	if updated.Addresses[0] != secondary {
		t.Fatalf("expected addresses slice to reorder with new primary first")
	}
	if !containsAliasAddress(updated.Addresses[1:], primary) {
		t.Fatalf("expected previous primary to remain in addresses slice")
	}

	promoted, err := manager.IdentitySetPrimary("gamma", tertiary[:], 300)
	if err != nil {
		t.Fatalf("set new primary with implicit add: %v", err)
	}
	if promoted.Primary != tertiary {
		t.Fatalf("expected tertiary to become primary")
	}
	if !containsAliasAddress(promoted.Addresses, tertiary) {
		t.Fatalf("expected tertiary address to be recorded")
	}
	if alias, ok := manager.IdentityReverse(tertiary[:]); !ok || alias != "gamma" {
		t.Fatalf("expected reverse mapping for tertiary")
	}
}

func TestIdentityRenamePreservesMetadata(t *testing.T) {
	manager := newTestManager(t)
	var owner [20]byte
	owner[19] = 11
	var linked [20]byte
	linked[16] = 4

	if err := manager.IdentitySetAlias(owner[:], "omega"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	record, ok := manager.IdentityResolve("omega")
	if !ok || record == nil {
		t.Fatalf("expected alias to resolve")
	}
	created := record.CreatedAt
	if _, err := manager.IdentityAddAddress("omega", linked[:], record.CreatedAt+1); err != nil {
		t.Fatalf("add linked: %v", err)
	}
	renamed, err := manager.IdentityRename("omega", "omicron", record.CreatedAt+2)
	if err != nil {
		t.Fatalf("rename alias: %v", err)
	}
	if renamed.Alias != "omicron" {
		t.Fatalf("expected alias to be omicron, got %s", renamed.Alias)
	}
	if renamed.CreatedAt != created {
		t.Fatalf("expected CreatedAt to remain %d, got %d", created, renamed.CreatedAt)
	}
	if !containsAliasAddress(renamed.Addresses, linked) {
		t.Fatalf("expected linked address to persist after rename")
	}
	if alias, ok := manager.IdentityReverse(linked[:]); !ok || alias != "omicron" {
		t.Fatalf("expected reverse mapping to update on rename")
	}
	var conflict [20]byte
	conflict[18] = 7
	if err := manager.IdentitySetAlias(conflict[:], "alpha"); err != nil {
		t.Fatalf("set conflicting alias: %v", err)
	}
	if _, err := manager.IdentityRename("omicron", "alpha", 0); !errors.Is(err, identity.ErrAliasTaken) {
		t.Fatalf("expected ErrAliasTaken when renaming to existing alias, got %v", err)
	}
}
