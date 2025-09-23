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
	if !ok {
		t.Fatalf("expected alias to resolve")
	}
	if resolved != addr {
		t.Fatalf("resolved address mismatch: got %x want %x", resolved, addr)
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
	if !ok {
		t.Fatalf("expected new alias to resolve")
	}
	if resolved != addr {
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
