package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

type mockGovernanceState struct {
	accounts       map[string]*types.Account
	escrowBalances map[string]*big.Int
	proposals      map[uint64]*Proposal
	votes          map[string]*Vote
	snapshots      map[uint64]*potso.StoredWeightSnapshot
	lastEpoch      uint64
	hasLastEpoch   bool
	nextID         uint64
	params         map[string][]byte
	roles          map[string]map[string]struct{}
	audit          []*AuditRecord
}

func TestValidatorForNetworkSeeds(t *testing.T) {
	t.Parallel()
	validator := validatorForParam("network.seeds")
	if validator == nil {
		t.Fatalf("expected validator for network.seeds")
	}
	valid := json.RawMessage(`{"version":1,"static":[{"nodeId":"0xabc123","address":"seed.example.org:46656"}]}`)
	if err := validator(valid); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	invalid := json.RawMessage(`{"version":1,"static":[{"nodeId":"","address":"seed.example.org:46656"}]}`)
	if err := validator(invalid); err == nil {
		t.Fatalf("expected validation error for empty nodeId")
	}
}

func newMockGovernanceState(initial map[[20]byte]*types.Account) *mockGovernanceState {
	accounts := make(map[string]*types.Account)
	for addr, acc := range initial {
		accounts[string(addr[:])] = cloneAccount(acc)
	}
	return &mockGovernanceState{
		accounts:       accounts,
		escrowBalances: make(map[string]*big.Int),
		proposals:      make(map[uint64]*Proposal),
		votes:          make(map[string]*Vote),
		snapshots:      make(map[uint64]*potso.StoredWeightSnapshot),
		params:         make(map[string][]byte),
		roles:          make(map[string]map[string]struct{}),
	}
}

func (m *mockGovernanceState) GetAccount(addr []byte) (*types.Account, error) {
	if acc, ok := m.accounts[string(addr)]; ok {
		return cloneAccount(acc), nil
	}
	return &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)}, nil
}

func (m *mockGovernanceState) PutAccount(addr []byte, account *types.Account) error {
	m.accounts[string(addr)] = cloneAccount(account)
	return nil
}

func (m *mockGovernanceState) GovernanceEscrowBalance(addr []byte) (*big.Int, error) {
	if bal, ok := m.escrowBalances[string(addr)]; ok {
		return new(big.Int).Set(bal), nil
	}
	return big.NewInt(0), nil
}

func (m *mockGovernanceState) GovernanceEscrowLock(addr []byte, amount *big.Int) (*big.Int, error) {
	if amount == nil {
		amount = big.NewInt(0)
	}
	current, _ := m.GovernanceEscrowBalance(addr)
	updated := new(big.Int).Add(current, amount)
	m.escrowBalances[string(addr)] = updated
	return new(big.Int).Set(updated), nil
}

func (m *mockGovernanceState) GovernanceEscrowUnlock(addr []byte, amount *big.Int) (*big.Int, error) {
	current, _ := m.GovernanceEscrowBalance(addr)
	unlock := big.NewInt(0)
	if amount != nil {
		if amount.Sign() < 0 {
			return nil, fmt.Errorf("unlock must not be negative")
		}
		unlock = new(big.Int).Set(amount)
	}
	if current.Cmp(unlock) < 0 {
		return nil, fmt.Errorf("unlock exceeds balance")
	}
	updated := new(big.Int).Sub(current, unlock)
	m.escrowBalances[string(addr)] = updated
	return new(big.Int).Set(updated), nil
}

func (m *mockGovernanceState) GovernanceNextProposalID() (uint64, error) {
	m.nextID++
	return m.nextID, nil
}

func (m *mockGovernanceState) GovernancePutProposal(p *Proposal) error {
	if p == nil {
		return nil
	}
	clone := *p
	if p.Deposit != nil {
		clone.Deposit = new(big.Int).Set(p.Deposit)
	}
	clone.Queued = p.Queued
	m.proposals[p.ID] = &clone
	return nil
}

func (m *mockGovernanceState) GovernanceGetProposal(id uint64) (*Proposal, bool, error) {
	proposal, ok := m.proposals[id]
	if !ok {
		return nil, false, nil
	}
	clone := *proposal
	if proposal.Deposit != nil {
		clone.Deposit = new(big.Int).Set(proposal.Deposit)
	}
	clone.Queued = proposal.Queued
	return &clone, true, nil
}

func (m *mockGovernanceState) GovernancePutVote(v *Vote) error {
	if v == nil {
		return fmt.Errorf("vote must not be nil")
	}
	key := fmt.Sprintf("%d/%x", v.ProposalID, v.Voter.Bytes())
	clone := *v
	m.votes[key] = &clone
	return nil
}

func (m *mockGovernanceState) GovernanceListVotes(id uint64) ([]*Vote, error) {
	prefix := fmt.Sprintf("%d/", id)
	var votes []*Vote
	for key, vote := range m.votes {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		cloned := *vote
		votes = append(votes, &cloned)
	}
	return votes, nil
}

