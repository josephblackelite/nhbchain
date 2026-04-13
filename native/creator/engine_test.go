package creator

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
)

type mockState struct {
	contents map[string]*Content
	stakes   map[string]*Stake
	ledgers  map[string]*PayoutLedger
	accounts map[string]*types.Account
	rate     *RateLimitSnapshot
}

func newMockState() *mockState {
	return &mockState{
		contents: make(map[string]*Content),
		stakes:   make(map[string]*Stake),
		ledgers:  make(map[string]*PayoutLedger),
		accounts: make(map[string]*types.Account),
	}
}

func (m *mockState) CreatorContentGet(id string) (*Content, bool, error) {
	content, ok := m.contents[id]
	if !ok {
		return nil, false, nil
	}
	clone := *content
	clone.Hash = content.Hash
	if content.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	return &clone, true, nil
}

func (m *mockState) CreatorContentPut(content *Content) error {
	if content == nil {
		return nil
	}
	clone := *content
	clone.Hash = content.Hash
	if content.TotalTips != nil {
		clone.TotalTips = new(big.Int).Set(content.TotalTips)
	}
	if content.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(content.TotalStake)
	}
	m.contents[content.ID] = &clone
	return nil
}

func stakeKey(creator [20]byte, fan [20]byte) string {
	return string(append(append([]byte{}, creator[:]...), fan[:]...))
}

func (m *mockState) CreatorStakeGet(creator [20]byte, fan [20]byte) (*Stake, bool, error) {
	stake, ok := m.stakes[stakeKey(creator, fan)]
	if !ok {
		return nil, false, nil
	}
	clone := *stake
	if stake.Amount != nil {
		clone.Amount = new(big.Int).Set(stake.Amount)
	}
	if stake.Shares != nil {
		clone.Shares = new(big.Int).Set(stake.Shares)
	}
	return &clone, true, nil
}

func (m *mockState) CreatorStakePut(stake *Stake) error {
	if stake == nil {
		return nil
	}
	clone := *stake
	if stake.Amount != nil {
		clone.Amount = new(big.Int).Set(stake.Amount)
	}
	if stake.Shares != nil {
		clone.Shares = new(big.Int).Set(stake.Shares)
	}
	m.stakes[stakeKey(stake.Creator, stake.Fan)] = &clone
	return nil
}

func (m *mockState) CreatorStakeDelete(creator [20]byte, fan [20]byte) error {
	delete(m.stakes, stakeKey(creator, fan))
	return nil
}

func (m *mockState) CreatorPayoutLedgerGet(creator [20]byte) (*PayoutLedger, bool, error) {
	ledger, ok := m.ledgers[string(creator[:])]
	if !ok {
		return nil, false, nil
	}
	return ledger.Clone(), true, nil
}

func (m *mockState) CreatorPayoutLedgerPut(ledger *PayoutLedger) error {
	if ledger == nil {
		return nil
	}
	m.ledgers[string(ledger.Creator[:])] = ledger.Clone()
	return nil
}

func (m *mockState) CreatorRateLimitGet() (*RateLimitSnapshot, bool, error) {
	if m.rate == nil {
		return nil, false, nil
	}
	return m.rate.Clone(), true, nil
}

func (m *mockState) CreatorRateLimitPut(snapshot *RateLimitSnapshot) error {
	if snapshot == nil {
		m.rate = nil
		return nil
	}
	m.rate = snapshot.Clone()
	return nil
}

func (m *mockState) GetAccount(addr []byte) (*types.Account, error) {
	if acc, ok := m.accounts[string(addr)]; ok && acc != nil {
		return cloneAccount(acc), nil
	}
	return nil, nil
}

func (m *mockState) PutAccount(addr []byte, account *types.Account) error {
	if account == nil {
		delete(m.accounts, string(addr))
		return nil
	}
	m.accounts[string(addr)] = cloneAccount(account)
	return nil
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return nil
	}
	clone := *acc
	if acc.BalanceNHB != nil {
		clone.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	}
	if acc.BalanceZNHB != nil {
		clone.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	}
	if acc.Stake != nil {
		clone.Stake = new(big.Int).Set(acc.Stake)
	}
	return &clone
}

