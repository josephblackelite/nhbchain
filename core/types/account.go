package types

import "math/big"

// Account now includes a field for a unique, human-readable username.
type Account struct {
	Nonce                   uint64   `json:"nonce"`
	BalanceNHB              *big.Int `json:"balanceNHB"`
	BalanceZNHB             *big.Int `json:"balanceZNHB"`
	Stake                   *big.Int `json:"stake"`
	Username                string   `json:"username"` // NEW: The registered username for this account
	EngagementScore         uint64   `json:"engagementScore"`
	EngagementDay           string   `json:"engagementDay"`
	EngagementMinutes       uint64   `json:"engagementMinutes"`
	EngagementTxCount       uint64   `json:"engagementTxCount"`
	EngagementEscrowEvents  uint64   `json:"engagementEscrowEvents"`
	EngagementGovEvents     uint64   `json:"engagementGovEvents"`
	EngagementLastHeartbeat uint64   `json:"engagementLastHeartbeat"`
	CodeHash                []byte   `json:"codeHash"`
	StorageRoot             []byte   `json:"storageRoot"`
}