func (m *mockGovernanceState) PotsoRewardsLastProcessedEpoch() (uint64, bool, error) {
	if !m.hasLastEpoch {
		return 0, false, nil
	}
	return m.lastEpoch, true, nil
}

func (m *mockGovernanceState) SnapshotPotsoWeights(epoch uint64) (*potso.StoredWeightSnapshot, bool, error) {
	snapshot, ok := m.snapshots[epoch]
	if !ok {
		return nil, false, nil
	}
	return cloneStoredWeightSnapshot(snapshot), true, nil
}

func (m *mockGovernanceState) SetRole(role string, addr []byte) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return fmt.Errorf("role must not be empty")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	bucket, ok := m.roles[trimmed]
	if !ok {
		bucket = make(map[string]struct{})
		m.roles[trimmed] = bucket
	}
	bucket[string(addr)] = struct{}{}
	return nil
}

func (m *mockGovernanceState) RemoveRole(role string, addr []byte) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return fmt.Errorf("role must not be empty")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	bucket, ok := m.roles[trimmed]
	if !ok {
		return nil
	}
	delete(bucket, string(addr))
	if len(bucket) == 0 {
		delete(m.roles, trimmed)
	}
	return nil
}

func (m *mockGovernanceState) GovernanceAppendAudit(r *AuditRecord) (*AuditRecord, error) {
	if r == nil {
		return nil, fmt.Errorf("audit record must not be nil")
	}
	clone := *r
	clone.Sequence = uint64(len(m.audit) + 1)
	if clone.Timestamp.IsZero() {
		clone.Timestamp = time.Now().UTC()
	}
	m.audit = append(m.audit, &clone)
	return &clone, nil
}

func (m *mockGovernanceState) ParamStoreSet(name string, value []byte) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("params key must not be empty")
	}
	m.params[trimmed] = append([]byte(nil), value...)
	return nil
}

func (m *mockGovernanceState) ParamStoreGet(name string) ([]byte, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, false
	}
	val, ok := m.params[trimmed]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), val...), true
}

type captureEmitter struct {
	events []events.Event
}

func (c *captureEmitter) Emit(evt events.Event) { c.events = append(c.events, evt) }

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	cloned := *acc
	if acc.BalanceZNHB != nil {
		cloned.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	} else {
		cloned.BalanceZNHB = big.NewInt(0)
	}
	if acc.BalanceNHB != nil {
		cloned.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	} else {
		cloned.BalanceNHB = big.NewInt(0)
	}
	if acc.Stake != nil {
		cloned.Stake = new(big.Int).Set(acc.Stake)
	} else {
		cloned.Stake = big.NewInt(0)
	}
	if acc.LockedZNHB != nil {
		cloned.LockedZNHB = new(big.Int).Set(acc.LockedZNHB)
	} else {
		cloned.LockedZNHB = big.NewInt(0)
	}
	return &cloned
}

func cloneStoredWeightSnapshot(snapshot *potso.StoredWeightSnapshot) *potso.StoredWeightSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := &potso.StoredWeightSnapshot{
		Epoch:           snapshot.Epoch,
		TotalEngagement: snapshot.TotalEngagement,
	}
	if snapshot.TotalStake != nil {
		clone.TotalStake = new(big.Int).Set(snapshot.TotalStake)
	}
	if len(snapshot.Entries) > 0 {
		clone.Entries = make([]potso.StoredWeightEntry, len(snapshot.Entries))
		for i := range snapshot.Entries {
			entry := snapshot.Entries[i]
			clone.Entries[i] = potso.StoredWeightEntry{
				Address:            entry.Address,
				Stake:              new(big.Int).Set(entry.Stake),
				Engagement:         entry.Engagement,
				StakeShareBps:      entry.StakeShareBps,
				EngagementShareBps: entry.EngagementShareBps,
				WeightBps:          entry.WeightBps,
			}
		}
	}
	return clone
}

func TestSubmitProposalRejectsUnknownParam(t *testing.T) {
	var proposer [20]byte
	proposer[19] = 1
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 3600,
		TimelockSeconds:     600,
		AllowedParams:       []string{"fees.baseFee"},
	})

	_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, `{"escrow.maxOpenDisputes":5}`, big.NewInt(200))
	if err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Fatalf("expected allow-list rejection, got %v", err)
	}
}

