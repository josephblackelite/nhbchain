package core

import (
	"errors"
	"testing"

	"nhbchain/core/events"
	"nhbchain/core/identity"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func TestNodeIdentityAliasLifecycle(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address()
	var addr [20]byte
	copy(addr[:], userAddr.Bytes())

	if err := node.IdentitySetAlias(addr, "FrankRocks"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	resolved, ok := node.IdentityResolve("frankrocks")
	if !ok || resolved == nil {
		t.Fatalf("expected alias to resolve")
	}
	if resolved.Primary != addr {
		t.Fatalf("resolved address mismatch")
	}
	alias, ok := node.IdentityReverse(addr)
	if !ok || alias != "frankrocks" {
		t.Fatalf("expected reverse alias frankrocks, got %q", alias)
	}

	if err := node.IdentitySetAlias(addr, "frankierocks"); err != nil {
		t.Fatalf("rename alias: %v", err)
	}
	if _, ok := node.IdentityResolve("frankrocks"); ok {
		t.Fatalf("old alias should not resolve")
	}
	resolved, ok = node.IdentityResolve("frankierocks")
	if !ok || resolved == nil || resolved.Primary != addr {
		t.Fatalf("new alias resolution failed")
	}
	alias, ok = node.IdentityReverse(addr)
	if !ok || alias != "frankierocks" {
		t.Fatalf("reverse alias mismatch after rename: %q", alias)
	}

	eventsList := node.state.Events()
	if len(eventsList) != 2 {
		t.Fatalf("expected 2 events, got %d", len(eventsList))
	}
	expectedAddr := crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
	if eventsList[0].Type != events.TypeIdentityAliasSet {
		t.Fatalf("unexpected first event type: %s", eventsList[0].Type)
	}
	if eventsList[0].Attributes["alias"] != "frankrocks" {
		t.Fatalf("unexpected alias attribute: %s", eventsList[0].Attributes["alias"])
	}
	if eventsList[0].Attributes["address"] != expectedAddr {
		t.Fatalf("unexpected address attribute: %s", eventsList[0].Attributes["address"])
	}
	if eventsList[1].Type != events.TypeIdentityAliasRenamed {
		t.Fatalf("unexpected second event type: %s", eventsList[1].Type)
	}
	if eventsList[1].Attributes["old"] != "frankrocks" || eventsList[1].Attributes["new"] != "frankierocks" {
		t.Fatalf("unexpected rename attributes: %+v", eventsList[1].Attributes)
	}
	if eventsList[1].Attributes["address"] != expectedAddr {
		t.Fatalf("unexpected rename address: %s", eventsList[1].Attributes["address"])
	}
}

func TestNodeIdentityDuplicateAlias(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	firstKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate first key: %v", err)
	}
	secondKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	var firstAddr, secondAddr [20]byte
	copy(firstAddr[:], firstKey.PubKey().Address().Bytes())
	copy(secondAddr[:], secondKey.PubKey().Address().Bytes())

	if err := node.IdentitySetAlias(firstAddr, "sharedalias"); err != nil {
		t.Fatalf("set alias for first: %v", err)
	}
	err = node.IdentitySetAlias(secondAddr, "sharedalias")
	if !errors.Is(err, identity.ErrAliasTaken) {
		t.Fatalf("expected ErrAliasTaken, got %v", err)
	}
}

func TestNodeIdentityAddressManagement(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	secondaryKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate secondary key: %v", err)
	}
	tertiaryKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate tertiary key: %v", err)
	}

	var ownerAddr, secondaryAddr, tertiaryAddr [20]byte
	copy(ownerAddr[:], ownerKey.PubKey().Address().Bytes())
	copy(secondaryAddr[:], secondaryKey.PubKey().Address().Bytes())
	copy(tertiaryAddr[:], tertiaryKey.PubKey().Address().Bytes())

	if err := node.IdentitySetAlias(ownerAddr, "delta"); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	record, err := node.IdentityAddAddress(ownerAddr, "delta", secondaryAddr)
	if err != nil {
		t.Fatalf("add address: %v", err)
	}
	if len(record.Addresses) != 2 {
		t.Fatalf("expected two addresses, got %d", len(record.Addresses))
	}
	eventsList := node.state.Events()
	if len(eventsList) != 2 {
		t.Fatalf("expected two events after add, got %d", len(eventsList))
	}
	if eventsList[1].Type != events.TypeIdentityAliasAddressLinked {
		t.Fatalf("expected address linked event, got %s", eventsList[1].Type)
	}

	record, err = node.IdentitySetPrimary(ownerAddr, "delta", tertiaryAddr)
	if err != nil {
		t.Fatalf("set primary with implicit add: %v", err)
	}
	if record.Primary != tertiaryAddr {
		t.Fatalf("expected tertiary to become primary")
	}
	eventsList = node.state.Events()
	if len(eventsList) != 3 {
		t.Fatalf("expected three events after primary change, got %d", len(eventsList))
	}
	if eventsList[2].Type != events.TypeIdentityAliasPrimaryUpdated {
		t.Fatalf("expected primary updated event, got %s", eventsList[2].Type)
	}

	record, err = node.IdentityRemoveAddress(ownerAddr, "delta", secondaryAddr)
	if err != nil {
		t.Fatalf("remove address: %v", err)
	}
	if alias, ok := node.IdentityReverse(secondaryAddr); ok || alias != "" {
		t.Fatalf("expected secondary address reverse mapping cleared")
	}
	eventsList = node.state.Events()
	if len(eventsList) != 4 {
		t.Fatalf("expected four events after removal, got %d", len(eventsList))
	}
	if eventsList[3].Type != events.TypeIdentityAliasAddressRemoved {
		t.Fatalf("expected address removed event, got %s", eventsList[3].Type)
	}

	otherKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate outsider key: %v", err)
	}
	var outsider [20]byte
	copy(outsider[:], otherKey.PubKey().Address().Bytes())
	if _, err := node.IdentityAddAddress(outsider, "delta", outsider); !errors.Is(err, identity.ErrNotAliasOwner) {
		t.Fatalf("expected ErrNotAliasOwner, got %v", err)
	}
}
