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
	LockedZNHB              *big.Int             `json:"lockedZNHB"`
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
