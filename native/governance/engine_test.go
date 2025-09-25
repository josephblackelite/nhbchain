package governance

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

type mockGovernanceState struct {
	accounts       map[string]*types.Account
	escrowBalances map[string]*big.Int
	proposals      map[uint64]*Proposal
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
