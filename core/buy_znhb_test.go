package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// newBuyZNHBStateProcessor seeds an admin wallet with the real genesis ZNHB
// total (1,000,000,000 at 18 decimals) and runs the real bootstrap split,
// so applyBuyZNHB is exercised against the same Sale/Reward Pool accounting
// it runs against in production -- not an arbitrary placeholder balance.
func newBuyZNHBStateProcessor(t *testing.T) (*StateProcessor, [20]byte) {
	t.Helper()
	sp := newStakingStateProcessor(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	sp.SetAdminWallet(adminAddr, true)

	if err := sp.setAccount(adminAddr[:], &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbExpectedTotalSupplyWei),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed admin wallet: %v", err)
	}
	if err := sp.EnsureZNHBPoolsBootstrapped(); err != nil {
		t.Fatalf("bootstrap ZNHB pools: %v", err)
	}
	return sp, adminAddr
}

func buyZNHBTx(t *testing.T, nonce uint64, znhbAmount, maxNHBAmount *big.Int) *types.Transaction {
	t.Helper()
	payload := struct {
		ZNHBAmount   *big.Int `json:"znhbAmount"`
		MaxNHBAmount *big.Int `json:"maxNHBAmount"`
		QuoteID      string   `json:"quoteId,omitempty"`
	}{ZNHBAmount: znhbAmount, MaxNHBAmount: maxNHBAmount}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeBuyZNHB,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
}

// expectedCost mirrors what applyBuyZNHB should charge for buying znhbAmount
// starting from a Sale Pool at cumulative_sale_distributed = c0, using the
// same curve package the production code uses. This tests that
// applyBuyZNHB WIRES INTO the curve correctly (right accounts charged/
// credited, right state advanced) -- the curve's own pricing math is
// already independently verified in core/tokenomics/curve's test suite.
func expectedCost(t *testing.T, c0, znhbAmount *big.Int) *big.Int {
	t.Helper()
	c1 := new(big.Int).Add(c0, znhbAmount)
	costRat, err := curve.Default().Cost(c0, c1)
	if err != nil {
		t.Fatalf("compute expected cost: %v", err)
	}
	return curve.RoundCostUp(costRat)
}

