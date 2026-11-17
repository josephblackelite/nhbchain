package loyalty

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// failingPutState fails PutAccount for one address so the atomicity of the
// paymaster -> customer movement can be exercised.
type failingPutState struct {
	*mockProgramState
	failAddr string
}

func (f *failingPutState) PutAccount(addr []byte, account *types.Account) error {
	if string(addr) == f.failAddr {
		return errors.New("put failed")
	}
	return f.mockProgramState.PutAccount(addr, account)
}

type persistFixture struct {
	state     *mockProgramState
	from      [20]byte
	merchant  [20]byte
	paymaster [20]byte
}

// newPersistFixture builds a 500 bps program (reward 50 on a 1000 spend) with a
// paymaster holding 1000 ZNHB. paymasterRole "customer" or "merchant" puts the
// paymaster on that role's address; anything else keeps it distinct.
func newPersistFixture(paymasterRole string) *persistFixture {
	f := &persistFixture{}
	f.from[19] = 0x01
	f.merchant[19] = 0x02
	f.paymaster[19] = 0x03
	switch paymasterRole {
	case "customer":
		f.paymaster = f.from
	case "merchant":
		f.paymaster = f.merchant
	}
	f.state = newMockProgramState(newConfig(0, 0, 0, 0, []byte("treasury")))

	var programID ProgramID
	programID[31] = 0xAA
	var businessID BusinessID
	businessID[31] = 0xBB
	f.state.addProgram(&Program{
		ID:          programID,
		Owner:       f.merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  500,
		Active:      true,
	})
	f.state.addBusinessMapping(f.merchant, &Business{ID: businessID, Owner: f.merchant, Paymaster: f.paymaster, Merchants: [][20]byte{f.merchant}})
	return f
}

func (f *persistFixture) ctx(from, to *types.Account) *BaseRewardContext {
	return &BaseRewardContext{
		From:        toBytes(f.from),
		To:          toBytes(f.merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: from,
		ToAccount:   to,
	}
}

func znhb(t *testing.T, st ProgramRewardState, addr [20]byte) *big.Int {
	t.Helper()
	acc, err := st.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return acc.BalanceZNHB
}

func newZNHBAccount(n int64) *types.Account {
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(n), Stake: big.NewInt(0)}
}

// TestApplyProgramRewardPersistsCustomerCredit proves the reward reaches the
// customer's stored account, not only the caller's in-memory copy, and that the
// paymaster debit and customer credit are the same amount.
func TestApplyProgramRewardPersistsCustomerCredit(t *testing.T) {
	f := newPersistFixture("")
	f.state.addAccount(f.paymaster[:], newZNHBAccount(1000))
	f.state.addAccount(f.from[:], newZNHBAccount(7))
	fromAcc := newZNHBAccount(7)

	ctx := f.ctx(fromAcc, nil)
	if result := NewEngine().ApplyProgramReward(f.state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected %q, got %q", resultAccrued, result)
	}
	if got := znhb(t, f.state, f.from).String(); got != "57" {
		t.Fatalf("persisted customer balance: want 57, got %s", got)
	}
	if got := znhb(t, f.state, f.paymaster).String(); got != "950" {
		t.Fatalf("persisted paymaster balance: want 950, got %s", got)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "57" {
		t.Fatalf("in-memory customer balance: want 57, got %s", got)
	}
	total := new(big.Int).Add(znhb(t, f.state, f.from), znhb(t, f.state, f.paymaster))
	if total.String() != "1007" {
		t.Fatalf("ZNHB not conserved: want 1007, got %s", total)
	}
}

// TestApplyProgramRewardPaymasterIsCustomer covers the paymaster and the
// customer being one address: the movement nets to zero and nothing is created
// or destroyed.
func TestApplyProgramRewardPaymasterIsCustomer(t *testing.T) {
	f := newPersistFixture("customer")
	f.state.addAccount(f.from[:], newZNHBAccount(1000))
	fromAcc := newZNHBAccount(1000)

	ctx := f.ctx(fromAcc, nil)
	if result := NewEngine().ApplyProgramReward(f.state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected %q, got %q", resultAccrued, result)
	}
	if got := znhb(t, f.state, f.from).String(); got != "1000" {
		t.Fatalf("persisted balance: want 1000, got %s", got)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "1000" {
		t.Fatalf("in-memory balance: want 1000, got %s", got)
	}
}

// TestApplyProgramRewardPaymasterIsMerchant covers the paymaster being the
// merchant the customer paid. Callers that persist their in-memory accounts
// after the hook must not write a stale merchant balance over the debit.
func TestApplyProgramRewardPaymasterIsMerchant(t *testing.T) {
	f := newPersistFixture("merchant")
	f.state.addAccount(f.merchant[:], newZNHBAccount(1000))
	f.state.addAccount(f.from[:], newZNHBAccount(0))
	fromAcc := newZNHBAccount(0)
	toAcc := newZNHBAccount(1000)

	ctx := f.ctx(fromAcc, toAcc)
	if result := NewEngine().ApplyProgramReward(f.state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected %q, got %q", resultAccrued, result)
	}
	if got := znhb(t, f.state, f.from).String(); got != "50" {
		t.Fatalf("persisted customer balance: want 50, got %s", got)
	}
	if got := znhb(t, f.state, f.merchant).String(); got != "950" {
		t.Fatalf("persisted merchant/paymaster balance: want 950, got %s", got)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "50" {
		t.Fatalf("in-memory customer balance: want 50, got %s", got)
	}
	if got := ctx.ToAccount.BalanceZNHB.String(); got != "950" {
		t.Fatalf("in-memory merchant balance must match the stored one: want 950, got %s", got)
	}
}

// TestApplyProgramRewardCustomerCreditFailureRestoresPaymaster proves a failed
// credit never leaves the paymaster debited.
func TestApplyProgramRewardCustomerCreditFailureRestoresPaymaster(t *testing.T) {
	f := newPersistFixture("")
	f.state.addAccount(f.paymaster[:], newZNHBAccount(1000))
	st := &failingPutState{mockProgramState: f.state, failAddr: string(f.from[:])}
	fromAcc := newZNHBAccount(0)

	ctx := f.ctx(fromAcc, nil)
	if result := NewEngine().ApplyProgramReward(st, &ProgramRewardContext{BaseRewardContext: ctx}); result != "recipient_persist_error" {
		t.Fatalf("expected recipient_persist_error, got %q", result)
	}
	if got := znhb(t, f.state, f.paymaster).String(); got != "1000" {
		t.Fatalf("paymaster must be restored: want 1000, got %s", got)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "0" {
		t.Fatalf("in-memory customer must be untouched: want 0, got %s", got)
	}
}
