package core

import (
	"math/big"
	"testing"

	"nhbchain/core/rewards"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

const (
	rewardBlockTimestamp1 int64 = 1_700_000_200
	rewardBlockTimestamp2 int64 = 1_700_000_201
)

func newRewardTestState(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}
	cfg := sp.EpochConfig()
	cfg.Length = 2
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 1
	cfg.SnapshotHistory = 16
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}
	rewardCfg := rewards.Config{
		Schedule:        []rewards.EmissionStep{{StartEpoch: 1, Amount: big.NewInt(100)}},
		ValidatorSplit:  2000,
		StakerSplit:     5000,
		EngagementSplit: 3000,
		HistoryLength:   16,
	}
	if err := sp.SetRewardConfig(rewardCfg); err != nil {
		t.Fatalf("set reward config: %v", err)
	}
	return sp
}

func seedEligibleValidator(t *testing.T, sp *StateProcessor, stake int64, engagement uint64) []byte {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	account := &types.Account{
		BalanceNHB:      big.NewInt(0),
		BalanceZNHB:     big.NewInt(0),
		Stake:           big.NewInt(stake),
		EngagementScore: engagement,
	}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("set account: %v", err)
	}
	if sp.EligibleValidators == nil {
		sp.EligibleValidators = make(map[string]*big.Int)
	}
	sp.EligibleValidators[string(addr)] = big.NewInt(stake)
	return addr
}

func finalizeRewardEpoch(t *testing.T, sp *StateProcessor) {
	t.Helper()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
		t.Fatalf("process block 2: %v", err)
	}
}

func TestRewardDistributionSums(t *testing.T) {
	sp := newRewardTestState(t)
	a := seedEligibleValidator(t, sp, 6000, 10)
	b := seedEligibleValidator(t, sp, 4000, 5)

	finalizeRewardEpoch(t, sp)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PlannedTotal.String() != "100" {
		t.Fatalf("planned total mismatch: %s", settlement.PlannedTotal.String())
	}
	if settlement.PaidTotal.Cmp(settlement.PlannedTotal) != 0 {
		t.Fatalf("paid total mismatch: %s vs %s", settlement.PaidTotal, settlement.PlannedTotal)
	}
	if len(settlement.Payouts) != 2 {
		t.Fatalf("expected 2 payouts, got %d", len(settlement.Payouts))
	}
	total := big.NewInt(0)
	for _, payout := range settlement.Payouts {
		total.Add(total, payout.Total)
	}
	if total.Cmp(settlement.PaidTotal) != 0 {
		t.Fatalf("payout sum mismatch: %s vs %s", total, settlement.PaidTotal)
	}
	accountA, err := sp.getAccount(a)
	if err != nil {
		t.Fatalf("get account a: %v", err)
	}
	accountB, err := sp.getAccount(b)
	if err != nil {
		t.Fatalf("get account b: %v", err)
	}
	balance := new(big.Int).Add(accountA.BalanceZNHB, accountB.BalanceZNHB)
	if balance.Cmp(settlement.PaidTotal) != 0 {
		t.Fatalf("balance mismatch: %s vs %s", balance, settlement.PaidTotal)
	}
}

func TestRewardRounding(t *testing.T) {
	sp := newRewardTestState(t)
	rewardCfg := sp.RewardConfig()
	rewardCfg.Schedule = []rewards.EmissionStep{{StartEpoch: 1, Amount: big.NewInt(5)}}
	rewardCfg.ValidatorSplit = 0
	rewardCfg.StakerSplit = 7000
	rewardCfg.EngagementSplit = 3000
	if err := sp.SetRewardConfig(rewardCfg); err != nil {
		t.Fatalf("set reward config: %v", err)
	}
	seedEligibleValidator(t, sp, 3000, 0)
	seedEligibleValidator(t, sp, 2000, 0)
	finalizeRewardEpoch(t, sp)
	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.String() != "3" {
		t.Fatalf("expected paid total 3, got %s", settlement.PaidTotal.String())
	}
	if settlement.UnusedEngagement().String() != "2" {
		t.Fatalf("expected unused engagement 2, got %s", settlement.UnusedEngagement().String())
	}
	if len(settlement.Payouts) != 2 {
		t.Fatalf("expected 2 payouts")
	}
	// Ensure rounding distributed the remainder deterministically.
	if settlement.Payouts[0].Total.Cmp(settlement.Payouts[1].Total) == 0 {
		t.Fatalf("expected differing payouts due to rounding remainder")
	}
}

func TestRewardEmptySets(t *testing.T) {
	sp := newRewardTestState(t)
	// No eligible validators
	finalizeRewardEpoch(t, sp)
	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if settlement.PaidTotal.Sign() != 0 {
		t.Fatalf("expected zero paid total, got %s", settlement.PaidTotal.String())
	}
	if settlement.UnusedTotal().Cmp(settlement.PlannedTotal) != 0 {
		t.Fatalf("unused total mismatch")
	}
	if len(settlement.Payouts) != 0 {
		t.Fatalf("expected no payouts")
	}
}

func TestRewardIdempotency(t *testing.T) {
	sp := newRewardTestState(t)
	seedEligibleValidator(t, sp, 5000, 0)
	finalizeRewardEpoch(t, sp)
	first, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	account, err := sp.getAccount(first.Payouts[0].Account)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	balance := new(big.Int).Set(account.BalanceZNHB)
	// Re-run final block lifecycle to ensure no double payment.
	if err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
		t.Fatalf("reprocess block: %v", err)
	}
	second, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected settlement")
	}
	if len(sp.rewardHistory) != 1 {
		t.Fatalf("expected single settlement record")
	}
	accountAfter, err := sp.getAccount(first.Payouts[0].Account)
	if err != nil {
		t.Fatalf("get account after: %v", err)
	}
	if accountAfter.BalanceZNHB.Cmp(balance) != 0 {
		t.Fatalf("balance changed on idempotent run")
	}
	if second.PaidTotal.Cmp(first.PaidTotal) != 0 {
		t.Fatalf("paid total changed on idempotent run")
	}
}