func TestSubmitProposalRejectsInvalidPolicyDelta(t *testing.T) {
	var proposer [20]byte
	proposer[18] = 7
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 3600,
		TimelockSeconds:     600,
		AllowedParams:       []string{"gov.tally.QuorumBps", "gov.tally.ThresholdBps"},
		QuorumBps:           6000,
		PassThresholdBps:    5000,
	})
	errPolicyInvalid := errors.New("policy invariants violated")
	engine.SetPolicyValidator(func(cur PolicyBaseline, delta PolicyDelta) error {
		if delta.QuorumBps != nil && *delta.QuorumBps < cur.PassThresholdBps {
			return errPolicyInvalid
		}
		if delta.PassThresholdBps != nil && *delta.PassThresholdBps > cur.QuorumBps {
			return errPolicyInvalid
		}
		return nil
	})
	emitter := &captureEmitter{}
	engine.SetEmitter(emitter)

	_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, `{"gov.tally.QuorumBps":4000}`, big.NewInt(200))
	if err == nil {
		t.Fatalf("expected policy invariant rejection")
	}
	if !errors.Is(err, errPolicyInvalid) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(emitter.events) == 0 {
		t.Fatalf("expected policy invalid event emission")
	}
	evt, ok := emitter.events[0].(governanceEvent)
	if !ok {
		t.Fatalf("unexpected event type: %T", emitter.events[0])
	}
	if event := evt.Event(); event == nil || event.Type != EventTypePolicyInvalid {
		t.Fatalf("expected %s event, got %+v", EventTypePolicyInvalid, event)
	}
}

func TestSubmitProposalRejectsLowDeposit(t *testing.T) {
	var proposer [20]byte
	proposer[10] = 2
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(500),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     50,
		AllowedParams:       []string{"fees.baseFee"},
	})

	_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, `{"fees.baseFee":5}`, big.NewInt(200))
	if err == nil || !strings.Contains(err.Error(), "deposit below minimum") {
		t.Fatalf("expected deposit rejection, got %v", err)
	}
}

func TestSubmitProposalRejectsEmptyParamKey(t *testing.T) {
	var proposer [20]byte
	proposer[11] = 4
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 600,
		TimelockSeconds:     120,
		AllowedParams:       []string{"fees.baseFee"},
	})

	_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, `{" ":5}`, big.NewInt(150))
	if err == nil || !strings.Contains(err.Error(), "key must not be empty") {
		t.Fatalf("expected empty key rejection, got %v", err)
	}
}

func TestSubmitProposalRejectsInsufficientBalance(t *testing.T) {
	var proposer [20]byte
	proposer[12] = 5
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(99), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 600,
		TimelockSeconds:     120,
		AllowedParams:       []string{"fees.baseFee"},
	})

	_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, `{"fees.baseFee":5}`, big.NewInt(150))
	if err == nil || !strings.Contains(err.Error(), "insufficient ZNHB balance") {
		t.Fatalf("expected insufficient balance, got %v", err)
	}
}

func TestSubmitProposalHappyPath(t *testing.T) {
	var proposer [20]byte
	proposer[5] = 3
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 600,
		TimelockSeconds:     120,
		AllowedParams:       []string{"fees.baseFee"},
	})
	engine.SetNowFunc(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	emitter := &captureEmitter{}
	engine.SetEmitter(emitter)

	payload := `{"fees.baseFee":1000}`
	deposit := big.NewInt(300)
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, payload, deposit)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if proposalID != 1 {
		t.Fatalf("unexpected proposal id: %d", proposalID)
	}

	acct, _ := state.GetAccount(proposer[:])
	expectedBalance := big.NewInt(700)
	if acct.BalanceZNHB.Cmp(expectedBalance) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", acct.BalanceZNHB.String(), expectedBalance.String())
	}

	escrow, _ := state.GovernanceEscrowBalance(proposer[:])
	if escrow.Cmp(deposit) != 0 {
		t.Fatalf("unexpected escrow balance: got %s want %s", escrow.String(), deposit.String())
	}

	stored := state.proposals[proposalID]
	if stored == nil {
		t.Fatalf("expected stored proposal")
	}
	if stored.Target != ProposalKindParamUpdate {
		t.Fatalf("unexpected target: %s", stored.Target)
	}
	if stored.ProposedChange != payload {
		t.Fatalf("unexpected payload: %s", stored.ProposedChange)
	}
	if stored.Deposit.Cmp(deposit) != 0 {
		t.Fatalf("unexpected stored deposit: got %s want %s", stored.Deposit.String(), deposit.String())
	}
	wantVotingEnd := time.Unix(1700000000+600, 0).UTC()
	if !stored.VotingEnd.Equal(wantVotingEnd) {
		t.Fatalf("unexpected voting end: got %s want %s", stored.VotingEnd, wantVotingEnd)
	}
	wantTimelock := time.Unix(1700000000+600+120, 0).UTC()
	if !stored.TimelockEnd.Equal(wantTimelock) {
		t.Fatalf("unexpected timelock: got %s want %s", stored.TimelockEnd, wantTimelock)
	}

	if len(state.audit) != 1 {
		t.Fatalf("expected audit entry recorded")
	}
	if state.audit[0].Event != AuditEventProposed {
		t.Fatalf("unexpected audit event: %s", state.audit[0].Event)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	evt, ok := emitter.events[0].(governanceEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", emitter.events[0])
	}
	payloadEvent := evt.Event()
	if payloadEvent.Type != EventTypeProposalProposed {
		t.Fatalf("unexpected event type: %s", payloadEvent.Type)
	}
	if payloadEvent.Attributes["id"] != "1" {
		t.Fatalf("unexpected event id: %s", payloadEvent.Attributes["id"])
	}
	if payloadEvent.Attributes["deposit"] != deposit.String() {
		t.Fatalf("unexpected event deposit: %s", payloadEvent.Attributes["deposit"])
	}
}

