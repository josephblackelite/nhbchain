package core

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/tokenomics/curve"
	"nhbchain/crypto"
)

func newZNHBRPCTestNode(t *testing.T) *Node {
	t.Helper()
	node := newTestNode(t)
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	if err := node.ConfigureAdminWalletForTests(adminAddr); err != nil {
		t.Fatalf("configure admin wallet: %v", err)
	}
	return node
}

func TestGetZNHBTokenomicsState_ReflectsFreshBootstrap(t *testing.T) {
	node := newZNHBRPCTestNode(t)

	state, err := node.GetZNHBTokenomicsState()
	if err != nil {
		t.Fatalf("get tokenomics state: %v", err)
	}
	if state.FullySoldOut {
		t.Fatalf("expected not fully sold out immediately after bootstrap")
	}
	if state.CurrentTrancheIndex != 0 {
		t.Fatalf("current tranche index = %d, want 0", state.CurrentTrancheIndex)
	}
	if state.CurrentTranchePrice != "0.050000000000000000" {
		t.Fatalf("current tranche price = %s, want 0.05 (18 decimals)", state.CurrentTranchePrice)
	}
	wantSalePool := new(big.Int).Set(znhbExpectedSalePoolWei)
	if state.SalePoolBalanceWei != wantSalePool.String() {
		t.Fatalf("sale pool balance = %s, want %s", state.SalePoolBalanceWei, wantSalePool)
	}
	wantRewardPool := new(big.Int).Set(znhbExpectedRewardPoolWei)
	if state.RewardPoolBalanceWei != wantRewardPool.String() {
		t.Fatalf("reward pool balance = %s, want %s", state.RewardPoolBalanceWei, wantRewardPool)
	}
	if state.CumulativeSaleDistributed != "0" {
		t.Fatalf("cumulative sale distributed = %s, want 0", state.CumulativeSaleDistributed)
	}
	if state.BuybackAccrualBalanceWei != "0" {
		t.Fatalf("buyback accrual balance = %s, want 0", state.BuybackAccrualBalanceWei)
	}
}

func TestGetZNHBTokenomicsState_NoAdminWalletReturnsZeroValues(t *testing.T) {
	node := newTestNode(t)

	state, err := node.GetZNHBTokenomicsState()
	if err != nil {
		t.Fatalf("get tokenomics state: %v", err)
	}
	if state.SalePoolBalanceWei != "0" || state.RewardPoolBalanceWei != "0" {
		t.Fatalf("expected zero pool balances with no admin wallet, got sale=%s reward=%s", state.SalePoolBalanceWei, state.RewardPoolBalanceWei)
	}
}

func TestQuoteBuyZNHB_MatchesCurveCostExactly(t *testing.T) {
	node := newZNHBRPCTestNode(t)

	oneZNHB := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	quote, err := node.QuoteBuyZNHB(oneZNHB)
	if err != nil {
		t.Fatalf("quote buy znhb: %v", err)
	}
	if quote.ZNHBAmountWei != oneZNHB.String() {
		t.Fatalf("znhbAmountWei = %s, want %s", quote.ZNHBAmountWei, oneZNHB)
	}

	params := curve.Default()
	costRat, err := params.Cost(big.NewInt(0), oneZNHB)
	if err != nil {
		t.Fatalf("compute expected cost: %v", err)
	}
	wantCost := curve.RoundCostUp(costRat)
	if quote.NHBCostWei != wantCost.String() {
		t.Fatalf("nhbCostWei = %s, want %s (curve.Cost/RoundCostUp for the same range)", quote.NHBCostWei, wantCost)
	}
}

func TestQuoteBuyZNHB_RejectsNonPositiveAmount(t *testing.T) {
	node := newZNHBRPCTestNode(t)

	if _, err := node.QuoteBuyZNHB(big.NewInt(0)); err == nil {
		t.Fatalf("expected error for zero amount")
	}
	if _, err := node.QuoteBuyZNHB(big.NewInt(-1)); err == nil {
		t.Fatalf("expected error for negative amount")
	}
}

func TestQuoteBuyZNHB_ExceedsSalePoolReportsErrExceedsSalePool(t *testing.T) {
	node := newZNHBRPCTestNode(t)

	over := new(big.Int).Add(curve.Default().SalePoolCapWei(), big.NewInt(1))
	_, err := node.QuoteBuyZNHB(over)
	if !errors.Is(err, curve.ErrExceedsSalePool) {
		t.Fatalf("expected curve.ErrExceedsSalePool, got %v", err)
	}
}
