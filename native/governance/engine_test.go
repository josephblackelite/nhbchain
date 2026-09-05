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
	swapSigners    map[string][20]byte
	audit          []*AuditRecord
	rewardPool     *big.Int
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

func TestValidatorForParamStaking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		key     string
		payload json.RawMessage
		wantErr bool
	}{
		{name: "apr valid", key: ParamKeyStakingAprBps, payload: json.RawMessage("1250")},
		{name: "apr invalid", key: ParamKeyStakingAprBps, payload: json.RawMessage("20000"), wantErr: true},
		{name: "payout valid", key: ParamKeyStakingPayoutPeriodDays, payload: json.RawMessage("30")},
		{name: "payout zero", key: ParamKeyStakingPayoutPeriodDays, payload: json.RawMessage("0"), wantErr: true},
		{name: "unbonding valid", key: ParamKeyStakingUnbondingDays, payload: json.RawMessage("7")},
		{name: "unbonding zero", key: ParamKeyStakingUnbondingDays, payload: json.RawMessage("0"), wantErr: true},
		{name: "min stake valid", key: ParamKeyStakingMinStakeWei, payload: json.RawMessage("\"1000000000000000000\"")},
		{name: "min stake negative", key: ParamKeyStakingMinStakeWei, payload: json.RawMessage("-1"), wantErr: true},
		{name: "max emission valid", key: ParamKeyStakingMaxEmissionPerYearWei, payload: json.RawMessage("0")},
		{name: "max emission negative", key: ParamKeyStakingMaxEmissionPerYearWei, payload: json.RawMessage("-5"), wantErr: true},
		{name: "mint nhb emission valid", key: ParamKeyMintNHBMaxEmissionPerYearWei, payload: json.RawMessage("\"1000\"")},
		{name: "mint nhb emission negative", key: ParamKeyMintNHBMaxEmissionPerYearWei, payload: json.RawMessage("-1"), wantErr: true},
		{name: "mint znhb emission valid", key: ParamKeyMintZNHBMaxEmissionPerYearWei, payload: json.RawMessage("100")},
		{name: "mint znhb emission negative", key: ParamKeyMintZNHBMaxEmissionPerYearWei, payload: json.RawMessage("\"-5\""), wantErr: true},
		{name: "reward asset valid", key: ParamKeyStakingRewardAsset, payload: json.RawMessage("\"ZNHB\"")},
		{name: "reward asset empty", key: ParamKeyStakingRewardAsset, payload: json.RawMessage("\"   \""), wantErr: true},
		{name: "compound valid", key: ParamKeyStakingCompoundDefault, payload: json.RawMessage("true")},
		{name: "compound invalid", key: ParamKeyStakingCompoundDefault, payload: json.RawMessage("\"maybe\""), wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			validator := validatorForParam(tc.key)
			if validator == nil {
				t.Fatalf("expected validator for %s", tc.key)
			}
			err := validator(tc.payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validator error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

// TestValidatorForParamMarketFlatFeeWei is the regression test for a real
// finding: market.flatFeeWei (native/market's buyer-side flat fee, see
// governance.ParamKeyMarketFlatFeeWei's doc comment) had no case in
// validatorForParam at all, so validateParamPayload rejected every
// policy.paramUpdate proposal targeting it with "missing validation rule"
// even once the key was added to AllowedParams.
func TestValidatorForParamMarketFlatFeeWei(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload json.RawMessage
		wantErr bool
	}{
		{name: "valid wei amount", payload: json.RawMessage("\"250000000000000000\"")},
		{name: "zero is allowed", payload: json.RawMessage("0")},
		{name: "negative rejected", payload: json.RawMessage("-1"), wantErr: true},
		{name: "non-numeric rejected", payload: json.RawMessage("\"not-a-number\""), wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			validator := validatorForParam(ParamKeyMarketFlatFeeWei)
			if validator == nil {
				t.Fatalf("expected validator for %s", ParamKeyMarketFlatFeeWei)
			}
			err := validator(tc.payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validator error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

// TestValidatorForParamPaymasterTopUpFeeWei mirrors
// TestValidatorForParamMarketFlatFeeWei above for the new
// paymaster.topUpFeeWei key (governance.ParamKeyPaymasterTopUpFeeWei, see
// its doc comment and core/sponsorship.go's
// readGovernedPaymasterTopUpFeeWei) -- guards against the same class of bug
// the market fee key once hit: a key present in AllowedParams but missing
// its validatorForParam case, which would reject every
// policy.paramUpdate proposal targeting it.
func TestValidatorForParamPaymasterTopUpFeeWei(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload json.RawMessage
		wantErr bool
	}{
		{name: "valid wei amount", payload: json.RawMessage("\"100000000000000\"")},
		{name: "zero is allowed", payload: json.RawMessage("0")},
		{name: "negative rejected", payload: json.RawMessage("-1"), wantErr: true},
		{name: "non-numeric rejected", payload: json.RawMessage("\"not-a-number\""), wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			validator := validatorForParam(ParamKeyPaymasterTopUpFeeWei)
			if validator == nil {
				t.Fatalf("expected validator for %s", ParamKeyPaymasterTopUpFeeWei)
			}
			err := validator(tc.payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validator error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestValidateParamPayloadAcceptsLegacyWrapperCaseInsensitive(t *testing.T) {
	t.Parallel()

	engine := NewEngine()
	engine.SetPolicy(ProposalPolicy{
		AllowedParams: []string{ParamKeyStakingAprBps},
	})

	payload := `{"Update":{"staking.aprBps":1250}}`
	validated, err := engine.validateParamPayload(payload)
	if err != nil {
		t.Fatalf("validate legacy wrapped payload: %v", err)
	}
	raw, ok := validated[ParamKeyStakingAprBps]
	if !ok {
		t.Fatalf("expected validated payload for %s", ParamKeyStakingAprBps)
	}
	if strings.TrimSpace(string(raw)) != "1250" {
		t.Fatalf("unexpected normalized payload value: %s", string(raw))
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
		swapSigners:    make(map[string][20]byte),
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

func (m *mockGovernanceState) ZNHBRewardPoolBalance() (*big.Int, error) {
	if m.rewardPool == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(m.rewardPool), nil
}

func (m *mockGovernanceState) ZNHBSetRewardPoolBalance(v *big.Int) error {
	if v == nil {
		v = big.NewInt(0)
	}
	m.rewardPool = new(big.Int).Set(v)
	return nil
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

func (m *mockGovernanceState) SwapSetPriceSigner(provider string, addr [20]byte) error {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return fmt.Errorf("provider must not be empty")
	}
	m.swapSigners[trimmed] = addr
	return nil
}

func (m *mockGovernanceState) SwapClearPriceSigner(provider string) error {
	trimmed := strings.TrimSpace(provider)
	if trimmed == "" {
		return fmt.Errorf("provider must not be empty")
	}
	delete(m.swapSigners, trimmed)
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

// TestSubmitProposalAcceptsMarketFlatFeeWei is the end-to-end regression
// test for the same finding TestValidatorForParamMarketFlatFeeWei covers at
// the unit level: a policy.paramUpdate proposal setting
// governance.ParamKeyMarketFlatFeeWei (config.toml's [governance]
// AllowedParams and config/config.go's defaultAllowedGovernanceParams both
// list it) must actually be admitted by SubmitProposal end-to-end, not just
// pass AllowedParams membership before failing at validateParamPayload's
// "missing validation rule" check.
func TestSubmitProposalAcceptsMarketFlatFeeWei(t *testing.T) {
	var proposer [20]byte
	proposer[5] = 7
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(100),
		VotingPeriodSeconds: 600,
		TimelockSeconds:     120,
		AllowedParams:       []string{ParamKeyMarketFlatFeeWei},
	})
	engine.SetNowFunc(func() time.Time { return time.Unix(1700000000, 0).UTC() })
	engine.SetEmitter(&captureEmitter{})

	payload := fmt.Sprintf(`{%q:"250000000000000000"}`, ParamKeyMarketFlatFeeWei)
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, payload, big.NewInt(300))
	if err != nil {
		t.Fatalf("expected market.flatFeeWei param update proposal to validate successfully, got: %v", err)
	}

	stored := state.proposals[proposalID]
	if stored == nil {
		t.Fatalf("expected stored proposal")
	}
	if stored.ProposedChange != payload {
		t.Fatalf("unexpected payload: %s", stored.ProposedChange)
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

// TestFinalizeSweepsRejectedDepositToAdminWallet reproduces the 2026-09-05
// finding: proposal 2's 1000 ZNHB deposit was rejected for lack of quorum
// and then sat permanently locked in its submitter's own escrow ledger
// entry, unreachable by anyone -- GovernanceEscrowLock (SubmitProposal) has
// exactly one matching unlock call site, the Passed-branch refund, so a
// rejected deposit was never swept anywhere. This proves Finalize now
// routes it to the configured admin/treasury wallet instead, crediting the
// Reward Pool in lockstep (the same fix CheckZNHBSupplyInvariant's
// transfer-fee incident needed) so the invariant stays satisfied.
func TestFinalizeSweepsRejectedDepositToAdminWallet(t *testing.T) {
	now := time.Unix(1_700_007_000, 0).UTC()

	var proposer [20]byte
	proposer[19] = 0x42
	var admin [20]byte
	admin[19] = 0x99

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: {BalanceZNHB: big.NewInt(0)},
		admin:    {BalanceZNHB: big.NewInt(5_000)},
	})
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposer[:])
	deposit := big.NewInt(1_000)
	const proposalID = 200
	state.proposals[proposalID] = &Proposal{
		ID:        proposalID,
		Submitter: submitter,
		Status:    ProposalStatusVotingPeriod,
		VotingEnd: now.Add(-time.Minute),
		Deposit:   new(big.Int).Set(deposit),
	}
	state.escrowBalances[string(proposer[:])] = new(big.Int).Set(deposit)
	if err := state.ZNHBSetRewardPoolBalance(big.NewInt(2_000)); err != nil {
		t.Fatalf("seed reward pool: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{QuorumBps: 2000, PassThresholdBps: 5000})
	engine.SetAdminWallet(admin, true)

	status, _, err := engine.Finalize(proposalID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if status != ProposalStatusRejected {
		t.Fatalf("expected rejected (no votes cast), got %s", status.StatusString())
	}

	proposerAccount, err := state.GetAccount(proposer[:])
	if err != nil {
		t.Fatalf("load proposer: %v", err)
	}
	if proposerAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("proposer should not be refunded a rejected deposit, got %s", proposerAccount.BalanceZNHB)
	}
	escrow, err := state.GovernanceEscrowBalance(proposer[:])
	if err != nil {
		t.Fatalf("escrow balance: %v", err)
	}
	if escrow.Sign() != 0 {
		t.Fatalf("expected proposer's escrow cleared after sweep, got %s", escrow)
	}

	adminAccount, err := state.GetAccount(admin[:])
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if adminAccount.BalanceZNHB.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("expected admin wallet credited the swept deposit (5000+1000), got %s", adminAccount.BalanceZNHB)
	}

	rewardPool, err := state.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("reward pool balance: %v", err)
	}
	if rewardPool.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("expected reward pool credited in lockstep (2000+1000) to keep the supply invariant satisfied, got %s", rewardPool)
	}

	proposal, ok, err := state.GovernanceGetProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("reload proposal: ok=%v err=%v", ok, err)
	}
	if proposal.Deposit == nil || proposal.Deposit.Sign() != 0 {
		t.Fatalf("expected proposal.Deposit zeroed after sweep, got %v", proposal.Deposit)
	}
}

// TestFinalizeSweepsOwnRejectedDepositWithoutDoubleCountingRewardPool covers
// the case where the admin wallet itself is the proposer whose deposit gets
// rejected: adminZNHBOwned (core/state_transition.go) already counts this
// exact amount via the admin wallet's own GovernanceEscrowBalance (see its
// doc comment, added after the 2026-08-26 incident), so crediting the
// Reward Pool here too would double-count it and immediately break the
// invariant -- the sweep must be a pure escrow -> balance reclassification
// in this one case.
func TestFinalizeSweepsOwnRejectedDepositWithoutDoubleCountingRewardPool(t *testing.T) {
	now := time.Unix(1_700_007_500, 0).UTC()

	var admin [20]byte
	admin[19] = 0x99

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		admin: {BalanceZNHB: big.NewInt(5_000)},
	})
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, admin[:])
	deposit := big.NewInt(1_000)
	const proposalID = 201
	state.proposals[proposalID] = &Proposal{
		ID:        proposalID,
		Submitter: submitter,
		Status:    ProposalStatusVotingPeriod,
		VotingEnd: now.Add(-time.Minute),
		Deposit:   new(big.Int).Set(deposit),
	}
	state.escrowBalances[string(admin[:])] = new(big.Int).Set(deposit)
	if err := state.ZNHBSetRewardPoolBalance(big.NewInt(2_000)); err != nil {
		t.Fatalf("seed reward pool: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{QuorumBps: 2000, PassThresholdBps: 5000})
	engine.SetAdminWallet(admin, true)

	status, _, err := engine.Finalize(proposalID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if status != ProposalStatusRejected {
		t.Fatalf("expected rejected (no votes cast), got %s", status.StatusString())
	}

	adminAccount, err := state.GetAccount(admin[:])
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if adminAccount.BalanceZNHB.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("expected admin wallet's own deposit reclassified back to balance (5000+1000), got %s", adminAccount.BalanceZNHB)
	}

	rewardPool, err := state.ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("reward pool balance: %v", err)
	}
	if rewardPool.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("reward pool must NOT change when the admin wallet forfeits its own deposit (would double-count), got %s", rewardPool)
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

// TestRoleAllowlistProposalRejectsMinterZNHBGrant is the governance-layer
// defense-in-depth companion to core/state_transition.go's unconditional
// applyMintTransaction rejection of ZNHB mints: even if an operator's
// AllowedRoles config were misconfigured to include "MINTER_ZNHB" (set
// deliberately here to prove that), a role-allowlist proposal granting it
// must still be rejected at submission. This does not itself prevent ZNHB
// inflation -- applyMintTransaction's unconditional rejection is what does
// that, and would still block the mint even if this role were somehow held
// -- it only stops governance from misleadingly appearing to authorize a
// mint path that can never actually be exercised.
func TestRoleAllowlistProposalRejectsMinterZNHBGrant(t *testing.T) {
	var proposer [20]byte
	proposer[9] = 7
	var grant [20]byte
	grant[2] = 9

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	// Deliberately include MINTER_ZNHB in the operator-configured
	// allow-list to prove the rejection is unconditional and structural --
	// not merely "MINTER_ZNHB happens not to be configured today".
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
		AllowedParams:       []string{"fees.baseFee"},
		AllowedRoles:        []string{"MINTER_ZNHB", "compliance"},
	})
	now := time.Unix(1_700_400_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := fmt.Sprintf(`{"grant":[{"role":"MINTER_ZNHB","address":"%s"}]}`,
		crypto.MustNewAddress(crypto.NHBPrefix, grant[:]).String(),
	)
	if _, err := engine.SubmitProposal(proposer, ProposalKindRoleAllowlist, payload, big.NewInt(75)); err == nil {
		t.Fatalf("expected role allowlist proposal granting MINTER_ZNHB to be rejected")
	} else if !strings.Contains(err.Error(), "MINTER_ZNHB") {
		t.Fatalf("expected error to mention MINTER_ZNHB, got %v", err)
	}

	// A lower-case grant submission must be caught the same way (EqualFold).
	lowerCasePayload := fmt.Sprintf(`{"grant":[{"role":"minter_znhb","address":"%s"}]}`,
		crypto.MustNewAddress(crypto.NHBPrefix, grant[:]).String(),
	)
	if _, err := engine.SubmitProposal(proposer, ProposalKindRoleAllowlist, lowerCasePayload, big.NewInt(75)); err == nil {
		t.Fatalf("expected lower-case minter_znhb grant to be rejected")
	} else if !strings.Contains(err.Error(), "cannot be granted") {
		t.Fatalf("expected error to mention the grant is disallowed, got %v", err)
	}

	// Revoking the role (rather than granting it) must remain unaffected --
	// there's no reason to block removing a role, only granting it. Uses
	// the exact allow-listed casing so the pre-existing case-sensitive
	// allow-list membership check (unrelated to this fix) doesn't also
	// reject it for a different reason.
	revokeOnlyPayload := fmt.Sprintf(`{"revoke":[{"role":"MINTER_ZNHB","address":"%s"}]}`,
		crypto.MustNewAddress(crypto.NHBPrefix, grant[:]).String(),
	)
	if _, err := engine.SubmitProposal(proposer, ProposalKindRoleAllowlist, revokeOnlyPayload, big.NewInt(75)); err != nil {
		t.Fatalf("expected revoke-only MINTER_ZNHB proposal to be allowed, got %v", err)
	}
}

// TestExecuteSwapPriceSignerProposal drives the full proposal lifecycle
// (submit -> pass -> queue -> execute) for ProposalKindSwapPriceSignerUpdate
// and asserts the registered signer address lands in state via exactly the
// same nhbstate.Manager.SwapSetPriceSigner call native/swap's
// PriceProofEngine.Verify reads at TxTypeSwapVoucherMint execution time --
// proving governance is a viable, non-bespoke way to provision Gap 2's
// missing price signer.
func TestExecuteSwapPriceSignerProposal(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 5
	var signer [20]byte
	signer[0] = 0xAB
	signer[19] = 0xCD

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
		AllowedParams:       []string{"fees.baseFee"},
	})
	now := time.Unix(1_700_300_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := fmt.Sprintf(`{"provider":"nowpayments","signerAddress":"%s","memo":"initial oracle signer"}`,
		crypto.MustNewAddress(crypto.NHBPrefix, signer[:]).String(),
	)
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindSwapPriceSignerUpdate, payload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit swap price signer proposal: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue swap price signer proposal: %v", err)
	}
	proposal = state.proposals[proposalID]
	proposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute swap price signer proposal: %v", err)
	}

	got, ok := state.swapSigners["nowpayments"]
	if !ok {
		t.Fatalf("expected a registered signer for provider nowpayments")
	}
	if got != signer {
		t.Fatalf("unexpected registered signer: got %x want %x", got, signer)
	}

	if len(state.audit) != 3 {
		t.Fatalf("expected three audit entries, got %d", len(state.audit))
	}
	if state.audit[2].Event != AuditEventExecuted {
		t.Fatalf("unexpected final audit event: %s", state.audit[2].Event)
	}

	// Now revoke it via a second proposal and confirm the signer is removed
	// entirely (not left registered to some sentinel value).
	revokePayload := `{"provider":"nowpayments","revoke":true}`
	revokeID, err := engine.SubmitProposal(proposer, ProposalKindSwapPriceSignerUpdate, revokePayload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit revoke proposal: %v", err)
	}
	revokeProposal := state.proposals[revokeID]
	revokeProposal.Status = ProposalStatusPassed
	if err := engine.QueueExecution(revokeID); err != nil {
		t.Fatalf("queue revoke proposal: %v", err)
	}
	revokeProposal = state.proposals[revokeID]
	revokeProposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(2 * time.Minute) })
	if err := engine.Execute(revokeID); err != nil {
		t.Fatalf("execute revoke proposal: %v", err)
	}
	if _, ok := state.swapSigners["nowpayments"]; ok {
		t.Fatalf("expected signer to be removed after revoke proposal")
	}
}