func voteStorageKey(proposalID uint64, voter [20]byte) string {
	return fmt.Sprintf("%d/%x", proposalID, voter)
}

func TestCastVoteRecordsBallot(t *testing.T) {
	var voter [20]byte
	voter[3] = 9
	now := time.Unix(1_700_000_500, 0).UTC()

	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:          1,
		Status:      ProposalStatusVotingPeriod,
		VotingStart: now.Add(-time.Hour),
		VotingEnd:   now.Add(time.Hour),
	}
	state.proposals[proposal.ID] = proposal
	state.snapshots[4] = &potso.StoredWeightSnapshot{
		Epoch: 4,
		Entries: []potso.StoredWeightEntry{
			{Address: voter, Stake: big.NewInt(10), WeightBps: 1200},
		},
	}
	state.lastEpoch = 4
	state.hasLastEpoch = true

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	emitter := &captureEmitter{}
	engine.SetEmitter(emitter)

	if err := engine.CastVote(1, voter, "yes"); err != nil {
		t.Fatalf("cast vote: %v", err)
	}

	stored, ok := state.votes[voteStorageKey(1, voter)]
	if !ok {
		t.Fatalf("expected stored vote")
	}
	if stored.Choice != VoteChoiceYes {
		t.Fatalf("unexpected choice: %s", stored.Choice)
	}
	if stored.PowerBps != 1200 {
		t.Fatalf("unexpected power: %d", stored.PowerBps)
	}
	if stored.Timestamp != now {
		t.Fatalf("unexpected timestamp: got %s want %s", stored.Timestamp, now)
	}

	if len(emitter.events) != 1 {
		t.Fatalf("expected event emission")
	}
	evt := emitter.events[0].(governanceEvent).Event()
	if evt.Type != EventTypeVoteCast {
		t.Fatalf("unexpected event type: %s", evt.Type)
	}
	if evt.Attributes["choice"] != "yes" {
		t.Fatalf("unexpected event choice: %s", evt.Attributes["choice"])
	}
	if evt.Attributes["powerBps"] != "1200" {
		t.Fatalf("unexpected event power: %s", evt.Attributes["powerBps"])
	}
}

func TestCastVoteOverwriteUpdatesBallot(t *testing.T) {
	var voter [20]byte
	voter[5] = 7
	now := time.Unix(1_700_000_700, 0).UTC()

	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:          2,
		Status:      ProposalStatusVotingPeriod,
		VotingStart: now.Add(-time.Minute),
		VotingEnd:   now.Add(time.Hour),
	}
	state.proposals[proposal.ID] = proposal
	state.snapshots[8] = &potso.StoredWeightSnapshot{
		Epoch:   8,
		Entries: []potso.StoredWeightEntry{{Address: voter, Stake: big.NewInt(5), WeightBps: 900}},
	}
	state.lastEpoch = 8
	state.hasLastEpoch = true

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })

	if err := engine.CastVote(2, voter, "abstain"); err != nil {
		t.Fatalf("initial vote: %v", err)
	}

	state.snapshots[8] = &potso.StoredWeightSnapshot{
		Epoch:   8,
		Entries: []potso.StoredWeightEntry{{Address: voter, Stake: big.NewInt(5), WeightBps: 1500}},
	}

	if err := engine.CastVote(2, voter, "no"); err != nil {
		t.Fatalf("overwrite vote: %v", err)
	}

	stored, ok := state.votes[voteStorageKey(2, voter)]
	if !ok {
		t.Fatalf("expected stored vote")
	}
	if stored.Choice != VoteChoiceNo {
		t.Fatalf("unexpected choice: %s", stored.Choice)
	}
	if stored.PowerBps != 1500 {
		t.Fatalf("unexpected power after overwrite: %d", stored.PowerBps)
	}
}

func TestCastVoteRejectsZeroPower(t *testing.T) {
	var voter [20]byte
	voter[2] = 1
	now := time.Unix(1_700_000_900, 0).UTC()

	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:          3,
		Status:      ProposalStatusVotingPeriod,
		VotingStart: now.Add(-time.Hour),
		VotingEnd:   now.Add(time.Hour),
	}
	state.proposals[proposal.ID] = proposal
	state.snapshots[10] = &potso.StoredWeightSnapshot{Epoch: 10}
	state.lastEpoch = 10
	state.hasLastEpoch = true

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })

	err := engine.CastVote(3, voter, "yes")
	if err == nil || !strings.Contains(err.Error(), "zero voting power") {
		t.Fatalf("expected zero power rejection, got %v", err)
	}
	if _, ok := state.votes[voteStorageKey(3, voter)]; ok {
		t.Fatalf("did not expect stored vote")
	}
}

