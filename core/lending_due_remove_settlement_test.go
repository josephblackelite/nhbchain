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

// Ledger PL-EXT-42 (bug-bounty reports of 2026-11-15 and 2026-11-18): the two lending settlements (auto-debit of fixed-term loans,
// payout of fixed-term deposits) read a day's due list, handled every entry and then deleted the WHOLE bucket. An entry the pass itself scheduled onto the same UTC day (a final cycle that falls one day after
// the cycle just handled, for a tenure that is one day over a multiple of 30) was deleted with the handled ones, so the loan's final
// interest, or a deposit's final payout including its principal, was never attempted again. From LendingAccountingFixesActivationHeight
// only the handled entries are removed.

// loanCycleOntoTheSettlingDay takes the 30-day loan of setupAutoDebitTestChain, makes it a 61-day loan (cycles due on day 30, day 60 and,
// capped at maturity, day 61) whose second cycle is settled late, on day 61, and runs one settlement pass for that day. It returns the
// processor and the loan id after the pass.
func loanCycleOntoTheSettlingDay(t *testing.T, fixes bool) (*StateProcessor, [32]byte, uint64, uint64) {
	t.Helper()
	sp, _, loanID, _ := setupAutoDebitTestChain(t)
	manager := nhbstate.NewManager(sp.Trie)
	loan, ok, err := manager.LendingGetFixedTermLoan(loanID)
	if err != nil || !ok || loan == nil {
		t.Fatalf("load loan: ok=%v err=%v", ok, err)
	}
	loan.TenureDays = 61
	loan.MaturityTime = loan.IssuedAtTime + 61*secondsPerDay
	loan.NextAutoDebitCycle = 2
	loan.RepaidWei = new(big.Int).Div(new(big.Int).Mul(loan.TotalInterestWei, big.NewInt(30)), big.NewInt(61))
	if err := manager.LendingPutFixedTermLoan(loan); err != nil {
		t.Fatalf("store the 61-day loan: %v", err)
	}
	day := loan.MaturityTime / secondsPerDay
	now := day*secondsPerDay + 1_000
	if err := manager.LendingAutoDebitAppendDue(day, loanID); err != nil {
		t.Fatalf("schedule the loan on day %d: %v", day, err)
	}
	if fixes {
		sp.SetLendingAccountingFixesActivationHeight(1)
	}
	sp.BeginBlock(500, time.Unix(int64(now), 0).UTC())
	err = sp.settleLendingAutoDebitsDueOnDay(manager, day, now)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d: %v", day, err)
	}
	return sp, loanID, day, now
}

func TestALoanCycleScheduledOntoTheSettlingDayIsKeptFromTheAccountingFixesHeight(t *testing.T) {
	sp, loanID, day, now := loanCycleOntoTheSettlingDay(t, true)
	manager := nhbstate.NewManager(sp.Trie)
	due, err := manager.LendingAutoDebitDueOnDay(day)
	if err != nil {
		t.Fatalf("load due list: %v", err)
	}
	if len(due) != 1 || due[0] != loanID {
		t.Fatalf("the final cycle scheduled onto the settling day was not kept: day %d holds %v", day, due)
	}
	loan, _, _ := manager.LendingGetFixedTermLoan(loanID)
	if loan.NextAutoDebitCycle != 3 {
		t.Fatalf("after the second cycle the loan waits for cycle 3, got %d", loan.NextAutoDebitCycle)
	}

	// The next block of the same day settles it: the whole interest is collected and nothing is left to schedule.
	sp.BeginBlock(501, time.Unix(int64(now)+3, 0).UTC())
	err = sp.settleLendingAutoDebitsDueOnDay(manager, day, now+3)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d again: %v", day, err)
	}
	loan, _, _ = manager.LendingGetFixedTermLoan(loanID)
	if loan.NextAutoDebitCycle != 4 {
		t.Fatalf("after the final cycle NextAutoDebitCycle is %d, want 4", loan.NextAutoDebitCycle)
	}
	if loan.RepaidWei.Cmp(loan.TotalInterestWei) < 0 {
		t.Fatalf("the final interest was not collected: repaid %s of %s", loan.RepaidWei, loan.TotalInterestWei)
	}
	if due, _ := manager.LendingAutoDebitDueOnDay(day); len(due) != 0 {
		t.Fatalf("a bucket with nothing left should be empty, holds %v", due)
	}
}

