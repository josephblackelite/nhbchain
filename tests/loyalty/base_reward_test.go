package loyalty_test

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/loyalty"
)

type staticPauseView struct {
	modules map[string]bool
}

func (s staticPauseView) IsPaused(module string) bool {
	if s.modules == nil {
		return false
	}
	return s.modules[module]
}

func mustPutAccount(t *testing.T, manager *nhbstate.Manager, addr [20]byte, account *types.Account) {
	t.Helper()
	if err := manager.PutAccount(addr[:], account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}

func mustAccount(t *testing.T, manager *nhbstate.Manager, addr [20]byte) *types.Account {
	t.Helper()
	account, err := manager.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return account
}

func findEvent(events []types.Event, eventType string) *types.Event {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func setupLoyaltyState(t *testing.T) (*core.StateProcessor, *nhbstate.Manager) {
	t.Helper()
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.RegisterToken("NHB", "Native", 18); err != nil {
		t.Fatalf("register NHB: %v", err)
	}
	if err := manager.RegisterToken("ZNHB", "ZapNHB", 18); err != nil {
		t.Fatalf("register ZNHB: %v", err)
	}
	return sp, manager
}

func TestBaseRewardAccruesAtDefaultRate(t *testing.T) {
	sp, manager := setupLoyaltyState(t)

	var treasury [20]byte
	treasury[19] = 0xAA
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
	}).Normalize()
	if cfg.BaseBps != loyalty.DefaultBaseRewardBps {
		t.Fatalf("expected default base bps %d, got %d", loyalty.DefaultBaseRewardBps, cfg.BaseBps)
	}
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	mustPutAccount(t, manager, treasury, &types.Account{BalanceZNHB: big.NewInt(20_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	var spender [20]byte
	spender[19] = 0xBB
	mustPutAccount(t, manager, spender, &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	fromAcc := mustAccount(t, manager, spender)
	toAcc := &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &loyalty.BaseRewardContext{
		From:        append([]byte(nil), spender[:]...),
		To:          []byte("merchant-address"),
		Token:       "NHB",
		Amount:      big.NewInt(20_000),
		Timestamp:   time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC),
		FromAccount: fromAcc,
		ToAccount:   toAcc,
	}

	sp.LoyaltyEngine.OnTransactionSuccess(sp, ctx)

	expected := big.NewInt(0).Mul(big.NewInt(20_000), big.NewInt(int64(loyalty.DefaultBaseRewardBps)))
	expected = expected.Quo(expected, big.NewInt(int64(loyalty.BaseRewardBpsDenominator)))
	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change, got %s", ctx.FromAccount.BalanceZNHB.String())
	}

	pending := sp.BlockContext().PendingRewards
	if len(pending) != 1 {
		t.Fatalf("expected one pending reward, got %d", len(pending))
	}
	if pending[0].AmountZNHB.Cmp(expected) != 0 {
		t.Fatalf("expected pending reward %s, got %s", expected.String(), pending[0].AmountZNHB.String())
	}

	treasuryAcc := mustAccount(t, manager, treasury)
	wantTreasury := big.NewInt(20_000)
	if treasuryAcc.BalanceZNHB.Cmp(wantTreasury) != 0 {
		t.Fatalf("expected treasury balance %s, got %s", wantTreasury.String(), treasuryAcc.BalanceZNHB.String())
	}

	dayKey := ctx.Timestamp.UTC().Format("2006-01-02")
	accrued, err := sp.LoyaltyBaseDailyAccrued(spender[:], dayKey)
	if err != nil {
		t.Fatalf("daily accrued: %v", err)
	}
	if accrued.Cmp(expected) != 0 {
		t.Fatalf("expected daily accrued %s, got %s", expected.String(), accrued.String())
	}

	evts := sp.Events()
	eventTypes := make([]string, len(evts))
	for i, evt := range evts {
		eventTypes[i] = evt.Type
	}
	if len(evts) != 3 {
		t.Fatalf("expected three events, got %d (%v)", len(evts), eventTypes)
	}
	if evts[0].Type != events.TypeLoyaltyRewardProposed {
		t.Fatalf("expected reward proposed event first, got %s", evts[0].Type)
	}
	evt := findEvent(evts, "loyalty.base.accrued")
	if evt == nil {
		t.Fatalf("expected base accrued event, got %#v", evts)
	}
	if evt.Attributes["reward"] != expected.String() {
		t.Fatalf("expected reward attribute %s, got %s", expected.String(), evt.Attributes["reward"])
	}
	if evt.Attributes["baseBps"] != "5000" {
		t.Fatalf("expected baseBps attribute 5000, got %s", evt.Attributes["baseBps"])
	}
	if evts[2].Type != "loyalty.program.skipped" {
		t.Fatalf("expected program skipped event third, got %s", evts[2].Type)
	}
}

func TestBaseRewardRespectsPause(t *testing.T) {
	sp, manager := setupLoyaltyState(t)

	var treasury [20]byte
	treasury[18] = 0xCC
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	mustPutAccount(t, manager, treasury, &types.Account{BalanceZNHB: big.NewInt(500), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	var spender [20]byte
	spender[17] = 0x01
	mustPutAccount(t, manager, spender, &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	fromAcc := mustAccount(t, manager, spender)
	ctx := &loyalty.BaseRewardContext{
		From:        append([]byte(nil), spender[:]...),
		To:          []byte("merchant"),
		Token:       "NHB",
		Amount:      big.NewInt(40_000),
		Timestamp:   time.Date(2024, 2, 2, 9, 0, 0, 0, time.UTC),
		FromAccount: fromAcc,
	}

	sp.LoyaltyEngine.SetPauses(staticPauseView{modules: map[string]bool{"loyalty": true}})
	sp.LoyaltyEngine.OnTransactionSuccess(sp, ctx)

	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward when paused")
	}
	events := sp.Events()
	if len(events) != 0 {
		t.Fatalf("expected no events when paused, got %d", len(events))
	}
}

func TestBaseRewardHonorsCapPerTx(t *testing.T) {
	sp, manager := setupLoyaltyState(t)

	var treasury [20]byte
	treasury[16] = 0xDD
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(50),
		DailyCapUser: big.NewInt(0),
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	mustPutAccount(t, manager, treasury, &types.Account{BalanceZNHB: big.NewInt(5_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	var spender [20]byte
	spender[15] = 0xEE
	mustPutAccount(t, manager, spender, &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	fromAcc := mustAccount(t, manager, spender)
	ctx := &loyalty.BaseRewardContext{
		From:        append([]byte(nil), spender[:]...),
		To:          []byte("merchant"),
		Token:       "NHB",
		Amount:      big.NewInt(20_000),
		Timestamp:   time.Date(2024, 2, 3, 18, 30, 0, 0, time.UTC),
		FromAccount: fromAcc,
	}

	sp.LoyaltyEngine.OnTransactionSuccess(sp, ctx)

	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change, got %s", ctx.FromAccount.BalanceZNHB.String())
	}
	pending := sp.BlockContext().PendingRewards
	if len(pending) != 1 {
		t.Fatalf("expected one pending reward, got %d", len(pending))
	}
	if pending[0].AmountZNHB.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected pending reward 50, got %s", pending[0].AmountZNHB.String())
	}
	evts := sp.Events()
	eventTypes := make([]string, len(evts))
	for i, evt := range evts {
		eventTypes[i] = evt.Type
	}
	if len(evts) != 3 {
		t.Fatalf("expected three events, got %d (%v)", len(evts), eventTypes)
	}
	if evts[0].Type != events.TypeLoyaltyRewardProposed {
		t.Fatalf("expected reward proposed event first, got %s", evts[0].Type)
	}
	evt := findEvent(evts, "loyalty.base.accrued")
	if evt == nil {
		t.Fatalf("expected base accrued event, got %#v", evts)
	}
	if evt.Attributes["reward"] != "50" {
		t.Fatalf("expected reward attribute 50, got %s", evt.Attributes["reward"])
	}
	if evt.Attributes["baseBps"] != "5000" {
		t.Fatalf("expected baseBps attribute 5000, got %s", evt.Attributes["baseBps"])
	}
	if evts[2].Type != "loyalty.program.skipped" {
		t.Fatalf("expected program skipped event third, got %s", evts[2].Type)
	}
}
