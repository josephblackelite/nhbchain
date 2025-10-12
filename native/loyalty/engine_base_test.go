package loyalty

import (
	"math/big"
	"reflect"
	"testing"
	"time"

	"nhbchain/core/types"
)

type mockState struct {
	cfg      *GlobalConfig
	accounts map[string]*types.Account
	daily    map[string]map[string]*big.Int
	total    map[string]*big.Int
	events   []types.Event
	queued   []queuedReward
}

type queuedReward struct {
	txHash [32]byte
	amount *big.Int
}

func newMockState(cfg *GlobalConfig) *mockState {
	return &mockState{
		cfg:      cfg.Clone().Normalize(),
		accounts: make(map[string]*types.Account),
		daily:    make(map[string]map[string]*big.Int),
		total:    make(map[string]*big.Int),
		events:   []types.Event{},
		queued:   []queuedReward{},
	}
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	clone := &types.Account{
		Nonce:           acc.Nonce,
		BalanceNHB:      new(big.Int).Set(acc.BalanceNHB),
		BalanceZNHB:     new(big.Int).Set(acc.BalanceZNHB),
		Stake:           new(big.Int).Set(acc.Stake),
		Username:        acc.Username,
		EngagementScore: acc.EngagementScore,
		CodeHash:        append([]byte(nil), acc.CodeHash...),
		StorageRoot:     append([]byte(nil), acc.StorageRoot...),
	}
	return clone
}

func (m *mockState) LoyaltyGlobalConfig() (*GlobalConfig, error) {
	return m.cfg.Clone(), nil
}

func (m *mockState) GetAccount(addr []byte) (*types.Account, error) {
	key := string(addr)
	if acc, ok := m.accounts[key]; ok {
		return cloneAccount(acc), nil
	}
	return cloneAccount(nil), nil
}

func (m *mockState) PutAccount(addr []byte, account *types.Account) error {
	key := string(addr)
	m.accounts[key] = cloneAccount(account)
	return nil
}

func (m *mockState) LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error) {
	if dayMap, ok := m.daily[day]; ok {
		if amt, exists := dayMap[string(addr)]; exists {
			return new(big.Int).Set(amt), nil
		}
	}
	return big.NewInt(0), nil
}

func (m *mockState) SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error {
	if _, ok := m.daily[day]; !ok {
		m.daily[day] = make(map[string]*big.Int)
	}
	m.daily[day][string(addr)] = new(big.Int).Set(amount)
	return nil
}

