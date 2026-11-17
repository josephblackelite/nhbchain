package loyalty

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// Some callers persist BaseRewardContext.FromAccount and ToAccount themselves
// after the engine returns, while the engine loads the paymaster and the
// customer from the state as separate objects and writes them back. A paymaster
// that is the spender or the merchant is therefore two objects for one
// address, and the caller's later write could overwrite the engine's debit (or
// the reverse): the reward would be minted or destroyed rather than moved. The
// engine therefore keeps the caller's in-memory objects in step with what it
// stored, so both writes agree. mockState hands out and stores copies, like
// the real state, so these tests see the same thing.

type aliasedRewardSetup struct {
	state                        *mockProgramState
	ctx                          *BaseRewardContext
	from, merchant, paymaster    [20]byte
	fromAccount, merchantAccount *types.Account
}

// newAliasedRewardSetup builds a 5% program (a reward of 50 on a spend of
// 1000) whose paymaster is the spender, the merchant or a separate wallet.
func newAliasedRewardSetup(t *testing.T, paymasterIsSpender, paymasterIsMerchant bool) *aliasedRewardSetup {
	t.Helper()
	cfg := newConfig(0, 0, 0, 0, []byte("treasury"))
	state := newMockProgramState(cfg)

	s := &aliasedRewardSetup{state: state}
	s.from[19] = 0x01
	s.merchant[19] = 0x02
	s.paymaster[19] = 0x03
	switch {
	case paymasterIsSpender:
		s.paymaster = s.from
	case paymasterIsMerchant:
		s.paymaster = s.merchant
	}

	var programID ProgramID
	programID[31] = 0xAA
	var businessID BusinessID
	businessID[31] = 0xBB

	for _, addr := range [][20]byte{s.from, s.merchant, s.paymaster} {
		state.addAccount(addr[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1000), Stake: big.NewInt(0)})
	}
	state.addProgram(&Program{
		ID:           programID,
		Owner:        s.merchant,
		TokenSymbol:  "ZNHB",
		AccrualBps:   500,
		MinSpendWei:  big.NewInt(100),
		CapPerTx:     big.NewInt(500),
		DailyCapUser: big.NewInt(1000),
		StartTime:    uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		Active:       true,
	})
	state.addBusinessMapping(s.merchant, &Business{ID: businessID, Owner: s.merchant, Paymaster: s.paymaster, Merchants: [][20]byte{s.merchant}})

	// The caller's in-memory objects, as the transaction handler holds them.
	s.fromAccount, _ = state.GetAccount(s.from[:])
	s.merchantAccount, _ = state.GetAccount(s.merchant[:])
	s.ctx = &BaseRewardContext{
		From:        toBytes(s.from),
		To:          toBytes(s.merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: s.fromAccount,
		ToAccount:   s.merchantAccount,
	}
	return s
}

func (s *aliasedRewardSetup) apply(t *testing.T) {
	t.Helper()
	engine := NewEngine()
	if result := engine.ApplyProgramReward(s.state, &ProgramRewardContext{BaseRewardContext: s.ctx}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}
}

func (s *aliasedRewardSetup) stored(addr [20]byte) *big.Int {
	acc, _ := s.state.GetAccount(addr[:])
	return acc.BalanceZNHB
}

// TestApplyProgramRewardPaymasterIsSpender: a spender funding its own reward
// gains nothing net. The movement nets to zero on one object, so the stored
// balance and the caller's in-memory one both stay where they were.
func TestApplyProgramRewardPaymasterIsSpender(t *testing.T) {
	s := newAliasedRewardSetup(t, true, false)
	s.apply(t)
	if got := s.fromAccount.BalanceZNHB.String(); got != "1000" {
		t.Fatalf("spender's in-memory ZNHB = %s, want 1000 (paid 50, received 50)", got)
	}
	if got := s.stored(s.from).String(); got != "1000" {
		t.Fatalf("stored ZNHB of the spender-paymaster = %s, want 1000", got)
	}
}

// TestApplyProgramRewardMerchantPaymasterKeepsCallerObjectsInStep: the
// merchant funding the reward is debited both in the state and on the caller's
// ToAccount, and the spender is credited both in the state and on
// FromAccount, so a caller that persists its objects afterwards writes back
// exactly what the engine stored instead of a stale balance.
func TestApplyProgramRewardMerchantPaymasterKeepsCallerObjectsInStep(t *testing.T) {
	s := newAliasedRewardSetup(t, false, true)
	s.apply(t)
	if got := s.merchantAccount.BalanceZNHB.String(); got != "950" {
		t.Fatalf("merchant's in-memory ZNHB = %s, want 950", got)
	}
	if got := s.fromAccount.BalanceZNHB.String(); got != "1050" {
		t.Fatalf("spender's in-memory ZNHB = %s, want 1050", got)
	}
	if got, want := s.stored(s.merchant).String(), s.merchantAccount.BalanceZNHB.String(); got != want {
		t.Fatalf("stored ZNHB of the merchant-paymaster = %s, in-memory %s: a caller persisting its object would overwrite the engine's debit", got, want)
	}
	if got, want := s.stored(s.from).String(), s.fromAccount.BalanceZNHB.String(); got != want {
		t.Fatalf("stored ZNHB of the spender = %s, in-memory %s: a caller persisting its object would overwrite the engine's credit", got, want)
	}
}

// TestApplyProgramRewardSeparatePaymasterIsPersisted: a paymaster that is
// neither the spender nor the merchant is nobody's in-memory object, so the
// engine writes its debit itself.
func TestApplyProgramRewardSeparatePaymasterIsPersisted(t *testing.T) {
	s := newAliasedRewardSetup(t, false, false)
	s.apply(t)
	if got := s.stored(s.paymaster).String(); got != "950" {
		t.Fatalf("stored paymaster ZNHB = %s, want 950", got)
	}
	if got := s.fromAccount.BalanceZNHB.String(); got != "1050" {
		t.Fatalf("spender's in-memory ZNHB = %s, want 1050", got)
	}
}
