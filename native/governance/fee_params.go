package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// The transfer fee schedule under governance (W11).
//
// Every transfer on the chain pays one fee: the first FreeAllowanceWei a wallet sends
// in an asset are free, afterwards a flat rate in basis points of the amount, NHB and
// ZNHB each with their own rate. Those three numbers used to be read from each
// validator's own configuration file. ProposalKindFeeParams moves them into chain
// state: a passed proposal, after its timelock, stores the new numbers as a PENDING
// schedule that takes effect from a block height at least MinFeeScheduleDelayBlocks
// after the block that executed it, the same height on every validator because the
// height is part of the stored record. Nothing here reads a clock or a node-local
// file.
//
// The kind is unavailable until the chain rule TransferFeeGovernanceActivationHeight is
// reached (Engine.SetFeeSchedule); below it a proposal of this kind is refused exactly
// as an unknown kind is, so blocks already committed are executed as before.

const (
	// ProposalKindFeeParams sets the transfer fee schedule: the NHB and ZNHB rates and
	// the free allowance per wallet and asset.
	ProposalKindFeeParams = "policy.feeParams"

	// ParamKeyTransferFeeSchedule is the parameter-store key that holds the schedule
	// record (FeeScheduleRecord, JSON). It has no validation rule in the generic
	// parameter engine, so a param.update proposal cannot write it: only the
	// ProposalKindFeeParams execution path does.
	ParamKeyTransferFeeSchedule = "fees.transferSchedule"

	// MaxTransferFeeBps is the hard ceiling on either rate: 100 basis points, one
	// percent of the amount. No proposal can set a rate above it, and a stored record
	// that carries one is ignored.
	MaxTransferFeeBps uint32 = 100

	// MinFeeScheduleDelayBlocks is the shortest delay between the block that executes a
	// fee proposal and the first block that charges the new schedule. A configured
	// delay below it is raised to it.
	MinFeeScheduleDelayBlocks uint64 = 100

	maxFeeScheduleMemoLength = 256
)

// MaxFreeAllowanceWei is the largest free allowance a proposal may set: one billion
// tokens, the order of the whole supply. A larger number is no allowance, it is no fee.
func MaxFreeAllowanceWei() *big.Int {
	v, _ := new(big.Int).SetString("1000000000000000000000000000", 10)
	return v
}

// FeeParamsPayload is the JSON a ProposalKindFeeParams proposal carries. Every field
// but AllowZeroAllowance and Memo is required, the key names are matched exactly,
// an unknown or repeated key is refused, and a number must be a bare JSON integer.
type FeeParamsPayload struct {
	NHBFeeBps          uint32 `json:"nhbFeeBps"`
	ZNHBFeeBps         uint32 `json:"znhbFeeBps"`
	FreeAllowanceWei   string `json:"freeAllowanceWei"`
	AllowZeroAllowance bool   `json:"allowZeroAllowance,omitempty"`
	Memo               string `json:"memo,omitempty"`
}

// FeeScheduleValues are the three numbers of a schedule.
type FeeScheduleValues struct {
	NHBFeeBps        uint32 `json:"nhbFeeBps"`
	ZNHBFeeBps       uint32 `json:"znhbFeeBps"`
	FreeAllowanceWei string `json:"freeAllowanceWei"`
}

// ScheduledFeeValues is a schedule with the block height it takes effect from and the
// proposal that set it.
type ScheduledFeeValues struct {
	FeeScheduleValues
	EffectiveHeight uint64 `json:"effectiveHeight"`
	ProposalID      uint64 `json:"proposalId"`
}

// FeeScheduleRecord is what the parameter store holds under ParamKeyTransferFeeSchedule:
// the schedule in force (Active, absent until the first one took effect) and at most one
// schedule that is waiting for its height (Pending).
type FeeScheduleRecord struct {
	Active  *ScheduledFeeValues `json:"active,omitempty"`
	Pending *ScheduledFeeValues `json:"pending,omitempty"`
}

// InForce returns the schedule that governs a block at the given height, nil when
// governance has set none (the configured values then apply). A pending schedule governs
// from its own height on.
func (r *FeeScheduleRecord) InForce(height uint64) *ScheduledFeeValues {
	if r == nil {
		return nil
	}
	if r.Pending != nil && height >= r.Pending.EffectiveHeight {
		return r.Pending
	}
	if r.Active != nil && height >= r.Active.EffectiveHeight {
		return r.Active
	}
	return nil
}

// Settle moves a pending schedule whose height has been reached into Active. The
// execution path calls it before it stores a new pending schedule, so a schedule that
// has taken effect is never lost to a later proposal.
func (r *FeeScheduleRecord) Settle(height uint64) {
	if r == nil || r.Pending == nil || height < r.Pending.EffectiveHeight {
		return
	}
	r.Active = r.Pending
	r.Pending = nil
}

