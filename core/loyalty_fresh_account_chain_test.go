package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

// PL-EXT-17: the same NHB passed through fresh accounts.
//
// A reporter sent 10,000 NHB along six new accounts, A to F, and every sender
// earned the base reward, because each sender's meters (the per-user daily cap,
// the per-counterparty cap) start at zero and a new account has no history. The
// reward is paid on what an account sends, not on value that left its owner's
// hands, so one stack of NHB under one person's control can earn the per-
// transaction reward at every hop.
//
// Until the base-reward budget floor (PL-CLM-02) there was little to take: with
// no fees the budget was zero and nothing was paid. A funded floor is what turns
// this into a real drain, so these tests exist before any funding is switched on.
// They state three facts about the code as built, and one acceptance test for the
// fix (skipped: no fix is built, and which one is an owner decision):
//
//  1. every hop of a chain of fresh accounts earns, whatever the caps;
//  2. the day's budget is a hard ceiling: the attack can capture the budget, never
//     more than it;
//  3. captured budget is taken from honest payers in the same settlement.

type abuseEnv struct {
	t        *testing.T
	sp       *StateProcessor
	manager  *nhbstate.Manager
	treasury [20]byte
	keys     []*crypto.PrivateKey
	nonce    []uint64
}

// newAbuseEnv builds a chain in the live reward shape (50 bps, 1 NHB minimum
// spend, 50 ZNHB per transaction, 200 ZNHB per user per day, a 5,000 USD daily
// cap with the price at 1) with a funded treasury, no fees, and a budget floor of
// floorZNHB ZNHB from block 100.
func newAbuseEnv(t *testing.T, accounts int, counterpartyCap *big.Int, floorZNHB int64) *abuseEnv {
	t.Helper()
	sp, manager := newTestStateProcessor(t)
	env := &abuseEnv{t: t, sp: sp, manager: manager}
	env.treasury[19] = 0x01
	cfg := (&loyalty.GlobalConfig{
		Active:               true,
		Treasury:             env.treasury[:],
		BaseBps:              50,
		MinSpend:             znhb(1),
		CapPerTx:             znhb(50),
		DailyCapUser:         znhb(200),
		DailyCapCounterparty: counterpartyCap,
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 6_000,
			DailyCapUsd:            5_000,
			EnableProRate:          true,
			EnableProRateSet:       true,
			PriceGuard:             loyalty.PriceGuardConfig{Enabled: false},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set loyalty config: %v", err)
	}
	setAccountBalance(t, sp, env.treasury, znhb(1_000_000))
	sp.SetLoyaltyBudgetFloor(100, znhb(floorZNHB))
	for i := 0; i < accounts; i++ {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		env.keys = append(env.keys, key)
		env.nonce = append(env.nonce, 0)
		acc := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.setAccount(key.PubKey().Address().Bytes(), acc); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	sp.BeginBlock(100, floorDay)
	return env
}

func (e *abuseEnv) addr(i int) []byte { return e.keys[i].PubKey().Address().Bytes() }

func (e *abuseEnv) fundNHB(i int, amount *big.Int) {
	e.t.Helper()
	acc, err := e.sp.getAccount(e.addr(i))
	if err != nil {
		e.t.Fatalf("load: %v", err)
	}
	acc.BalanceNHB = new(big.Int).Add(acc.BalanceNHB, amount)
	if err := e.sp.setAccount(e.addr(i), acc); err != nil {
		e.t.Fatalf("fund: %v", err)
	}
}

// pay sends amount NHB from account from to account to as a plain transfer.
func (e *abuseEnv) pay(from, to int, amount *big.Int) {
	e.t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    e.nonce[from],
		To:       e.addr(to),
		Value:    new(big.Int).Set(amount),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(e.keys[from].PrivateKey); err != nil {
		e.t.Fatalf("sign: %v", err)
	}
	if err := e.sp.ApplyTransaction(tx); err != nil {
		e.t.Fatalf("pay %d -> %d: %v", from, to, err)
	}
	e.nonce[from]++
}

// passAlong moves the same stack of NHB from account first through to account last.
func (e *abuseEnv) passAlong(first, last int, amount *big.Int) {
	e.t.Helper()
	for i := first; i < last; i++ {
		e.pay(i, i+1, amount)
	}
}

func (e *abuseEnv) settle() { e.sp.EndBlockRewards(floorDay) }

func (e *abuseEnv) znhbOf(i int) *big.Int {
	e.t.Helper()
	acc, err := e.sp.getAccount(e.addr(i))
	if err != nil {
		e.t.Fatalf("load: %v", err)
	}
	return new(big.Int).Set(acc.BalanceZNHB)
}

func (e *abuseEnv) totalZNHB(from, to int) *big.Int {
	total := big.NewInt(0)
	for i := from; i <= to; i++ {
		total.Add(total, e.znhbOf(i))
	}
	return total
}

// Fact 1. Six fresh accounts, the same 10,000 NHB: each of the five senders earns
// the full 50 ZNHB per-transaction reward, 250 ZNHB for value that never left one
// owner's control, five times what the same 10,000 NHB earns when it is spent once.
func TestPLEXT17_FreshAccountChain_EverySenderEarnsForTheSameNHB(t *testing.T) {
	for name, pairCap := range map[string]*big.Int{
		"no counterparty cap (the relaunch genesis sets none)": nil,
		"a 100 ZNHB per-pair cap":                              znhb(100),
	} {
		t.Run(name, func(t *testing.T) {
			env := newAbuseEnv(t, 6, pairCap, 10_000)
			stack := znhb(10_000)
			env.fundNHB(0, stack)
			env.passAlong(0, 5, stack)
			env.settle()
			for i := 0; i < 5; i++ {
				if got := env.znhbOf(i); got.Cmp(znhb(50)) != 0 {
					t.Fatalf("sender %d earned %s ZNHB wei, want the full 50 ZNHB", i, got)
				}
			}
			if got := env.znhbOf(5); got.Sign() != 0 {
				t.Fatalf("the last holder sent nothing and earned %s", got)
			}
			if total := env.totalZNHB(0, 5); total.Cmp(znhb(250)) != 0 {
				t.Fatalf("total paid %s, want 250 ZNHB for one stack of 10,000 NHB", total)
			}
			// What the same 10,000 NHB earns when it is spent once.
			honest := newAbuseEnv(t, 2, pairCap, 10_000)
			honest.fundNHB(0, stack)
			honest.pay(0, 1, stack)
			honest.settle()
			if got := honest.znhbOf(0); got.Cmp(znhb(50)) != 0 {
				t.Fatalf("a single honest payment earned %s, want 50 ZNHB", got)
			}
		})
	}
}

// Fact 2. However many fresh accounts and however many rounds, what the attack
// takes is bounded by the day's budget: the floor, here under the USD cap.
func TestPLEXT17_FreshAccountChain_CannotExceedTheDailyBudget(t *testing.T) {
	const accounts = 120
	env := newAbuseEnv(t, accounts, nil, 1_000) // floor of 1,000 ZNHB, under the 5,000 cap
	stack := znhb(10_000)
	env.fundNHB(0, stack)
	env.passAlong(0, accounts-1, stack) // 119 hops would earn 5,950 ZNHB if unbounded
	env.settle()
	total := env.totalZNHB(0, accounts-1)
	if total.Cmp(znhb(1_000)) > 0 {
		t.Fatalf("the attack was paid %s, more than the 1,000 ZNHB budget", total)
	}
	// Each reward is pro-rated with integer division, so the sum is short of the
	// budget by less than a wei per reward.
	if short := new(big.Int).Sub(znhb(1_000), total); short.Cmp(big.NewInt(accounts)) >= 0 {
		t.Fatalf("the attack was paid %s; with demand far above the budget it should take (nearly) all of it (1,000 ZNHB)", total)
	}
	treasury, err := env.sp.getAccount(env.treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if paidOut := new(big.Int).Sub(znhb(1_000_000), treasury.BalanceZNHB); paidOut.Cmp(total) != 0 {
		t.Fatalf("treasury paid out %s but the accounts were credited %s", paidOut, total)
	}
}

// Fact 3. The budget is shared: in one settlement the fresh-account hops and an
// honest payer are pro-rated together, so the honest payer gets less than the
// reward the same payment earns on a quiet day.
func TestPLEXT17_FreshAccountChain_CrowdsOutAnHonestPayer(t *testing.T) {
	const accounts = 62 // 61 hops: 3,050 ZNHB of demand, plus the honest payer's 50
	env := newAbuseEnv(t, accounts+2, nil, 1_000)
	honest, merchant := accounts, accounts+1
	stack := znhb(10_000)
	env.fundNHB(0, stack)
	env.fundNHB(honest, stack)
	env.passAlong(0, accounts-1, stack)
	env.pay(honest, merchant, stack)
	env.settle()
	got := env.znhbOf(honest)
	if got.Cmp(znhb(50)) >= 0 {
		t.Fatalf("the honest payer earned %s: expected a pro-rated share below the full 50 ZNHB", got)
	}
	if got.Sign() <= 0 {
		t.Fatalf("the honest payer earned nothing")
	}
}

// Acceptance test for whatever fix the owner chooses (for example: no base reward
// on NHB that arrived within the day from an account that itself earned a reward
// on it, or a minimum age or history for an account to earn). SKIPPED: nothing of
// the kind is built. Enable it, and expect the three tests above to be revisited,
// when it is. Until it passes, no funding should be switched on
// (LoyaltyBudgetFloorActivationHeight stays 0).
func TestPLEXT17_Mitigation_AcceptanceTest(t *testing.T) {
	t.Skip("PL-EXT-17 mitigation not built (owner decision); funding must stay off until this passes")
	env := newAbuseEnv(t, 6, nil, 10_000)
	stack := znhb(10_000)
	env.fundNHB(0, stack)
	env.passAlong(0, 5, stack)
	env.settle()
	// One stack of NHB must not earn more than one payment's reward in total.
	if total := env.totalZNHB(0, 5); total.Cmp(znhb(50)) > 0 {
		t.Fatalf("a stack of NHB passed through five fresh accounts earned %s, more than one reward (50 ZNHB)", total)
	}
}