func TestExecuteBuybackParamsProposal(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 7

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	now := time.Unix(1_700_300_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := `{"feeShareBps":3000,"discountBps":750,"safetyMarginBps":250,"memo":"raise fee share"}`
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindBuybackParams, payload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit buyback params proposal: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue buyback params proposal: %v", err)
	}
	proposal = state.proposals[proposalID]
	proposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute buyback params proposal: %v", err)
	}

	assertParam := func(key string, want string) {
		t.Helper()
		got, ok := state.params[key]
		if !ok {
			t.Fatalf("expected param %s to be set", key)
		}
		if string(got) != want {
			t.Fatalf("param %s = %s, want %s", key, got, want)
		}
	}
	assertParam(ParamKeyBuybackFeeShareBps, "3000")
	assertParam(ParamKeyBuybackDiscountBps, "750")
	assertParam(ParamKeyBuybackSafetyMarginBps, "250")
}

func TestSubmitBuybackParamsProposalRejectsOutOfRangeBps(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 9

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	engine.SetNowFunc(func() time.Time { return time.Unix(1_700_300_000, 0).UTC() })

	payload := `{"feeShareBps":10001,"discountBps":0,"safetyMarginBps":0}`
	if _, err := engine.SubmitProposal(proposer, ProposalKindBuybackParams, payload, big.NewInt(75)); err == nil {
		t.Fatalf("expected submission to reject feeShareBps > 10000")
	}
}

