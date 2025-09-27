package governance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
	"nhbchain/native/potso"
	"nhbchain/p2p/seeds"
)

const (
	// EventTypeProposalProposed is emitted when a new proposal is accepted.
	EventTypeProposalProposed = "gov.proposed"
	// EventTypeVoteCast is emitted when a voter records or updates a ballot.
	EventTypeVoteCast = "gov.vote"
	// EventTypeProposalFinalized is emitted when the proposal outcome is determined.
	EventTypeProposalFinalized = "gov.finalized"
	// EventTypeProposalQueued marks proposals that have been queued for execution after passing.
	EventTypeProposalQueued = "gov.queued"
	// EventTypeProposalExecuted marks proposals whose payload has been applied to state.
	EventTypeProposalExecuted = "gov.executed"
	// EventTypePolicyInvalid marks proposals rejected during policy preflight.
	EventTypePolicyInvalid = "gov.policy.invalid"
)

const (
	maxBasisPoints                   = 10_000
	maxEmissionWei                   = "9223372036854775807" // conservative cap
	maxGovernanceUint                = uint64(1<<53 - 1)
	minSlashingWindowSeconds  uint64 = 60
	maxSlashingWindowSeconds  uint64 = 30 * 24 * 60 * 60
	maxEvidenceTTLSeconds     uint64 = 90 * 24 * 60 * 60
	minGovernanceVotingPeriod uint64 = 3600
)

const (
	paramKeySlashingEnabled       = "slashing.policy.enabled"
	paramKeySlashingMaxPenaltyBps = "slashing.policy.maxPenaltyBps"
	paramKeySlashingWindow        = "slashing.policy.windowSeconds"
	paramKeySlashingMaxSlashWei   = "slashing.policy.maxSlashWei"
	paramKeySlashingEvidenceTTL   = "slashing.policy.evidenceTtlSeconds"
)

var (
	errStateNotConfigured = errors.New("governance: state not configured")
)

type proposalState interface {
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
	GovernanceEscrowBalance(addr []byte) (*big.Int, error)
	GovernanceEscrowLock(addr []byte, amount *big.Int) (*big.Int, error)
	GovernanceEscrowUnlock(addr []byte, amount *big.Int) (*big.Int, error)
	GovernanceNextProposalID() (uint64, error)
	GovernancePutProposal(p *Proposal) error
	GovernanceGetProposal(id uint64) (*Proposal, bool, error)
	GovernancePutVote(v *Vote) error
	GovernanceListVotes(id uint64) ([]*Vote, error)
	GovernanceAppendAudit(entry *AuditRecord) (*AuditRecord, error)
	ParamStoreSet(name string, value []byte) error
	SetRole(role string, addr []byte) error
	RemoveRole(role string, addr []byte) error
	PotsoRewardsLastProcessedEpoch() (uint64, bool, error)
	SnapshotPotsoWeights(epoch uint64) (*potso.StoredWeightSnapshot, bool, error)
}

// ProposalPolicy captures the runtime knobs that control proposal admission.
// The engine expects the values to be pre-normalised (e.g. MinDepositWei in Wei).
//
// AllowedParams must contain the canonical parameter keys permitted for
// parameter update proposals.
type ProposalPolicy struct {
	MinDepositWei                  *big.Int
	VotingPeriodSeconds            uint64
	TimelockSeconds                uint64
	AllowedParams                  []string
	QuorumBps                      uint64
	PassThresholdBps               uint64
	AllowedRoles                   []string
	TreasuryAllowList              [][20]byte
	BlockTimestampToleranceSeconds uint64
}

// Engine orchestrates proposal admission and bookkeeping for governance
// operations.
type Engine struct {
	state               proposalState
	emitter             events.Emitter
	nowFn               func() time.Time
	minDeposit          *big.Int
	votingPeriodSeconds uint64
	timelockSeconds     uint64
	allowedParams       map[string]struct{}
	paramValidators     map[string]paramValidator
	allowedRoles        map[string]struct{}
	treasuryAllow       map[[20]byte]struct{}
	quorumBps           uint64
	passThresholdBps    uint64
	policyValidator     PolicyValidator
}

// NewEngine constructs a governance engine with default no-op dependencies.
func NewEngine() *Engine {
	return &Engine{
		emitter:         events.NoopEmitter{},
		nowFn:           func() time.Time { return time.Now().UTC() },
		minDeposit:      big.NewInt(0),
		allowedParams:   map[string]struct{}{},
		paramValidators: map[string]paramValidator{},
		allowedRoles:    map[string]struct{}{},
		treasuryAllow:   map[[20]byte]struct{}{},
	}
}

// PolicyBaseline captures the governance fields relevant to policy validation.
type PolicyBaseline struct {
	QuorumBps        uint32
	PassThresholdBps uint32
	VotingPeriodSecs uint64
}

// PolicyDelta describes the subset of policy fields a proposal may mutate.
type PolicyDelta struct {
	QuorumBps        *uint32
	PassThresholdBps *uint32
}

// PolicyValidator evaluates whether applying the supplied delta over the
// baseline configuration preserves runtime invariants.
type PolicyValidator func(PolicyBaseline, PolicyDelta) error

type roleMutation struct {
	role string
	addr [20]byte
}

type parsedRoleAllowlist struct {
	grant  []roleMutation
	revoke []roleMutation
	memo   string
}

type treasuryTransferDecoded struct {
	to     [20]byte
	amount *big.Int
	memo   string
	kind   string
}

type parsedTreasuryDirective struct {
	source    [20]byte
	transfers []treasuryTransferDecoded
	memo      string
	total     *big.Int
}

type parsedSlashingPolicy struct {
	payload  SlashingPolicyPayload
	maxSlash *big.Int
}

type paramValidator func(raw json.RawMessage) error

// SetState wires the engine to the state backend providing persistence helpers.
func (e *Engine) SetState(state proposalState) { e.state = state }

