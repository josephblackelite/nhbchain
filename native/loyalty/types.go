package loyalty

import "math/big"

// ProgramID uniquely identifies a loyalty program.
// In practice this can be computed as keccak256(owner || salt) or supplied
// explicitly by governance tooling.
type ProgramID [32]byte

// RewardMode selects how a Program's per-purchase reward is computed.
// The zero value is RewardModeBps, so every program persisted before this
// field existed keeps its exact prior behavior (percentage-of-spend)
// without any migration step.
type RewardMode uint8

const (
	// RewardModeBps computes the reward as spend * AccrualBps / 10_000.
	RewardModeBps RewardMode = iota
	// RewardModeFixed pays a flat FixedRewardWei per qualifying purchase,
	// regardless of spend size.
	RewardModeFixed
)

// Program captures the on-chain configuration for a merchant loyalty program.
//
// New fields must always be appended at the end, never inserted between
// existing ones: Program is persisted via the generic KVPut/KVGet helpers,
// which RLP-encode structs positionally (by declaration order, not by
// field name). Inserting a field in the middle would silently misdecode
// every already-persisted Program on any live chain.
type Program struct {
	ID                 ProgramID
	Owner              [20]byte
	Pool               [20]byte
	TokenSymbol        string
	AccrualBps         uint32
	MinSpendWei        *big.Int
	CapPerTx           *big.Int
	DailyCapUser       *big.Int
	DailyCapProgram    *big.Int
	EpochCapProgram    *big.Int
	EpochLengthSeconds uint64
	IssuanceCapUser    *big.Int
	StartTime          uint64
	EndTime            uint64
	Active             bool
	// RewardMode and FixedRewardWei were added after the fields above --
	// appended at the end for RLP backward compatibility (see the note on
	// this type), and tagged `rlp:"optional"` since go-ethereum's RLP
	// decoder otherwise requires an exact list-length match: without this
	// tag, decoding any Program persisted before these fields existed
	// would fail outright rather than defaulting them. RewardMode's zero
	// value is RewardModeBps, so every pre-existing program keeps its
	// exact prior behavior (percentage-of-spend via AccrualBps) with no
	// migration step.
	RewardMode     RewardMode `rlp:"optional"`
	FixedRewardWei *big.Int   `rlp:"optional"`
}

// BusinessID uniquely identifies a registered business entity.
type BusinessID [32]byte

// Business captures the on-chain configuration for a registered business.
type Business struct {
	ID                  BusinessID
	Owner               [20]byte
	Name                string
	Paymaster           [20]byte
	Merchants           [][20]byte
	PaymasterReserveMin *big.Int
}
