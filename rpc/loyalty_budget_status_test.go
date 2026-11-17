package rpc

// nhb_getLoyaltyBudgetStatus reports what the loyalty engine has: the budget it
// works out for the day, what it has paid and been asked to pay, and when the day
// rolls over. It used to return the same three literals whatever the chain held.

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/loyalty"
)

func znhbWei(tokens int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(tokens), big.NewInt(1_000_000_000_000_000_000))
}

type loyaltyBudgetResult struct {
	TwapScalingFactor string `json:"twapScalingFactor"`
	BudgetRemaining   string `json:"budgetRemaining"`
	ResetAt           int64  `json:"resetAt"`
	Day               string `json:"day"`
	PaidToday         string `json:"paidToday"`
	ProposedToday     string `json:"proposedToday"`
	GuardFallback     string `json:"guardFallback"`
}

func loyaltyBudgetStatus(t *testing.T, env *testEnv) loyaltyBudgetResult {
	t.Helper()
	rec, rpcErr := serveBearer(t, env.server, "", "nhb_getLoyaltyBudgetStatus")
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("nhb_getLoyaltyBudgetStatus: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	raw, _ := decodeRPCResponse(t, rec)
	var out loyaltyBudgetResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode result %s: %v", raw, err)
	}
	return out
}

func TestLoyaltyBudgetStatusReportsTheEnginesBudget(t *testing.T) {
	env := newTestEnv(t)
	fixed := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	env.node.SetTimeSource(func() time.Time { return fixed })

	cfg := (&loyalty.GlobalConfig{
		Active:   true,
		Treasury: make([]byte, 20),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 5000,
			PriceGuard: loyalty.PriceGuardConfig{
				PricePair:               "ZNHB/USD",
				TwapWindowSeconds:       60,
				PriceMaxAgeSeconds:      60,
				FallbackMinEmissionZNHB: big.NewInt(0),
			},
		},
	}).Normalize()
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		if err := m.SetLoyaltyGlobalConfig(cfg); err != nil {
			return err
		}
		// 100 ZNHB of fees over the trailing week and a 50% cap: a 50 ZNHB day.
		if err := nhbstate.NewRollingFees(m).AddDay(fixed.AddDate(0, 0, -1), big.NewInt(0), znhbWei(100)); err != nil {
			return err
		}
		// The engine was asked for 80 ZNHB today and paid 20 of it.
		if _, err := m.AddProposedTodayZNHB(fixed, znhbWei(80)); err != nil {
			return err
		}
		_, err := m.AddPaidTodayZNHB(fixed, znhbWei(20))
		return err
	}); err != nil {
		t.Fatalf("seed loyalty state: %v", err)
	}
	before := env.node.PendingStateRoot()

	got := loyaltyBudgetStatus(t, env)

	if want := znhbWei(30).String(); got.BudgetRemaining != want {
		t.Fatalf("budgetRemaining %s, want %s (a 50 ZNHB budget less the 20 paid)", got.BudgetRemaining, want)
	}
	if got.PaidToday != znhbWei(20).String() || got.ProposedToday != znhbWei(80).String() {
		t.Fatalf("paidToday %s, proposedToday %s; want 20 and 80 ZNHB", got.PaidToday, got.ProposedToday)
	}
	if got.TwapScalingFactor != "0.25" {
		t.Fatalf("twapScalingFactor %q, want 0.25 (20 paid of 80 asked)", got.TwapScalingFactor)
	}
	if got.Day != "20240501" {
		t.Fatalf("day %q, want 20240501", got.Day)
	}
	if want := time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC).Unix(); got.ResetAt != want {
		t.Fatalf("resetAt %d, want %d (the next UTC midnight)", got.ResetAt, want)
	}
	if got.GuardFallback != "" {
		t.Fatalf("guardFallback %q, want none: the price guard is off", got.GuardFallback)
	}
	if after := env.node.PendingStateRoot(); !bytes.Equal(before, after) {
		t.Fatalf("a read changed the state the next block would carry")
	}
}

// Whatever the chain holds, the answer is what the engine itself computes for the
// day: with no loyalty configuration the budget is zero, not a number made up.
func TestLoyaltyBudgetStatusWithNothingConfiguredIsWhatTheEngineComputes(t *testing.T) {
	env := newTestEnv(t)
	fixed := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	env.node.SetTimeSource(func() time.Time { return fixed })

	var want *big.Int
	if err := env.node.WithStateView(func(m *nhbstate.Manager) error {
		remaining, _, err := m.GetRemainingDailyBudgetZNHB(fixed)
		want = remaining
		return err
	}); err != nil {
		t.Fatalf("engine budget: %v", err)
	}

	got := loyaltyBudgetStatus(t, env)
	if got.BudgetRemaining != want.String() {
		t.Fatalf("budgetRemaining %s, want %s as the engine computes it", got.BudgetRemaining, want)
	}
	if got.TwapScalingFactor != "1.0" || got.PaidToday != "0" || got.ProposedToday != "0" {
		t.Fatalf("with nothing proposed or paid: factor %q, paid %s, proposed %s; want 1.0, 0, 0", got.TwapScalingFactor, got.PaidToday, got.ProposedToday)
	}
}