// SetEmitter configures the event emitter used by the engine. Passing nil resets
// the emitter to a no-op implementation.
func (e *Engine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

// SetPolicyValidator registers the callback invoked when validating policy
// proposal deltas. Passing nil disables preflight validation.
func (e *Engine) SetPolicyValidator(validator PolicyValidator) {
	if e == nil {
		return
	}
	e.policyValidator = validator
}

// SetNowFunc overrides the time source used to stamp proposals. Nil restores the
// default UTC clock.
func (e *Engine) SetNowFunc(now func() time.Time) {
	if now == nil {
		e.nowFn = func() time.Time { return time.Now().UTC() }
		return
	}
	e.nowFn = now
}

// SetPolicy updates the runtime policy governing proposal admission.
func (e *Engine) SetPolicy(policy ProposalPolicy) {
	if e == nil {
		return
	}
	if policy.MinDepositWei != nil {
		e.minDeposit = new(big.Int).Set(policy.MinDepositWei)
	} else {
		e.minDeposit = big.NewInt(0)
	}
	e.votingPeriodSeconds = policy.VotingPeriodSeconds
	e.timelockSeconds = policy.TimelockSeconds
	e.allowedParams = make(map[string]struct{}, len(policy.AllowedParams))
	e.paramValidators = make(map[string]paramValidator, len(policy.AllowedParams))
	for _, raw := range policy.AllowedParams {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		e.allowedParams[trimmed] = struct{}{}
		if validator := validatorForParam(trimmed); validator != nil {
			e.paramValidators[trimmed] = validator
		}
	}
	e.allowedRoles = make(map[string]struct{}, len(policy.AllowedRoles))
	for _, role := range policy.AllowedRoles {
		trimmed := strings.TrimSpace(role)
		if trimmed == "" {
			continue
		}
		e.allowedRoles[trimmed] = struct{}{}
	}
	e.treasuryAllow = make(map[[20]byte]struct{}, len(policy.TreasuryAllowList))
	for _, addr := range policy.TreasuryAllowList {
		e.treasuryAllow[addr] = struct{}{}
	}
	e.quorumBps = policy.QuorumBps
	e.passThresholdBps = policy.PassThresholdBps
}

var (
	maxBaseFeeWei    = new(big.Int).SetUint64(1_000_000_000_000_000) // 1e15 wei
	maxEmissionInt   = mustBigInt(maxEmissionWei)
	maxSlashWeiLimit = mustBigInt(maxEmissionWei)
)

func mustBigInt(value string) *big.Int {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return big.NewInt(0)
	}
	n, ok := new(big.Int).SetString(normalized, 10)
	if !ok {
		panic(fmt.Sprintf("invalid big integer literal %q", value))
	}
	return n
}

func validatorForParam(key string) paramValidator {
	switch key {
	case "fees.baseFee":
		return func(raw json.RawMessage) error {
			amount, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("fees.baseFee: %w", err)
			}
			if amount.Cmp(maxBaseFeeWei) > 0 {
				return fmt.Errorf("fees.baseFee: value exceeds %s wei", maxBaseFeeWei.String())
			}
			return nil
		}
	case "potso.weights.AlphaStakeBps":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("potso.weights.AlphaStakeBps: %w", err)
			}
			if value > maxBasisPoints {
				return fmt.Errorf("potso.weights.AlphaStakeBps: must be <= %d", maxBasisPoints)
			}
			return nil
		}
	case "potso.rewards.EmissionPerEpochWei":
		return func(raw json.RawMessage) error {
			amount, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("potso.rewards.EmissionPerEpochWei: %w", err)
			}
			if amount.Cmp(maxEmissionInt) > 0 {
				return fmt.Errorf("potso.rewards.EmissionPerEpochWei: value exceeds %s wei", maxEmissionInt.String())
			}
			return nil
		}
	case "potso.abuse.MaxUserShareBps":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("potso.abuse.MaxUserShareBps: %w", err)
			}
			if value > maxBasisPoints {
				return fmt.Errorf("potso.abuse.MaxUserShareBps: must be <= %d", maxBasisPoints)
			}
			return nil
		}
	case "potso.abuse.MinStakeToEarnWei":
		return func(raw json.RawMessage) error {
			amount, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("potso.abuse.MinStakeToEarnWei: %w", err)
			}
			if amount.Sign() < 0 {
				return fmt.Errorf("potso.abuse.MinStakeToEarnWei: must be >= 0")
			}
			return nil
		}
	case "potso.abuse.QuadraticTxDampenAfter":
		return func(raw json.RawMessage) error {
			_, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("potso.abuse.QuadraticTxDampenAfter: %w", err)
			}
			return nil
		}
	case "potso.abuse.QuadraticTxDampenPower":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("potso.abuse.QuadraticTxDampenPower: %w", err)
			}
			if value == 1 {
				return fmt.Errorf("potso.abuse.QuadraticTxDampenPower: must be 0 to disable or >= 2 for damping")
			}
			return nil
		}
	case "network.seeds":
		return func(raw json.RawMessage) error {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed == "" {
				return fmt.Errorf("network.seeds: payload must not be empty")
			}
			if _, err := seeds.Parse([]byte(trimmed)); err != nil {
				return fmt.Errorf("network.seeds: %w", err)
			}
			return nil
		}
	case ParamKeyMinimumValidatorStake:
		return func(raw json.RawMessage) error {
			amount, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", ParamKeyMinimumValidatorStake, err)
			}
			if amount.Sign() <= 0 {
				return fmt.Errorf("%s: must be positive", ParamKeyMinimumValidatorStake)
			}
			return nil
		}
	case paramKeySlashingMaxPenaltyBps:
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", paramKeySlashingMaxPenaltyBps, err)
			}
			if value > maxBasisPoints {
				return fmt.Errorf("%s: must be <= %d", paramKeySlashingMaxPenaltyBps, maxBasisPoints)
			}
			return nil
		}
	case paramKeySlashingWindow:
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", paramKeySlashingWindow, err)
			}
			if value < minSlashingWindowSeconds || value > maxSlashingWindowSeconds {
				return fmt.Errorf("%s: must be between %d and %d seconds", paramKeySlashingWindow, minSlashingWindowSeconds, maxSlashingWindowSeconds)
			}
			return nil
		}
	case paramKeySlashingEvidenceTTL:
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", paramKeySlashingEvidenceTTL, err)
			}
			if value < minSlashingWindowSeconds || value > maxEvidenceTTLSeconds {
				return fmt.Errorf("%s: must be between %d and %d seconds", paramKeySlashingEvidenceTTL, minSlashingWindowSeconds, maxEvidenceTTLSeconds)
			}
			return nil
		}
	case paramKeySlashingMaxSlashWei:
		return func(raw json.RawMessage) error {
			amount, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", paramKeySlashingMaxSlashWei, err)
			}
			if amount.Cmp(maxSlashWeiLimit) > 0 {
				return fmt.Errorf("%s: value exceeds %s", paramKeySlashingMaxSlashWei, maxSlashWeiLimit.String())
			}
			return nil
		}
	case paramKeySlashingEnabled:
		return func(raw json.RawMessage) error {
			_, err := parseBoolRaw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", paramKeySlashingEnabled, err)
			}
			return nil
		}
	case escrow.ParamKeyRealmMinThreshold:
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", escrow.ParamKeyRealmMinThreshold, err)
			}
			if value == 0 || value > 100 {
				return fmt.Errorf("%s: must be between 1 and 100", escrow.ParamKeyRealmMinThreshold)
			}
			return nil
		}
	case escrow.ParamKeyRealmMaxThreshold:
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", escrow.ParamKeyRealmMaxThreshold, err)
			}
			if value == 0 || value > 100 {
				return fmt.Errorf("%s: must be between 1 and 100", escrow.ParamKeyRealmMaxThreshold)
			}
			return nil
		}
	case escrow.ParamKeyRealmAllowedSchemes:
		return func(raw json.RawMessage) error {
			var values []string
			if err := json.Unmarshal(raw, &values); err != nil {
				var single string
				if err := json.Unmarshal(raw, &single); err != nil {
					return fmt.Errorf("%s: invalid payload", escrow.ParamKeyRealmAllowedSchemes)
				}
				values = []string{single}
			}
			if len(values) == 0 {
				return fmt.Errorf("%s: must not be empty", escrow.ParamKeyRealmAllowedSchemes)
			}
			for _, entry := range values {
				trimmed := strings.ToLower(strings.TrimSpace(entry))
				switch trimmed {
				case "single", "committee":
				default:
					if trimmed == "" {
						return fmt.Errorf("%s: scheme must not be empty", escrow.ParamKeyRealmAllowedSchemes)
					}
					if _, err := strconv.ParseUint(trimmed, 10, 8); err != nil {
						return fmt.Errorf("%s: unknown scheme %q", escrow.ParamKeyRealmAllowedSchemes, entry)
					}
				}
			}
			return nil
		}
	case "gov.deposit.MinProposalDeposit":
		return func(raw json.RawMessage) error {
			_, err := parseUintRaw(raw)
			if err != nil {
				return fmt.Errorf("gov.deposit.MinProposalDeposit: %w", err)
			}
			return nil
		}
	case "gov.tally.QuorumBps":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("gov.tally.QuorumBps: %w", err)
			}
			if value > maxBasisPoints {
				return fmt.Errorf("gov.tally.QuorumBps: must be <= %d", maxBasisPoints)
			}
			return nil
		}
	case "gov.tally.ThresholdBps":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("gov.tally.ThresholdBps: %w", err)
			}
			if value < 5_000 || value > maxBasisPoints {
				return fmt.Errorf("gov.tally.ThresholdBps: must be between 5000 and %d", maxBasisPoints)
			}
			return nil
		}
	case "gov.timelock.DurationSeconds":
		return func(raw json.RawMessage) error {
			value, err := parseUint64Raw(raw)
			if err != nil {
				return fmt.Errorf("gov.timelock.DurationSeconds: %w", err)
			}
			if value < 3_600 || value > 30*24*60*60 {
				return fmt.Errorf("gov.timelock.DurationSeconds: must be between 3600 and 2592000 seconds")
			}
			return nil
		}
	default:
		return nil
	}
}