func TestCastVoteRejectsOutsideWindow(t *testing.T) {
	var voter [20]byte
	voter[9] = 4
	now := time.Unix(1_700_001_100, 0).UTC()

	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:          4,
		Status:      ProposalStatusVotingPeriod,
		VotingStart: now.Add(-2 * time.Hour),
		VotingEnd:   now.Add(-time.Minute),
	}
	state.proposals[proposal.ID] = proposal
	state.snapshots[11] = &potso.StoredWeightSnapshot{
		Epoch:   11,
		Entries: []potso.StoredWeightEntry{{Address: voter, Stake: big.NewInt(1), WeightBps: 100}},
	}
	state.lastEpoch = 11
	state.hasLastEpoch = true

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })

	err := engine.CastVote(4, voter, "yes")
	if err == nil || !strings.Contains(err.Error(), "voting period closed") {
		t.Fatalf("expected voting closed error, got %v", err)
	}
	if _, ok := state.votes[voteStorageKey(4, voter)]; ok {
		t.Fatalf("did not expect stored vote")
	}
}

func TestFinalizeRejectsBeforeVotingEnd(t *testing.T) {
	var proposer [20]byte
	proposer[1] = 2
	now := time.Unix(1_700_002_000, 0).UTC()
	deposit := big.NewInt(500)

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposer[:])
	state.proposals[1] = &Proposal{
		ID:        1,
		Submitter: submitter,
		Status:    ProposalStatusVotingPeriod,
		Deposit:   new(big.Int).Set(deposit),
		VotingEnd: now.Add(time.Hour),
	}
	state.escrowBalances[string(proposer[:])] = new(big.Int).Set(deposit)

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{QuorumBps: 2000, PassThresholdBps: 5000})

	if _, _, err := engine.Finalize(1); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("expected voting in progress error, got %v", err)
	}
}