func TestApplyBuyZNHB_MovesBothLegsAtomicallyAtCurvePrice(t *testing.T) {
	sp, adminAddr := newBuyZNHBStateProcessor(t)

	znhbAmount := new(big.Int).Mul(big.NewInt(1_000), curveWeiPerToken())
	cost := expectedCost(t, big.NewInt(0), znhbAmount)

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	// Fund the buyer generously above the expected cost so the purchase
	// itself is the thing under test, not balance sizing.
	funding := new(big.Int).Add(cost, big.NewInt(1_000_000))
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  funding,
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	maxNHB := new(big.Int).Add(cost, big.NewInt(1)) // 1 attoNHB of slippage room
	tx := buyZNHBTx(t, 0, znhbAmount, maxNHB)
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	wantBuyerNHB := new(big.Int).Sub(funding, cost)
	if buyer.BalanceNHB.Cmp(wantBuyerNHB) != 0 {
		t.Fatalf("buyer NHB = %s, want %s (funding %s minus curve cost %s)", buyer.BalanceNHB, wantBuyerNHB, funding, cost)
	}
	if buyer.BalanceZNHB.Cmp(znhbAmount) != 0 {
		t.Fatalf("buyer ZNHB = %s, want %s", buyer.BalanceZNHB, znhbAmount)
	}
	if buyer.Nonce != 1 {
		t.Fatalf("buyer nonce = %d, want 1", buyer.Nonce)
	}

	admin, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if admin.BalanceNHB.Cmp(cost) != 0 {
		t.Fatalf("admin NHB (revenue) = %s, want %s", admin.BalanceNHB, cost)
	}
	wantAdminZNHB := new(big.Int).Sub(znhbExpectedTotalSupplyWei, znhbAmount)
	if admin.BalanceZNHB.Cmp(wantAdminZNHB) != 0 {
		t.Fatalf("admin ZNHB = %s, want %s", admin.BalanceZNHB, wantAdminZNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	wantSalePool := new(big.Int).Sub(znhbExpectedSalePoolWei, znhbAmount)
	if salePool.Cmp(wantSalePool) != 0 {
		t.Fatalf("sale pool balance = %s, want %s", salePool, wantSalePool)
	}
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative sold: %v", err)
	}
	if cumulative.Cmp(znhbAmount) != 0 {
		t.Fatalf("cumulative sold = %s, want %s", cumulative, znhbAmount)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant should hold after a purchase: %v", err)
	}
}

func TestApplyBuyZNHB_OrderSplittingCostsTheSameAsOneShot(t *testing.T) {
	// Regression guard, at the transaction-application layer, for the same
	// property core/tokenomics/curve already proves at the math layer:
	// buying in two transactions must cost exactly the same total as
	// buying in one.
	total := new(big.Int).Mul(big.NewInt(100_000), curveWeiPerToken())
	half := new(big.Int).Div(total, big.NewInt(2))

	// Scenario A: one purchase of the full amount.
	spA, _ := newBuyZNHBStateProcessor(t)
	buyerKeyA, _ := crypto.GeneratePrivateKey()
	buyerAddrA := buyerKeyA.PubKey().Address().Bytes()
	costA := expectedCost(t, big.NewInt(0), total)
	fundingA := new(big.Int).Add(costA, big.NewInt(1_000_000))
	if err := spA.setAccount(buyerAddrA, &types.Account{BalanceNHB: fundingA, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer A: %v", err)
	}
	txA := buyZNHBTx(t, 0, total, new(big.Int).Add(costA, big.NewInt(1)))
	if err := txA.Sign(buyerKeyA.PrivateKey); err != nil {
		t.Fatalf("sign A: %v", err)
	}
	if err := spA.ApplyTransaction(txA); err != nil {
		t.Fatalf("apply A: %v", err)
	}
	buyerA, _ := spA.getAccount(buyerAddrA)
	totalPaidOneShot := new(big.Int).Sub(fundingA, buyerA.BalanceNHB)

	// Scenario B: two purchases of half the amount each.
	spB, _ := newBuyZNHBStateProcessor(t)
	buyerKeyB, _ := crypto.GeneratePrivateKey()
	buyerAddrB := buyerKeyB.PubKey().Address().Bytes()
	fundingB := new(big.Int).Add(costA, big.NewInt(1_000_000)) // same generous funding
	if err := spB.setAccount(buyerAddrB, &types.Account{BalanceNHB: fundingB, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer B: %v", err)
	}
	costHalf1 := expectedCost(t, big.NewInt(0), half)
	tx1 := buyZNHBTx(t, 0, half, new(big.Int).Add(costHalf1, big.NewInt(1)))
	if err := tx1.Sign(buyerKeyB.PrivateKey); err != nil {
		t.Fatalf("sign B1: %v", err)
	}
	if err := spB.ApplyTransaction(tx1); err != nil {
		t.Fatalf("apply B1: %v", err)
	}
	costHalf2 := expectedCost(t, half, new(big.Int).Sub(total, half))
	tx2 := buyZNHBTx(t, 1, new(big.Int).Sub(total, half), new(big.Int).Add(costHalf2, big.NewInt(1)))
	if err := tx2.Sign(buyerKeyB.PrivateKey); err != nil {
		t.Fatalf("sign B2: %v", err)
	}
	if err := spB.ApplyTransaction(tx2); err != nil {
		t.Fatalf("apply B2: %v", err)
	}
	buyerB, _ := spB.getAccount(buyerAddrB)
	totalPaidSplit := new(big.Int).Sub(fundingB, buyerB.BalanceNHB)

	if totalPaidOneShot.Cmp(totalPaidSplit) != 0 {
		t.Fatalf("splitting the purchase changed the total cost: one-shot=%s split=%s -- order-splitting exploit reopened", totalPaidOneShot, totalPaidSplit)
	}
}

func TestApplyBuyZNHB_InsufficientNHBBalanceRejected(t *testing.T) {
	sp, _ := newBuyZNHBStateProcessor(t)

	znhbAmount := new(big.Int).Mul(big.NewInt(1_000), curveWeiPerToken())
	cost := expectedCost(t, big.NewInt(0), znhbAmount)

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	tooLittle := new(big.Int).Sub(cost, big.NewInt(1))
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  tooLittle,
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, znhbAmount, new(big.Int).Add(cost, big.NewInt(1_000)))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection for insufficient NHB balance")
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	if buyer.BalanceNHB.Cmp(tooLittle) != 0 {
		t.Fatalf("buyer NHB balance must be unchanged on rejection, got %s", buyer.BalanceNHB)
	}
	if buyer.BalanceZNHB.Sign() != 0 {
		t.Fatalf("buyer must not receive ZNHB on rejection, got %s", buyer.BalanceZNHB)
	}
}

func TestApplyBuyZNHB_ExceedsMaxNHBAmountRejected(t *testing.T) {
	// Slippage protection: even with ample real NHB balance, a MaxNHBAmount
	// set below the true curve cost must reject the purchase rather than
	// silently charging more than the buyer authorized.
	sp, _ := newBuyZNHBStateProcessor(t)

	znhbAmount := new(big.Int).Mul(big.NewInt(1_000), curveWeiPerToken())
	cost := expectedCost(t, big.NewInt(0), znhbAmount)

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	ampleFunding := new(big.Int).Add(cost, big.NewInt(1_000_000))
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  ampleFunding,
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tooLowMax := new(big.Int).Sub(cost, big.NewInt(1))
	tx := buyZNHBTx(t, 0, znhbAmount, tooLowMax)
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection when the true cost exceeds the buyer's MaxNHBAmount")
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	if buyer.BalanceNHB.Cmp(ampleFunding) != 0 {
		t.Fatalf("buyer NHB balance must be unchanged on slippage rejection, got %s", buyer.BalanceNHB)
	}
}

func TestApplyBuyZNHB_ExceedsSalePoolCapacityRejected(t *testing.T) {
	sp, _ := newBuyZNHBStateProcessor(t)

	over := new(big.Int).Add(znhbExpectedSalePoolWei, big.NewInt(1))
	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil), // absurdly generous, cap is the thing under test
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, over, new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection for a purchase exceeding the Sale Pool's total 800,000,000 ZNHB capacity")
	}
}

