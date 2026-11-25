package core

import (
	"errors"
	"strings"
	"testing"

	"nhbchain/config"
	"nhbchain/storage"
)

// PL-EXT-73: a node must not start when the transfer fee would be credited to its OWN validator
// address. These tests pin the rule itself, then the node-level answer for the four ways a node can
// come to have (or lack) a real treasury.

func TestCheckTransferFeeCollector(t *testing.T) {
	const someCollector = "nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj"
	tests := []struct {
		name           string
		fees           config.Fees
		hasAdminWallet bool
		wantErr        bool
	}{
		{"no admin wallet, no collector, NHB fee on", config.Fees{TransferFeeBps: 20}, false, true},
		{"no admin wallet, no collector, only the ZNHB fee on", config.Fees{TransferFeeBpsZNHB: 10}, false, true},
		{"no admin wallet, no collector, both fees on", config.Fees{TransferFeeBps: 20, TransferFeeBpsZNHB: 10}, false, true},
		{"no admin wallet, collector of blanks only", config.Fees{TransferFeeBps: 20, TransferFeeCollector: "  \t "}, false, true},
		{"admin wallet resolved", config.Fees{TransferFeeBps: 20, TransferFeeBpsZNHB: 10}, true, false},
		{"collector configured", config.Fees{TransferFeeBps: 20, TransferFeeCollector: someCollector}, false, false},
		{"collector configured with surrounding blanks", config.Fees{TransferFeeBpsZNHB: 10, TransferFeeCollector: " " + someCollector + " "}, false, false},
		{"both fees zero credit nothing", config.Fees{}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckTransferFeeCollector(tc.fees, tc.hasAdminWallet)
			if tc.wantErr {
				if !errors.Is(err, ErrTransferFeeCollectorUnset) {
					t.Fatalf("got %v, want ErrTransferFeeCollectorUnset", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("got %v, want nil", err)
			}
		})
	}
}

// The message is what an operator reads when the node refuses: it must name all three ways out.
func TestTransferFeeCollectorErrorNamesTheWaysOut(t *testing.T) {
	msg := ErrTransferFeeCollectorUnset.Error()
	for _, want := range []string{"TransferFeeCollector", "NHB_MASTER_TREASURY", "admin wallet", "TransferFeeBps"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must mention %q so the operator knows what to set: %s", want, msg)
		}
	}
}

func TestNilNodeCheckTransferFeeCollectorIsQuiet(t *testing.T) {
	var node *Node
	if err := node.CheckTransferFeeCollector(config.Fees{TransferFeeBps: 20}); err != nil {
		t.Fatalf("a nil node has nothing to refuse: %v", err)
	}
}

func TestNodeCheckTransferFeeCollector(t *testing.T) {
	fees := config.Fees{TransferFeeBps: 20, TransferFeeBpsZNHB: 10}

	t.Run("genesis names an admin wallet", func(t *testing.T) {
		path := writeTreasuryTestSpec(t, treasuryTestSpec(t))
		db := storage.NewMemDB()
		t.Cleanup(db.Close)
		node, err := startTreasuryTestNode(t, db, path)
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := node.CheckTransferFeeCollector(fees); err != nil {
			t.Fatalf("a genesis with an admin wallet must pass: %v", err)
		}
	})

	t.Run("genesis names no admin wallet", func(t *testing.T) {
		spec := treasuryTestSpec(t)
		spec.AdminWallet = ""
		db := storage.NewMemDB()
		t.Cleanup(db.Close)
		node, err := startTreasuryTestNode(t, db, writeTreasuryTestSpec(t, spec))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := node.CheckTransferFeeCollector(fees); !errors.Is(err, ErrTransferFeeCollectorUnset) {
			t.Fatalf("got %v, want ErrTransferFeeCollectorUnset", err)
		}
		withCollector := fees
		withCollector.TransferFeeCollector = treasuryTestAddrString(0x05)
		if err := node.CheckTransferFeeCollector(withCollector); err != nil {
			t.Fatalf("a configured collector must pass: %v", err)
		}
	})

	t.Run("no admin wallet in the genesis but NHB_MASTER_TREASURY is set", func(t *testing.T) {
		spec := treasuryTestSpec(t)
		spec.AdminWallet = ""
		t.Setenv("NHB_MASTER_TREASURY", treasuryTestAddrString(0x02))
		db := storage.NewMemDB()
		t.Cleanup(db.Close)
		node, err := startTreasuryTestNode(t, db, writeTreasuryTestSpec(t, spec))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := node.CheckTransferFeeCollector(fees); err != nil {
			t.Fatalf("the environment override is a real treasury and must pass: %v", err)
		}
	})

	t.Run("an unparseable NHB_MASTER_TREASURY is not a treasury", func(t *testing.T) {
		spec := treasuryTestSpec(t)
		spec.AdminWallet = ""
		t.Setenv("NHB_MASTER_TREASURY", "not-an-address")
		db := storage.NewMemDB()
		t.Cleanup(db.Close)
		node, err := startTreasuryTestNode(t, db, writeTreasuryTestSpec(t, spec))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := node.CheckTransferFeeCollector(fees); !errors.Is(err, ErrTransferFeeCollectorUnset) {
			t.Fatalf("got %v, want ErrTransferFeeCollectorUnset", err)
		}
	})

	t.Run("the test helper that installs an admin wallet counts", func(t *testing.T) {
		node := newTestNode(t)
		if err := node.CheckTransferFeeCollector(fees); !errors.Is(err, ErrTransferFeeCollectorUnset) {
			t.Fatalf("an auto-created genesis has no admin wallet: got %v", err)
		}
		if err := node.ConfigureAdminWalletForTests(treasuryTestAddr(0x07)); err != nil {
			t.Fatalf("configure admin wallet: %v", err)
		}
		if err := node.CheckTransferFeeCollector(fees); err != nil {
			t.Fatalf("after an admin wallet is installed the check must pass: %v", err)
		}
	})
}
