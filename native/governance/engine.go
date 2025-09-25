package governance

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

const (
	// ProposalKindParamUpdate is the only proposal kind supported in GOV-1B.
	ProposalKindParamUpdate = "param.update"
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
	ParamStoreSet(name string, value []byte) error
	PotsoRewardsLastProcessedEpoch() (uint64, bool, error)
	SnapshotPotsoWeights(epoch uint64) (*potso.StoredWeightSnapshot, bool, error)
}

// ProposalPolicy captures the runtime knobs that control proposal admission.
// The engine expects the values to be pre-normalised (e.g. MinDepositWei in Wei).
//
// AllowedParams must contain the canonical parameter keys permitted for
// parameter update proposals.
type ProposalPolicy struct {
	MinDepositWei       *big.Int
	VotingPeriodSeconds uint64
	TimelockSeconds     uint64
	AllowedParams       []string
	QuorumBps           uint64
	PassThresholdBps    uint64
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
	quorumBps           uint64
	passThresholdBps    uint64
}

// NewEngine constructs a governance engine with default no-op dependencies.
func NewEngine() *Engine {
	return &Engine{
		emitter:       events.NoopEmitter{},
		nowFn:         func() time.Time { return time.Now().UTC() },
		minDeposit:    big.NewInt(0),
		allowedParams: map[string]struct{}{},
	}
}

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
	for _, raw := range policy.AllowedParams {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		e.allowedParams[trimmed] = struct{}{}
	}
	e.quorumBps = policy.QuorumBps
	e.passThresholdBps = policy.PassThresholdBps
}

func (e *Engine) emit(event *types.Event) {
	if e == nil || e.emitter == nil || event == nil {
		return
	}
	e.emitter.Emit(governanceEvent{evt: event})
}

func (e *Engine) now() time.Time {
	if e == nil || e.nowFn == nil {
		return time.Now().UTC()
	}
	return e.nowFn()
}

// ProposeParamChange admits a parameter change proposal after validating the
// payload, deposit, and kind against the configured policy. The function returns
// the allocated proposal identifier on success.
func (e *Engine) ProposeParamChange(proposer [20]byte, kind string, payloadJSON string, deposit *big.Int) (uint64, error) {
	if e == nil || e.state == nil {
		return 0, errStateNotConfigured
	}
	proposalKind := strings.TrimSpace(kind)
	if proposalKind != ProposalKindParamUpdate {
		return 0, fmt.Errorf("governance: unsupported proposal kind %q", kind)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return 0, fmt.Errorf("governance: invalid payload: %w", err)
	}
	for key := range payload {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return 0, fmt.Errorf("governance: payload key must not be empty")
		}
		if _, ok := e.allowedParams[trimmed]; !ok {
			return 0, fmt.Errorf("governance: parameter %q not in allow-list", trimmed)
		}
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
	return nil
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
				return ProposalStatusUnspecified, nil, fmt.Errorf("governance: yes tally overflow")
			}
			yesPower += weight
		case VoteChoiceNo:
			if math.MaxUint64-noPower < weight {
				return ProposalStatusUnspecified, nil, fmt.Errorf("governance: no tally overflow")
			}
			noPower += weight
		case VoteChoiceAbstain:
			if math.MaxUint64-abstainPower < weight {
				return ProposalStatusUnspecified, nil, fmt.Errorf("governance: abstain tally overflow")
			}
			abstainPower += weight
		default:
			return ProposalStatusUnspecified, nil, fmt.Errorf("governance: invalid vote choice %q", vote.Choice)
		}
	}

	if math.MaxUint64-yesPower < noPower {
		return ProposalStatusUnspecified, nil, fmt.Errorf("governance: tally overflow")
	}
	running := yesPower + noPower
	if math.MaxUint64-running < abstainPower {
		return ProposalStatusUnspecified, nil, fmt.Errorf("governance: tally overflow")
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
	if strings.TrimSpace(proposal.Target) != ProposalKindParamUpdate {
		return fmt.Errorf("governance: proposal %d has unsupported target %q", proposalID, proposal.Target)
	}

	if strings.TrimSpace(proposal.ProposedChange) == "" {
		return fmt.Errorf("governance: proposal %d has empty payload", proposalID)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(proposal.ProposedChange), &payload); err != nil {
		return fmt.Errorf("governance: invalid proposed change: %w", err)
	}
	for key, raw := range payload {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return fmt.Errorf("governance: payload key must not be empty")
		}
		if _, allowed := e.allowedParams[trimmed]; !allowed {
			return fmt.Errorf("governance: parameter %q not in allow-list", trimmed)
		}
		if err := e.state.ParamStoreSet(trimmed, append([]byte(nil), raw...)); err != nil {
			return err
		}
	}

	proposal.Status = ProposalStatusExecuted
	if err := e.state.GovernancePutProposal(proposal); err != nil {
		return err
	}
	e.emit(newExecutedEvent(proposal))
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
