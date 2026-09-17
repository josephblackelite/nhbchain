package loyalty

import "math/big"

// AccrualKindBase/AccrualKindProgram distinguish which of the two
// independent reward mechanisms produced an AccrualRecord. A single
// qualifying spend can trigger both the chain-wide base spend reward
// (ApplyBaseReward, engine_base.go) and a business's own loyalty program
// reward (ApplyProgramReward, engine_program.go) -- these are recorded as
// two separate AccrualRecord entries rather than merged into one, since
// correlating them back to "the same spend" after the fact would require
// fragile cross-event matching (same tx hash, different reward amounts and
// caps) for no real benefit to a reader of the accrual history.
const (
	AccrualKindBase    = "base"
	AccrualKindProgram = "program"
)

// AccrualRecord is a single individual accrual line item -- the durable,
// queryable counterpart to the ephemeral loyalty.base.accrued /
// loyalty.program.accrued types.Event log entries emitted alongside it.
// Events are never persisted anywhere queryable after the block that
// produced them; AccrualRecord is appended to a day-bucketed index (see
// core/state's AppendLoyaltyProgramAccrualRecord / AppendLoyaltyBaseAccrualRecord)
// so RPC callers (loyalty_listAccruals) can list what actually accrued for a
// program on a given UTC day, powering the "Rewards Accrual History"
// business dashboard feature.
//
// Field order is fixed and must never change for already-persisted records:
// this struct is RLP-encoded (rlp.EncodeToBytes), which encodes struct
// fields positionally by declaration order, not by name. New fields must
// always be appended at the end, never inserted between existing ones, and
// tagged `rlp:"optional"` the way loyalty.Program does it -- otherwise every
// already-persisted record on a live chain would fail to decode.
//
// TxHash is included specifically because the storage layer
// (core/state.Manager.KVAppend) dedups an appended list by the exact
// serialized bytes of the appended value (see its bytes.Equal check): two
// genuinely distinct accrual events that happened to serialize identically
// in every other field (same program, same address, same amount, same
// second) would otherwise collide and the second would be silently dropped.
// A transaction hash is unique per transaction, which guarantees two real,
// distinct accruals never serialize identically.
type AccrualRecord struct {
	ProgramID ProgramID
	Address   [20]byte
	Amount    *big.Int
	Kind      string
	TxHash    [32]byte
	Timestamp uint64
}
