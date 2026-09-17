package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/lending"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

type lendingFixedTermSupplyTestPayload struct {
	PoolID     string `json:"poolId,omitempty"`
	TenureDays uint64 `json:"tenureDays"`
	Payout     string `json:"payout"`
}

func mustSignLendingFixedTermSupplyTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, value *big.Int, tenureDays uint64, payout lending.FixedTermDepositPayout) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(lendingFixedTermSupplyTestPayload{PoolID: "default", TenureDays: tenureDays, Payout: string(payout)})
	if err != nil {
		t.Fatalf("marshal fixed-term supply payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingSupplyFixedTerm,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    new(big.Int).Set(value),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign fixed-term supply tx: %v", err)
	}
	return tx
}

// TestSettleLendingDepositPayoutsDoesNotDoublePayInterestAcrossADelayedRetry
// is a regression test for a real bug caught during Milestone 3 design
// review: settleOneLendingDepositPayout pays interest and principal as two
// separate engine calls, but (pre-fix) only ever persisted the resulting
// deposit/market mutations at the very end of a fully successful step. If
// the interest call succeeded (moving real NHB to the depositor) but the
// SEPARATE principal call then failed with a soft, retryable error (e.g.
// insufficient pool liquidity right at that moment) and the whole step
// delayed to a later retry, the interest payment's bookkeeping (PaidInterestWei,
// the reserve decrement) was silently discarded -- never written to state --
// even though the real balance transfer had already happened. A later retry
// would then re-decide the SAME interest installment as still fully owed
// and pay it AGAIN, a genuine double payment out of the pool's fixed-term
// deposit reserve.
//
// This test engineers exactly that window: a fixed-term loan's auto-debit
// funds the deposit reserve, a lump-sum deposit's interest step succeeds,
// but the pool's real spendable liquidity is deliberately drained (via a
// second borrower) below the deposit's principal so the principal step
// fails and the payout is delayed. Liquidity is then restored and the
// retry runs. The fix persists the interest step's own mutation immediately
// (before ever attempting principal), so the retry sees the interest as
// already paid and only re-attempts principal.
func TestSettleLendingDepositPayoutsDoesNotDoublePayInterestAcrossADelayedRetry(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
	})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	// The deposit-side rate schedule has no built-in default -- seed a
	// governance param directly, mirroring what a passed
	// policy.lendingDepositRateSchedule proposal's execution would leave
	// behind (see other core tests' identical ParamStoreSet precedent, e.g.
	// TestApplyRedeemNHB_RejectsAboveConfiguredPerTxMax).
	depositSchedule := governance.LendingRateSchedulePayload{
		Schedule: []governance.LendingTenureRate{{TenureDays: 30, RateBps: 200}},
	}
	depositScheduleJSON, err := json.Marshal(depositSchedule)
	if err != nil {
		t.Fatalf("marshal deposit rate schedule: %v", err)
	}
	if err := nhbstate.NewManager(sp.Trie).ParamStoreSet(governance.ParamKeyLendingFixedTermDepositRateSchedule, depositScheduleJSON); err != nil {
		t.Fatalf("seed deposit rate schedule: %v", err)
	}

	supplierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate supplier key: %v", err)
	}
	borrowerKey, err := crypto.GeneratePrivateKey() // B1: originates the fixed-term LOAN that backs the deposit's interest capacity.
	if err != nil {
		t.Fatalf("generate borrower key: %v", err)
	}
	drainerKey, err := crypto.GeneratePrivateKey() // B2: a second, flexible borrower used purely to drain real pool liquidity.
	if err != nil {
		t.Fatalf("generate drainer key: %v", err)
	}
	depositorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate depositor key: %v", err)
	}

	ampleCollateral := mustBigInt(t, "50000000000000000000000")
	if err := sp.setAccount(supplierKey.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, "10000000000000000000"), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed supplier account: %v", err)
	}
	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(ampleCollateral)}); err != nil {
		t.Fatalf("seed borrower account: %v", err)
	}
	if err := sp.setAccount(drainerKey.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(ampleCollateral)}); err != nil {
		t.Fatalf("seed drainer account: %v", err)
	}
	if err := sp.setAccount(depositorKey.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, "3000000000000000000"), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed depositor account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}

	b1Principal := mustBigInt(t, "2000000000000000000")       // 2 tokens -- 30-day default schedule (1200bps) owes 0.24 tokens interest.
	depositPrincipal := mustBigInt(t, "3000000000000000000")  // 3 tokens -- 30-day 200bps deposit owes 0.06 tokens interest, comfortably under the 0.24 receivable.
	drainerBorrowAmount := mustBigInt(t, "10500000000000000000")
	drainerRepayAmount := mustBigInt(t, "8000000000000000000")

	txs := []*types.Transaction{
		mustSignLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "10000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, ampleCollateral, lendingNativePayload{PoolID: "default"}),
		mustSignLendingFixedTermBorrowTx(t, borrowerKey, 1, b1Principal, 30),
		mustSignLendingTx(t, drainerKey, types.TxTypeLendingDepositZNHB, 0, ampleCollateral, lendingNativePayload{PoolID: "default"}),
		mustSignLendingFixedTermSupplyTx(t, depositorKey, 0, depositPrincipal, 30, lending.FixedTermDepositPayoutLumpSumAtMaturity),
		mustSignLendingTx(t, drainerKey, types.TxTypeLendingBorrowNHB, 1, drainerBorrowAmount, lendingNativePayload{PoolID: "default"}),
	}

	var borrowTxHash, supplyTxHash []byte
	for idx, tx := range txs {
		height := uint64(idx + 1)
		blockTime := time.Unix(int64(height), 0).UTC()
		sp.BeginBlock(height, blockTime)
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("apply tx %d: %v", idx, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(height); err != nil {
			t.Fatalf("commit tx %d: %v", idx, err)
		}
		hash, err := tx.Hash()
		if err != nil {
			t.Fatalf("hash tx %d: %v", idx, err)
		}
		if idx == 2 {
			borrowTxHash = hash
		}
		if idx == 4 {
			supplyTxHash = hash
		}
	}

	var loanID, depositID [32]byte
	copy(loanID[:], borrowTxHash)
	copy(depositID[:], supplyTxHash)

	manager := nhbstate.NewManager(sp.Trie)
	loan, ok, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil || !ok || loan == nil {
		t.Fatalf("load loan: ok=%v err=%v", ok, err)
	}
	deposit, ok, err := manager.LendingGetFixedTermDeposit(depositID)
	if err != nil || !ok || deposit == nil {
		t.Fatalf("load deposit: ok=%v err=%v", ok, err)
	}
	expectedDepositInterest := mustBigInt(t, "60000000000000000")
	if deposit.TotalInterestOwedWei.Cmp(expectedDepositInterest) != 0 {
		t.Fatalf("test fixture sanity: expected deposit interest %s, got %s", expectedDepositInterest, deposit.TotalInterestOwedWei)
	}

	// Advance past BOTH the loan's single auto-debit cycle and the deposit's
	// lump-sum maturity in one call: auto-debit runs first (funding the
	// reserve from B1's own loan-principal balance), then deposit-payout
	// settlement attempts the lump-sum payout in the same pass.
	maturity := loan.MaturityTime
	if deposit.MaturityTime > maturity {
		maturity = deposit.MaturityTime
	}
	firstAttemptAt := int64(maturity) + 3_600

	if err := sp.ProcessBlockLifecycle(100, firstAttemptAt); err != nil {
		t.Fatalf("process block lifecycle at maturity: %v", err)
	}

	manager = nhbstate.NewManager(sp.Trie)
	depositAfterFirstAttempt, ok, err := manager.LendingGetFixedTermDeposit(depositID)
	if err != nil || !ok || depositAfterFirstAttempt == nil {
		t.Fatalf("reload deposit after first attempt: ok=%v err=%v", ok, err)
	}
	if depositAfterFirstAttempt.Status != lending.FixedTermDepositStatusActive {
		t.Fatalf("test fixture sanity: expected the principal leg to still be pending (pool liquidity deliberately drained), got status %s", depositAfterFirstAttempt.Status)
	}
	depositorAfterFirstAttempt, err := manager.GetAccount(depositorKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("load depositor balance after first attempt: %v", err)
	}
	if depositorAfterFirstAttempt.BalanceNHB.Cmp(expectedDepositInterest) != 0 {
		t.Fatalf("test fixture sanity: expected exactly one interest installment paid after the first attempt, got %s want %s", depositorAfterFirstAttempt.BalanceNHB, expectedDepositInterest)
	}

	// Restore enough real pool liquidity for the principal leg to succeed on
	// retry -- a real repay tx from the drainer, using the same NHB they
	// received when they borrowed it.
	repayTx := mustSignLendingTx(t, drainerKey, types.TxTypeLendingRepayNHB, 2, drainerRepayAmount, lendingNativePayload{PoolID: "default"})
	sp.BeginBlock(101, time.Unix(101, 0).UTC())
	if err := sp.ApplyTransaction(repayTx); err != nil {
		sp.EndBlock()
		t.Fatalf("apply drainer repay: %v", err)
	}
	sp.EndBlock()
	if _, err := sp.Commit(101); err != nil {
		t.Fatalf("commit drainer repay: %v", err)
	}

	secondAttemptAt := firstAttemptAt + lending.AutoDebitRetryIntervalSeconds + 3_600
	if err := sp.ProcessBlockLifecycle(200, secondAttemptAt); err != nil {
		t.Fatalf("process block lifecycle on retry: %v", err)
	}

	manager = nhbstate.NewManager(sp.Trie)
	finalDeposit, ok, err := manager.LendingGetFixedTermDeposit(depositID)
	if err != nil || !ok || finalDeposit == nil {
		t.Fatalf("reload deposit after retry: ok=%v err=%v", ok, err)
	}
	if finalDeposit.Status != lending.FixedTermDepositStatusMatured {
		t.Fatalf("expected the deposit matured after the retry succeeds, got status %s", finalDeposit.Status)
	}
	if finalDeposit.PaidInterestWei.Cmp(expectedDepositInterest) != 0 {
		t.Fatalf("expected the deposit's own ledger to show exactly one interest installment paid, got %s want %s", finalDeposit.PaidInterestWei, expectedDepositInterest)
	}

	finalDepositorAccount, err := manager.GetAccount(depositorKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("load depositor balance after retry: %v", err)
	}
	wantFinalBalance := new(big.Int).Add(depositPrincipal, expectedDepositInterest)
	if finalDepositorAccount.BalanceNHB.Cmp(wantFinalBalance) != 0 {
		t.Fatalf("expected the depositor to receive EXACTLY principal+interest once (no double-paid interest across the delayed retry): got %s want %s", finalDepositorAccount.BalanceNHB, wantFinalBalance)
	}

	finalMarket, ok, err := manager.LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("load market after retry: ok=%v err=%v", ok, err)
	}
	if finalMarket.FixedTermDepositReserveWei.Sign() != 0 {
		t.Fatalf("expected the deposit reserve fully drawn down with nothing stranded, got %s", finalMarket.FixedTermDepositReserveWei)
	}
	if finalMarket.TotalFixedTermDepositInterestOwedWei.Sign() != 0 {
		t.Fatalf("expected zero outstanding deposit interest obligation after the retry, got %s", finalMarket.TotalFixedTermDepositInterestOwedWei)
	}
	if finalMarket.TotalFixedTermDepositPrincipalWei.Sign() != 0 {
		t.Fatalf("expected zero outstanding deposit principal after the retry, got %s", finalMarket.TotalFixedTermDepositPrincipalWei)
	}
}

func TestRedTeamProbeExistingPackageBinaryTrust(t *testing.T) {
	t.Log("probe: does a modified but existing-package test binary execute?")
}