func parseUintRaw(raw json.RawMessage) (*big.Int, error) {
	var value interface{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid numeric value: %w", err)
	}
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
	default:
		return nil, fmt.Errorf("value must be a number or decimal string")
	}
	text = strings.TrimPrefix(text, "+")
	if text == "" {
		return nil, fmt.Errorf("value must not be empty")
	}
	if strings.ContainsAny(text, ".eE") {
		return nil, fmt.Errorf("value must be an integer")
	}
	if strings.HasPrefix(text, "-") {
		return nil, fmt.Errorf("value must not be negative")
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(text, 10); !ok {
		return nil, fmt.Errorf("invalid integer encoding")
	}
	return amount, nil
}

func parseUint64Raw(raw json.RawMessage) (uint64, error) {
	amount, err := parseUintRaw(raw)
	if err != nil {
		return 0, err
	}
	if !amount.IsUint64() {
		return 0, fmt.Errorf("value exceeds uint64 range")
	}
	value := amount.Uint64()
	if value > maxGovernanceUint {
		return 0, fmt.Errorf("value exceeds governance limit %d", maxGovernanceUint)
	}
	return value, nil
}

func parseBoolRaw(raw json.RawMessage) (bool, error) {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("invalid boolean value: %w", err)
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		switch normalized {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return false, fmt.Errorf("boolean string must be true or false")
		}
	default:
		return false, fmt.Errorf("value must be boolean")
	}
	return false, fmt.Errorf("value must be boolean")
}

func (e *Engine) validateParamPayload(payloadJSON string) (map[string]json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("governance: payload must contain at least one parameter")
	}
	validated := make(map[string]json.RawMessage, len(payload))
	for key, raw := range payload {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return nil, fmt.Errorf("governance: payload key must not be empty")
		}
		if _, ok := e.allowedParams[trimmed]; !ok {
			return nil, fmt.Errorf("governance: parameter %q not in allow-list", trimmed)
		}
		validator, ok := e.paramValidators[trimmed]
		if !ok {
			return nil, fmt.Errorf("governance: parameter %q missing validation rule", trimmed)
		}
		if err := validator(raw); err != nil {
			return nil, err
		}
		validated[trimmed] = append(json.RawMessage(nil), raw...)
	}
	return validated, nil
}

func parseUintString(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	if strings.ContainsAny(trimmed, ".eE") {
		return nil, fmt.Errorf("value must be an integer string")
	}
	trimmed = strings.TrimPrefix(trimmed, "+")
	if strings.HasPrefix(trimmed, "-") {
		return nil, fmt.Errorf("value must not be negative")
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(trimmed, 10); !ok {
		return nil, fmt.Errorf("invalid integer encoding")
	}
	return amount, nil
}

func decodeAddress(addr string) ([20]byte, error) {
	var out [20]byte
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return out, fmt.Errorf("address must not be empty")
	}
	decoded, err := crypto.DecodeAddress(trimmed)
	if err != nil {
		return out, fmt.Errorf("invalid address %q: %w", trimmed, err)
	}
	bytes := decoded.Bytes()
	if len(bytes) != 20 {
		return out, fmt.Errorf("address must be 20 bytes")
	}
	copy(out[:], bytes)
	return out, nil
}

func formatAddress(addr [20]byte) string {
	return crypto.NewAddress(crypto.NHBPrefix, append([]byte(nil), addr[:]...)).String()
}

func parseSlashingPolicyPayload(payloadJSON string) (*parsedSlashingPolicy, error) {
	var payload SlashingPolicyPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	if payload.WindowSeconds < minSlashingWindowSeconds || payload.WindowSeconds > maxSlashingWindowSeconds {
		return nil, fmt.Errorf("governance: slashing window must be between %d and %d seconds", minSlashingWindowSeconds, maxSlashingWindowSeconds)
	}
	if payload.MaxPenaltyBps > uint32(maxBasisPoints) {
		return nil, fmt.Errorf("governance: maxPenaltyBps must be <= %d", maxBasisPoints)
	}
	if payload.EvidenceTTL == 0 {
		return nil, fmt.Errorf("governance: evidenceTtlSeconds must be greater than zero")
	}
	if payload.EvidenceTTL < payload.WindowSeconds {
		return nil, fmt.Errorf("governance: evidenceTtlSeconds must be >= windowSeconds")
	}
	if payload.EvidenceTTL > maxEvidenceTTLSeconds {
		return nil, fmt.Errorf("governance: evidenceTtlSeconds must be <= %d", maxEvidenceTTLSeconds)
	}
	maxSlash, err := parseUintString(payload.MaxSlashWei)
	if err != nil {
		return nil, fmt.Errorf("governance: invalid maxSlashWei: %w", err)
	}
	if maxSlash.Cmp(maxSlashWeiLimit) > 0 {
		return nil, fmt.Errorf("governance: maxSlashWei must be <= %s", maxSlashWeiLimit.String())
	}
	return &parsedSlashingPolicy{payload: payload, maxSlash: maxSlash}, nil
}

