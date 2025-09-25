package governance

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
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

func TestProposeParamChangeRejectsUnknownParam(t *testing.T) {
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

	_, err := engine.ProposeParamChange(proposer, ProposalKindParamUpdate, `{"escrow.maxOpenDisputes":5}`, big.NewInt(200))
	if err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Fatalf("expected allow-list rejection, got %v", err)
	}
}

func TestProposeParamChangeRejectsLowDeposit(t *testing.T) {
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

	_, err := engine.ProposeParamChange(proposer, ProposalKindParamUpdate, `{"fees.baseFee":5}`, big.NewInt(200))
	if err == nil || !strings.Contains(err.Error(), "deposit below minimum") {
		t.Fatalf("expected deposit rejection, got %v", err)
	}
}

func TestProposeParamChangeLocksDepositAndEmitsEvent(t *testing.T) {
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
	proposalID, err := engine.ProposeParamChange(proposer, ProposalKindParamUpdate, payload, deposit)
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
