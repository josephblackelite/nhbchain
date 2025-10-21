package governance

import (
	"math/big"
	"strings"
	"time"

	"nhbchain/crypto"
)

// ProposalStatus enumerates the lifecycle phases a governance proposal
// transitions through as it accrues deposits, enters voting, and executes.
type ProposalStatus uint8

const (
	// ProposalStatusUnspecified indicates the proposal has not yet been
	// initialised and should not appear in state.
	ProposalStatusUnspecified ProposalStatus = iota
	// ProposalStatusDepositPeriod indicates the proposal is awaiting the
	// minimum deposit threshold before it can enter the voting period.
	ProposalStatusDepositPeriod
	// ProposalStatusVotingPeriod identifies proposals actively accepting
	// votes from eligible participants.
	ProposalStatusVotingPeriod
	// ProposalStatusPassed marks proposals that met quorum and threshold
	// requirements and are awaiting timelock completion or execution.
	ProposalStatusPassed
	// ProposalStatusRejected marks proposals that failed to meet quorum or
	// threshold requirements during the voting window.
	ProposalStatusRejected
	// ProposalStatusFailed marks proposals that encountered an execution
	// error after passing, requiring manual operator intervention.
	ProposalStatusFailed
	// ProposalStatusExpired marks proposals whose timelock window elapsed
	// without execution.
	ProposalStatusExpired
	// ProposalStatusExecuted indicates the proposal actions have been
	// successfully applied on-chain.
	ProposalStatusExecuted
)

// Proposal captures the immutable metadata, on-chain accounting, and state
// transitions associated with a governance proposal. It intentionally mirrors
// the persistence contract so off-chain services can index proposals without
// additional decoding.
type Proposal struct {
	ID             uint64         `json:"id"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary"`
	MetadataURI    string         `json:"metadata_uri"`
	Submitter      crypto.Address `json:"submitter"`
	Status         ProposalStatus `json:"status"`
	Deposit        *big.Int       `json:"deposit"`
	SubmitTime     time.Time      `json:"submit_time"`
	VotingStart    time.Time      `json:"voting_start"`
	VotingEnd      time.Time      `json:"voting_end"`
	TimelockEnd    time.Time      `json:"timelock_end"`
	Target         string         `json:"target"`
	ProposedChange string         `json:"proposed_change"`
	Queued         bool           `json:"queued"`
}

// VoteChoice enumerates the supported governance ballot selections.
type VoteChoice string

const (
	// VoteChoiceUnspecified marks an unset or invalid ballot and should not
	// be persisted.
	VoteChoiceUnspecified VoteChoice = ""
	// VoteChoiceYes signals support for the proposal contents.
	VoteChoiceYes VoteChoice = "yes"
	// VoteChoiceNo signals opposition to the proposal contents.
	VoteChoiceNo VoteChoice = "no"
	// VoteChoiceAbstain records participation without expressing support
	// or opposition and typically counts towards quorum only.
	VoteChoiceAbstain VoteChoice = "abstain"
)