func (e *Engine) parseRoleAllowlistPayload(payloadJSON string) (*parsedRoleAllowlist, error) {
	if len(e.allowedRoles) == 0 {
		return nil, fmt.Errorf("governance: role allowlist proposals are disabled")
	}
	var payload RoleAllowlistPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	result := &parsedRoleAllowlist{memo: strings.TrimSpace(payload.Memo)}
	for _, entry := range payload.Grant {
		role := strings.TrimSpace(entry.Role)
		if role == "" {
			return nil, fmt.Errorf("governance: grant role must not be empty")
		}
		if _, ok := e.allowedRoles[role]; !ok {
			return nil, fmt.Errorf("governance: role %q not in allow-list", role)
		}
		addr, err := decodeAddress(entry.Address)
		if err != nil {
			return nil, fmt.Errorf("governance: invalid grant address: %w", err)
		}
		result.grant = append(result.grant, roleMutation{role: role, addr: addr})
	}
	for _, entry := range payload.Revoke {
		role := strings.TrimSpace(entry.Role)
		if role == "" {
			return nil, fmt.Errorf("governance: revoke role must not be empty")
		}
		if _, ok := e.allowedRoles[role]; !ok {
			return nil, fmt.Errorf("governance: role %q not in allow-list", role)
		}
		addr, err := decodeAddress(entry.Address)
		if err != nil {
			return nil, fmt.Errorf("governance: invalid revoke address: %w", err)
		}
		result.revoke = append(result.revoke, roleMutation{role: role, addr: addr})
	}
	if len(result.grant) == 0 && len(result.revoke) == 0 {
		return nil, fmt.Errorf("governance: role payload must include grant or revoke entries")
	}
	return result, nil
}

func (e *Engine) parseTreasuryDirectivePayload(payloadJSON string) (*parsedTreasuryDirective, error) {
	if len(e.treasuryAllow) == 0 {
		return nil, fmt.Errorf("governance: treasury directives are disabled")
	}
	var payload TreasuryDirectivePayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("governance: invalid payload: %w", err)
	}
	src, err := decodeAddress(payload.Source)
	if err != nil {
		return nil, fmt.Errorf("governance: invalid source address: %w", err)
	}
	if _, ok := e.treasuryAllow[src]; !ok {
		return nil, fmt.Errorf("governance: source %s not in treasury allow-list", crypto.NewAddress(crypto.NHBPrefix, src[:]).String())
	}
	if len(payload.Transfers) == 0 {
		return nil, fmt.Errorf("governance: treasury directive must include at least one transfer")
	}
	result := &parsedTreasuryDirective{source: src, memo: strings.TrimSpace(payload.Memo), total: big.NewInt(0)}
	for idx, entry := range payload.Transfers {
		to, err := decodeAddress(entry.To)
		if err != nil {
			return nil, fmt.Errorf("governance: invalid transfer #%d address: %w", idx, err)
		}
		amount, err := parseUintString(entry.AmountWei)
		if err != nil {
			return nil, fmt.Errorf("governance: invalid transfer #%d amount: %w", idx, err)
		}
		if amount.Sign() <= 0 {
			return nil, fmt.Errorf("governance: transfer #%d amount must be positive", idx)
		}
		result.total = new(big.Int).Add(result.total, amount)
		result.transfers = append(result.transfers, treasuryTransferDecoded{
			to:     to,
			amount: new(big.Int).Set(amount),
			memo:   strings.TrimSpace(entry.Memo),
			kind:   strings.TrimSpace(entry.Kind),
		})
	}
	return result, nil
}

func (e *Engine) applyParamUpdates(payloadJSON string) ([]string, error) {
	payload, err := e.validateParamPayload(payloadJSON)
	if err != nil {
		return nil, err
	}
	if err := e.preflightPolicyDelta(payload); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(payload))
	for key, raw := range payload {
		if err := e.state.ParamStoreSet(key, append([]byte(nil), raw...)); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func (e *Engine) applySlashingPolicy(parsed *parsedSlashingPolicy) (map[string]interface{}, error) {
	if parsed == nil {
		return nil, fmt.Errorf("governance: nil slashing policy payload")
	}
	policy := parsed.payload
	if err := e.state.ParamStoreSet(paramKeySlashingEnabled, []byte(strconv.FormatBool(policy.Enabled))); err != nil {
		return nil, err
	}
	if err := e.state.ParamStoreSet(paramKeySlashingMaxPenaltyBps, []byte(strconv.FormatUint(uint64(policy.MaxPenaltyBps), 10))); err != nil {
		return nil, err
	}
	if err := e.state.ParamStoreSet(paramKeySlashingWindow, []byte(strconv.FormatUint(policy.WindowSeconds, 10))); err != nil {
		return nil, err
	}
	if err := e.state.ParamStoreSet(paramKeySlashingEvidenceTTL, []byte(strconv.FormatUint(policy.EvidenceTTL, 10))); err != nil {
		return nil, err
	}
	if err := e.state.ParamStoreSet(paramKeySlashingMaxSlashWei, []byte(parsed.maxSlash.String())); err != nil {
		return nil, err
	}
	detail := map[string]interface{}{
		"enabled":            policy.Enabled,
		"maxPenaltyBps":      policy.MaxPenaltyBps,
		"windowSeconds":      policy.WindowSeconds,
		"maxSlashWei":        parsed.maxSlash.String(),
		"evidenceTtlSeconds": policy.EvidenceTTL,
	}
	if strings.TrimSpace(policy.Notes) != "" {
		detail["notes"] = strings.TrimSpace(policy.Notes)
	}
	return detail, nil
}

func (e *Engine) applyRoleAllowlist(parsed *parsedRoleAllowlist) (map[string]interface{}, error) {
	if parsed == nil {
		return nil, fmt.Errorf("governance: nil role payload")
	}
	grants := make([]map[string]string, 0, len(parsed.grant))
	for _, entry := range parsed.grant {
		if err := e.state.SetRole(entry.role, entry.addr[:]); err != nil {
			return nil, err
		}
		grants = append(grants, map[string]string{
			"role":    entry.role,
			"address": formatAddress(entry.addr),
		})
	}
	revocations := make([]map[string]string, 0, len(parsed.revoke))
	for _, entry := range parsed.revoke {
		if err := e.state.RemoveRole(entry.role, entry.addr[:]); err != nil {
			return nil, err
		}
		revocations = append(revocations, map[string]string{
			"role":    entry.role,
			"address": formatAddress(entry.addr),
		})
	}
	detail := map[string]interface{}{
		"grants":  grants,
		"revokes": revocations,
	}
	if parsed.memo != "" {
		detail["memo"] = parsed.memo
	}
	return detail, nil
}

func (e *Engine) applyTreasuryDirective(parsed *parsedTreasuryDirective) (map[string]interface{}, error) {
	if parsed == nil {
		return nil, fmt.Errorf("governance: nil treasury directive")
	}
	sourceAccount, err := e.state.GetAccount(parsed.source[:])
	if err != nil {
		return nil, err
	}
	if sourceAccount == nil {
		sourceAccount = &types.Account{}
	}
	if sourceAccount.BalanceZNHB == nil {
		sourceAccount.BalanceZNHB = big.NewInt(0)
	}
	if sourceAccount.BalanceZNHB.Cmp(parsed.total) < 0 {
		return nil, fmt.Errorf("governance: treasury insufficient balance")
	}
	sourceAccount.BalanceZNHB = new(big.Int).Sub(sourceAccount.BalanceZNHB, parsed.total)
	if err := e.state.PutAccount(parsed.source[:], sourceAccount); err != nil {
		return nil, err
	}
	transfers := make([]map[string]string, 0, len(parsed.transfers))
	for _, transfer := range parsed.transfers {
		account, err := e.state.GetAccount(transfer.to[:])
		if err != nil {
			return nil, err
		}
		if account == nil {
			account = &types.Account{}
		}
		if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
		}
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, transfer.amount)
		if err := e.state.PutAccount(transfer.to[:], account); err != nil {
			return nil, err
		}
		entry := map[string]string{
			"to":     formatAddress(transfer.to),
			"amount": transfer.amount.String(),
		}
		if transfer.memo != "" {
			entry["memo"] = transfer.memo
		}
		if transfer.kind != "" {
			entry["kind"] = transfer.kind
		}
		transfers = append(transfers, entry)
	}
	detail := map[string]interface{}{
		"source":    formatAddress(parsed.source),
		"totalWei":  parsed.total.String(),
		"transfers": transfers,
	}
	if parsed.memo != "" {
		detail["memo"] = parsed.memo
	}
	return detail, nil
}

