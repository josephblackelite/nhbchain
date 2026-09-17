package subscriptions

import "fmt"

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
