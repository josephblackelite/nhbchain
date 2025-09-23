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