func TestApplyBuyZNHB_SalePoolLedgerDesyncIsCaught(t *testing.T) {
	// Defense-in-depth: even if the Sale Pool sub-ledger were somehow
	// desynced from cumulative_sale_distributed (a bug elsewhere), the
	// explicit sale-pool-balance check in applyBuyZNHB must still catch it
	// independently, rather than relying solely on the curve's cap check.
	sp, _ := newBuyZNHBStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	// Artificially drain the Sale Pool sub-ledger without touching
	// cumulative_sale_distributed, simulating a desync.
	if err := manager.ZNHBSetSalePoolBalance(big.NewInt(10)); err != nil {
		t.Fatalf("simulate desync: %v", err)
	}

	znhbAmount := new(big.Int).Mul(big.NewInt(1_000), curveWeiPerToken())
	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(buyerAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000_000_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	tx := buyZNHBTx(t, 0, znhbAmount, big.NewInt(1_000_000_000))
	if err := tx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected the sale-pool-balance check to independently reject a desynced ledger")
	}
}

func TestApplySwapMintAndBurn_Disabled(t *testing.T) {
	sp, _ := newBuyZNHBStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()
	if err := sp.setAccount(senderAddr, &types.Account{
		BalanceNHB:  big.NewInt(1_000),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	burnPayload := struct {
		Amount           *big.Int `json:"amount"`
		TargetStablecoin string   `json:"targetStablecoin"`
		RecipientAddress string   `json:"recipientAddress"`
	}{Amount: big.NewInt(500)}
	data, err := rlp.EncodeToBytes(burnPayload)
	if err != nil {
		t.Fatalf("encode burn payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapBurn,
		Nonce:    0,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected disabled TxTypeSwapBurn to be rejected")
	}

	sender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if sender.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("disabled swap burn must not destroy funds, got NHB balance %s", sender.BalanceNHB)
	}
}

func curveWeiPerToken() *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(curve.Decimals)), nil)
}
