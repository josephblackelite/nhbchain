package potso

import (
	"math/big"
	"time"
)

// StakeUnbondSeconds defines the cooldown applied to stake unbond requests.
const StakeUnbondSeconds uint64 = 7 * 24 * 60 * 60 // 7 days

// StakeLock captures a bonded stake position and its unbonding lifecycle timestamps.
type StakeLock struct {
	Owner      [20]byte
	Amount     *big.Int
	CreatedAt  uint64
	UnbondAt   uint64
	WithdrawAt uint64
}

// WithdrawalRef tracks a lock scheduled for withdrawal within the unbonding queue buckets.
type WithdrawalRef struct {
	Owner  [20]byte
	Nonce  uint64
	Amount *big.Int
}

// StakeLockInfo exposes lock metadata for account queries.
type StakeLockInfo struct {
	Nonce      uint64   `json:"nonce"`
	Amount     *big.Int `json:"amount"`
	CreatedAt  uint64   `json:"createdAt"`
	UnbondAt   uint64   `json:"unbondAt"`
	WithdrawAt uint64   `json:"withdrawAt"`
}

// StakeAccountInfo summarises the staking position for an owner, grouping bonded and
// unbonding totals for convenience.
type StakeAccountInfo struct {
	Owner          [20]byte
	Bonded         *big.Int
	PendingUnbond  *big.Int
	Withdrawable   *big.Int
	Locks          []StakeLockInfo
	ComputedAtUnix int64
}

// WithdrawResult captures an individual lock payout returned to the caller.
type WithdrawResult struct {
	Nonce  uint64   `json:"nonce"`
	Amount *big.Int `json:"amount"`
}

// WithdrawDay derives the queue bucket identifier for the provided unix timestamp.
func WithdrawDay(ts uint64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).UTC().Format(DayFormat)
}
