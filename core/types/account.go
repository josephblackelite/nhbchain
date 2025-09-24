package types

import "math/big"

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
	Nonce              uint64        `json:"nonce"`
	BalanceNHB         *big.Int      `json:"balanceNHB"`
	BalanceZNHB        *big.Int      `json:"balanceZNHB"`
	Stake              *big.Int      `json:"stake"`
	LockedZNHB         *big.Int      `json:"lockedZNHB"`
	DelegatedValidator []byte        `json:"delegatedValidator,omitempty"`
	PendingUnbonds     []StakeUnbond `json:"pendingUnbonds,omitempty"`
	NextUnbondingID    uint64        `json:"nextUnbondingId,omitempty"`
	Username           string        `json:"username"` // NEW: The registered username for this account
	EngagementScore    uint64        `json:"engagementScore"`
	CodeHash           []byte        `json:"codeHash"`
	StorageRoot        []byte        `json:"storageRoot"`
}
