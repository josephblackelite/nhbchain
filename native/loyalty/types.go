package loyalty

import "math/big"

// ProgramID uniquely identifies a loyalty program.
// In practice this can be computed as keccak256(owner || salt) or supplied
// explicitly by governance tooling.
type ProgramID [32]byte

// Program captures the on-chain configuration for a merchant loyalty program.
type Program struct {
	ID           ProgramID
	Owner        [20]byte
	Pool         [20]byte
	TokenSymbol  string
	AccrualBps   uint32
	MinSpendWei  *big.Int
	CapPerTx     *big.Int
	DailyCapUser *big.Int
	StartTime    uint64
	EndTime      uint64
	Active       bool
}

// BusinessID uniquely identifies a registered business entity.
type BusinessID [32]byte

// Business captures the on-chain configuration for a registered business.
type Business struct {
	ID        BusinessID
	Owner     [20]byte
	Name      string
	Paymaster [20]byte
	Merchants [][20]byte
}
