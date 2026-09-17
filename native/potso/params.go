package potso

import "fmt"

// EngineParams controls runtime limits applied to POTSO heartbeat processing.
type EngineParams struct {
	// MaxHeartbeatsPerEpoch bounds how many heartbeats a single address may
	// submit within the same epoch. Exceeding the limit results in
	// heartbeats being rejected to prevent wash engagement farming.
	MaxHeartbeatsPerEpoch uint64
}

// DefaultEngineParams returns conservative defaults suitable for production
// networks. The limit assumes a one minute cadence and a 24 hour epoch.
func DefaultEngineParams() EngineParams {
	return EngineParams{MaxHeartbeatsPerEpoch: 1440}
}

// Validate ensures the supplied parameters fall within safe operating ranges.
func (p EngineParams) Validate() error {
	if p.MaxHeartbeatsPerEpoch == 0 {
		return fmt.Errorf("max heartbeats per epoch must be positive")
	}
	return nil
}