func (m *mockState) LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error) {
	if amt, ok := m.total[string(addr)]; ok {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockState) SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error {
	m.total[string(addr)] = new(big.Int).Set(amount)
	return nil
}

func (m *mockState) AppendEvent(evt *types.Event) {
	if evt == nil {
		return
	}
	attrs := make(map[string]string, len(evt.Attributes))
	for k, v := range evt.Attributes {
		attrs[k] = v
	}
	m.events = append(m.events, types.Event{Type: evt.Type, Attributes: attrs})
}

func (m *mockState) QueuePendingBaseReward(ctx *BaseRewardContext, reward *big.Int) {
	if reward == nil {
		return
	}
	entry := queuedReward{amount: new(big.Int).Set(reward)}
	if ctx != nil {
		entry.txHash = ctx.TxHash
	}
	m.queued = append(m.queued, entry)
}

func (m *mockState) addAccount(addr []byte, acc *types.Account) {
	m.accounts[string(addr)] = cloneAccount(acc)
}

func newConfig(baseBps uint32, minSpend, capPerTx, dailyCap int64, treasury []byte) *GlobalConfig {
	return (&GlobalConfig{
		Active:       true,
		Treasury:     append([]byte(nil), treasury...),
		BaseBps:      baseBps,
		MinSpend:     big.NewInt(minSpend),
		CapPerTx:     big.NewInt(capPerTx),
		DailyCapUser: big.NewInt(dailyCap),
	}).Normalize()
}

func TestApplyBaseRewardHappyPath(t *testing.T) {
	treasury := []byte("treasury")
	from := []byte("from")
	cfg := newConfig(5_000, 100, 500, 1000, treasury)
	state := newMockState(cfg)
	state.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 2, 15, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	engine.ApplyBaseReward(state, ctx)

	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change, got %s", ctx.FromAccount.BalanceZNHB.String())
	}
	if len(state.queued) != 1 {
		t.Fatalf("expected one queued reward, got %d", len(state.queued))
	}
	if state.queued[0].amount.String() != "500" {
		t.Fatalf("expected queued reward 500, got %s", state.queued[0].amount.String())
	}
	treasuryAcc, _ := state.GetAccount(treasury)
	if got := treasuryAcc.BalanceZNHB.String(); got != "1000" {
		t.Fatalf("expected treasury balance unchanged at 1000, got %s", got)
	}
	daily, _ := state.LoyaltyBaseDailyAccrued(from, "2024-01-02")
	if daily.String() != "500" {
		t.Fatalf("expected daily accrued 500, got %s", daily.String())
	}
	total, _ := state.LoyaltyBaseTotalAccrued(from)
	if total.String() != "500" {
		t.Fatalf("expected total accrued 500, got %s", total.String())
	}
	if len(state.events) != 1 || state.events[0].Type != eventBaseAccrued {
		t.Fatalf("expected accrued event, got %#v", state.events)
	}
	if state.events[0].Attributes["reward"] != "500" {
		t.Fatalf("expected reward attribute 500, got %s", state.events[0].Attributes["reward"])
	}
	if got := state.events[0].Attributes["baseBps"]; got != "5000" {
		t.Fatalf("expected baseBps attribute 5000, got %s", got)
	}
}

func TestApplyBaseRewardPerTxCap(t *testing.T) {
	treasury := []byte("treasury")
	from := []byte("from")
	cfg := newConfig(2000, 0, 30, 0, treasury)
	state := newMockState(cfg)
	state.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(100), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}
	NewEngine().ApplyBaseReward(state, ctx)
	if len(state.events) == 0 {
		t.Fatalf("expected event to be recorded")
	}
	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change")
	}
	if state.events[0].Attributes["reward"] != "30" {
		t.Fatalf("expected reward attribute 30, got %s", state.events[0].Attributes["reward"])
	}
	if got := state.events[0].Attributes["baseBps"]; got != "2000" {
		t.Fatalf("expected baseBps attribute 2000, got %s", got)
	}
	if len(state.queued) != 1 || state.queued[0].amount.String() != "30" {
		t.Fatalf("expected queued reward 30, got %#v", state.queued)
	}
}

func TestApplyBaseRewardDailyCap(t *testing.T) {
	treasury := []byte("treasury")
	from := []byte("from")
	cfg := newConfig(1000, 0, 1000, 60, treasury)
	state := newMockState(cfg)
	state.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	state.SetLoyaltyBaseDailyAccrued(from, "2024-01-04", big.NewInt(50))
	state.SetLoyaltyBaseTotalAccrued(from, big.NewInt(50))
	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 4, 10, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}
	NewEngine().ApplyBaseReward(state, ctx)
	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change")
	}
	daily, _ := state.LoyaltyBaseDailyAccrued(from, "2024-01-04")
	if daily.String() != "60" {
		t.Fatalf("expected daily accrued 60, got %s", daily.String())
	}
	total, _ := state.LoyaltyBaseTotalAccrued(from)
	if total.String() != "60" {
		t.Fatalf("expected total accrued 60, got %s", total.String())
	}
	if len(state.queued) != 1 || state.queued[0].amount.String() != "10" {
		t.Fatalf("expected queued reward 10, got %#v", state.queued)
	}
}