// TestExecuteSwapRiskParamsProposal mirrors TestExecuteBuybackParamsProposal
// exactly: submit -> vote (implicit via forcing Status to Passed, matching
// the buyback test's own shortcut) -> queue -> timelock elapses -> execute
// -> confirm every one of the four redeem-side wei-denominated param-store
// keys holds the proposal's values. There is no mint-side equivalent -- see
// ProposalKindSwapRiskParams's doc comment for why.
func TestExecuteSwapRiskParamsProposal(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 11

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	now := time.Unix(1_700_300_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := `{` +
		`"redeemPerTxMinWei":"2","redeemPerTxMaxWei":"2000","redeemPerAddressDailyCapWei":"8000","redeemPerAddressMonthlyCapWei":"80000",` +
		`"memo":"tighten caps"}`
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindSwapRiskParams, payload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit swap risk params proposal: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue swap risk params proposal: %v", err)
	}
	proposal = state.proposals[proposalID]
	proposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute swap risk params proposal: %v", err)
	}

	assertParam := func(key string, want string) {
		t.Helper()
		got, ok := state.params[key]
		if !ok {
			t.Fatalf("expected param %s to be set", key)
		}
		if string(got) != want {
			t.Fatalf("param %s = %s, want %s", key, got, want)
		}
	}
	assertParam(ParamKeySwapRiskRedeemPerTxMinWei, "2")
	assertParam(ParamKeySwapRiskRedeemPerTxMaxWei, "2000")
	assertParam(ParamKeySwapRiskRedeemPerAddressDailyCapWei, "8000")
	assertParam(ParamKeySwapRiskRedeemPerAddressMonthlyCapWei, "80000")
}

