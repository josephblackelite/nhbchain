package core

import (
	"math"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/native/fees"
)

func TestApplyTransactionFeeDefaultsToSender(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
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

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000)}
	sender := make([]byte, 20)
	sender[19] = 0xAA

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(500))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	expectedSender := new(big.Int).Sub(initialSender, expectedFee)
	if fromAcc.BalanceNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("sender balance mismatch: got %s want %s", fromAcc.BalanceNHB, expectedSender)
	}
	if toAcc.BalanceNHB.Cmp(initialRecipient) != 0 {
		t.Fatalf("recipient balance mutated: got %s want %s", toAcc.BalanceNHB, initialRecipient)
	}

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedFee) != 0 {
		t.Fatalf("route wallet balance mismatch: got %v want %v", routeAcc.BalanceNHB, expectedFee)
	}
}

// feeRouteAliasOutcome runs applyTransactionFee for a 10,000-unit transfer whose
// domain fee (5%) is routed to the sender or to the recipient of that very
// transfer, then persists both parties the way every real call site does right
// after applyTransactionFee returns, and reports the route wallet's balance as
// stored. setGate/height select the PL-R1-FEEROUTE activation state: the gate is
// left at its default (off) when setGate is false.
func feeRouteAliasOutcome(t *testing.T, asset string, routeIsSender, setGate bool, height uint64) (final, start *big.Int) {
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
	if routeIsSender {
		copy(owner[:], sender)
	} else {
		copy(owner[:], recipient)
	}

	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        500,
				OwnerWallet:           owner,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					asset: {MDRBasisPoints: 500, OwnerWallet: owner},
				},
			},
		},
	})

	txType := types.TxTypeTransfer
	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}
	if asset == fees.AssetZNHB {
		txType = types.TxTypeTransferZNHB
		fromAcc = &types.Account{BalanceZNHB: big.NewInt(100_000)}
		toAcc = &types.Account{BalanceZNHB: big.NewInt(50_000)}
	}
	tx := &types.Transaction{Type: txType, MerchantAddress: domain, Value: big.NewInt(10_000), To: recipient}

	balance := func(acc *types.Account) *big.Int {
		if asset == fees.AssetZNHB {
			return new(big.Int).Set(acc.BalanceZNHB)
		}
		return new(big.Int).Set(acc.BalanceNHB)
	}
	if routeIsSender {
		start = balance(fromAcc)
	} else {
		start = balance(toAcc)
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
	stored, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route wallet: %v", err)
	}
	return balance(stored), start
}

const feeRouteTestActivation = 100

// TestApplyTransactionFeeRouteWalletIsSenderNotLost is the regression for
// PL-R1-FEEROUTE when a domain's fee OwnerWallet is the transfer's own sender:
// the credit must land on the *types.Account the caller already loaded for the
// sender (fromAcc) and persists after this call returns -- not on a second copy
// fetched fresh from state, which the caller's later setAccount(sender, fromAcc)
// overwrites, destroying the fee. The sender paid the fee to itself, so a kept
// fee nets back to the starting balance and a lost one leaves the sender 500
// short.
//
// For NHB the fix is gated by FeeRouteAliasActivationHeight, because it changes
// the state root of blocks already committed: off by default and below the
// height the historical (lossy) result must come out; from the height the fee
// is kept. The ZNHB fix is not gated and holds at every height, as in the
// release that is live.
func TestApplyTransactionFeeRouteWalletIsSenderNotLost(t *testing.T) {
	cases := []struct {
		name    string
		asset   string
		setGate bool
		height  uint64
		kept    bool
	}{
		{"NHBDefaultOffKeepsHistoricalBehavior", fees.AssetNHB, false, 10_000_000, false},
		{"NHBBelowActivationKeepsHistoricalBehavior", fees.AssetNHB, true, feeRouteTestActivation - 1, false},
		{"NHBAtActivationKeepsTheFee", fees.AssetNHB, true, feeRouteTestActivation, true},
		{"NHBAboveActivationKeepsTheFee", fees.AssetNHB, true, feeRouteTestActivation + 50, true},
		{"ZNHBDefaultOffStillKeepsTheFee", fees.AssetZNHB, false, 10_000_000, true},
		{"ZNHBBelowActivationStillKeepsTheFee", fees.AssetZNHB, true, feeRouteTestActivation - 1, true},
		{"ZNHBAtActivationKeepsTheFee", fees.AssetZNHB, true, feeRouteTestActivation, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			final, start := feeRouteAliasOutcome(t, tc.asset, true, tc.setGate, tc.height)
			want := new(big.Int).Set(start)
			if !tc.kept {
				want.Sub(want, big.NewInt(500))
			}
			if final.Cmp(want) != 0 {
				t.Fatalf("route wallet is the sender: got %s want %s (kept=%v)", final, want, tc.kept)
			}
		})
	}
}

// TestApplyTransactionFeeRouteWalletIsRecipientNotLost is the same regression for
// the other aliasing case: the fee OwnerWallet is the transfer's own recipient.
// A kept fee leaves the recipient 500 up; a lost one leaves it where it started.
func TestApplyTransactionFeeRouteWalletIsRecipientNotLost(t *testing.T) {
	cases := []struct {
		name    string
		asset   string
		setGate bool
		height  uint64
		kept    bool
	}{
		{"NHBDefaultOffKeepsHistoricalBehavior", fees.AssetNHB, false, 10_000_000, false},
		{"NHBBelowActivationKeepsHistoricalBehavior", fees.AssetNHB, true, feeRouteTestActivation - 1, false},
		{"NHBAtActivationKeepsTheFee", fees.AssetNHB, true, feeRouteTestActivation, true},
		{"ZNHBDefaultOffStillKeepsTheFee", fees.AssetZNHB, false, 10_000_000, true},
		{"ZNHBBelowActivationStillKeepsTheFee", fees.AssetZNHB, true, feeRouteTestActivation - 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			final, start := feeRouteAliasOutcome(t, tc.asset, false, tc.setGate, tc.height)
			want := new(big.Int).Set(start)
			if tc.kept {
				want.Add(want, big.NewInt(500))
			}
			if final.Cmp(want) != 0 {
				t.Fatalf("route wallet is the recipient: got %s want %s (kept=%v)", final, want, tc.kept)
			}
		})
	}
}

