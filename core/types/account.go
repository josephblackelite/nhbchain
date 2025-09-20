package types

import "math/big"

// Account now includes a field for a unique, human-readable username.
type Account struct {
	Nonce           uint64   `json:"nonce"`
	BalanceNHB      *big.Int `json:"balanceNHB"`
	BalanceZNHB     *big.Int `json:"balanceZNHB"`
	Stake           *big.Int `json:"stake"`
	Username        string   `json:"username"` // NEW: The registered username for this account
	EngagementScore uint64   `json:"engagementScore"`
	CodeHash        []byte   `json:"codeHash"`
	StorageRoot     []byte   `json:"storageRoot"`
}
