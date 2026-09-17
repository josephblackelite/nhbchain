package config_test

import (
	"testing"

	"nhbchain/config"
	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func newTestNode(t *testing.T) *core.Node {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := core.NewNode(db, key, "", true, true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

func TestLoyaltyEnforceProdGuard(t *testing.T) {
	t.Setenv("NHB_ENV", "prod")

	node := newTestNode(t)
	cfg := config.Global{
		Loyalty: config.Loyalty{
			Dynamic: config.LoyaltyDynamic{
				EnableProRate:  false,
				EnforceProRate: true,
			},
		},
	}

	if err := node.SetGlobalConfig(cfg); err == nil {
		t.Fatalf("expected production guard to reject disabling pro-rate")
	}
}

func TestLoyaltyEnforceAllowedOutsideProd(t *testing.T) {
	t.Setenv("NHB_ENV", "staging")

	node := newTestNode(t)
	cfg := config.Global{
		Loyalty: config.Loyalty{
			Dynamic: config.LoyaltyDynamic{
				EnableProRate:  false,
				EnforceProRate: true,
			},
		},
	}

	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("unexpected guard trip outside production: %v", err)
	}
}
