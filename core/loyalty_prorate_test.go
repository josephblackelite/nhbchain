package core

import (
	"math/big"
	"testing"
	"time"

	events "nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newTestStateProcessor(t *testing.T) (*StateProcessor, *nhbstate.Manager) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { _ = db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	manager := nhbstate.NewManager(trie)
	return sp, manager
}

func setAccountBalance(t *testing.T, sp *StateProcessor, addr [20]byte, balance *big.Int) {
	t.Helper()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(balance), Stake: big.NewInt(0)}
	if err := sp.setAccount(addr[:], account); err != nil {
		t.Fatalf("set account: %v", err)
	}
}

func configureLoyalty(t *testing.T, manager *nhbstate.Manager, treasury [20]byte, enableProRate bool, dailyCapBps uint32) {
	t.Helper()
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: dailyCapBps,
			DailyCapUsd:            0,
			EnableProRate:          enableProRate,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled: false,
			},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set loyalty config: %v", err)
	}
}

func TestLoyaltyProrateExactFit(t *testing.T) {
	sp, manager := newTestStateProcessor(t)

	var treasury [20]byte
	treasury[19] = 0x01
	configureLoyalty(t, manager, treasury, true, 10_000)
	setAccountBalance(t, sp, treasury, big.NewInt(2_000))

	now := time.Date(2024, 3, 2, 12, 0, 0, 0, time.UTC)
	tracker := nhbstate.NewRollingFees(manager)
	if err := tracker.AddDay(now, big.NewInt(0), big.NewInt(1_000)); err != nil {
		t.Fatalf("add rolling fees: %v", err)
	}

	var merchantA, merchantB [20]byte
	merchantA[19] = 0x10
	merchantB[19] = 0x11

	sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: merchantA, AmountZNHB: big.NewInt(400)})
	sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: merchantB, AmountZNHB: big.NewInt(600)})

	sp.EndBlockRewards(now)

	if len(sp.blockCtx.PendingRewards) != 0 {
		t.Fatalf("expected pending rewards cleared, got %d", len(sp.blockCtx.PendingRewards))
	}

	accA, err := sp.getAccount(merchantA[:])
	if err != nil {
		t.Fatalf("load merchant A: %v", err)
	}
	if accA.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("merchant A balance mismatch: got %s want %s", accA.BalanceZNHB, big.NewInt(400))
	}

	accB, err := sp.getAccount(merchantB[:])
	if err != nil {
		t.Fatalf("load merchant B: %v", err)
	}
	if accB.BalanceZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("merchant B balance mismatch: got %s want %s", accB.BalanceZNHB, big.NewInt(600))
	}

	treasuryAcc, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if treasuryAcc.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("unexpected treasury balance: got %s want %s", treasuryAcc.BalanceZNHB, big.NewInt(1_000))
	}

	proposedTotal, err := manager.AddProposedTodayZNHB(now, nil)
	if err != nil {
		t.Fatalf("load proposed total: %v", err)
	}
	if proposedTotal.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("unexpected proposed total: got %s want %s", proposedTotal, big.NewInt(1_000))
	}

	paidTotal, err := manager.AddPaidTodayZNHB(now, nil)
	if err != nil {
		t.Fatalf("load paid total: %v", err)
	}
	if paidTotal.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("unexpected paid total: got %s want %s", paidTotal, big.NewInt(1_000))
	}

	for _, evt := range sp.Events() {
		if evt.Type == events.TypeLoyaltyBudgetProRated {
			t.Fatalf("unexpected pro-rate event for exact fit")
		}
	}
}

