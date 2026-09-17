package subscriptions

import (
	"math/big"
	"testing"
)

func testConfig() Config {
	return Config{
		ManagementFeeBps:     100, // 1%
		ManagementFeeCapBps:  500,
		MaxRetries:           3,
		RetryIntervalSeconds: 86400,
	}
}

func TestComputeManagementFee(t *testing.T) {
	cfg := testConfig()
	fee := ComputeManagementFee(big.NewInt(10_000), cfg)
	if fee.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("fee = %s, want 100 (1%% of 10000)", fee)
	}

	zeroFeeCfg := cfg
	zeroFeeCfg.ManagementFeeBps = 0
	if got := ComputeManagementFee(big.NewInt(10_000), zeroFeeCfg); got.Sign() != 0 {
		t.Fatalf("expected zero fee when ManagementFeeBps is 0, got %s", got)
	}

	if got := ComputeManagementFee(nil, cfg); got.Sign() != 0 {
		t.Fatalf("expected zero fee for nil amount, got %s", got)
	}

	if got := ComputeManagementFee(big.NewInt(0), cfg); got.Sign() != 0 {
		t.Fatalf("expected zero fee for zero amount, got %s", got)
	}
}

func TestDecideCharge_Success(t *testing.T) {
	cfg := testConfig()
	sub := &Subscription{
		PriceWei:        big.NewInt(10_000),
		IntervalSeconds: 2_592_000,
		FailedAttempts:  0,
	}
	now := uint64(1_000_000)
	decision := DecideCharge(sub, cfg, big.NewInt(50_000), now)

	if !decision.Success {
		t.Fatalf("expected success, got failure: %s", decision.FailureReason)
	}
	if decision.FeeWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("fee = %s, want 100", decision.FeeWei)
	}
	if decision.MerchantNetWei.Cmp(big.NewInt(9_900)) != 0 {
		t.Fatalf("merchant net = %s, want 9900", decision.MerchantNetWei)
	}
	if decision.NewStatus != SubscriptionStatusActive {
		t.Fatalf("status = %v, want active", decision.NewStatus)
	}
	if decision.NewFailedAttempts != 0 {
		t.Fatalf("failed attempts = %d, want 0", decision.NewFailedAttempts)
	}
	if decision.NextChargeAt != now+sub.IntervalSeconds {
		t.Fatalf("next charge at = %d, want %d", decision.NextChargeAt, now+sub.IntervalSeconds)
	}
}

func TestDecideCharge_SuccessResetsFailedAttempts(t *testing.T) {
	cfg := testConfig()
	sub := &Subscription{
		PriceWei:        big.NewInt(10_000),
		IntervalSeconds: 86400,
		FailedAttempts:  2, // was past-due, this charge finally clears it
	}
	decision := DecideCharge(sub, cfg, big.NewInt(20_000), 0)
	if !decision.Success {
		t.Fatalf("expected success")
	}
	if decision.NewFailedAttempts != 0 {
		t.Fatalf("failed attempts = %d, want reset to 0 on success", decision.NewFailedAttempts)
	}
}

func TestDecideCharge_InsufficientBalancePastDue(t *testing.T) {
	cfg := testConfig()
	sub := &Subscription{
		PriceWei:        big.NewInt(10_000),
		IntervalSeconds: 86400,
		FailedAttempts:  0,
	}
	now := uint64(1_000_000)
	decision := DecideCharge(sub, cfg, big.NewInt(500), now)

	if decision.Success {
		t.Fatalf("expected failure for insufficient balance")
	}
	if decision.NewStatus != SubscriptionStatusPastDue {
		t.Fatalf("status = %v, want past_due", decision.NewStatus)
	}
	if decision.NewFailedAttempts != 1 {
		t.Fatalf("failed attempts = %d, want 1", decision.NewFailedAttempts)
	}
	if decision.NextChargeAt != now+cfg.RetryIntervalSeconds {
		t.Fatalf("next charge at = %d, want now+RetryIntervalSeconds", decision.NextChargeAt)
	}
	if decision.FeeWei.Sign() != 0 || decision.MerchantNetWei.Sign() != 0 {
		t.Fatalf("expected zero fee/net on a failed charge")
	}
}

func TestDecideCharge_SuspendsAfterMaxRetries(t *testing.T) {
	cfg := testConfig() // MaxRetries: 3
	sub := &Subscription{
		PriceWei:        big.NewInt(10_000),
		IntervalSeconds: 86400,
		FailedAttempts:  2, // this failure will be attempt #3 == MaxRetries
	}
	decision := DecideCharge(sub, cfg, big.NewInt(0), 0)

	if decision.Success {
		t.Fatalf("expected failure")
	}
	if decision.NewStatus != SubscriptionStatusSuspended {
		t.Fatalf("status = %v, want suspended after reaching MaxRetries", decision.NewStatus)
	}
	if decision.NewFailedAttempts != 3 {
		t.Fatalf("failed attempts = %d, want 3", decision.NewFailedAttempts)
	}
	if decision.NextChargeAt != 0 {
		t.Fatalf("next charge at = %d, want 0 (suspended, dropped from due-index)", decision.NextChargeAt)
	}
}

func TestDecideCharge_ExactBalanceSucceeds(t *testing.T) {
	cfg := testConfig()
	sub := &Subscription{PriceWei: big.NewInt(10_000), IntervalSeconds: 86400}
	decision := DecideCharge(sub, cfg, big.NewInt(10_000), 0)
	if !decision.Success {
		t.Fatalf("expected success when balance exactly equals price")
	}
}

func TestComputeManagementFee_NeverExceedsAmount(t *testing.T) {
	// A pathological config (bps near the denominator) must never let the
	// fee exceed the charge amount itself.
	cfg := Config{ManagementFeeBps: 9_999, ManagementFeeCapBps: 9_999}
	fee := ComputeManagementFee(big.NewInt(3), cfg)
	if fee.Cmp(big.NewInt(3)) > 0 {
		t.Fatalf("fee %s must never exceed the amount 3", fee)
	}
}
