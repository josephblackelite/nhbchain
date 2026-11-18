package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/fees"
)

// PL-R5T-59 (second half): the share of an NHB domain fee that funds the buyback
// accrual account was credited to a copy loaded from state, which the transfer's own
// later write of its sender or recipient overwrites, so the share was destroyed and
// the accrual ledger recorded a balance the account did not hold. The fee wallet half
// is PL-R1-FEEROUTE; both follow FeeRouteAliasActivationHeight.

// accrualAliasOutcome applies a 5% domain fee on a 10,000 transfer (500; 20% of it,
// 100, to the buyback accrual account, 400 to the fee wallet) and persists both
// parties as every call site does. accrualRole selects who the accrual account is.
func accrualAliasOutcome(t *testing.T, accrualRole string, setGate bool, height uint64) (stored, start, mirror *big.Int) {
	t.Helper()
	sp := newStakingStateProcessor(t)
	if setGate {
		sp.SetFeeRouteAliasActivationHeight(feeRouteTestActivation)
	}
	sp.BeginBlock(height, time.Unix(1_900_000_000, 0).UTC())

	sender := make([]byte, 20)
	sender[19] = 0xAA
	recipient := make([]byte, 20)
	recipient[19] = 0xBB
	var owner [20]byte
	owner[19] = 0xCC
	third := make([]byte, 20)
	third[19] = 0xDD

	accrual := third
	switch accrualRole {
	case "sender":
		accrual = sender
	case "recipient":
		accrual = recipient
	}
	sp.SetBuybackAccrualAddress(crypto.MustNewAddress(crypto.NHBPrefix, accrual))
	var signer [20]byte
	signer[19] = 0x01
	if err := sp.SetBuybackConfig(buyback.Config{FeeShareBps: 2000, DiscountBps: 500, SafetyMarginBps: 500, SignerThreshold: 1, Signers: [][20]byte{signer}}); err != nil {
		t.Fatalf("buyback config: %v", err)
	}

	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        500,
				OwnerWallet:           owner,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB: {MDRBasisPoints: 500, OwnerWallet: owner},
				},
			},
		},
	})

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}
	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000), To: recipient}
	switch accrualRole {
	case "sender":
		start = new(big.Int).Set(fromAcc.BalanceNHB)
	case "recipient":
		start = new(big.Int).Set(toAcc.BalanceNHB)
	default:
		start = big.NewInt(0)
	}
	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}
	if err := sp.setAccount(sender, fromAcc); err != nil {
		t.Fatalf("persist sender: %v", err)
	}
	if err := sp.setAccount(recipient, toAcc); err != nil {
		t.Fatalf("persist recipient: %v", err)
	}
	acc, err := sp.getAccount(accrual)
	if err != nil {
		t.Fatalf("load accrual account: %v", err)
	}
	mirror, err = nhbstate.NewManager(sp.Trie).ZNHBBuybackAccrualBalance()
	if err != nil {
		t.Fatalf("accrual ledger: %v", err)
	}
	return new(big.Int).Set(acc.BalanceNHB), start, mirror
}

func TestBuybackAccrualShareIsNotLostWhenTheAccrualAccountIsTheSender(t *testing.T) {
	cases := []struct {
		name    string
		setGate bool
		height  uint64
		kept    bool
	}{
		{"DefaultOffKeepsHistoricalBehavior", false, 10_000_000, false},
		{"BelowActivationKeepsHistoricalBehavior", true, feeRouteTestActivation - 1, false},
		{"AtActivationKeepsTheShare", true, feeRouteTestActivation, true},
		{"AboveActivationKeepsTheShare", true, feeRouteTestActivation + 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, start, mirror := accrualAliasOutcome(t, "sender", tc.setGate, tc.height)
			// The sender paid the whole 500; a kept share (100) comes back to it.
			want := new(big.Int).Sub(start, big.NewInt(500))
			if tc.kept {
				want.Add(want, big.NewInt(100))
			}
			if stored.Cmp(want) != 0 {
				t.Fatalf("accrual account (the sender): got %s want %s (kept=%v)", stored, want, tc.kept)
			}
			if tc.kept && mirror.Cmp(stored) != 0 {
				t.Fatalf("the accrual ledger records %s but the account holds %s", mirror, stored)
			}
		})
	}
}

func TestBuybackAccrualShareIsNotLostWhenTheAccrualAccountIsTheRecipient(t *testing.T) {
	cases := []struct {
		name    string
		setGate bool
		height  uint64
		kept    bool
	}{
		{"DefaultOffKeepsHistoricalBehavior", false, 10_000_000, false},
		{"BelowActivationKeepsHistoricalBehavior", true, feeRouteTestActivation - 1, false},
		{"AtActivationKeepsTheShare", true, feeRouteTestActivation, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, start, mirror := accrualAliasOutcome(t, "recipient", tc.setGate, tc.height)
			want := new(big.Int).Set(start)
			if tc.kept {
				want.Add(want, big.NewInt(100))
			}
			if stored.Cmp(want) != 0 {
				t.Fatalf("accrual account (the recipient): got %s want %s (kept=%v)", stored, want, tc.kept)
			}
			if tc.kept && mirror.Cmp(stored) != 0 {
				t.Fatalf("the accrual ledger records %s but the account holds %s", mirror, stored)
			}
		})
	}
}

// A third wallet is credited at every height, gate or none: nothing changes for it.
func TestBuybackAccrualShareToAThirdAccountIsUnaffectedByTheGate(t *testing.T) {
	for _, tc := range []struct {
		setGate bool
		height  uint64
	}{{false, 10_000_000}, {true, feeRouteTestActivation - 1}, {true, feeRouteTestActivation}} {
		stored, _, mirror := accrualAliasOutcome(t, "third", tc.setGate, tc.height)
		if stored.Cmp(big.NewInt(100)) != 0 || mirror.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("gate=%v height=%d: accrual holds %s, ledger %s, want 100 and 100", tc.setGate, tc.height, stored, mirror)
		}
	}
}
