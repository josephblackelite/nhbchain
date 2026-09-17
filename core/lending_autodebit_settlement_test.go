package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

type lendingFixedTermBorrowTestPayload struct {
	PoolID     string `json:"poolId,omitempty"`
	TenureDays uint64 `json:"tenureDays"`
}

func mustSignLendingFixedTermBorrowTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, value *big.Int, tenureDays uint64) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(lendingFixedTermBorrowTestPayload{PoolID: "default", TenureDays: tenureDays})
	if err != nil {
		t.Fatalf("marshal fixed-term borrow payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingBorrowFixedTerm,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    new(big.Int).Set(value),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign fixed-term borrow tx: %v", err)
	}
	return tx
}

// setupAutoDebitTestChain seeds a lending pool (flexible supply + a
// borrower's ZNHB collateral) and originates one 30-day fixed-term loan,
// driving every step through the real transaction pipeline (ApplyTransaction),
// exactly like TestStateProcessorPersistsLendingPoolState. Returns the
// StateProcessor, the borrower's key/address, and the loan ID.
func setupAutoDebitTestChain(t *testing.T) (sp *StateProcessor, borrowerKey *crypto.PrivateKey, loanID [32]byte, issuedAtTime int64) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	tr, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err = NewStateProcessor(tr)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	sp.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               7_500,
		LiquidationThreshold: 8_000,
	})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)

	supplierKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate supplier key: %v", err)
	}
	borrowerKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate borrower key: %v", err)
	}

	if err := sp.setAccount(supplierKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  mustBigInt(t, "10000000000000000000000"),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed supplier account: %v", err)
	}
	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: mustBigInt(t, "50000000000000000000000"),
	}); err != nil {
		t.Fatalf("seed borrower account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}

	txs := []*types.Transaction{
		mustSignLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "1500000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, mustBigInt(t, "50000000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingFixedTermBorrowTx(t, borrowerKey, 1, mustBigInt(t, "100000000000000000000"), 30),
	}

	var borrowTxHash []byte
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
		if idx == len(txs)-1 {
			hash, err := tx.Hash()
			if err != nil {
				t.Fatalf("hash borrow tx: %v", err)
			}
			borrowTxHash = hash
		}
	}
	copy(loanID[:], borrowTxHash)

	manager := nhbstate.NewManager(sp.Trie)
	loan, ok, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		t.Fatalf("load loan: %v", err)
	}
	if !ok || loan == nil {
		t.Fatalf("expected fixed-term loan to exist after borrow")
	}
	if !loan.AutoDebitEnabled {
		t.Fatalf("expected auto-debit enabled by default at issuance")
	}
	if loan.NextAutoDebitCycle != 1 {
		t.Fatalf("expected NextAutoDebitCycle=1 at issuance, got %d", loan.NextAutoDebitCycle)
	}
	dueDay := loan.MaturityTime / secondsPerDay // 30-day loan: single cycle due at maturity
	due, err := manager.LendingAutoDebitDueOnDay(dueDay)
	if err != nil {
		t.Fatalf("load due bucket: %v", err)
	}
	found := false
	for _, id := range due {
		if id == loanID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected loan %x scheduled in the auto-debit due bucket for day %d", loanID, dueDay)
	}

	return sp, borrowerKey, loanID, int64(loan.IssuedAtTime)
}

// TestSettleLendingAutoDebitsCollectsInterestAndRoutesToPool drives a real
// 30-day fixed-term loan from origination through its single auto-debit
// cycle via the actual block-lifecycle hook (ProcessBlockLifecycle), and
// confirms: the full interest is collected automatically with no
// RepayFixedTerm transaction from the borrower, the interest is routed into
// the flexible pool's SupplyIndex (not sitting as protocol fees), and the
// loan's own bookkeeping (RepaidWei, NextAutoDebitCycle, ConsecutiveMissedAutoDebits)
// ends up exactly as expected.
func TestSettleLendingAutoDebitsCollectsInterestAndRoutesToPool(t *testing.T) {
	sp, borrowerKey, loanID, issuedAtTime := setupAutoDebitTestChain(t)
	manager := nhbstate.NewManager(sp.Trie)

	suppliedBefore, ok, err := manager.LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("load market before settlement: ok=%v err=%v", ok, err)
	}
	totalSuppliedBefore := new(big.Int).Set(suppliedBefore.TotalNHBSupplied)

	borrowerBefore, err := manager.GetAccount(borrowerKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("load borrower balance before settlement: %v", err)
	}
	balanceBefore := new(big.Int).Set(borrowerBefore.BalanceNHB)
	if balanceBefore.Sign() <= 0 {
		t.Fatalf("test fixture sanity: expected borrower to hold spendable NHB from the loan principal, got %s", balanceBefore)
	}

	// Advance past the loan's maturity (single-cycle due date for a 30-day
	// loan) and run the real block-lifecycle hook.
	dueAt := issuedAtTime + 30*86400 + 3_600 // one hour past due, comfortably clears the day boundary
	if err := sp.ProcessBlockLifecycle(100, dueAt); err != nil {
		t.Fatalf("process block lifecycle at maturity: %v", err)
	}

	manager = nhbstate.NewManager(sp.Trie)
	loan, ok, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil || !ok || loan == nil {
		t.Fatalf("reload loan after settlement: ok=%v err=%v", ok, err)
	}
	if loan.ConsecutiveMissedAutoDebits != 0 {
		t.Fatalf("expected no missed payments after a successful auto-debit, got %d", loan.ConsecutiveMissedAutoDebits)
	}
	if loan.NextAutoDebitCycle != 2 {
		t.Fatalf("expected NextAutoDebitCycle to advance past the loan's single cycle (2), got %d", loan.NextAutoDebitCycle)
	}
	if loan.RepaidWei.Cmp(loan.TotalInterestWei) != 0 {
		t.Fatalf("expected RepaidWei to equal the full interest %s after the single-cycle auto-debit, got %s", loan.TotalInterestWei, loan.RepaidWei)
	}
	if loan.Status != lending.FixedTermLoanStatusActive {
		t.Fatalf("expected the loan to remain active (principal still owed), got %s", loan.Status)
	}

	borrowerAfter, err := manager.GetAccount(borrowerKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("load borrower balance after settlement: %v", err)
	}
	wantBalanceAfter := new(big.Int).Sub(balanceBefore, loan.TotalInterestWei)
	if borrowerAfter.BalanceNHB.Cmp(wantBalanceAfter) != 0 {
		t.Fatalf("expected borrower balance to drop by exactly the auto-debited interest: got %s want %s", borrowerAfter.BalanceNHB, wantBalanceAfter)
	}

	marketAfter, ok, err := manager.LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("load market after settlement: ok=%v err=%v", ok, err)
	}
	wantSuppliedAfter := new(big.Int).Add(totalSuppliedBefore, loan.TotalInterestWei)
	if marketAfter.TotalNHBSupplied.Cmp(wantSuppliedAfter) != 0 {
		t.Fatalf("expected the interest to be routed into the pool's TotalNHBSupplied: got %s want %s", marketAfter.TotalNHBSupplied, wantSuppliedAfter)
	}

	dueDay := loan.MaturityTime / secondsPerDay
	remainingDue, err := manager.LendingAutoDebitDueOnDay(dueDay)
	if err != nil {
		t.Fatalf("load due bucket after settlement: %v", err)
	}
	if len(remainingDue) != 0 {
		t.Fatalf("expected the processed due bucket to be cleared, got %d entries", len(remainingDue))
	}
}