func TestLoyaltyProrateScaling(t *testing.T) {
	sp, manager := newTestStateProcessor(t)

	var treasury [20]byte
	treasury[19] = 0x02
	configureLoyalty(t, manager, treasury, true, 10_000)
	setAccountBalance(t, sp, treasury, big.NewInt(2_000))

	now := time.Date(2024, 3, 3, 9, 0, 0, 0, time.UTC)
	tracker := nhbstate.NewRollingFees(manager)
	if err := tracker.AddDay(now, big.NewInt(0), big.NewInt(500)); err != nil {
		t.Fatalf("add rolling fees: %v", err)
	}

	var merchantA, merchantB [20]byte
	merchantA[19] = 0x21
	merchantB[19] = 0x22

	sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: merchantA, AmountZNHB: big.NewInt(400)})
	sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: merchantB, AmountZNHB: big.NewInt(600)})

	sp.EndBlockRewards(now)

	accA, err := sp.getAccount(merchantA[:])
	if err != nil {
		t.Fatalf("load merchant A: %v", err)
	}
	if accA.BalanceZNHB.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("merchant A balance mismatch: got %s want %s", accA.BalanceZNHB, big.NewInt(200))
	}

	accB, err := sp.getAccount(merchantB[:])
	if err != nil {
		t.Fatalf("load merchant B: %v", err)
	}
	if accB.BalanceZNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("merchant B balance mismatch: got %s want %s", accB.BalanceZNHB, big.NewInt(300))
	}

	treasuryAcc, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if treasuryAcc.BalanceZNHB.Cmp(big.NewInt(1_500)) != 0 {
		t.Fatalf("unexpected treasury balance: got %s want %s", treasuryAcc.BalanceZNHB, big.NewInt(1_500))
	}

	paidTotal, err := manager.AddPaidTodayZNHB(now, nil)
	if err != nil {
		t.Fatalf("load paid total: %v", err)
	}
	if paidTotal.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("unexpected paid total: got %s want %s", paidTotal, big.NewInt(500))
	}

	var prorateEvt *types.Event
	for i := range sp.Events() {
		if sp.Events()[i].Type == events.TypeLoyaltyBudgetProRated {
			prorateEvt = &sp.Events()[i]
			break
		}
	}
	if prorateEvt == nil {
		t.Fatalf("expected pro-rate event")
	}
	if prorateEvt.Attributes["ratio_fp"] != "500000000000000000" {
		t.Fatalf("unexpected ratio attribute: %s", prorateEvt.Attributes["ratio_fp"])
	}
	if prorateEvt.Attributes["budget_zn"] != "500" {
		t.Fatalf("unexpected budget attribute: %s", prorateEvt.Attributes["budget_zn"])
	}
	if prorateEvt.Attributes["demand_zn"] != "1000" {
		t.Fatalf("unexpected demand attribute: %s", prorateEvt.Attributes["demand_zn"])
	}
}

func TestLoyaltyProrateZeroBudget(t *testing.T) {
	sp, manager := newTestStateProcessor(t)

	var treasury [20]byte
	treasury[19] = 0x03
	configureLoyalty(t, manager, treasury, true, 0)
	setAccountBalance(t, sp, treasury, big.NewInt(1_000))

	now := time.Date(2024, 3, 4, 18, 0, 0, 0, time.UTC)

	var merchant [20]byte
	merchant[19] = 0x31
	sp.blockCtx.PendingRewards.AddPendingReward(nhbstate.PendingReward{Recipient: merchant, AmountZNHB: big.NewInt(250)})

	sp.EndBlockRewards(now)

	acc, err := sp.getAccount(merchant[:])
	if err != nil {
		t.Fatalf("load merchant: %v", err)
	}
	if acc.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected zero payout, got %s", acc.BalanceZNHB)
	}

	treasuryAcc, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if treasuryAcc.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("unexpected treasury balance: got %s want %s", treasuryAcc.BalanceZNHB, big.NewInt(1_000))
	}

	paidTotal, err := manager.AddPaidTodayZNHB(now, nil)
	if err != nil {
		t.Fatalf("load paid total: %v", err)
	}
	if paidTotal.Sign() != 0 {
		t.Fatalf("expected zero paid total, got %s", paidTotal)
	}

	var prorateEvt *types.Event
	for i := range sp.Events() {
		if sp.Events()[i].Type == events.TypeLoyaltyBudgetProRated {
			prorateEvt = &sp.Events()[i]
			break
		}
	}
	if prorateEvt == nil {
		t.Fatalf("expected pro-rate event when budget zero")
	}
	if prorateEvt.Attributes["ratio_fp"] != "0" {
		t.Fatalf("expected zero ratio, got %s", prorateEvt.Attributes["ratio_fp"])
	}
}
