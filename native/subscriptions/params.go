package subscriptions

import (
	"fmt"
	"math"
	"math/big"
)

// Default configuration values. ManagementFeeBps is deliberately modest --
// a small fraction of a subscription payment, positioned well under card
// network / Stripe-class take rates (~2.9%+30c) per explicit product
// direction -- and is bounded by ManagementFeeCapBps so raising it later
// via config never silently exceeds what was originally promised without a
// deploy that also raises the cap.
const (
	ManagementFeeBpsDenominator = 10_000
	DefaultManagementFeeBps     = uint32(100) // 1.00%
	DefaultManagementFeeCapBps  = uint32(500) // 5.00% hard ceiling
	DefaultMaxRetries           = uint32(3)
	DefaultRetryIntervalSeconds = uint64(24 * 60 * 60) // 1 day between dunning retries
)

// Bounds on the terms of a plan. The chain charges every subscription by
// itself, in the lifecycle of a block, with no transaction paying for the work
// (balances change and a history record and an event are added), so how often
// a subscription is charged bounds the work one subscribe transaction can
// cause. They are constants, not configuration: a node whose bounds differed
// from its peers' would accept a plan they refuse.
const (
	// MinPlanIntervalSeconds is the shortest billing interval a plan may have,
	// one day. A shorter one would have the chain charge the same payer on
	// every block for the price of one transaction, and settlement never
	// schedules a subscription's next charge sooner than this after the last one
	// whatever the stored interval says.
	MinPlanIntervalSeconds = uint64(24 * 60 * 60)
	// MaxPlanIntervalSeconds is the longest billing interval and
	// MaxTrialPeriodSeconds the longest trial, ten years of 365 days each. They
	// keep "now plus the interval" far from the end of the uint64 range, where
	// it would wrap around to a time in the past and make the subscription due
	// again at once.
	MaxPlanIntervalSeconds = uint64(10 * 365 * 24 * 60 * 60)
	MaxTrialPeriodSeconds  = MaxPlanIntervalSeconds
)

// minPlanPriceWei is the smallest price a plan may charge per cycle: one whole
// token of either asset, the same dust floor the buyback ask and the lending
// deposit use. A charge only moves its price from the payer to the merchant,
// so two accounts of one owner can run a subscription at the cost of the
// management fee alone: a hundredth of a token per charge at the default rate,
// and nothing at all at a price of one wei.
var minPlanPriceWei = big.NewInt(1_000_000_000_000_000_000)

// MinPlanPriceWei returns the smallest price, in wei, a plan may charge per
// cycle. The caller gets its own copy.
func MinPlanPriceWei() *big.Int {
	return new(big.Int).Set(minPlanPriceWei)
}

// AddSeconds returns t+d, or the largest uint64 when the sum does not fit. A
// time computed from a stored duration must never wrap around to the past: an
// entry scheduled in the past is due again on the very next block.
func AddSeconds(t, d uint64) uint64 {
	if d > math.MaxUint64-t {
		return math.MaxUint64
	}
	return t + d
}

// Config captures the subscriptions engine's deployment-configured
// parameters. Mirrors native/lending's Config/RiskParameters split: wired
// once at node construction (cmd/nhb/main.go, cmd/consensusd/main.go), not
// itself stored on-chain or governance-adjustable in this version -- the
// same deliberate, documented scope decision native/lending's own
// DeveloperFeeBps makes (contrast with buyback's FeeShareBps, which is
// governance-adjustable specifically because it required that governance
// kind be built anyway).
type Config struct {
	// ManagementFeeBps is NHBCoin's own platform fee for running the
	// subscriptions engine, charged in addition to and independently of
	// the ordinary transfer fee (native/fees) -- a subscription charge
	// never goes through applyTransactionFee at all, since it is not a
	// TxTypeTransfer/TxTypeTransferZNHB (see
	// core/subscriptions_settlement.go's doc comment).
	ManagementFeeBps uint32
	// ManagementFeeCapBps is a hard ceiling ManagementFeeBps may never
	// exceed, checked by Validate.
	ManagementFeeCapBps uint32
	// Treasury receives every charge's management-fee share.
	Treasury [20]byte
	// MaxRetries is how many consecutive failed charge attempts a
	// Subscription tolerates (spaced RetryIntervalSeconds apart) before it
	// is force-transitioned to SubscriptionStatusSuspended and permanently
	// dropped from the due-index.
	MaxRetries uint32
	// RetryIntervalSeconds spaces out consecutive retry attempts after a
	// failed charge -- distinct from the Plan's own IntervalSeconds, which
	// only applies between successful charges.
	RetryIntervalSeconds uint64
}

// DefaultConfig returns the engine's baseline configuration. Treasury is
// intentionally left zero-valued -- callers must set a real treasury
// address before subscriptions can settle (settleSubscriptionCharges
// refuses to run without one, mirroring buyback's hasBuybackConfig gate).
func DefaultConfig() Config {
	return Config{
		ManagementFeeBps:     DefaultManagementFeeBps,
		ManagementFeeCapBps:  DefaultManagementFeeCapBps,
		MaxRetries:           DefaultMaxRetries,
		RetryIntervalSeconds: DefaultRetryIntervalSeconds,
	}
}

// Validate reports whether the configuration is internally consistent.
func (c Config) Validate() error {
	if c.ManagementFeeCapBps > ManagementFeeBpsDenominator {
		return fmt.Errorf("subscriptions: managementFeeCapBps %d exceeds %d", c.ManagementFeeCapBps, ManagementFeeBpsDenominator)
	}
	if c.ManagementFeeBps > c.ManagementFeeCapBps {
		return fmt.Errorf("subscriptions: managementFeeBps %d exceeds cap %d", c.ManagementFeeBps, c.ManagementFeeCapBps)
	}
	if c.ManagementFeeBps > 0 && c.Treasury == ([20]byte{}) {
		return fmt.Errorf("subscriptions: treasury address required when managementFeeBps > 0")
	}
	if c.MaxRetries == 0 {
		return fmt.Errorf("subscriptions: maxRetries must be positive")
	}
	if c.RetryIntervalSeconds == 0 {
		return fmt.Errorf("subscriptions: retryIntervalSeconds must be positive")
	}
	return nil
}