// Valid reports whether the vote choice represents a supported selection.
func (c VoteChoice) Valid() bool {
	switch c {
	case VoteChoiceYes, VoteChoiceNo, VoteChoiceAbstain:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer for logging and event emission.
func (c VoteChoice) String() string { return string(c) }

// Vote describes a single participant's ballot on a proposal including their
// weighted voting power and selection. Recording the vote in a deterministic
// struct simplifies auditing and indexer integration.
type Vote struct {
	ProposalID uint64         `json:"proposal_id"`
	Voter      crypto.Address `json:"voter"`
	Choice     VoteChoice     `json:"choice"`
	PowerBps   uint32         `json:"power_bps"`
	Timestamp  time.Time      `json:"timestamp"`
}

// Tally captures the aggregated voting power distribution for a proposal
// alongside the governance parameters applied to determine the outcome.
type Tally struct {
	TurnoutBps       uint64 `json:"turnout_bps"`
	QuorumBps        uint64 `json:"quorum_bps"`
	YesPowerBps      uint64 `json:"yes_power_bps"`
	NoPowerBps       uint64 `json:"no_power_bps"`
	AbstainPowerBps  uint64 `json:"abstain_power_bps"`
	YesRatioBps      uint64 `json:"yes_ratio_bps"`
	PassThresholdBps uint64 `json:"pass_threshold_bps"`
	TotalBallots     uint64 `json:"total_ballots"`
}

// StatusString provides a developer-friendly textual representation of the
// proposal status suitable for logs and APIs.
func (s ProposalStatus) StatusString() string {
	switch s {
	case ProposalStatusDepositPeriod:
		return "deposit_period"
	case ProposalStatusVotingPeriod:
		return "voting_period"
	case ProposalStatusPassed:
		return "passed"
	case ProposalStatusRejected:
		return "rejected"
	case ProposalStatusFailed:
		return "failed"
	case ProposalStatusExpired:
		return "expired"
	case ProposalStatusExecuted:
		return "executed"
	default:
		return "unspecified"
	}
}

// ProposalKind enumerates the canonical governance proposal targets supported by
// the runtime. The constants are exposed to both RPC clients and on-chain
// execution in order to provide deterministic dispatch for governance payloads.
const (
	ProposalKindParamUpdate       = "param.update"
	ProposalKindParamEmergency    = "param.emergency_override"
	ProposalKindSlashingPolicy    = "policy.slashing"
	ProposalKindRoleAllowlist     = "role.allowlist"
	ProposalKindTreasuryDirective = "treasury.directive"
)

const (
	// ParamKeyMinimumValidatorStake controls the minimum stake required for
	// an account to qualify for validator eligibility and selection.
	ParamKeyMinimumValidatorStake = "staking.minimumValidatorStake"
	// ParamKeyStakingAprBps controls the APR applied to staking positions.
	ParamKeyStakingAprBps = "staking.aprBps"
	// ParamKeyStakingPayoutPeriodDays controls the reward payout cadence in days.
	ParamKeyStakingPayoutPeriodDays = "staking.payoutPeriodDays"
	// ParamKeyStakingUnbondingDays controls the unbonding window in days.
	ParamKeyStakingUnbondingDays = "staking.unbondingDays"
	// ParamKeyStakingMinStakeWei controls the minimum delegable stake in Wei.
	ParamKeyStakingMinStakeWei = "staking.minStakeWei"
	// ParamKeyStakingMaxEmissionPerYearWei caps the annual reward emission in Wei.
	ParamKeyStakingMaxEmissionPerYearWei = "staking.maxEmissionPerYearWei"
	// ParamKeyMintNHBMaxEmissionPerYearWei caps the NHB mint emission per calendar year.
	ParamKeyMintNHBMaxEmissionPerYearWei = "mint.nhb.maxEmissionPerYearWei"
	// ParamKeyMintZNHBMaxEmissionPerYearWei caps the ZNHB mint emission per calendar year.
	ParamKeyMintZNHBMaxEmissionPerYearWei = "mint.znhb.maxEmissionPerYearWei"
	// ParamKeyStakingRewardAsset selects the asset used for staking rewards.
	ParamKeyStakingRewardAsset = "staking.rewardAsset"
	// ParamKeyStakingCompoundDefault toggles auto-compounding by default for new delegations.
	ParamKeyStakingCompoundDefault = "staking.compoundDefault"
	// ParamKeyLoyaltyDynamicTargetBps controls the adaptive loyalty target basis points.
	ParamKeyLoyaltyDynamicTargetBps = "loyalty.dynamic.targetBps"
	// ParamKeyLoyaltyDynamicMinBps controls the adaptive loyalty lower bound basis points.
	ParamKeyLoyaltyDynamicMinBps = "loyalty.dynamic.minBps"
	// ParamKeyLoyaltyDynamicMaxBps controls the adaptive loyalty upper bound basis points.
	ParamKeyLoyaltyDynamicMaxBps = "loyalty.dynamic.maxBps"
	// ParamKeyLoyaltyDynamicSmoothingStepBps controls the maximum adjustment per epoch.
	ParamKeyLoyaltyDynamicSmoothingStepBps = "loyalty.dynamic.smoothingStepBps"
	// ParamKeyLoyaltyDynamicCoverageMax configures the maximum healthy coverage ratio.
	ParamKeyLoyaltyDynamicCoverageMax = "loyalty.dynamic.coverageMax"
	// ParamKeyLoyaltyDynamicCoverageLookbackDays controls the trailing activity window length.
	ParamKeyLoyaltyDynamicCoverageLookbackDays = "loyalty.dynamic.coverageLookbackDays"
	// ParamKeyLoyaltyDynamicDailyCapPctOf7dFees limits daily issuance to a share of recent fees.
	ParamKeyLoyaltyDynamicDailyCapPctOf7dFees = "loyalty.dynamic.dailyCapPctOf7dFees"
	// ParamKeyLoyaltyDynamicDailyCapUSD caps daily issuance in USD terms.
	ParamKeyLoyaltyDynamicDailyCapUSD = "loyalty.dynamic.dailyCapUsd"
	// ParamKeyLoyaltyDynamicYearlyCapPctOfInitialSupply caps annual issuance relative to initial supply.
	ParamKeyLoyaltyDynamicYearlyCapPctOfInitialSupply = "loyalty.dynamic.yearlyCapPctOfInitialSupply"
	// ParamKeyLoyaltyDynamicPricePair selects the oracle pair for coverage calculations.
	ParamKeyLoyaltyDynamicPricePair = "loyalty.dynamic.priceGuard.pricePair"
	// ParamKeyLoyaltyDynamicPriceTwapWindowSeconds controls the oracle TWAP window.
	ParamKeyLoyaltyDynamicPriceTwapWindowSeconds = "loyalty.dynamic.priceGuard.twapWindowSeconds"
	// ParamKeyLoyaltyDynamicPriceMaxAgeSeconds caps oracle staleness.
	ParamKeyLoyaltyDynamicPriceMaxAgeSeconds = "loyalty.dynamic.priceGuard.priceMaxAgeSeconds"
	// ParamKeyLoyaltyDynamicPriceMaxDeviationBps constrains acceptable oracle deviation.
	ParamKeyLoyaltyDynamicPriceMaxDeviationBps = "loyalty.dynamic.priceGuard.maxDeviationBps"
	// ParamKeyLoyaltyDynamicPriceGuardEnabled toggles oracle guardrails.
	ParamKeyLoyaltyDynamicPriceGuardEnabled = "loyalty.dynamic.priceGuard.enabled"
	defaultMinimumValidatorStake            = 1000
)

// DefaultMinimumValidatorStake exposes the legacy minimum validator stake used
// prior to parameter governance. It remains available as a migration fallback
// for networks upgrading from releases that relied on the static constant.
func DefaultMinimumValidatorStake() *big.Int {
	return big.NewInt(defaultMinimumValidatorStake)
}

// AuditEvent identifies the lifecycle milestone captured by a governance audit
// record. Stored values are designed for long-term readability while remaining
// compact enough for on-chain persistence.
type AuditEvent string

const (
	AuditEventProposed  AuditEvent = "proposed"
	AuditEventVote      AuditEvent = "vote"
	AuditEventFinalized AuditEvent = "finalized"
	AuditEventQueued    AuditEvent = "queued"
	AuditEventExecuted  AuditEvent = "executed"
	AuditEventFailed    AuditEvent = "failed"
)

// AuditRecord captures an immutable governance lifecycle entry. Records are
// written append-only and referenced by a monotonically increasing sequence so
// operators and auditors can reconstruct the exact ordering of governance
// actions without relying on external event streams.
type AuditRecord struct {
	Sequence   uint64      `json:"sequence"`
	Timestamp  time.Time   `json:"timestamp"`
	Event      AuditEvent  `json:"event"`
	ProposalID uint64      `json:"proposal_id"`
	Actor      AddressText `json:"actor"`
	Details    string      `json:"details"`
}

// AddressText provides a stable textual representation of an optional
// governance actor address for JSON encoding. Empty strings are preserved when
// no actor is associated with the record, simplifying audit log diffing.
type AddressText string

// Bytes converts the textual representation back to raw bytes if the string is
// a valid Bech32 address. Invalid inputs return nil to keep audit log ingestion
// resilient to historic entries that may have been produced before validation
// tightened.
func (a AddressText) Bytes() []byte {
	trimmed := strings.TrimSpace(string(a))
	if trimmed == "" {
		return nil
	}
	addr, err := crypto.DecodeAddress(trimmed)
	if err != nil {
		return nil
	}
	return append([]byte(nil), addr.Bytes()...)
}

// SlashingPolicyPayload defines the expected schema for slashing policy
// proposals. Basis point fields must be within [0, 10_000] and the window must
// be at least one block interval to prevent nonsensical configurations.
type SlashingPolicyPayload struct {
	Enabled       bool   `json:"enabled"`
	MaxPenaltyBps uint32 `json:"maxPenaltyBps"`
	WindowSeconds uint64 `json:"windowSeconds"`
	MaxSlashWei   string `json:"maxSlashWei"`
	EvidenceTTL   uint64 `json:"evidenceTtlSeconds"`
	Notes         string `json:"notes,omitempty"`
}

// RoleAddressPair captures a role membership mutation in role allowlist
// proposals.
type RoleAddressPair struct {
	Role    string `json:"role"`
	Address string `json:"address"`
}

// RoleAllowlistPayload enumerates the additions and removals to apply when
// executing a role allowlist proposal.
type RoleAllowlistPayload struct {
	Grant  []RoleAddressPair `json:"grant"`
	Revoke []RoleAddressPair `json:"revoke"`
	Memo   string            `json:"memo,omitempty"`
}

// TreasuryTransfer describes a single debit from the treasury source to a
// recipient address expressed in Wei.
type TreasuryTransfer struct {
	To        string `json:"to"`
	AmountWei string `json:"amountWei"`
	Memo      string `json:"memo,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// TreasuryDirectivePayload instructs the runtime to debit an allow-listed
// treasury account and credit a set of recipients. Transfers execute atomically
// as part of the governance proposal payload.
type TreasuryDirectivePayload struct {
	Source    string             `json:"source"`
	Transfers []TreasuryTransfer `json:"transfers"`
	Memo      string             `json:"memo,omitempty"`
}