func (m *mockState) setAccount(addr [20]byte, amount int64) {
	m.accounts[string(addr[:])] = &types.Account{BalanceNHB: big.NewInt(amount), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func (m *mockState) account(addr [20]byte) *types.Account {
	if acc, ok := m.accounts[string(addr[:])]; ok {
		return cloneAccount(acc)
	}
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func sumBalances(state *mockState, addrs ...[20]byte) *big.Int {
	total := big.NewInt(0)
	for _, addr := range addrs {
		acc := state.account(addr)
		total = new(big.Int).Add(total, acc.BalanceNHB)
	}
	return total
}

func addr(last byte) [20]byte {
	var out [20]byte
	out[19] = last
	return out
}

func TestStakeRateLimitPersistsAcrossRestart(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() int64 { return 100 })

	fan := addr(0x01)
	nearCap := new(big.Int).Sub(fanStakeEpochCap, big.NewInt(5))
	if err := engine.enforceStakeLimit(fan, nearCap); err != nil {
		t.Fatalf("initial stake limit application failed: %v", err)
	}
	if err := engine.enforceStakeLimit(fan, big.NewInt(4)); err != nil {
		t.Fatalf("additional stake within window failed: %v", err)
	}
	if err := engine.enforceStakeLimit(fan, big.NewInt(2)); !errors.Is(err, errStakeEpochCap) {
		t.Fatalf("expected stake limit breach before restart: %v", err)
	}

	restarted := NewEngine()
	restarted.SetState(state)
	restarted.SetNowFunc(func() int64 { return 100 })

	if err := restarted.enforceStakeLimit(fan, big.NewInt(2)); !errors.Is(err, errStakeEpochCap) {
		t.Fatalf("stake limit did not persist across restart: %v", err)
	}
}

func TestTipRateLimitPersistsAcrossRestart(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() int64 { return 200 })

	creator := addr(0x02)
	now := int64(200)
	for i := 0; i < tipRateBurst; i++ {
		if err := engine.enforceTipLimit(creator, now); err != nil {
			t.Fatalf("tip limit warmup failed at %d: %v", i, err)
		}
	}
	if err := engine.enforceTipLimit(creator, now); !errors.Is(err, errTipRateLimited) {
		t.Fatalf("expected tip limit breach before restart: %v", err)
	}

	restarted := NewEngine()
	restarted.SetState(state)
	restarted.SetNowFunc(func() int64 { return 200 })

	if err := restarted.enforceTipLimit(creator, now); !errors.Is(err, errTipRateLimited) {
		t.Fatalf("tip limit did not persist across restart: %v", err)
	}
}

func TestTipAndClaimPreservesTotalSupply(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	payoutVault := addr(0xAA)
	rewards := addr(0xBB)
	engine.SetPayoutVault(payoutVault)
	engine.SetRewardsTreasury(rewards)

	fan := addr(0x01)
	creator := addr(0x02)
	state.setAccount(fan, 10_000)
	state.setAccount(creator, 0)
	state.setAccount(payoutVault, 0)
	state.setAccount(rewards, 0)

	content := &Content{ID: "content-1", Creator: creator, TotalTips: big.NewInt(0), TotalStake: big.NewInt(0)}
	if err := state.CreatorContentPut(content); err != nil {
		t.Fatalf("failed to seed content: %v", err)
	}

	initialTotal := sumBalances(state, fan, creator, payoutVault, rewards)

	if _, err := engine.TipContent(fan, content.ID, big.NewInt(250)); err != nil {
		t.Fatalf("tip failed: %v", err)
	}

	afterTip := sumBalances(state, fan, creator, payoutVault, rewards)
	if initialTotal.Cmp(afterTip) != 0 {
		t.Fatalf("total supply changed after tip: want %s got %s", initialTotal, afterTip)
	}

	ledger, payout, err := engine.ClaimPayouts(creator)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if payout.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("unexpected payout amount: %s", payout)
	}
	if ledger.PendingDistribution.Sign() != 0 {
		t.Fatalf("expected pending distribution to reset, got %s", ledger.PendingDistribution)
	}

	finalTotal := sumBalances(state, fan, creator, payoutVault, rewards)
	if initialTotal.Cmp(finalTotal) != 0 {
		t.Fatalf("total supply changed after claim: want %s got %s", initialTotal, finalTotal)
	}
}

func TestStakeClaimUnstakeKeepsBalancesConserved(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	payoutVault := addr(0xCC)
	rewards := addr(0xDD)
	engine.SetPayoutVault(payoutVault)
	engine.SetRewardsTreasury(rewards)

	fan := addr(0x10)
	creator := addr(0x11)
	state.setAccount(fan, 10_000)
	state.setAccount(creator, 0)
	state.setAccount(payoutVault, 0)
	state.setAccount(rewards, 500)

	ledger := newLedger(creator)
	if err := state.CreatorPayoutLedgerPut(ledger); err != nil {
		t.Fatalf("failed to seed ledger: %v", err)
	}

	initialTotal := sumBalances(state, fan, creator, payoutVault, rewards)

	deposit := big.NewInt(5_000)
	stake, reward, err := engine.StakeCreator(fan, creator, deposit)
	if err != nil {
		t.Fatalf("stake failed: %v", err)
	}
	if reward.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("unexpected reward minted: %s", reward)
	}
	if stake.Amount.Cmp(deposit) != 0 {
		t.Fatalf("unexpected stake amount: %s", stake.Amount)
	}
	vaultBalance := state.account(payoutVault).BalanceNHB
	if vaultBalance.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("reward not deposited to vault, got %s", vaultBalance)
	}
	treasuryBalance := state.account(rewards).BalanceNHB
	if treasuryBalance.Cmp(big.NewInt(375)) != 0 {
		t.Fatalf("treasury not debited correctly, got %s", treasuryBalance)
	}

	if _, amt, err := engine.ClaimPayouts(creator); err != nil {
		t.Fatalf("claim failed: %v", err)
	} else if amt.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("unexpected claim amount: %s", amt)
	}

	redeemedShares := new(big.Int).Set(stake.Shares)
	if redeemedShares.Sign() == 0 {
		t.Fatalf("expected stake shares to be minted")
	}
	if _, err := engine.UnstakeCreator(fan, creator, redeemedShares); err != nil {
		t.Fatalf("unstake failed: %v", err)
	}

	finalTotal := sumBalances(state, fan, creator, payoutVault, rewards)
	if initialTotal.Cmp(finalTotal) != 0 {
		t.Fatalf("total supply changed after stake cycle: want %s got %s", initialTotal, finalTotal)
	}
}
