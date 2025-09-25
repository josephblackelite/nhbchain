package governance

import (
	"math/big"
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