func TestApplyBaseRewardInsufficientTreasury(t *testing.T) {
	treasury := []byte("treasury")
	from := []byte("from")
	cfg := newConfig(1000, 0, 1000, 0, treasury)
	state := newMockState(cfg)
	state.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(20), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 5, 10, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}
	NewEngine().ApplyBaseReward(state, ctx)
	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward due to insufficient treasury")
	}
	if len(state.events) != 1 || state.events[0].Type != eventBaseSkipped {
		t.Fatalf("expected skip event, got %#v", state.events)
	}
	if state.events[0].Attributes["reason"] != "treasury_insufficient" {
		t.Fatalf("expected treasury_insufficient reason, got %s", state.events[0].Attributes["reason"])
	}
	if len(state.queued) != 0 {
		t.Fatalf("expected no queued rewards, got %#v", state.queued)
	}
}

func TestApplyBaseRewardDeterminism(t *testing.T) {
	treasury := []byte("treasury")
	from := []byte("from")
	cfg := newConfig(750, 0, 1000, 1000, treasury)
	stateA := newMockState(cfg)
	stateA.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	stateB := newMockState(cfg)
	stateB.addAccount(treasury, &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	ctxA := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1234),
		Timestamp:   time.Date(2024, 1, 6, 23, 59, 0, 0, time.FixedZone("custom", -3*3600)),
		FromAccount: &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)},
	}
	ctxB := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("to"),
		Token:       "NHB",
		Amount:      big.NewInt(1234),
		Timestamp:   time.Date(2024, 1, 6, 23, 59, 0, 0, time.FixedZone("custom", -3*3600)),
		FromAccount: &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)},
	}

	engine := NewEngine()
	engine.ApplyBaseReward(stateA, ctxA)
	engine.ApplyBaseReward(stateB, ctxB)

	if len(stateA.events) == 0 || len(stateB.events) == 0 {
		t.Fatalf("expected events to be emitted")
	}
	if !reflect.DeepEqual(stateA.events, stateB.events) {
		t.Fatalf("expected deterministic events, got %v vs %v", stateA.events, stateB.events)
	}
	if stateA.events[0].Attributes["day"] == "" {
		t.Fatalf("expected day attribute to be set")
	}
	if len(stateA.queued) != 1 || len(stateB.queued) != 1 {
		t.Fatalf("expected queued rewards recorded")
	}
	if stateA.queued[0].amount.Cmp(stateB.queued[0].amount) != 0 {
		t.Fatalf("expected deterministic queued rewards, got %s vs %s", stateA.queued[0].amount.String(), stateB.queued[0].amount.String())
	}
}

func TestApplyBaseRewardDefaultRatePrecision(t *testing.T) {
	treasury := []byte("precision-treasury")
	cfg := (&GlobalConfig{
		Active:       true,
		Treasury:     append([]byte(nil), treasury...),
		BaseBps:      50,
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
	}).Normalize()
	state := newMockState(cfg)

	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	treasuryBalance := new(big.Int).Mul(big.NewInt(1_000), unit)
	state.addAccount(treasury, &types.Account{BalanceZNHB: treasuryBalance, BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	amount := new(big.Int).Mul(big.NewInt(100), unit)
	from := []byte("spender")
	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        append([]byte(nil), from...),
		To:          []byte("merchant"),
		Token:       "NHB",
		Amount:      amount,
		Timestamp:   time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	engine.ApplyBaseReward(state, ctx)

	want := new(big.Int).Quo(new(big.Int).Mul(new(big.Int).Set(amount), big.NewInt(50)), big.NewInt(int64(BaseRewardBpsDenominator)))
	if len(state.queued) != 1 || state.queued[0].amount.Cmp(want) != 0 {
		t.Fatalf("expected queued reward %s, got %#v", want.String(), state.queued)
	}
	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no immediate balance change")
	}
	treasuryAcc, _ := state.GetAccount(treasury)
	if treasuryAcc.BalanceZNHB.Cmp(treasuryBalance) != 0 {
		t.Fatalf("expected treasury balance unchanged %s, got %s", treasuryBalance.String(), treasuryAcc.BalanceZNHB.String())
	}
}