func TestBeforeTheAccountingFixesHeightALoanCycleScheduledOntoTheSettlingDayIsStillLost(t *testing.T) {
	// The recorded behaviour of every earlier release, pinned so a change to it is deliberate.
	sp, loanID, day, now := loanCycleOntoTheSettlingDay(t, false)
	manager := nhbstate.NewManager(sp.Trie)
	if due, _ := manager.LendingAutoDebitDueOnDay(day); len(due) != 0 {
		t.Fatalf("before the height the whole bucket is cleared, day %d holds %v", day, due)
	}
	sp.BeginBlock(501, time.Unix(int64(now)+3, 0).UTC())
	err := sp.settleLendingAutoDebitsDueOnDay(manager, day, now+3)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d again: %v", day, err)
	}
	loan, _, _ := manager.LendingGetFixedTermLoan(loanID)
	if loan.NextAutoDebitCycle != 3 || loan.RepaidWei.Cmp(loan.TotalInterestWei) >= 0 {
		t.Fatalf("before the height the final cycle is never attempted: cycle %d, repaid %s of %s", loan.NextAutoDebitCycle, loan.RepaidWei, loan.TotalInterestWei)
	}
}

// depositCycleOntoTheSettlingDay builds the deposit side of the same: a 61-day periodic deposit whose second payout is made late, on day 61,
// so that the third (final) payout is scheduled onto the same day.
func depositCycleOntoTheSettlingDay(t *testing.T, fixes bool) (*StateProcessor, [32]byte, uint64, uint64) {
	t.Helper()
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
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)
	depositSchedule, err := json.Marshal(governance.LendingRateSchedulePayload{Schedule: []governance.LendingTenureRate{{TenureDays: 30, RateBps: 200}}})
	if err != nil {
		t.Fatalf("marshal deposit rate schedule: %v", err)
	}
	if err := nhbstate.NewManager(sp.Trie).ParamStoreSet(governance.ParamKeyLendingFixedTermDepositRateSchedule, depositSchedule); err != nil {
		t.Fatalf("seed deposit rate schedule: %v", err)
	}

	supplierKey, _ := crypto.GeneratePrivateKey()
	borrowerKey, _ := crypto.GeneratePrivateKey()
	depositorKey, _ := crypto.GeneratePrivateKey()
	ample := mustBigInt(t, "50000000000000000000000")
	for _, seed := range []struct {
		key  *crypto.PrivateKey
		nhb  string
		znhb *big.Int
	}{
		{supplierKey, "10000000000000000000", big.NewInt(0)},
		{borrowerKey, "0", new(big.Int).Set(ample)},
		{depositorKey, "3000000000000000000", big.NewInt(0)},
	} {
		if err := sp.setAccount(seed.key.PubKey().Address().Bytes(), &types.Account{BalanceNHB: mustBigInt(t, seed.nhb), BalanceZNHB: seed.znhb}); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit seeded state: %v", err)
	}
	txs := []*types.Transaction{
		mustSignLendingTx(t, supplierKey, types.TxTypeLendingSupplyNHB, 0, mustBigInt(t, "10000000000000000000"), lendingNativePayload{PoolID: "default"}),
		mustSignLendingTx(t, borrowerKey, types.TxTypeLendingDepositZNHB, 0, ample, lendingNativePayload{PoolID: "default"}),
		mustSignLendingFixedTermBorrowTx(t, borrowerKey, 1, mustBigInt(t, "2000000000000000000"), 30),
		mustSignLendingFixedTermSupplyTx(t, depositorKey, 0, mustBigInt(t, "3000000000000000000"), 30, lending.FixedTermDepositPayoutLumpSumAtMaturity),
	}
	var supplyTxHash []byte
	for idx, tx := range txs {
		height := uint64(idx + 1)
		sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
		if err := sp.ApplyTransaction(tx); err != nil {
			sp.EndBlock()
			t.Fatalf("apply tx %d: %v", idx, err)
		}
		sp.EndBlock()
		if _, err := sp.Commit(height); err != nil {
			t.Fatalf("commit tx %d: %v", idx, err)
		}
		if idx == 3 {
			supplyTxHash, err = tx.Hash()
			if err != nil {
				t.Fatalf("hash deposit tx: %v", err)
			}
		}
	}
	var depositID [32]byte
	copy(depositID[:], supplyTxHash)

	manager := nhbstate.NewManager(sp.Trie)
	deposit, ok, err := manager.LendingGetFixedTermDeposit(depositID)
	if err != nil || !ok || deposit == nil {
		t.Fatalf("load deposit: ok=%v err=%v", ok, err)
	}
	deposit.Payout = lending.FixedTermDepositPayoutPeriodic
	deposit.TenureDays = 61
	deposit.MaturityTime = deposit.IssuedAtTime + 61*secondsPerDay
	deposit.NextPayoutCycle = 2
	deposit.PaidInterestWei = new(big.Int).Div(new(big.Int).Mul(deposit.TotalInterestOwedWei, big.NewInt(30)), big.NewInt(61))
	if err := manager.LendingPutFixedTermDeposit(deposit); err != nil {
		t.Fatalf("store the 61-day periodic deposit: %v", err)
	}
	// The reserve that pays periodic interest is topped up by loan interest; fund it for the second payout directly.
	market, ok, err := manager.LendingGetMarket("default")
	if err != nil || !ok {
		t.Fatalf("load market: ok=%v err=%v", ok, err)
	}
	market.FixedTermDepositReserveWei = new(big.Int).Set(deposit.TotalInterestOwedWei)
	market.TotalFixedTermDepositInterestOwedWei = new(big.Int).Set(deposit.TotalInterestOwedWei)
	if err := manager.LendingPutMarket("default", market); err != nil {
		t.Fatalf("store the funded market: %v", err)
	}

	day := deposit.MaturityTime / secondsPerDay
	now := day*secondsPerDay + 1_000
	if err := manager.LendingDepositPayoutAppendDue(day, depositID); err != nil {
		t.Fatalf("schedule the deposit on day %d: %v", day, err)
	}
	if fixes {
		sp.SetLendingAccountingFixesActivationHeight(1)
	}
	sp.BeginBlock(500, time.Unix(int64(now), 0).UTC())
	err = sp.settleLendingDepositPayoutsDueOnDay(manager, day, now)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d: %v", day, err)
	}
	return sp, depositID, day, now
}

