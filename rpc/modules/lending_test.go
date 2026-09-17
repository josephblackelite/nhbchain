package modules

import (
	"bytes"
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func newLendingTestNode(t *testing.T) *core.Node {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

// seedLegacyLendingAccount writes pre-migration lending fields directly onto
// an account (the format used before the on-chain lending.Market/UserAccount
// records existed), giving GetUserAccount/GetPools/GetMarket's legacy
// reconciliation logic real data to migrate. Without this, a fresh node has
// nothing for reconcileLegacyUserAccount to act on and the write-on-read bug
// can't be observed either with or without the fix.
func seedLegacyLendingAccount(t *testing.T, node *core.Node, addr [20]byte) {
	t.Helper()
	err := node.WithState(func(manager *nhbstate.Manager) error {
		account, err := manager.GetAccount(addr[:])
		if err != nil {
			return err
		}
		account.CollateralBalance = big.NewInt(5_000)
		account.SupplyShares = big.NewInt(1_000)
		account.DebtPrincipal = big.NewInt(250)
		return manager.PutAccount(addr[:], account)
	})
	if err != nil {
		t.Fatalf("seed legacy lending account: %v", err)
	}
}

// Regression test for a live bug where GetMarket/GetPools/GetUserAccount --
// unauthenticated, read-only RPC methods -- wrote lazily-computed migration
// records through node.WithState as a side effect. That mutated the live
// pending state trie, so the write got silently baked into this validator's
// next self-proposed block even though no other validator ever executed the
// read. Every one of these calls must leave the pending state root exactly
// as it found it.
func TestLendingReadMethodsDoNotMutatePendingStateRoot(t *testing.T) {
	node := newLendingTestNode(t)
	module := NewLendingModule(node)

	var unseenAddr [20]byte
	for i := range unseenAddr {
		unseenAddr[i] = 0xAB
	}
	seedLegacyLendingAccount(t, node, unseenAddr)

	cases := []struct {
		name string
		call func() error
	}{
		{"GetMarket", func() error {
			_, _, moduleErr := module.GetMarket(defaultLendingPoolID)
			if moduleErr != nil {
				return moduleErr
			}
			return nil
		}},
		{"GetPools", func() error {
			_, _, moduleErr := module.GetPools()
			if moduleErr != nil {
				return moduleErr
			}
			return nil
		}},
		{"GetUserAccount", func() error {
			_, moduleErr := module.GetUserAccount(defaultLendingPoolID, unseenAddr)
			if moduleErr != nil {
				return moduleErr
			}
			return nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := node.PendingStateRoot()
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			after := node.PendingStateRoot()
			if !bytes.Equal(before, after) {
				t.Fatalf("%s mutated pending state root: before=%x after=%x", tc.name, before, after)
			}
		})
	}
}

// A read query followed immediately by a self-proposed block must not have
// its computed-but-discarded migration data silently reappear in the block:
// CreateBlock builds off the live pending trie, so if GetUserAccount ever
// regresses to writing through node.WithState again, this test will start
// producing a state root that depends on RPC traffic instead of only on
// included transactions.
func TestLendingReadMethodsDoNotAffectNextProposal(t *testing.T) {
	node := newLendingTestNode(t)
	module := NewLendingModule(node)

	var unseenAddr [20]byte
	for i := range unseenAddr {
		unseenAddr[i] = 0xCD
	}
	seedLegacyLendingAccount(t, node, unseenAddr)

	baseline, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create baseline block: %v", err)
	}

	if _, moduleErr := module.GetUserAccount(defaultLendingPoolID, unseenAddr); moduleErr != nil {
		t.Fatalf("get user account: %v", moduleErr)
	}
	if _, _, moduleErr := module.GetPools(); moduleErr != nil {
		t.Fatalf("get pools: %v", moduleErr)
	}

	afterQueries, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block after queries: %v", err)
	}

	if !bytes.Equal(afterQueries.Header.StateRoot, baseline.Header.StateRoot) {
		t.Fatalf("expected identical proposal state root after read-only RPC calls: baseline=%x afterQueries=%x",
			baseline.Header.StateRoot, afterQueries.Header.StateRoot)
	}
}
