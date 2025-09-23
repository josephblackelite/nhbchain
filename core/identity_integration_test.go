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
	node, err := NewNode(db, validatorKey, "", true)
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
	if !ok {
		t.Fatalf("expected alias to resolve")
	}
	if resolved != addr {
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
	if !ok || resolved != addr {
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
	expectedAddr := crypto.NewAddress(crypto.NHBPrefix, addr[:]).String()
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
	node, err := NewNode(db, validatorKey, "", true)
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