func TestFinalizeOutcomes(t *testing.T) {
	type voteCase struct {
		choice VoteChoice
		power  uint32
	}
	tests := []struct {
		name                 string
		policy               ProposalPolicy
		deposit              *big.Int
		votes                []voteCase
		expectedStatus       ProposalStatus
		expectedTurnout      uint64
		expectedYesRatio     uint64
		expectedYesPower     uint64
		expectedNoPower      uint64
		expectedAbstainPower uint64
		expectDepositReturn  bool
	}{
		{
			name: "passes at threshold",
			policy: ProposalPolicy{
				QuorumBps:        3000,
				PassThresholdBps: 5000,
			},
			deposit:              big.NewInt(1_000),
			votes:                []voteCase{{VoteChoiceYes, 1500}, {VoteChoiceNo, 1500}},
			expectedStatus:       ProposalStatusPassed,
			expectedTurnout:      3000,
			expectedYesRatio:     5000,
			expectedYesPower:     1500,
			expectedNoPower:      1500,
			expectedAbstainPower: 0,
			expectDepositReturn:  true,
		},
		{
			name: "fails quorum despite high yes",
			policy: ProposalPolicy{
				QuorumBps:        4000,
				PassThresholdBps: 5000,
			},
			deposit:              big.NewInt(250),
			votes:                []voteCase{{VoteChoiceYes, 2000}},
			expectedStatus:       ProposalStatusRejected,
			expectedTurnout:      2000,
			expectedYesRatio:     10_000,
			expectedYesPower:     2000,
			expectedNoPower:      0,
			expectedAbstainPower: 0,
			expectDepositReturn:  false,
		},
		{
			name: "all abstain",
			policy: ProposalPolicy{
				QuorumBps:        3000,
				PassThresholdBps: 6000,
			},
			votes:                []voteCase{{VoteChoiceAbstain, 4000}},
			expectedStatus:       ProposalStatusRejected,
			expectedTurnout:      4000,
			expectedYesRatio:     0,
			expectedYesPower:     0,
			expectedNoPower:      0,
			expectedAbstainPower: 4000,
			expectDepositReturn:  false,
		},
		{
			name: "no votes recorded",
			policy: ProposalPolicy{
				QuorumBps:        2000,
				PassThresholdBps: 5000,
			},
			deposit:              big.NewInt(250),
			expectedStatus:       ProposalStatusRejected,
			expectedTurnout:      0,
			expectedYesRatio:     0,
			expectedYesPower:     0,
			expectedNoPower:      0,
			expectedAbstainPower: 0,
			expectDepositReturn:  false,
		},
	}

	for idx, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()

			now := time.Unix(1_700_003_000+int64(idx), 0).UTC()
			var proposer [20]byte
			proposer[0] = byte(idx + 1)

			state := newMockGovernanceState(map[[20]byte]*types.Account{
				proposer: &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
			})
			submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposer[:])
			proposalID := uint64(100 + idx)
			state.proposals[proposalID] = &Proposal{
				ID:        proposalID,
				Submitter: submitter,
				Status:    ProposalStatusVotingPeriod,
				VotingEnd: now.Add(-time.Minute),
				Deposit: func() *big.Int {
					if tc.deposit == nil {
						return nil
					}
					return new(big.Int).Set(tc.deposit)
				}(),
			}
			if tc.deposit != nil {
				state.escrowBalances[string(proposer[:])] = new(big.Int).Set(tc.deposit)
			}

			for voteIdx, vote := range tc.votes {
				voterBytes := append(make([]byte, 19), byte(voteIdx+1))
				voter := crypto.MustNewAddress(crypto.NHBPrefix, voterBytes)
				if err := state.GovernancePutVote(&Vote{ProposalID: proposalID, Voter: voter, Choice: vote.choice, PowerBps: vote.power}); err != nil {
					t.Fatalf("store vote: %v", err)
				}
			}

			engine := NewEngine()
			engine.SetState(state)
			engine.SetNowFunc(func() time.Time { return now })
			engine.SetPolicy(tc.policy)
			emitter := &captureEmitter{}
			engine.SetEmitter(emitter)

			status, tally, err := engine.Finalize(proposalID)
			if err != nil {
				t.Fatalf("finalize: %v", err)
			}
			if status != tc.expectedStatus {
				t.Fatalf("unexpected status: got %s want %s", status.StatusString(), tc.expectedStatus.StatusString())
			}
			if tally == nil {
				t.Fatalf("expected tally")
			}
			if tally.TurnoutBps != tc.expectedTurnout {
				t.Fatalf("unexpected turnout: got %d want %d", tally.TurnoutBps, tc.expectedTurnout)
			}
			if tally.YesRatioBps != tc.expectedYesRatio {
				t.Fatalf("unexpected yes ratio: got %d want %d", tally.YesRatioBps, tc.expectedYesRatio)
			}
			if tally.YesPowerBps != tc.expectedYesPower {
				t.Fatalf("unexpected yes power: got %d want %d", tally.YesPowerBps, tc.expectedYesPower)
			}
			if tally.NoPowerBps != tc.expectedNoPower {
				t.Fatalf("unexpected no power: got %d want %d", tally.NoPowerBps, tc.expectedNoPower)
			}
			if tally.AbstainPowerBps != tc.expectedAbstainPower {
				t.Fatalf("unexpected abstain power: got %d want %d", tally.AbstainPowerBps, tc.expectedAbstainPower)
			}

			account, err := state.GetAccount(proposer[:])
			if err != nil {
				t.Fatalf("get account: %v", err)
			}
			escrow, err := state.GovernanceEscrowBalance(proposer[:])
			if err != nil {
				t.Fatalf("escrow balance: %v", err)
			}

			if tc.expectDepositReturn {
				if tc.deposit == nil {
					t.Fatalf("expected deposit but test case missing deposit")
				}
				if account.BalanceZNHB.Cmp(tc.deposit) != 0 {
					t.Fatalf("deposit not returned: got %s want %s", account.BalanceZNHB.String(), tc.deposit.String())
				}
				if escrow.Sign() != 0 {
					t.Fatalf("expected escrow cleared, got %s", escrow.String())
				}
			} else if tc.deposit != nil {
				if account.BalanceZNHB.Sign() != 0 {
					t.Fatalf("expected proposer balance to remain zero, got %s", account.BalanceZNHB.String())
				}
				if escrow.Cmp(tc.deposit) != 0 {
					t.Fatalf("expected escrow to retain deposit: got %s want %s", escrow.String(), tc.deposit.String())
				}
			}

			if len(emitter.events) != 1 {
				t.Fatalf("expected finalize event, got %d", len(emitter.events))
			}
			evt := emitter.events[0].(governanceEvent).Event()
			if evt.Type != EventTypeProposalFinalized {
				t.Fatalf("unexpected event type: %s", evt.Type)
			}
			if evt.Attributes["status"] != status.StatusString() {
				t.Fatalf("unexpected event status: %s", evt.Attributes["status"])
			}
			if evt.Attributes["turnoutBps"] != fmt.Sprintf("%d", tc.expectedTurnout) {
				t.Fatalf("unexpected event turnout: %s", evt.Attributes["turnoutBps"])
			}
		})
	}
}

