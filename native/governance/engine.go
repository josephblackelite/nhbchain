package governance

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// ProposalKindParamUpdate is the only proposal kind supported in GOV-1B.
	ProposalKindParamUpdate = "param.update"
	// EventTypeProposalProposed is emitted when a new proposal is accepted.
	EventTypeProposalProposed = "gov.proposed"
)

var (
	errStateNotConfigured = errors.New("governance: state not configured")
)

type proposalState interface {
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
	GovernanceEscrowBalance(addr []byte) (*big.Int, error)
	GovernanceEscrowLock(addr []byte, amount *big.Int) (*big.Int, error)
	GovernanceNextProposalID() (uint64, error)
	GovernancePutProposal(p *Proposal) error
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