// TestSubmitSwapRiskParamsProposalRejectsBadOrdering covers
// parseSwapRiskParamsPayload's sanity checks: a per-tx max that exceeds its
// own daily cap must be rejected at submission time, before any deposit is
// locked or proposal created.
func TestSubmitSwapRiskParamsProposalRejectsBadOrdering(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 12

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	engine.SetNowFunc(func() time.Time { return time.Unix(1_700_300_000, 0).UTC() })

	payload := `{` +
		`"redeemPerTxMinWei":"0","redeemPerTxMaxWei":"10000","redeemPerAddressDailyCapWei":"5000","redeemPerAddressMonthlyCapWei":"50000"` +
		`}`
	if _, err := engine.SubmitProposal(proposer, ProposalKindSwapRiskParams, payload, big.NewInt(75)); err == nil {
		t.Fatalf("expected submission to reject redeemPerTxMaxWei (10000) exceeding redeemPerAddressDailyCapWei (5000)")
	}
}

// TestSwapPriceSignerProposalDeterministicAcrossValidators simulates two
// independent validators (two separate mockGovernanceState instances seeded
// identically) executing the identical queued proposal payload, and asserts
// both end up with byte-identical SwapSetPriceSigner state. This is the
// property required for the mechanism to be safe to use in production:
// every validator that replays the same gov_execute call must converge on
// the same result, deterministically, from the same inputs.
func TestSwapPriceSignerProposalDeterministicAcrossValidators(t *testing.T) {
	var proposer [20]byte
	proposer[7] = 9
	var signer [20]byte
	signer[5] = 0x11
	signer[15] = 0x22

	newSeededEngine := func() (*Engine, *mockGovernanceState) {
		state := newMockGovernanceState(map[[20]byte]*types.Account{
			proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
		})
		engine := NewEngine()
		engine.SetState(state)
		engine.SetPolicy(ProposalPolicy{
			MinDepositWei:       big.NewInt(0),
			VotingPeriodSeconds: 60,
			TimelockSeconds:     0,
			AllowedParams:       []string{"fees.baseFee"},
		})
		now := time.Unix(1_700_400_000, 0).UTC()
		engine.SetNowFunc(func() time.Time { return now })
		return engine, state
	}

	payload := fmt.Sprintf(`{"provider":"otc-gateway","signerAddress":"%s"}`,
		crypto.MustNewAddress(crypto.NHBPrefix, signer[:]).String(),
	)

	engineA, stateA := newSeededEngine()
	idA, err := engineA.SubmitProposal(proposer, ProposalKindSwapPriceSignerUpdate, payload, big.NewInt(0))
	if err != nil {
		t.Fatalf("validator A submit: %v", err)
	}
	stateA.proposals[idA].Status = ProposalStatusPassed
	if err := engineA.QueueExecution(idA); err != nil {
		t.Fatalf("validator A queue: %v", err)
	}
	stateA.proposals[idA].TimelockEnd = time.Time{}
	if err := engineA.Execute(idA); err != nil {
		t.Fatalf("validator A execute: %v", err)
	}

	engineB, stateB := newSeededEngine()
	idB, err := engineB.SubmitProposal(proposer, ProposalKindSwapPriceSignerUpdate, payload, big.NewInt(0))
	if err != nil {
		t.Fatalf("validator B submit: %v", err)
	}
	stateB.proposals[idB].Status = ProposalStatusPassed
	if err := engineB.QueueExecution(idB); err != nil {
		t.Fatalf("validator B queue: %v", err)
	}
	stateB.proposals[idB].TimelockEnd = time.Time{}
	if err := engineB.Execute(idB); err != nil {
		t.Fatalf("validator B execute: %v", err)
	}

	gotA, okA := stateA.swapSigners["otc-gateway"]
	gotB, okB := stateB.swapSigners["otc-gateway"]
	if !okA || !okB {
		t.Fatalf("expected both validators to register a signer: okA=%v okB=%v", okA, okB)
	}
	if gotA != gotB {
		t.Fatalf("validators diverged: A=%x B=%x", gotA, gotB)
	}
	if gotA != signer {
		t.Fatalf("unexpected signer: got %x want %x", gotA, signer)
	}
}