func TestQueueExecutionMarksProposalAndIsIdempotent(t *testing.T) {
	now := time.Unix(1_700_006_000, 0).UTC()
	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:             7,
		Status:         ProposalStatusPassed,
		TimelockEnd:    now.Add(time.Hour),
		Target:         ProposalKindParamUpdate,
		ProposedChange: `{"fees.baseFee":5}`,
	}
	if err := state.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{AllowedParams: []string{"fees.baseFee"}})
	emitter := &captureEmitter{}
	engine.SetEmitter(emitter)

	if err := engine.QueueExecution(7); err != nil {
		t.Fatalf("queue execution: %v", err)
	}

	stored, ok, err := state.GovernanceGetProposal(7)
	if err != nil {
		t.Fatalf("reload proposal: %v", err)
	}
	if !ok {
		t.Fatalf("expected proposal persisted")
	}
	if !stored.Queued {
		t.Fatalf("expected proposal marked queued")
	}
	if err := engine.QueueExecution(7); err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Fatalf("expected already queued error, got %v", err)
	}

	storedAgain, ok, err := state.GovernanceGetProposal(7)
	if err != nil {
		t.Fatalf("reload proposal after retry: %v", err)
	}
	if !ok {
		t.Fatalf("expected proposal persisted after retry")
	}
	if !storedAgain.Queued {
		t.Fatalf("expected proposal to remain queued")
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected exactly one queued event, got %d", len(emitter.events))
	}
	evt, ok := emitter.events[0].(governanceEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", emitter.events[0])
	}
	payload := evt.Event()
	if payload.Type != EventTypeProposalQueued {
		t.Fatalf("unexpected event type: %s", payload.Type)
	}
	if payload.Attributes["id"] != "7" {
		t.Fatalf("unexpected queued id: %s", payload.Attributes["id"])
	}
}

func TestExecuteProposalAppliesParams(t *testing.T) {
	now := time.Unix(1_700_007_000, 0).UTC()
	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:             8,
		Status:         ProposalStatusPassed,
		TimelockEnd:    now.Add(-time.Second),
		Target:         ProposalKindParamUpdate,
		ProposedChange: `{"fees.baseFee":25,"potso.weights.AlphaStakeBps":7500,"staking.minimumValidatorStake":3500}`,
		Queued:         true,
	}
	if err := state.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{AllowedParams: []string{"fees.baseFee", "potso.weights.AlphaStakeBps", ParamKeyMinimumValidatorStake}})
	emitter := &captureEmitter{}
	engine.SetEmitter(emitter)

	if err := engine.Execute(8); err != nil {
		t.Fatalf("execute proposal: %v", err)
	}

	stored, ok, err := state.GovernanceGetProposal(8)
	if err != nil {
		t.Fatalf("reload proposal: %v", err)
	}
	if !ok {
		t.Fatalf("expected proposal persisted")
	}
	if stored.Status != ProposalStatusExecuted {
		t.Fatalf("expected executed status, got %s", stored.Status.StatusString())
	}
	value, ok := state.ParamStoreGet("fees.baseFee")
	if !ok {
		t.Fatalf("expected fees.baseFee updated")
	}
	if string(value) != "25" {
		t.Fatalf("unexpected base fee value: %s", string(value))
	}
	alpha, ok := state.ParamStoreGet("potso.weights.AlphaStakeBps")
	if !ok {
		t.Fatalf("expected alpha stake updated")
	}
	if string(alpha) != "7500" {
		t.Fatalf("unexpected alpha stake value: %s", string(alpha))
	}
	minStake, ok := state.ParamStoreGet(ParamKeyMinimumValidatorStake)
	if !ok {
		t.Fatalf("expected minimum validator stake updated")
	}
	if string(minStake) != "3500" {
		t.Fatalf("unexpected minimum validator stake value: %s", string(minStake))
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected one event, got %d", len(emitter.events))
	}
	evt, ok := emitter.events[0].(governanceEvent)
	if !ok {
		t.Fatalf("unexpected event type %T", emitter.events[0])
	}
	payload := evt.Event()
	if payload.Type != EventTypeProposalExecuted {
		t.Fatalf("unexpected event type: %s", payload.Type)
	}
	if payload.Attributes["status"] != ProposalStatusExecuted.StatusString() {
		t.Fatalf("unexpected status attribute: %s", payload.Attributes["status"])
	}

	if err := engine.Execute(8); err == nil || !strings.Contains(err.Error(), "already executed") {
		t.Fatalf("expected idempotency error, got %v", err)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected event count to remain one, got %d", len(emitter.events))
	}
}

func TestExecuteRespectsTimelock(t *testing.T) {
	now := time.Unix(1_700_008_000, 0).UTC()
	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:             9,
		Status:         ProposalStatusPassed,
		TimelockEnd:    now.Add(time.Hour),
		Target:         ProposalKindParamUpdate,
		ProposedChange: `{"fees.baseFee":10}`,
		Queued:         true,
	}
	if err := state.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{AllowedParams: []string{"fees.baseFee"}})

	if err := engine.Execute(9); err == nil || !strings.Contains(err.Error(), "timelock") {
		t.Fatalf("expected timelock error, got %v", err)
	}

	now = now.Add(2 * time.Hour)
	engine.SetNowFunc(func() time.Time { return now })
	if err := engine.Execute(9); err != nil {
		t.Fatalf("execute after timelock: %v", err)
	}
}