func (e *Engine) appendAudit(event AuditEvent, proposalID uint64, actor []byte, details interface{}) error {
	if e == nil || e.state == nil {
		return errStateNotConfigured
	}
	record := &AuditRecord{
		Timestamp:  e.now(),
		Event:      event,
		ProposalID: proposalID,
	}
	if len(actor) == 20 {
		var addr [20]byte
		copy(addr[:], actor)
		record.Actor = AddressText(formatAddress(addr))
	}
	if details != nil {
		switch v := details.(type) {
		case string:
			record.Details = v
		default:
			payload, err := json.Marshal(v)
			if err != nil {
				return err
			}
			record.Details = string(payload)
		}
	}
	if _, err := e.state.GovernanceAppendAudit(record); err != nil {
		return err
	}
	return nil
}

func (e *Engine) emit(event *types.Event) {
	if e == nil || e.emitter == nil || event == nil {
		return
	}
	e.emitter.Emit(governanceEvent{evt: event})
}

func (e *Engine) preflightPolicyDelta(payload map[string]json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	delta, hasDelta, err := buildPolicyDelta(payload)
	if err != nil {
		return err
	}
	if !hasDelta {
		return nil
	}
	if e.policyValidator == nil {
		return nil
	}
	current := e.currentBaseline()
	if err := e.policyValidator(current, delta); err != nil {
		e.emit(newPolicyInvalidEvent(err))
		return err
	}
	return nil
}

func buildPolicyDelta(payload map[string]json.RawMessage) (PolicyDelta, bool, error) {
	var delta PolicyDelta
	var hasDelta bool
	for key, raw := range payload {
		switch key {
		case "gov.tally.QuorumBps":
			value, err := parseUint64Raw(raw)
			if err != nil {
				return PolicyDelta{}, false, err
			}
			if value > math.MaxUint32 {
				return PolicyDelta{}, false, fmt.Errorf("governance: quorum exceeds uint32 bounds")
			}
			v := uint32(value)
			delta.QuorumBps = &v
			hasDelta = true
		case "gov.tally.ThresholdBps":
			value, err := parseUint64Raw(raw)
			if err != nil {
				return PolicyDelta{}, false, err
			}
			if value > math.MaxUint32 {
				return PolicyDelta{}, false, fmt.Errorf("governance: threshold exceeds uint32 bounds")
			}
			v := uint32(value)
			delta.PassThresholdBps = &v
			hasDelta = true
		}
	}
	return delta, hasDelta, nil
}

func (e *Engine) currentBaseline() PolicyBaseline {
	governance := PolicyBaseline{
		QuorumBps:        uint32(e.quorumBps),
		PassThresholdBps: uint32(e.passThresholdBps),
		VotingPeriodSecs: e.votingPeriodSeconds,
	}
	if governance.VotingPeriodSecs < minVotingPeriodSeconds() {
		governance.VotingPeriodSecs = minVotingPeriodSeconds()
	}
	return governance
}

func minVotingPeriodSeconds() uint64 { return minGovernanceVotingPeriod }

func (e *Engine) now() time.Time {
	if e == nil || e.nowFn == nil {
		return time.Now().UTC()
	}
	return e.nowFn()
}

