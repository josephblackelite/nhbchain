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