func TestADepositPayoutScheduledOntoTheSettlingDayIsKeptFromTheAccountingFixesHeight(t *testing.T) {
	sp, depositID, day, now := depositCycleOntoTheSettlingDay(t, true)
	manager := nhbstate.NewManager(sp.Trie)
	due, err := manager.LendingDepositPayoutDueOnDay(day)
	if err != nil {
		t.Fatalf("load due list: %v", err)
	}
	if len(due) != 1 || due[0] != depositID {
		t.Fatalf("the final payout scheduled onto the settling day was not kept: day %d holds %v", day, due)
	}
	deposit, _, _ := manager.LendingGetFixedTermDeposit(depositID)
	if deposit.NextPayoutCycle != 3 || deposit.Status != lending.FixedTermDepositStatusActive {
		t.Fatalf("after the second payout the deposit waits for cycle 3 and stays active: cycle %d, status %s", deposit.NextPayoutCycle, deposit.Status)
	}

	sp.BeginBlock(501, time.Unix(int64(now)+3, 0).UTC())
	err = sp.settleLendingDepositPayoutsDueOnDay(manager, day, now+3)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d again: %v", day, err)
	}
	deposit, _, _ = manager.LendingGetFixedTermDeposit(depositID)
	if deposit.Status != lending.FixedTermDepositStatusMatured {
		t.Fatalf("the final payout, principal included, was not made: status %s", deposit.Status)
	}
	if due, _ := manager.LendingDepositPayoutDueOnDay(day); len(due) != 0 {
		t.Fatalf("a bucket with nothing left should be empty, holds %v", due)
	}
}

func TestBeforeTheAccountingFixesHeightADepositPayoutScheduledOntoTheSettlingDayIsStillLost(t *testing.T) {
	sp, depositID, day, now := depositCycleOntoTheSettlingDay(t, false)
	manager := nhbstate.NewManager(sp.Trie)
	if due, _ := manager.LendingDepositPayoutDueOnDay(day); len(due) != 0 {
		t.Fatalf("before the height the whole bucket is cleared, day %d holds %v", day, due)
	}
	sp.BeginBlock(501, time.Unix(int64(now)+3, 0).UTC())
	err := sp.settleLendingDepositPayoutsDueOnDay(manager, day, now+3)
	sp.EndBlock()
	if err != nil {
		t.Fatalf("settle day %d again: %v", day, err)
	}
	deposit, _, _ := manager.LendingGetFixedTermDeposit(depositID)
	if deposit.Status != lending.FixedTermDepositStatusActive || deposit.NextPayoutCycle != 3 {
		t.Fatalf("before the height the final payout is never attempted: status %s, cycle %d", deposit.Status, deposit.NextPayoutCycle)
	}
}