// Validate reports why the values are not an acceptable schedule: a rate above the
// ceiling, or an allowance that is not a plain non-negative integer within the cap. A
// zero allowance is valid here; whether it is wanted is the proposal's own flag.
func (v FeeScheduleValues) Validate() error {
	if v.NHBFeeBps > MaxTransferFeeBps {
		return fmt.Errorf("nhbFeeBps %d is above the ceiling of %d", v.NHBFeeBps, MaxTransferFeeBps)
	}
	if v.ZNHBFeeBps > MaxTransferFeeBps {
		return fmt.Errorf("znhbFeeBps %d is above the ceiling of %d", v.ZNHBFeeBps, MaxTransferFeeBps)
	}
	allowance, err := ParseFreeAllowance(v.FreeAllowanceWei)
	if err != nil {
		return err
	}
	if allowance.Cmp(MaxFreeAllowanceWei()) > 0 {
		return fmt.Errorf("freeAllowanceWei %s is above the cap of %s", v.FreeAllowanceWei, MaxFreeAllowanceWei())
	}
	return nil
}

// Allowance returns the free allowance as a number (zero when it cannot be read; call
// Validate first).
func (v FeeScheduleValues) Allowance() *big.Int {
	allowance, err := ParseFreeAllowance(v.FreeAllowanceWei)
	if err != nil {
		return new(big.Int)
	}
	return allowance
}

// ParseFreeAllowance reads a decimal allowance: ASCII digits only, no sign, no
// exponent, no separator, no leading zero unless the whole number is "0".
func ParseFreeAllowance(raw string) (*big.Int, error) {
	if raw == "" {
		return nil, errors.New("freeAllowanceWei is required")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("freeAllowanceWei %q must be a plain decimal integer of digits only", raw)
		}
	}
	if len(raw) > 1 && raw[0] == '0' {
		return nil, fmt.Errorf("freeAllowanceWei %q must not have a leading zero", raw)
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("freeAllowanceWei %q is not a decimal integer", raw)
	}
	return value, nil
}

// ParseFeeScheduleRecord reads the stored record and validates every value in it. An
// empty value is no record. Anything that is not a well-formed record of acceptable
// schedules is an error: the readers then act as if governance had set nothing.
func ParseFeeScheduleRecord(raw []byte) (*FeeScheduleRecord, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record FeeScheduleRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("governance: fee schedule record: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("governance: fee schedule record: trailing data")
	}
	if record.Active != nil {
		if err := record.Active.Validate(); err != nil {
			return nil, fmt.Errorf("governance: fee schedule record: active: %w", err)
		}
	}
	if record.Pending != nil {
		if err := record.Pending.Validate(); err != nil {
			return nil, fmt.Errorf("governance: fee schedule record: pending: %w", err)
		}
	}
	return &record, nil
}

// parseFeeParamsPayload validates a ProposalKindFeeParams payload. It is strict where
// the older payload kinds are lenient, because it sets what every transfer costs: exact
// key names, no repeated key, no unknown key, every number a bare JSON integer, every
// required field present (an omitted rate must not read as a rate of zero).
func parseFeeParamsPayload(payloadJSON string) (*FeeParamsPayload, error) {
	decoder := json.NewDecoder(strings.NewReader(payloadJSON))
	open, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("governance: invalid payload: a JSON object is required")
	}
	raws := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("governance: invalid payload: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("governance: invalid payload: object key expected")
		}
		switch key {
		case "nhbFeeBps", "znhbFeeBps", "freeAllowanceWei", "allowZeroAllowance", "memo":
		default:
			return nil, fmt.Errorf("governance: feeParams: unknown field %q", key)
		}
		if _, dup := raws[key]; dup {
			return nil, fmt.Errorf("governance: feeParams: field %q given more than once", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("governance: invalid payload: %w", err)
		}
		raws[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("governance: invalid payload: trailing data")
	}

	payload := &FeeParamsPayload{}
	for _, name := range []string{"nhbFeeBps", "znhbFeeBps", "freeAllowanceWei"} {
		if _, ok := raws[name]; !ok {
			return nil, fmt.Errorf("governance: feeParams: %s is required", name)
		}
	}
	// A JSON null decodes into a zero value without an error; a null is never a value here.
	for name, raw := range raws {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("governance: feeParams: %s must not be null", name)
		}
	}
	if err := json.Unmarshal(raws["nhbFeeBps"], &payload.NHBFeeBps); err != nil {
		return nil, fmt.Errorf("governance: feeParams: nhbFeeBps must be a whole number: %w", err)
	}
	if err := json.Unmarshal(raws["znhbFeeBps"], &payload.ZNHBFeeBps); err != nil {
		return nil, fmt.Errorf("governance: feeParams: znhbFeeBps must be a whole number: %w", err)
	}
	if err := json.Unmarshal(raws["freeAllowanceWei"], &payload.FreeAllowanceWei); err != nil {
		return nil, fmt.Errorf("governance: feeParams: freeAllowanceWei must be a string of digits: %w", err)
	}
	if raw, ok := raws["allowZeroAllowance"]; ok {
		if err := json.Unmarshal(raw, &payload.AllowZeroAllowance); err != nil {
			return nil, fmt.Errorf("governance: feeParams: allowZeroAllowance must be true or false: %w", err)
		}
	}
	if raw, ok := raws["memo"]; ok {
		if err := json.Unmarshal(raw, &payload.Memo); err != nil {
			return nil, fmt.Errorf("governance: feeParams: memo must be a string: %w", err)
		}
	}
	if len(payload.Memo) > maxFeeScheduleMemoLength {
		return nil, fmt.Errorf("governance: feeParams: memo is longer than %d bytes", maxFeeScheduleMemoLength)
	}
	values := FeeScheduleValues{NHBFeeBps: payload.NHBFeeBps, ZNHBFeeBps: payload.ZNHBFeeBps, FreeAllowanceWei: payload.FreeAllowanceWei}
	if err := values.Validate(); err != nil {
		return nil, fmt.Errorf("governance: feeParams: %w", err)
	}
	zero := values.Allowance().Sign() == 0
	if zero && !payload.AllowZeroAllowance {
		return nil, errors.New("governance: feeParams: a free allowance of zero means every transfer pays the fee; set allowZeroAllowance to true to say so")
	}
	if !zero && payload.AllowZeroAllowance {
		return nil, errors.New("governance: feeParams: allowZeroAllowance is true but the allowance is not zero")
	}
	return payload, nil
}