func TestParseSwapPriceSignerPayloadRejectsZeroAddress(t *testing.T) {
	var zero [20]byte
	payload := fmt.Sprintf(`{"provider":"nowpayments","signerAddress":"%s"}`,
		crypto.MustNewAddress(crypto.NHBPrefix, zero[:]).String(),
	)
	if _, err := parseSwapPriceSignerPayload(payload); err == nil {
		t.Fatalf("expected error for zero signer address")
	}
}

func TestParseSwapPriceSignerPayloadRequiresProvider(t *testing.T) {
	if _, err := parseSwapPriceSignerPayload(`{"signerAddress":""}`); err == nil {
		t.Fatalf("expected error for missing provider")
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

// TestExecuteLendingRateScheduleProposal mirrors
// TestExecuteSwapRiskParamsProposal: submit -> force Passed -> queue ->
// timelock elapses -> execute -> confirm the single
// lending.fixedTerm.rateSchedule param-store key holds the proposal's
// canonical JSON schedule.
func TestExecuteLendingRateScheduleProposal(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 20

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	now := time.Unix(1_700_300_000, 0).UTC()
	engine.SetNowFunc(func() time.Time { return now })

	payload := `{"schedule":[{"tenureDays":30,"rateBps":1200},{"tenureDays":90,"rateBps":1600}],"memo":"bump rates"}`
	proposalID, err := engine.SubmitProposal(proposer, ProposalKindLendingRateSchedule, payload, big.NewInt(75))
	if err != nil {
		t.Fatalf("submit lending rate schedule proposal: %v", err)
	}
	proposal := state.proposals[proposalID]
	proposal.Status = ProposalStatusPassed

	if err := engine.QueueExecution(proposalID); err != nil {
		t.Fatalf("queue lending rate schedule proposal: %v", err)
	}
	proposal = state.proposals[proposalID]
	proposal.TimelockEnd = now.Add(-time.Second)
	engine.SetNowFunc(func() time.Time { return now.Add(time.Minute) })
	if err := engine.Execute(proposalID); err != nil {
		t.Fatalf("execute lending rate schedule proposal: %v", err)
	}

	got, ok := state.params[ParamKeyLendingFixedTermRateSchedule]
	if !ok {
		t.Fatalf("expected param %s to be set", ParamKeyLendingFixedTermRateSchedule)
	}
	var stored LendingRateSchedulePayload
	if err := json.Unmarshal(got, &stored); err != nil {
		t.Fatalf("decode stored schedule: %v", err)
	}
	if len(stored.Schedule) != 2 {
		t.Fatalf("expected 2 stored tenure entries, got %d", len(stored.Schedule))
	}
	rates := map[uint64]uint64{}
	for _, entry := range stored.Schedule {
		rates[entry.TenureDays] = entry.RateBps
	}
	if rates[30] != 1200 || rates[90] != 1600 {
		t.Fatalf("unexpected stored schedule: %+v", rates)
	}
}

// TestSubmitLendingRateScheduleProposalRejectsInvalidPayload covers
// parseLendingRateSchedulePayload's validation branches: an empty schedule,
// a duplicate tenureDays, a zero rateBps, and an over-max entry count must
// all be rejected at submission time, before any deposit is locked or
// proposal created.
func TestSubmitLendingRateScheduleProposalRejectsInvalidPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{name: "empty schedule", payload: `{"schedule":[]}`},
		{name: "duplicate tenure", payload: `{"schedule":[{"tenureDays":30,"rateBps":1200},{"tenureDays":30,"rateBps":1300}]}`},
		{name: "zero tenureDays", payload: `{"schedule":[{"tenureDays":0,"rateBps":1200}]}`},
		{name: "zero rateBps", payload: `{"schedule":[{"tenureDays":30,"rateBps":0}]}`},
		{name: "oversized tenureDays", payload: `{"schedule":[{"tenureDays":3651,"rateBps":1200}]}`},
		{name: "oversized rateBps", payload: `{"schedule":[{"tenureDays":30,"rateBps":10001}]}`},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var proposer [20]byte
			proposer[3] = byte(21 + i)

			state := newMockGovernanceState(map[[20]byte]*types.Account{
				proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
			})

			engine := NewEngine()
			engine.SetState(state)
			engine.SetPolicy(ProposalPolicy{
				MinDepositWei:       big.NewInt(50),
				VotingPeriodSeconds: 60,
				TimelockSeconds:     10,
			})
			engine.SetNowFunc(func() time.Time { return time.Unix(1_700_300_000, 0).UTC() })

			if _, err := engine.SubmitProposal(proposer, ProposalKindLendingRateSchedule, tc.payload, big.NewInt(75)); err == nil {
				t.Fatalf("expected submission to reject payload: %s", tc.payload)
			}
		})
	}
}

// TestSubmitLendingRateScheduleProposalRejectsOverMaxEntries covers the
// maxLendingRateScheduleEntries bound with a schedule one entry over the
// limit -- kept separate from the table above since it needs a generated
// payload rather than a literal.
func TestSubmitLendingRateScheduleProposalRejectsOverMaxEntries(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 30

	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})

	engine := NewEngine()
	engine.SetState(state)
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	engine.SetNowFunc(func() time.Time { return time.Unix(1_700_300_000, 0).UTC() })

	entries := make([]string, 0, maxLendingRateScheduleEntries+1)
	for i := 0; i < maxLendingRateScheduleEntries+1; i++ {
		entries = append(entries, fmt.Sprintf(`{"tenureDays":%d,"rateBps":100}`, i+1))
	}
	payload := `{"schedule":[` + strings.Join(entries, ",") + `]}`

	if _, err := engine.SubmitProposal(proposer, ProposalKindLendingRateSchedule, payload, big.NewInt(75)); err == nil {
		t.Fatalf("expected submission to reject a schedule exceeding %d entries", maxLendingRateScheduleEntries)
	}
}