// TestSettleLendingAutoDebitsMarksDelinquentAfterThreeMisses drives a
// borrower with an empty NHB balance through three consecutive failed
// auto-debit retries and confirms the loan is marked delinquent on the
// third, with no further attempts scheduled afterward.
func TestSettleLendingAutoDebitsMarksDelinquentAfterThreeMisses(t *testing.T) {
	sp, borrowerKey, loanID, issuedAtTime := setupAutoDebitTestChain(t)

	// Drain the borrower's NHB balance directly (test setup, not the
	// behavior under test) so every auto-debit attempt fails for
	// insufficient balance.
	if err := sp.setAccount(borrowerKey.PubKey().Address().Bytes(), &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
	}); err != nil {
		t.Fatalf("drain borrower balance: %v", err)
	}
	if _, err := sp.Commit(10); err != nil {
		t.Fatalf("commit drained balance: %v", err)
	}

	dueAt := issuedAtTime + 30*86400 + 3_600
	height := uint64(100)
	for attempt := 1; attempt <= lending.AutoDebitMaxConsecutiveMisses; attempt++ {
		if err := sp.ProcessBlockLifecycle(height, dueAt); err != nil {
			t.Fatalf("process block lifecycle attempt %d: %v", attempt, err)
		}
		manager := nhbstate.NewManager(sp.Trie)
		loan, ok, err := manager.LendingGetFixedTermLoan(loanID)
		if err != nil || !ok || loan == nil {
			t.Fatalf("reload loan after attempt %d: ok=%v err=%v", attempt, ok, err)
		}
		if uint32(attempt) < lending.AutoDebitMaxConsecutiveMisses {
			if loan.ConsecutiveMissedAutoDebits != uint32(attempt) {
				t.Fatalf("attempt %d: expected ConsecutiveMissedAutoDebits=%d, got %d", attempt, attempt, loan.ConsecutiveMissedAutoDebits)
			}
			if loan.Status != lending.FixedTermLoanStatusActive {
				t.Fatalf("attempt %d: expected loan to remain active before the delinquency threshold, got %s", attempt, loan.Status)
			}
		} else {
			if loan.Status != lending.FixedTermLoanStatusDelinquent {
				t.Fatalf("attempt %d: expected the loan marked delinquent, got %s", attempt, loan.Status)
			}
		}
		height++
		dueAt += lending.AutoDebitRetryIntervalSeconds
	}

	// No further attempt should be scheduled once delinquent -- advancing
	// well past the last retry window and re-running the lifecycle hook
	// must not change anything further.
	manager := nhbstate.NewManager(sp.Trie)
	loanBefore, _, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		t.Fatalf("reload loan before final check: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(height, dueAt+lending.AutoDebitRetryIntervalSeconds*10); err != nil {
		t.Fatalf("process block lifecycle well past delinquency: %v", err)
	}
	manager = nhbstate.NewManager(sp.Trie)
	loanAfter, _, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil {
		t.Fatalf("reload loan after final check: %v", err)
	}
	if loanAfter.ConsecutiveMissedAutoDebits != loanBefore.ConsecutiveMissedAutoDebits {
		t.Fatalf("expected no further auto-debit attempts once delinquent: before=%d after=%d", loanBefore.ConsecutiveMissedAutoDebits, loanAfter.ConsecutiveMissedAutoDebits)
	}
}