// The gate must survive Copy: the copies a block is built, validated and
// committed on come from it, and a field left out of Copy's literal would be zero
// there, which switches the fix on from height 0.
func TestFeeRouteAliasGateSurvivesCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	if sp.feeRouteAliasActivationHeight != math.MaxUint64 {
		t.Fatalf("expected the fix to default to off, got %d", sp.feeRouteAliasActivationHeight)
	}
	if sp.govCreditPreservationActivationHeight != math.MaxUint64 {
		t.Fatalf("expected the governance credit fix to default to off, got %d", sp.govCreditPreservationActivationHeight)
	}
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.feeRouteAliasActivationHeight != math.MaxUint64 || copied.govCreditPreservationActivationHeight != math.MaxUint64 {
		t.Fatalf("copy lost the default: fee route %d, governance credit %d", copied.feeRouteAliasActivationHeight, copied.govCreditPreservationActivationHeight)
	}

	sp.SetFeeRouteAliasActivationHeight(feeRouteTestActivation)
	sp.SetGovCreditPreservationActivationHeight(feeRouteTestActivation + 1)
	copied, err = sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.feeRouteAliasActivationHeight != feeRouteTestActivation {
		t.Fatalf("copy has fee route height %d, want %d", copied.feeRouteAliasActivationHeight, feeRouteTestActivation)
	}
	if copied.govCreditPreservationActivationHeight != feeRouteTestActivation+1 {
		t.Fatalf("copy has governance credit height %d, want %d", copied.govCreditPreservationActivationHeight, feeRouteTestActivation+1)
	}
}

// The two node-level settings reach the state processor, survive Copy, and are
// still there after the processor is rebuilt from the trie.
func TestNodeFeeRouteAndGovCreditHeightsReachBlockState(t *testing.T) {
	node := newTestNode(t)

	node.stateMu.RLock()
	feeUnset, govUnset := node.state.feeRouteAliasActivationHeight, node.state.govCreditPreservationActivationHeight
	node.stateMu.RUnlock()
	if feeUnset != math.MaxUint64 || govUnset != math.MaxUint64 {
		t.Fatalf("a new node has fee route height %d and governance credit height %d, want both off (MaxUint64)", feeUnset, govUnset)
	}

	node.SetFeeRouteAliasActivationHeight(feeRouteTestActivation)
	node.SetGovCreditPreservationActivationHeight(feeRouteTestActivation + 1)

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	check := func(label string, sp *StateProcessor) {
		t.Helper()
		if sp.feeRouteAliasActivationHeight != feeRouteTestActivation {
			t.Fatalf("%s has fee route height %d, want %d", label, sp.feeRouteAliasActivationHeight, feeRouteTestActivation)
		}
		if sp.govCreditPreservationActivationHeight != feeRouteTestActivation+1 {
			t.Fatalf("%s has governance credit height %d, want %d", label, sp.govCreditPreservationActivationHeight, feeRouteTestActivation+1)
		}
	}
	check("node state", node.state)
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	check("a copy of the state", copied)
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	check("a rebuilt state processor", node.state)
}

func TestApplyTransactionFeeSenderInsufficient(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 1
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

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000)}
	sender := make([]byte, 20)
	sender[18] = 0xBB

	fromAcc := &types.Account{BalanceNHB: big.NewInt(400)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(25_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc)
	if err == nil {
		t.Fatalf("expected error when sender lacks funds")
	}
	if err.Error() != "fees: insufficient balance to route fee" {
		t.Fatalf("unexpected error: %v", err)
	}

	if fromAcc.BalanceNHB.Cmp(initialSender) != 0 {
		t.Fatalf("sender balance mutated on error: got %s want %s", fromAcc.BalanceNHB, initialSender)
	}
	if toAcc.BalanceNHB.Cmp(initialRecipient) != 0 {
		t.Fatalf("recipient balance mutated on error: got %s want %s", toAcc.BalanceNHB, initialRecipient)
	}
}

func TestApplyTransactionFeeRecipientOptIn(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 2
	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        300,
				OwnerWallet:           owner,
				FeePayer:              fees.FeePayerRecipient,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB: {MDRBasisPoints: 300, OwnerWallet: owner},
				},
			},
		},
	})

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(8_000)}
	sender := make([]byte, 20)
	sender[17] = 0xCC

	fromAcc := &types.Account{BalanceNHB: big.NewInt(75_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(60_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(300))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	if fromAcc.BalanceNHB.Cmp(initialSender) != 0 {
		t.Fatalf("sender balance mutated under recipient policy: got %s want %s", fromAcc.BalanceNHB, initialSender)
	}

	expectedRecipient := new(big.Int).Sub(initialRecipient, expectedFee)
	if toAcc.BalanceNHB.Cmp(expectedRecipient) != 0 {
		t.Fatalf("recipient balance mismatch: got %s want %s", toAcc.BalanceNHB, expectedRecipient)
	}

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedFee) != 0 {
		t.Fatalf("route wallet balance mismatch: got %v want %v", routeAcc.BalanceNHB, expectedFee)
	}
}