// SubmitProposal admits a governance proposal after validating the payload,
// deposit, and kind against the configured policy. The function returns the
// allocated proposal identifier on success.
func (e *Engine) SubmitProposal(proposer [20]byte, kind string, payloadJSON string, deposit *big.Int) (uint64, error) {
	if e == nil || e.state == nil {
		return 0, errStateNotConfigured
	}
	proposalKind := strings.TrimSpace(kind)
	if proposalKind == "" {
		return 0, fmt.Errorf("governance: proposal kind must not be empty")
	}
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return 0, fmt.Errorf("governance: payload must not be empty")
	}

	switch proposalKind {
	case ProposalKindParamUpdate, ProposalKindParamEmergency:
		payload, err := e.validateParamPayload(payloadJSON)
		if err != nil {
			return 0, err
		}
		if err := e.preflightPolicyDelta(payload); err != nil {
			return 0, err
		}
	case ProposalKindSlashingPolicy:
		if _, err := parseSlashingPolicyPayload(payloadJSON); err != nil {
			return 0, err
		}
	case ProposalKindRoleAllowlist:
		if _, err := e.parseRoleAllowlistPayload(payloadJSON); err != nil {
			return 0, err
		}
	case ProposalKindTreasuryDirective:
		if _, err := e.parseTreasuryDirectivePayload(payloadJSON); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("governance: unsupported proposal kind %q", kind)
	}

	lockAmount := big.NewInt(0)
	if deposit != nil {
		lockAmount = new(big.Int).Set(deposit)
	}
	if lockAmount.Sign() < 0 {
		return 0, fmt.Errorf("governance: deposit must not be negative")
	}
	if e.minDeposit != nil && lockAmount.Cmp(e.minDeposit) < 0 {
		return 0, fmt.Errorf("governance: deposit below minimum")
	}

	account, err := e.state.GetAccount(proposer[:])
	if err != nil {
		return 0, err
	}
	if account == nil {
		account = &types.Account{}
	}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
	if account.BalanceZNHB.Cmp(lockAmount) < 0 {
		return 0, fmt.Errorf("governance: insufficient ZNHB balance for deposit")
	}
	account.BalanceZNHB = new(big.Int).Sub(account.BalanceZNHB, lockAmount)
	if err := e.state.PutAccount(proposer[:], account); err != nil {
		return 0, err
	}
	if _, err := e.state.GovernanceEscrowLock(proposer[:], lockAmount); err != nil {
		return 0, err
	}

	proposalID, err := e.state.GovernanceNextProposalID()
	if err != nil {
		return 0, err
	}

	now := e.now()
	votingEnd := now.Add(time.Duration(e.votingPeriodSeconds) * time.Second)
	timelockEnd := votingEnd.Add(time.Duration(e.timelockSeconds) * time.Second)

	submitter := crypto.NewAddress(crypto.NHBPrefix, append([]byte(nil), proposer[:]...))
	depositCopy := new(big.Int).Set(lockAmount)
	proposal := &Proposal{
		ID:             proposalID,
		Submitter:      submitter,
		Status:         ProposalStatusVotingPeriod,
		Deposit:        depositCopy,
		SubmitTime:     now,
		VotingStart:    now,
		VotingEnd:      votingEnd,
		TimelockEnd:    timelockEnd,
		Target:         proposalKind,
		ProposedChange: payloadJSON,
	}
	if err := e.state.GovernancePutProposal(proposal); err != nil {
		return 0, err
	}

	e.emit(newProposedEvent(proposal))
	if err := e.appendAudit(AuditEventProposed, proposalID, proposer[:], map[string]string{"kind": proposalKind}); err != nil {
		return 0, err
	}
	return proposalID, nil
}

// CastVote records the caller's ballot selection for an active proposal using
// the voting power derived from the previous epoch's POTSO composite weights.
// The latest submission overwrites any prior ballot to simplify wallet UX.
func (e *Engine) CastVote(proposalID uint64, voter [20]byte, choice string) error {
	if e == nil || e.state == nil {
		return errStateNotConfigured
	}
	proposal, ok, err := e.state.GovernanceGetProposal(proposalID)
	if err != nil {
		return err
	}
	if !ok || proposal == nil {
		return fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	if proposal.Status != ProposalStatusVotingPeriod {
		return fmt.Errorf("governance: proposal %d not accepting votes", proposalID)
	}
	now := e.now()
	if !proposal.VotingStart.IsZero() && now.Before(proposal.VotingStart) {
		return fmt.Errorf("governance: voting has not started")
	}
	if !proposal.VotingEnd.IsZero() && now.After(proposal.VotingEnd) {
		return fmt.Errorf("governance: voting period closed")
	}

	normalized := strings.ToLower(strings.TrimSpace(choice))
	var voteChoice VoteChoice
	switch normalized {
	case VoteChoiceYes.String():
		voteChoice = VoteChoiceYes
	case VoteChoiceNo.String():
		voteChoice = VoteChoiceNo
	case VoteChoiceAbstain.String():
		voteChoice = VoteChoiceAbstain
	default:
		return fmt.Errorf("governance: invalid vote choice %q", choice)
	}

	epoch, ok, err := e.state.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("governance: potso snapshot unavailable")
	}
	snapshot, ok, err := e.state.SnapshotPotsoWeights(epoch)
	if err != nil {
		return err
	}
	if !ok || snapshot == nil {
		return fmt.Errorf("governance: potso snapshot unavailable")
	}

	var power uint64
	for _, entry := range snapshot.Entries {
		if entry.Address == voter {
			power = entry.WeightBps
			break
		}
	}
	if power == 0 {
		return fmt.Errorf("governance: voter has zero voting power")
	}
	if power > math.MaxUint32 {
		return fmt.Errorf("governance: voting power exceeds bounds")
	}

	vote := &Vote{
		ProposalID: proposalID,
		Voter:      crypto.NewAddress(crypto.NHBPrefix, append([]byte(nil), voter[:]...)),
		Choice:     voteChoice,
		PowerBps:   uint32(power),
		Timestamp:  now,
	}
	if err := e.state.GovernancePutVote(vote); err != nil {
		return err
	}

	e.emit(newVoteEvent(vote))
	if err := e.appendAudit(AuditEventVote, proposalID, voter[:], map[string]interface{}{
		"choice":   voteChoice.String(),
		"powerBps": power,
	}); err != nil {
		return err
	}
	return nil
}

// ComputeTally aggregates the voting power distribution for the supplied
// proposal. The method is side-effect free and can be used by read models to
// surface intermediate tallies without mutating state. The returned status
// reflects whether the proposal meets the quorum and threshold requirements.
func (e *Engine) ComputeTally(proposal *Proposal, votes []*Vote) (*Tally, ProposalStatus, error) {
	if e == nil {
		return nil, ProposalStatusUnspecified, fmt.Errorf("governance: engine not configured")
	}
	if proposal == nil {
		return nil, ProposalStatusUnspecified, fmt.Errorf("governance: proposal must not be nil")
	}

	var (
		yesPower     uint64
		noPower      uint64
		abstainPower uint64
	)
	for _, vote := range votes {
		if vote == nil {
			continue
		}
		weight := uint64(vote.PowerBps)
		switch vote.Choice {
		case VoteChoiceYes:
			if math.MaxUint64-yesPower < weight {
				return nil, ProposalStatusUnspecified, fmt.Errorf("governance: yes tally overflow")
			}
			yesPower += weight
		case VoteChoiceNo:
			if math.MaxUint64-noPower < weight {
				return nil, ProposalStatusUnspecified, fmt.Errorf("governance: no tally overflow")
			}
			noPower += weight
		case VoteChoiceAbstain:
			if math.MaxUint64-abstainPower < weight {
				return nil, ProposalStatusUnspecified, fmt.Errorf("governance: abstain tally overflow")
			}
			abstainPower += weight
		default:
			return nil, ProposalStatusUnspecified, fmt.Errorf("governance: invalid vote choice %q", vote.Choice)
		}
	}

	if math.MaxUint64-yesPower < noPower {
		return nil, ProposalStatusUnspecified, fmt.Errorf("governance: tally overflow")
	}
	running := yesPower + noPower
	if math.MaxUint64-running < abstainPower {
		return nil, ProposalStatusUnspecified, fmt.Errorf("governance: tally overflow")
	}
	totalPower := running + abstainPower
	yesDenom := yesPower + noPower
	var yesRatio uint64
	if yesDenom > 0 {
		yesRatio = (yesPower * 10_000) / yesDenom
	}
	tally := &Tally{
		TurnoutBps:       totalPower,
		QuorumBps:        e.quorumBps,
		YesPowerBps:      yesPower,
		NoPowerBps:       noPower,
		AbstainPowerBps:  abstainPower,
		YesRatioBps:      yesRatio,
		PassThresholdBps: e.passThresholdBps,
		TotalBallots:     uint64(len(votes)),
	}

	status := ProposalStatusRejected
	meetsQuorum := totalPower >= e.quorumBps
	if meetsQuorum && yesRatio >= e.passThresholdBps {
		status = ProposalStatusPassed
	}

	return tally, status, nil
}

