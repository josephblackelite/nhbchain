package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/reputation"
)

func TestNodeReputationVerifySkillAuthorized(t *testing.T) {
	node := newTestNode(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	node.SetTimeSource(func() time.Time { return fixed })

	verifierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("verifier key: %v", err)
	}
	subjectKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("subject key: %v", err)
	}
	verifierAddr := toAddress(verifierKey)
	subjectAddr := toAddress(subjectKey)
	assignRole(t, node, roleReputationVerifier, verifierAddr)

	expires := fixed.Add(6 * time.Hour).Unix()
	verification, err := node.ReputationVerifySkill(verifierAddr, subjectAddr, "Solidity", expires)
	if err != nil {
		t.Fatalf("verify skill: %v", err)
	}
	if verification == nil {
		t.Fatalf("expected verification result")
	}
	if verification.IssuedAt != fixed.Unix() {
		t.Fatalf("expected issuedAt %d, got %d", fixed.Unix(), verification.IssuedAt)
	}
	if verification.ExpiresAt != expires {
		t.Fatalf("expected expiresAt %d, got %d", expires, verification.ExpiresAt)
	}
	if verification.Skill != "Solidity" {
		t.Fatalf("expected skill 'Solidity', got %q", verification.Skill)
	}

	if err := node.WithState(func(m *nhbstate.Manager) error {
		ledger := reputation.NewLedger(m)
		stored, ok, err := ledger.Get(subjectAddr, "Solidity", verifierAddr)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("expected verification persisted")
		}
		if stored.IssuedAt != verification.IssuedAt {
			return fmt.Errorf("expected issuedAt %d, got %d", verification.IssuedAt, stored.IssuedAt)
		}
		if stored.ExpiresAt != verification.ExpiresAt {
			return fmt.Errorf("expected expiresAt %d, got %d", verification.ExpiresAt, stored.ExpiresAt)
		}
		return nil
	}); err != nil {
		t.Fatalf("ledger verification: %v", err)
	}

	events := node.Events()
	if len(events) == 0 {
		t.Fatalf("expected event to be emitted")
	}
	evt := events[len(events)-1]
	if evt.Type != reputation.EventTypeSkillVerified {
		t.Fatalf("expected event type %q, got %q", reputation.EventTypeSkillVerified, evt.Type)
	}
	if evt.Attributes["skill"] != "Solidity" {
		t.Fatalf("expected skill attribute 'Solidity', got %q", evt.Attributes["skill"])
	}
	if evt.Attributes["subject"] == "" || evt.Attributes["verifier"] == "" {
		t.Fatalf("expected subject and verifier attributes")
	}
	if evt.Attributes["issuedAt"] != strconv.FormatInt(fixed.Unix(), 10) {
		t.Fatalf("expected issuedAt attribute %d, got %q", fixed.Unix(), evt.Attributes["issuedAt"])
	}
	if evt.Attributes["expiresAt"] != strconv.FormatInt(expires, 10) {
		t.Fatalf("expected expiresAt attribute %d, got %q", expires, evt.Attributes["expiresAt"])
	}
}

func TestNodeReputationVerifySkillUnauthorized(t *testing.T) {
	node := newTestNode(t)
	node.SetTimeSource(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })

	verifierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("verifier key: %v", err)
	}
	subjectKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("subject key: %v", err)
	}
	verifierAddr := toAddress(verifierKey)
	subjectAddr := toAddress(subjectKey)

	initialEvents := len(node.Events())
	_, err = node.ReputationVerifySkill(verifierAddr, subjectAddr, "Design", 0)
	if !errors.Is(err, ErrReputationVerifierUnauthorized) {
		t.Fatalf("expected ErrReputationVerifierUnauthorized, got %v", err)
	}
	if len(node.Events()) != initialEvents {
		t.Fatalf("expected no events to be emitted on failure")
	}
	if err := node.WithState(func(m *nhbstate.Manager) error {
		ledger := reputation.NewLedger(m)
		_, ok, err := ledger.Get(subjectAddr, "Design", verifierAddr)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("unexpected verification persisted")
		}
		return nil
	}); err != nil {
		t.Fatalf("ledger check: %v", err)
	}
}

func TestNodeReputationVerifySkillInvalidSkill(t *testing.T) {
	node := newTestNode(t)
	node.SetTimeSource(func() time.Time { return time.Unix(1_700_000_200, 0).UTC() })

	verifierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("verifier key: %v", err)
	}
	subjectKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("subject key: %v", err)
	}
	verifierAddr := toAddress(verifierKey)
	subjectAddr := toAddress(subjectKey)
	assignRole(t, node, roleReputationVerifier, verifierAddr)

	initialEvents := len(node.Events())
	_, err = node.ReputationVerifySkill(verifierAddr, subjectAddr, "   ", 0)
	if err == nil || !strings.Contains(err.Error(), "skill required") {
		t.Fatalf("expected skill required error, got %v", err)
	}
	if len(node.Events()) != initialEvents {
		t.Fatalf("expected no events to be emitted on validation failure")
	}
	if err := node.WithState(func(m *nhbstate.Manager) error {
		ledger := reputation.NewLedger(m)
		_, ok, err := ledger.Get(subjectAddr, "", verifierAddr)
		if err == nil && ok {
			return fmt.Errorf("unexpected verification persisted")
		}
		return nil
	}); err != nil {
		t.Fatalf("ledger check: %v", err)
	}
}
