package loyalty

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

// PL-R5T-15: the reward path debits a paymaster only when the wallet is the
// owner's own or has a standing record of consent, once the rule is active.

// consentState layers the rule's two methods over the plain mock program state.
type consentState struct {
	*mockProgramState
	required bool
	assigned map[[20]byte]bool
	readErr  error
}

func (c *consentState) LoyaltyPaymasterConsentRequired() bool { return c.required }

func (c *consentState) LoyaltyPaymasterAssigned(id BusinessID, paymaster [20]byte) (bool, error) {
	if c.readErr != nil {
		return false, c.readErr
	}
	return c.assigned[paymaster], nil
}

type consentCase struct {
	state     *consentState
	ctx       *BaseRewardContext
	paymaster [20]byte
	owner     [20]byte
	from      [20]byte
}

// newConsentCase is the happy-path setup of TestApplyProgramRewardHappyPath with
// the paymaster a wallet other than the business owner.
func newConsentCase(t *testing.T, paymasterIsOwner bool) *consentCase {
	t.Helper()
	cfg := newConfig(0, 0, 0, 0, []byte("treasury"))
	state := &consentState{mockProgramState: newMockProgramState(cfg), assigned: map[[20]byte]bool{}}
	c := &consentCase{state: state}
	c.from[19] = 0x01
	var merchant [20]byte
	merchant[19] = 0x02
	c.owner = merchant
	c.paymaster[19] = 0x03
	if paymasterIsOwner {
		c.paymaster = c.owner
	}
	var programID ProgramID
	programID[31] = 0xAA
	var businessID BusinessID
	businessID[31] = 0xBB
	state.addAccount(c.paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	state.addProgram(&Program{
		ID: programID, Owner: merchant, TokenSymbol: "ZNHB", AccrualBps: 500,
		MinSpendWei: big.NewInt(100), CapPerTx: big.NewInt(500), DailyCapUser: big.NewInt(1000),
		StartTime: uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()), Active: true,
	})
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: c.paymaster, Merchants: [][20]byte{merchant}})
	c.ctx = &BaseRewardContext{
		From: toBytes(c.from), To: toBytes(merchant), Token: "NHB", Amount: big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)},
	}
	return c
}

func (c *consentCase) run() string {
	return NewEngine().ApplyProgramReward(c.state, &ProgramRewardContext{BaseRewardContext: c.ctx})
}

func (c *consentCase) paymasterBalance(t *testing.T) string {
	t.Helper()
	acc, err := c.state.GetAccount(c.paymaster[:])
	if err != nil {
		t.Fatalf("paymaster account: %v", err)
	}
	return acc.BalanceZNHB.String()
}

func TestProgramRewardWithTheRuleOffPaysAnUnconsentedPaymaster(t *testing.T) {
	c := newConsentCase(t, false)
	c.state.required = false
	if got := c.run(); got != resultAccrued {
		t.Fatalf("rule off: result %q, want %q (the legacy behaviour)", got, resultAccrued)
	}
	if got := c.paymasterBalance(t); got != "950" {
		t.Fatalf("rule off: paymaster balance %s, want 950", got)
	}
}

func TestProgramRewardWithTheRuleOnSkipsAWalletWithNoStandingRecord(t *testing.T) {
	c := newConsentCase(t, false)
	c.state.required = true
	if got := c.run(); got != "paymaster_not_consented" {
		t.Fatalf("result %q, want paymaster_not_consented", got)
	}
	if got := c.paymasterBalance(t); got != "1000" {
		t.Fatalf("the unconsented wallet was debited: %s", got)
	}
	if c.ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("the spender was credited")
	}
	if len(c.state.events) != 1 || c.state.events[0].Type != eventProgramSkipped {
		t.Fatalf("want one skip event, got %#v", c.state.events)
	}
	if reason := c.state.events[0].Attributes["reason"]; reason != "paymaster_not_consented" {
		t.Fatalf("skip reason %q", reason)
	}
}

func TestProgramRewardWithTheRuleOnPaysAWalletWithAStandingRecord(t *testing.T) {
	c := newConsentCase(t, false)
	c.state.required = true
	c.state.assigned[c.paymaster] = true
	if got := c.run(); got != resultAccrued {
		t.Fatalf("result %q, want %q", got, resultAccrued)
	}
	if got := c.paymasterBalance(t); got != "950" {
		t.Fatalf("paymaster balance %s, want 950", got)
	}
}

func TestProgramRewardWithTheRuleOnPaysTheOwnersOwnWalletWithoutARecord(t *testing.T) {
	c := newConsentCase(t, true)
	c.state.required = true
	if got := c.run(); got != resultAccrued {
		t.Fatalf("result %q, want %q", got, resultAccrued)
	}
}

func TestProgramRewardWithTheRuleOnFailsClosedWhenTheRecordCannotBeRead(t *testing.T) {
	c := newConsentCase(t, false)
	c.state.required = true
	c.state.readErr = errors.New("read failed")
	if got := c.run(); got != "paymaster_error" {
		t.Fatalf("result %q, want paymaster_error", got)
	}
	if got := c.paymasterBalance(t); got != "1000" {
		t.Fatalf("paymaster debited after a failed read: %s", got)
	}
}

// A state that does not carry the rule (every older fake, and any state built
// before the rule existed) behaves as it always did.
func TestProgramRewardOnAStateWithoutTheRulePaysAsBefore(t *testing.T) {
	c := newConsentCase(t, false)
	plain := c.state.mockProgramState
	if got := NewEngine().ApplyProgramReward(plain, &ProgramRewardContext{BaseRewardContext: c.ctx}); got != resultAccrued {
		t.Fatalf("result %q, want %q", got, resultAccrued)
	}
}