func TestExecuteSlashingPolicyProposal(t *testing.T) {
	var proposer [20]byte
	proposer[0] = 8
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     30,
		AllowedParams:       []string{"fees.baseFee"},
	})
	created := time.Unix(1_700_100_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return created })

	payload := `{"enabled":true,"maxPenaltyBps":400,"windowSeconds":600,"maxSlashWei":"2500","evidenceTtlSeconds":1200}`
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindSlashingPolicy, payload, big.NewInt(200))
	if err != nil {
		t.Fatalf("submit slashing policy: %v", err)
	}
	stored := state.proposals[proposalID]
	stored.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue slashing policy: %v", err)
	}
	engine.SetNowFunc(func() time.Time { return created.Add(2 * time.Hour) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute slashing policy: %v", err)
	}

	enabled, ok := state.ParamStoreGet(paramKeySlashingEnabled)
	if !ok || string(enabled) != "true" {
		t.Fatalf("expected slashing enabled, got %s", string(enabled))
	}
	maxPenalty, _ := state.ParamStoreGet(paramKeySlashingMaxPenaltyBps)
	if string(maxPenalty) != "400" {
		t.Fatalf("unexpected max penalty: %s", string(maxPenalty))
	}
	window, _ := state.ParamStoreGet(paramKeySlashingWindow)
	if string(window) != "600" {
		t.Fatalf("unexpected window: %s", string(window))
	}
	maxSlash, _ := state.ParamStoreGet(paramKeySlashingMaxSlashWei)
	if string(maxSlash) != "2500" {
		t.Fatalf("unexpected max slash: %s", string(maxSlash))
	}
	evidence, _ := state.ParamStoreGet(paramKeySlashingEvidenceTTL)
	if string(evidence) != "1200" {
		t.Fatalf("unexpected evidence ttl: %s", string(evidence))
	}

	if len(state.audit) != 3 {
		t.Fatalf("expected three audit entries, got %d", len(state.audit))
	}
	if state.audit[2].Event != AuditEventExecuted {
		t.Fatalf("unexpected final audit event: %s", state.audit[2].Event)
	}
}

func TestExecuteRoleAllowlistProposal(t *testing.T) {
	var proposer [20]byte
	proposer[9] = 3
	var revoke [20]byte
	revoke[1] = 2
	var grant [20]byte
	grant[2] = 4

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})
	state.roles["compliance"] = map[string]struct{}{string(revoke[:]): struct{}{}}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
		AllowedParams:       []string{"fees.baseFee"},
		AllowedRoles:        []string{"compliance"},
	})
	now := time.Unix(1_700_200_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := fmt.Sprintf(`{"grant":[{"role":"compliance","address":"%s"}],"revoke":[{"role":"compliance","address":"%s"}]}`,
		crypto.MustNewAddress(crypto.NHBPrefix, grant[:]).String(),
		crypto.MustNewAddress(crypto.NHBPrefix, revoke[:]).String(),
	)
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindRoleAllowlist, payload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit role allowlist: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue role allowlist: %v", err)
	}
	proposal = state.proposals[proposalID]
	proposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute role allowlist: %v", err)
	}

	grantBucket := state.roles["compliance"]
	if _, ok := grantBucket[string(grant[:])]; !ok {
		t.Fatalf("expected grant address in role set")
	}
	if _, ok := grantBucket[string(revoke[:])]; ok {
		t.Fatalf("expected revoke address removed")
	}
}

func TestExecuteTreasuryDirective(t *testing.T) {
	var proposer [20]byte
	proposer[8] = 1
	var treasury [20]byte
	treasury[19] = 7
	var recipient [20]byte
	recipient[0] = 9

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(500), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
		treasury: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 30,
		TimelockSeconds:     5,
		AllowedParams:       []string{"fees.baseFee"},
		TreasuryAllowList:   [][20]byte{treasury},
	})
	now := time.Unix(1_700_300_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := fmt.Sprintf(`{"source":"%s","transfers":[{"to":"%s","amountWei":"250"}]}`,
		crypto.MustNewAddress(crypto.NHBPrefix, treasury[:]).String(),
		crypto.MustNewAddress(crypto.NHBPrefix, recipient[:]).String(),
	)
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindTreasuryDirective, payload, big.NewInt(10))
	if err != nil {
		t.Fatalf("submit treasury directive: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue treasury directive: %v", err)
	}
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute treasury directive: %v", err)
	}

	treasuryAccount, _ := state.GetAccount(treasury[:])
	if treasuryAccount.BalanceZNHB.String() != "750" {
		t.Fatalf("unexpected treasury balance: %s", treasuryAccount.BalanceZNHB.String())
	}
	recipientAccount, _ := state.GetAccount(recipient[:])
	if recipientAccount.BalanceZNHB.String() != "250" {
		t.Fatalf("unexpected recipient balance: %s", recipientAccount.BalanceZNHB.String())
	}
}
