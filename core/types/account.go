package types

import "math/big"

// LendingIndexSnapshot stores the indexes captured when the user's position was last updated.
type LendingIndexSnapshot struct {
	SupplyIndex *big.Int `json:"supplyIndex"`
	BorrowIndex *big.Int `json:"borrowIndex"`
}

// LendingBreakerFlags tracks per-account breaker overrides for lending actions.
type LendingBreakerFlags struct {
	CollateralDisabled bool `json:"collateralDisabled"`
	BorrowDisabled     bool `json:"borrowDisabled"`
}

// Uint128x128 represents a fixed-precision unsigned 128.128 value encoded in
// big-endian byte order. The zero value corresponds to an unset accumulator and
// can be distinguished from populated snapshots.
type Uint128x128 [32]byte

// Bytes returns a copy of the encoded representation. The zero value returns a
// nil slice to preserve legacy semantics where missing snapshots are treated as
// unset accumulators.
func (u Uint128x128) Bytes() []byte {
	if u.IsZero() {
		return nil
	}
	return append([]byte(nil), u[:]...)
}

// IsZero reports whether all bytes in the encoded representation are zero.
func (u Uint128x128) IsZero() bool {
	for _, b := range u {
		if b != 0 {
			return false
		}
	}
	return true
}

// Uint128x128FromBytes constructs a Uint128x128 from the provided big-endian
// byte slice. Inputs shorter than 32 bytes are left-padded. Nil inputs yield the
// zero value.
func Uint128x128FromBytes(data []byte) Uint128x128 {
	var out Uint128x128
	if len(data) == 0 {
		return out
	}
	if len(data) >= len(out) {
		copy(out[:], data[len(data)-len(out):])
		return out
	}
	copy(out[len(out)-len(data):], data)
	return out
}

type uint128 = Uint128x128

// StakingRewards captures the staking reward accrual metadata tracked for an
// account. The structure mirrors the deterministic staking snapshot persisted in
// state to make JSON APIs and downstream consumers aware of pending rewards.
type StakingRewards struct {
	AccruedZNHB        *big.Int `json:"accruedZNHB"`
	LastIndexUQ128x128 uint128  `json:"lastIndexUQ128x128"`
	LastPayoutUnix     int64    `json:"lastPayoutUnix"`
}

// StakeUnbond represents a pending release of delegated stake back to a delegator.
// It is persisted in account metadata and consumed once the release time elapses.
type StakeUnbond struct {
	ID          uint64   `json:"id"`
	Validator   []byte   `json:"validator"`
	Amount      *big.Int `json:"amount"`
	ReleaseTime uint64   `json:"releaseTime"`
}

// Account now includes a field for a unique, human-readable username.
type Account struct {
	Nonce                   uint64               `json:"nonce"`
	BalanceNHB              *big.Int             `json:"balanceNHB"`
	BalanceZNHB             *big.Int             `json:"balanceZNHB"`
	Stake                   *big.Int             `json:"stake"`
	StakeShares             *big.Int             `json:"stakeShares"`
	StakeLastIndex          *big.Int             `json:"stakeLastIndex"`
	StakeLastPayoutTs       uint64               `json:"stakeLastPayoutTs"`
	LockedZNHB              *big.Int             `json:"lockedZNHB"`
	StakingRewards          StakingRewards       `json:"stakingRewards"`
	DelegatedValidator      []byte               `json:"delegatedValidator,omitempty"`
	PendingUnbonds          []StakeUnbond        `json:"pendingUnbonds,omitempty"`
	NextUnbondingID         uint64               `json:"nextUnbondingId,omitempty"`
	Username                string               `json:"username"` // NEW: The registered username for this account
	EngagementScore         uint64               `json:"engagementScore"`
	EngagementDay           string               `json:"engagementDay"`
	EngagementMinutes       uint64               `json:"engagementMinutes"`
	EngagementTxCount       uint64               `json:"engagementTxCount"`
	EngagementEscrowEvents  uint64               `json:"engagementEscrowEvents"`
	EngagementGovEvents     uint64               `json:"engagementGovEvents"`
	EngagementLastHeartbeat uint64               `json:"engagementLastHeartbeat"`
	CollateralBalance       *big.Int             `json:"collateralBalance"`
	DebtPrincipal           *big.Int             `json:"debtPrincipal"`
	SupplyShares            *big.Int             `json:"supplyShares"`
	LendingSnapshot         LendingIndexSnapshot `json:"lendingSnapshot"`
	LendingBreaker          LendingBreakerFlags  `json:"lendingBreaker"`
	CodeHash                []byte               `json:"codeHash"`
	StorageRoot             []byte               `json:"storageRoot"`
}