// FeeScheduleHost is what the proposal engine needs from the block being executed to
// handle ProposalKindFeeParams: whether the kind is available yet, the block height,
// and the configured delay. The zero value is "not available".
type FeeScheduleHost struct {
	Enabled     bool
	Height      uint64
	DelayBlocks uint64
}

// SetFeeSchedule hands the engine the block context for ProposalKindFeeParams (see
// FeeScheduleHost). The state processor calls it for every engine it builds.
func (e *Engine) SetFeeSchedule(host FeeScheduleHost) {
	if e == nil {
		return
	}
	e.feeSchedule = host
}

// paramStoreReader is the read side of the parameter store. The production state
// implements it; a state that does not holds no schedule.
type paramStoreReader interface {
	ParamStoreGet(name string) ([]byte, bool, error)
}

// applyFeeParams stores a passed ProposalKindFeeParams proposal as the pending schedule.
// A schedule that has already taken effect is first moved to Active, so it is never lost;
// a schedule still waiting is replaced by this one (the later proposal supersedes it).
func (e *Engine) applyFeeParams(proposalID uint64, payload *FeeParamsPayload) (map[string]interface{}, error) {
	if payload == nil {
		return nil, errors.New("governance: nil fee params payload")
	}
	values := FeeScheduleValues{NHBFeeBps: payload.NHBFeeBps, ZNHBFeeBps: payload.ZNHBFeeBps, FreeAllowanceWei: payload.FreeAllowanceWei}
	if err := values.Validate(); err != nil {
		return nil, fmt.Errorf("governance: feeParams: %w", err)
	}
	height := e.feeSchedule.Height
	delay := e.feeSchedule.DelayBlocks
	if delay < MinFeeScheduleDelayBlocks {
		delay = MinFeeScheduleDelayBlocks
	}
	if height > ^uint64(0)-delay {
		return nil, errors.New("governance: feeParams: effective height overflows")
	}
	record := &FeeScheduleRecord{}
	if reader, ok := e.state.(paramStoreReader); ok {
		raw, found, err := reader.ParamStoreGet(ParamKeyTransferFeeSchedule)
		if err != nil {
			return nil, err
		}
		if found {
			if previous, perr := ParseFeeScheduleRecord(raw); perr == nil && previous != nil {
				record = previous
			}
		}
	}
	record.Settle(height)
	var superseded uint64
	if record.Pending != nil {
		superseded = record.Pending.ProposalID
	}
	effective := height + delay
	record.Pending = &ScheduledFeeValues{FeeScheduleValues: values, EffectiveHeight: effective, ProposalID: proposalID}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if err := e.state.ParamStoreSet(ParamKeyTransferFeeSchedule, encoded); err != nil {
		return nil, err
	}
	detail := map[string]interface{}{
		"nhbFeeBps":        payload.NHBFeeBps,
		"znhbFeeBps":       payload.ZNHBFeeBps,
		"freeAllowanceWei": payload.FreeAllowanceWei,
		"effectiveHeight":  effective,
	}
	if superseded != 0 {
		detail["supersededProposal"] = superseded
	}
	if strings.TrimSpace(payload.Memo) != "" {
		detail["memo"] = strings.TrimSpace(payload.Memo)
	}
	return detail, nil
}