// Finalize closes the voting window for the proposal, tallies the recorded
// ballots, and transitions the proposal to a terminal status. The proposal must
// be in the voting period and the voting end timestamp must have elapsed prior
// to calling this method.
func (e *Engine) Finalize(proposalID uint64) (ProposalStatus, *Tally, error) {
	if e == nil || e.state == nil {
		return ProposalStatusUnspecified, nil, errStateNotConfigured
	}
	proposal, ok, err := e.state.GovernanceGetProposal(proposalID)
	if err != nil {
		return ProposalStatusUnspecified, nil, err
	}
	if !ok || proposal == nil {
		return ProposalStatusUnspecified, nil, fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	if proposal.Status != ProposalStatusVotingPeriod {
		return proposal.Status, nil, fmt.Errorf("governance: proposal %d not in voting period", proposalID)
	}
	if proposal.VotingEnd.IsZero() {
		return ProposalStatusUnspecified, nil, fmt.Errorf("governance: proposal %d missing voting end", proposalID)
	}
	now := e.now()
	if now.Before(proposal.VotingEnd) {
		return ProposalStatusUnspecified, nil, fmt.Errorf("governance: voting still in progress")
	}

	votes, err := e.state.GovernanceListVotes(proposalID)
	if err != nil {
		return ProposalStatusUnspecified, nil, err
	}

	tally, status, err := e.ComputeTally(proposal, votes)
	if err != nil {
		return ProposalStatusUnspecified, nil, err
	}

	if status == ProposalStatusPassed && proposal.Deposit != nil && proposal.Deposit.Sign() > 0 {
		submitter := append([]byte(nil), proposal.Submitter.Bytes()...)
		if len(submitter) != 20 {
			return ProposalStatusUnspecified, nil, fmt.Errorf("governance: invalid submitter address length")
		}
		account, err := e.state.GetAccount(submitter)
		if err != nil {
			return ProposalStatusUnspecified, nil, err
		}
		if account == nil {
			account = &types.Account{}
		}
		if account.BalanceZNHB == nil {
			account.BalanceZNHB = big.NewInt(0)
		}
		account.BalanceZNHB = new(big.Int).Add(account.BalanceZNHB, proposal.Deposit)
		if err := e.state.PutAccount(submitter, account); err != nil {
			return ProposalStatusUnspecified, nil, err
		}
		if _, err := e.state.GovernanceEscrowUnlock(submitter, proposal.Deposit); err != nil {
			return ProposalStatusUnspecified, nil, err
		}
		proposal.Deposit = big.NewInt(0)
	}

	proposal.Status = status
	if err := e.state.GovernancePutProposal(proposal); err != nil {
		return ProposalStatusUnspecified, nil, err
	}

	e.emit(newFinalizedEvent(proposal, tally))
	detail := map[string]interface{}{
		"status":           status.StatusString(),
		"turnoutBps":       tally.TurnoutBps,
		"yesPowerBps":      tally.YesPowerBps,
		"noPowerBps":       tally.NoPowerBps,
		"abstainPowerBps":  tally.AbstainPowerBps,
		"yesRatioBps":      tally.YesRatioBps,
		"totalBallots":     tally.TotalBallots,
		"passThresholdBps": tally.PassThresholdBps,
		"quorumBps":        tally.QuorumBps,
	}
	if err := e.appendAudit(AuditEventFinalized, proposalID, nil, detail); err != nil {
		return ProposalStatusUnspecified, nil, err
	}
	return status, tally, nil
}

// QueueExecution marks a passed proposal as queued, signalling that it will be
// eligible for execution after the configured timelock window elapses.
func (e *Engine) QueueExecution(proposalID uint64) error {
	if e == nil || e.state == nil {
		return errStateNotConfigured
	}
	proposal, ok, err := e.state.GovernanceGetProposal(proposalID)
	if err != nil {
		return err
	}
	if !ok || proposal == nil {
		return fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	if proposal.Status != ProposalStatusPassed {
		return fmt.Errorf("governance: proposal %d not passed", proposalID)
	}
	if proposal.Queued {
		return fmt.Errorf("governance: proposal %d already queued", proposalID)
	}
	if proposal.TimelockEnd.IsZero() {
		return fmt.Errorf("governance: proposal %d missing timelock", proposalID)
	}
	proposal.Queued = true
	if err := e.state.GovernancePutProposal(proposal); err != nil {
		return err
	}
	e.emit(newQueuedEvent(proposal))
	detail := map[string]interface{}{
		"timelockEnd": proposal.TimelockEnd.Unix(),
		"kind":        strings.TrimSpace(proposal.Target),
	}
	if err := e.appendAudit(AuditEventQueued, proposalID, nil, detail); err != nil {
		return err
	}
	return nil
}

// Execute applies the queued proposal payload to the parameter store once the
// timelock delay has elapsed. The method refuses to execute proposals more than
// once to provide idempotency guarantees.
func (e *Engine) Execute(proposalID uint64) error {
	if e == nil || e.state == nil {
		return errStateNotConfigured
	}
	proposal, ok, err := e.state.GovernanceGetProposal(proposalID)
	if err != nil {
		return err
	}
	if !ok || proposal == nil {
		return fmt.Errorf("governance: proposal %d not found", proposalID)
	}
	if proposal.Status == ProposalStatusExecuted {
		return fmt.Errorf("governance: proposal %d already executed", proposalID)
	}
	if proposal.Status != ProposalStatusPassed {
		return fmt.Errorf("governance: proposal %d not executable", proposalID)
	}
	if !proposal.Queued {
		return fmt.Errorf("governance: proposal %d not queued", proposalID)
	}
	if !proposal.TimelockEnd.IsZero() {
		now := e.now()
		if now.Before(proposal.TimelockEnd) {
			return fmt.Errorf("governance: timelock not yet elapsed")
		}
	}
	if strings.TrimSpace(proposal.ProposedChange) == "" {
		return fmt.Errorf("governance: proposal %d has empty payload", proposalID)
	}

	target := strings.TrimSpace(proposal.Target)
	if target == "" {
		return fmt.Errorf("governance: proposal %d missing target", proposalID)
	}
	detail := map[string]interface{}{"kind": target}

	switch target {
	case ProposalKindParamUpdate, ProposalKindParamEmergency:
		keys, err := e.applyParamUpdates(proposal.ProposedChange)
		if err != nil {
			return err
		}
		detail["keys"] = keys
	case ProposalKindSlashingPolicy:
		parsed, err := parseSlashingPolicyPayload(proposal.ProposedChange)
		if err != nil {
			return err
		}
		slashingDetail, err := e.applySlashingPolicy(parsed)
		if err != nil {
			return err
		}
		for k, v := range slashingDetail {
			detail[k] = v
		}
	case ProposalKindRoleAllowlist:
		parsed, err := e.parseRoleAllowlistPayload(proposal.ProposedChange)
		if err != nil {
			return err
		}
		roleDetail, err := e.applyRoleAllowlist(parsed)
		if err != nil {
			return err
		}
		for k, v := range roleDetail {
			detail[k] = v
		}
	case ProposalKindTreasuryDirective:
		parsed, err := e.parseTreasuryDirectivePayload(proposal.ProposedChange)
		if err != nil {
			return err
		}
		treasuryDetail, err := e.applyTreasuryDirective(parsed)
		if err != nil {
			return err
		}
		for k, v := range treasuryDetail {
			detail[k] = v
		}
	default:
		return fmt.Errorf("governance: proposal %d has unsupported target %q", proposalID, proposal.Target)
	}

	proposal.Status = ProposalStatusExecuted
	if err := e.state.GovernancePutProposal(proposal); err != nil {
		return err
	}
	e.emit(newExecutedEvent(proposal))
	if err := e.appendAudit(AuditEventExecuted, proposalID, nil, detail); err != nil {
		return err
	}
	return nil
}

type governanceEvent struct {
	evt *types.Event
}

func (g governanceEvent) EventType() string {
	if g.evt == nil {
		return ""
	}
	return g.evt.Type
}

func (g governanceEvent) Event() *types.Event { return g.evt }

func newProposedEvent(p *Proposal) *types.Event {
	attrs := make(map[string]string)
	if p == nil {
		return &types.Event{Type: EventTypeProposalProposed, Attributes: attrs}
	}
	attrs["id"] = strconv.FormatUint(p.ID, 10)
	if bytes := p.Submitter.Bytes(); len(bytes) == 20 {
		attrs["proposer"] = hex.EncodeToString(bytes)
	}
	if strings.TrimSpace(p.Target) != "" {
		attrs["kind"] = p.Target
	}
	if p.Deposit != nil {
		attrs["deposit"] = p.Deposit.String()
	}
	if !p.VotingStart.IsZero() {
		attrs["votingStart"] = strconv.FormatInt(p.VotingStart.Unix(), 10)
	}
	if !p.VotingEnd.IsZero() {
		attrs["votingEnd"] = strconv.FormatInt(p.VotingEnd.Unix(), 10)
	}
	if !p.TimelockEnd.IsZero() {
		attrs["timelockEnd"] = strconv.FormatInt(p.TimelockEnd.Unix(), 10)
	}
	return &types.Event{Type: EventTypeProposalProposed, Attributes: attrs}
}

func newVoteEvent(v *Vote) *types.Event {
	attrs := make(map[string]string)
	if v == nil {
		return &types.Event{Type: EventTypeVoteCast, Attributes: attrs}
	}
	attrs["id"] = strconv.FormatUint(v.ProposalID, 10)
	if bytes := v.Voter.Bytes(); len(bytes) == 20 {
		attrs["voter"] = hex.EncodeToString(bytes)
	}
	if v.Choice.Valid() {
		attrs["choice"] = v.Choice.String()
	}
	attrs["powerBps"] = strconv.FormatUint(uint64(v.PowerBps), 10)
	if !v.Timestamp.IsZero() {
		attrs["timestamp"] = strconv.FormatInt(v.Timestamp.Unix(), 10)
	}
	return &types.Event{Type: EventTypeVoteCast, Attributes: attrs}
}

func newFinalizedEvent(p *Proposal, tally *Tally) *types.Event {
	attrs := make(map[string]string)
	if p != nil {
		attrs["id"] = strconv.FormatUint(p.ID, 10)
		if status := p.Status.StatusString(); status != "" {
			attrs["status"] = status
		}
	}
	if tally != nil {
		attrs["turnoutBps"] = strconv.FormatUint(tally.TurnoutBps, 10)
		attrs["quorumBps"] = strconv.FormatUint(tally.QuorumBps, 10)
		attrs["yesPowerBps"] = strconv.FormatUint(tally.YesPowerBps, 10)
		attrs["noPowerBps"] = strconv.FormatUint(tally.NoPowerBps, 10)
		attrs["abstainPowerBps"] = strconv.FormatUint(tally.AbstainPowerBps, 10)
		attrs["yesRatioBps"] = strconv.FormatUint(tally.YesRatioBps, 10)
		attrs["passThresholdBps"] = strconv.FormatUint(tally.PassThresholdBps, 10)
		attrs["totalBallots"] = strconv.FormatUint(tally.TotalBallots, 10)
	}
	return &types.Event{Type: EventTypeProposalFinalized, Attributes: attrs}
}

func newQueuedEvent(p *Proposal) *types.Event {
	attrs := make(map[string]string)
	if p != nil {
		attrs["id"] = strconv.FormatUint(p.ID, 10)
		if !p.TimelockEnd.IsZero() {
			attrs["timelockEnd"] = strconv.FormatInt(p.TimelockEnd.Unix(), 10)
		}
	}
	return &types.Event{Type: EventTypeProposalQueued, Attributes: attrs}
}

func newExecutedEvent(p *Proposal) *types.Event {
	attrs := make(map[string]string)
	if p != nil {
		attrs["id"] = strconv.FormatUint(p.ID, 10)
		if status := p.Status.StatusString(); status != "" {
			attrs["status"] = status
		}
	}
	return &types.Event{Type: EventTypeProposalExecuted, Attributes: attrs}
}

func newPolicyInvalidEvent(err error) *types.Event {
	attrs := make(map[string]string)
	if err != nil {
		attrs["reason"] = err.Error()
	}
	return &types.Event{Type: EventTypePolicyInvalid, Attributes: attrs}
}
