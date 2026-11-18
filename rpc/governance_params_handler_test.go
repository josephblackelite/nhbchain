package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"nhbchain/core/state"
	"nhbchain/native/governance"
	"nhbchain/native/potso"
)

// gov_params: the voting rules this node applies to a new governance proposal
// -- voting period, timelock, minimum deposit, quorum, pass threshold, the
// parameter and role allow-lists -- and the engagement total of the weight
// snapshot that voting power is drawn from. Anonymous and read-only. The rules
// are read from the node's own configuration, not from chain state: another
// validator can hold different ones.

type govEngagementResponse struct {
	Available       bool   `json:"available"`
	Epoch           uint64 `json:"epoch"`
	TotalEngagement uint64 `json:"totalEngagement"`
	TotalStakeWei   string `json:"totalStakeWei"`
	EligibleVoters  uint64 `json:"eligibleVoters"`
}

type govParamsResponse struct {
	VotingPeriodSeconds uint64                `json:"votingPeriodSeconds"`
	TimelockSeconds     uint64                `json:"timelockSeconds"`
	MinDepositWei       string                `json:"minDepositWei"`
	QuorumBps           uint64                `json:"quorumBps"`
	PassThresholdBps    uint64                `json:"passThresholdBps"`
	AllowedParams       []string              `json:"allowedParams"`
	AllowedRoles        []string              `json:"allowedRoles"`
	Engagement          govEngagementResponse `json:"engagement"`
}

func govParamsPolicy() governance.ProposalPolicy {
	minDeposit, _ := new(big.Int).SetString("1000000000000000000000", 10)
	return governance.ProposalPolicy{
		MinDepositWei:       minDeposit,
		VotingPeriodSeconds: 604800,
		TimelockSeconds:     172800,
		QuorumBps:           2000,
		PassThresholdBps:    5000,
		AllowedParams:       []string{"fees.baseFee", "staking.aprBps", "market.flatFeeWei"},
		AllowedRoles:        []string{"MINTER_NHB", "ROLE_LOYALTY_ADMIN"},
	}
}

func callGovParams(t *testing.T, env *testEnv, params ...json.RawMessage) (govParamsResponse, *RPCError) {
	t.Helper()
	req := &RPCRequest{ID: 1, Params: params}
	recorder := httptest.NewRecorder()
	env.server.handleGovernanceParams(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		return govParamsResponse{}, rpcErr
	}
	var resp govParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp, nil
}

func seedGovernanceWeightSnapshot(t *testing.T, env *testEnv, epoch uint64, totalEngagement uint64, totalStake int64, entries int, lastProcessed bool) {
	t.Helper()
	snapshot := &potso.StoredWeightSnapshot{
		Epoch:           epoch,
		TotalStake:      big.NewInt(totalStake),
		TotalEngagement: totalEngagement,
	}
	for i := 0; i < entries; i++ {
		snapshot.Entries = append(snapshot.Entries, potso.StoredWeightEntry{Address: testAddr(byte(i + 1)), Stake: big.NewInt(1), WeightBps: 100})
	}
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.SetSnapshotPotsoWeights(epoch, snapshot); err != nil {
			return err
		}
		if lastProcessed {
			return manager.PotsoRewardsSetLastProcessedEpoch(epoch)
		}
		return nil
	}); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
}

func TestHandleGovernanceParamsReportsTheLivePolicy(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())

	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.VotingPeriodSeconds != 604800 || resp.TimelockSeconds != 172800 {
		t.Fatalf("windows = %d/%d, want 604800/172800", resp.VotingPeriodSeconds, resp.TimelockSeconds)
	}
	if resp.MinDepositWei != "1000000000000000000000" {
		t.Fatalf("minDepositWei = %q, want 1000e18", resp.MinDepositWei)
	}
	if resp.QuorumBps != 2000 || resp.PassThresholdBps != 5000 {
		t.Fatalf("quorum/threshold = %d/%d, want 2000/5000", resp.QuorumBps, resp.PassThresholdBps)
	}
	if want := []string{"fees.baseFee", "staking.aprBps", "market.flatFeeWei"}; !reflect.DeepEqual(resp.AllowedParams, want) {
		t.Fatalf("allowedParams = %v, want %v (in the node's order)", resp.AllowedParams, want)
	}
	if want := []string{"MINTER_NHB", "ROLE_LOYALTY_ADMIN"}; !reflect.DeepEqual(resp.AllowedRoles, want) {
		t.Fatalf("allowedRoles = %v, want %v", resp.AllowedRoles, want)
	}
}

func TestHandleGovernanceParamsFollowsAPolicyChangeAtOnce(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	if resp, _ := callGovParams(t, env); resp.VotingPeriodSeconds != 604800 {
		t.Fatalf("setup: voting period = %d", resp.VotingPeriodSeconds)
	}

	changed := govParamsPolicy()
	changed.VotingPeriodSeconds = 600
	changed.TimelockSeconds = 120
	changed.QuorumBps = 3000
	changed.PassThresholdBps = 6000
	changed.MinDepositWei = big.NewInt(5)
	changed.AllowedParams = []string{"fees.baseFee"}
	env.node.SetGovernancePolicy(changed)

	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.VotingPeriodSeconds != 600 || resp.TimelockSeconds != 120 || resp.QuorumBps != 3000 || resp.PassThresholdBps != 6000 || resp.MinDepositWei != "5" {
		t.Fatalf("response did not follow the policy change: %+v", resp)
	}
	if !reflect.DeepEqual(resp.AllowedParams, []string{"fees.baseFee"}) {
		t.Fatalf("allowedParams = %v", resp.AllowedParams)
	}
}

func TestHandleGovernanceParamsReportsAZeroDepositWhenNoneIsSet(t *testing.T) {
	env := newTestEnv(t)
	policy := govParamsPolicy()
	policy.MinDepositWei = nil
	env.node.SetGovernancePolicy(policy)
	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.MinDepositWei != "0" {
		t.Fatalf("minDepositWei = %q, want \"0\" for a policy with no minimum", resp.MinDepositWei)
	}
}

func TestHandleGovernanceParamsListsAreCleanAndNeverNull(t *testing.T) {
	env := newTestEnv(t)
	policy := govParamsPolicy()
	policy.AllowedParams = []string{" fees.baseFee ", "", "fees.baseFee", "   ", "staking.aprBps"}
	policy.AllowedRoles = nil
	env.node.SetGovernancePolicy(policy)

	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handleGovernanceParams(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(fields["allowedParams"]); got != `["fees.baseFee","staking.aprBps"]` {
		t.Fatalf("allowedParams = %s, want the trimmed names without blanks or repeats", got)
	}
	// an empty list is [], not null, so a client never has to tell them apart
	if got := string(fields["allowedRoles"]); got != `[]` {
		t.Fatalf("allowedRoles = %s, want []", got)
	}
}

func TestHandleGovernanceParamsSaysSoWhenThereIsNoWeightSnapshot(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.Engagement.Available || resp.Engagement.TotalEngagement != 0 || resp.Engagement.Epoch != 0 || resp.Engagement.EligibleVoters != 0 {
		t.Fatalf("engagement = %+v, want unavailable and empty", resp.Engagement)
	}
}

func TestHandleGovernanceParamsReportsTheEngagementTotalOfTheLastProcessedEpoch(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	// an older epoch and a newer one that has not been processed yet must not be used
	seedGovernanceWeightSnapshot(t, env, 3, 111, 1000, 1, false)
	seedGovernanceWeightSnapshot(t, env, 4, 12345, 5000, 2, true)
	seedGovernanceWeightSnapshot(t, env, 5, 999999, 7000, 3, false)

	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	want := govEngagementResponse{Available: true, Epoch: 4, TotalEngagement: 12345, TotalStakeWei: "5000", EligibleVoters: 2}
	if resp.Engagement != want {
		t.Fatalf("engagement = %+v, want %+v", resp.Engagement, want)
	}
}

// A reward epoch can be marked processed with no weight snapshot stored for it;
// the rules are still reported and the engagement is unavailable, not an error.
func TestHandleGovernanceParamsToleratesAProcessedEpochWithNoSnapshot(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	if err := env.node.WithState(func(manager *state.Manager) error {
		return manager.PotsoRewardsSetLastProcessedEpoch(9)
	}); err != nil {
		t.Fatalf("mark epoch processed: %v", err)
	}
	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.Engagement.Available || resp.Engagement.Epoch != 0 || resp.Engagement.TotalEngagement != 0 {
		t.Fatalf("engagement = %+v, want unavailable", resp.Engagement)
	}
	if resp.VotingPeriodSeconds != 604800 {
		t.Fatalf("the rules are missing from the answer: %+v", resp)
	}
}

// A snapshot stored under an epoch that no processed-epoch marker points to is not
// the one voting power is drawn from.
func TestHandleGovernanceParamsIgnoresASnapshotWhenNoEpochIsMarkedProcessed(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	seedGovernanceWeightSnapshot(t, env, 0, 777, 1000, 1, false)
	resp, rpcErr := callGovParams(t, env)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if resp.Engagement.Available || resp.Engagement.TotalEngagement != 0 {
		t.Fatalf("engagement = %+v, want unavailable with no processed epoch", resp.Engagement)
	}
}

// An unreadable record is an error, not a silent "unavailable": a client must not
// take a failed read for "no snapshot yet".
func TestHandleGovernanceParamsReportsAnUnreadableEpochMarkerAsAnError(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	if err := env.node.WithState(func(manager *state.Manager) error {
		// the key manager.PotsoRewardsLastProcessedEpoch reads, holding a value that is not a number
		return manager.KVPut([]byte("potso/rewards/lastProcessed"), "not a number")
	}); err != nil {
		t.Fatalf("corrupt marker: %v", err)
	}
	if _, err := env.node.GovernanceParams(); err == nil {
		t.Fatal("an unreadable epoch marker was not reported")
	}
	_, rpcErr := callGovParams(t, env)
	if rpcErr == nil || rpcErr.Code != codeServerError {
		t.Fatalf("expected a server error, got %+v", rpcErr)
	}
}

func TestHandleGovernanceParamsReportsAnUnreadableSnapshotAsAnError(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.PotsoRewardsSetLastProcessedEpoch(4); err != nil {
			return err
		}
		return manager.KVPut(state.SnapshotPotsoWeightsKey(4), "not a snapshot")
	}); err != nil {
		t.Fatalf("corrupt snapshot: %v", err)
	}
	if _, err := env.node.GovernanceParams(); err == nil {
		t.Fatal("an unreadable weight snapshot was not reported")
	}
	_, rpcErr := callGovParams(t, env)
	if rpcErr == nil || rpcErr.Code != codeServerError {
		t.Fatalf("expected a server error, got %+v", rpcErr)
	}
}

func TestHandleGovernanceParamsTakesNoParameters(t *testing.T) {
	env := newTestEnv(t)
	_, rpcErr := callGovParams(t, env, json.RawMessage(`{}`))
	if rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected an invalid-params error, got %+v", rpcErr)
	}
}

func TestHandleGovernanceParamsChangesNoState(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())
	seedGovernanceWeightSnapshot(t, env, 4, 12345, 5000, 2, true)

	root := func() string { return string(env.node.PendingStateRoot()) }
	before := root()
	if before == "" {
		t.Fatal("no state root to compare")
	}
	for i := 0; i < 3; i++ {
		if _, rpcErr := callGovParams(t, env); rpcErr != nil {
			t.Fatalf("unexpected rpc error: %+v", rpcErr)
		}
	}
	if root() != before {
		t.Fatal("reading gov_params changed the state root")
	}
}

// Through the real dispatch, with no credential at all: the method is an open read.
func TestGovParamsIsAnonymousThroughTheRealDispatch(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetGovernancePolicy(govParamsPolicy())

	body := buildRPCRequestBody(t, 7, "gov_params", []json.RawMessage{})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous gov_params returned %d: %s", rec.Code, rec.Body.String())
	}
	raw, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp govParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VotingPeriodSeconds != 604800 || resp.QuorumBps != 2000 {
		t.Fatalf("response over the real dispatch = %+v", resp)
	}
}

func TestGovParamsIsNotOperatorOnlyAndIsAKnownMethod(t *testing.T) {
	if IsOperatorOnlyMethod("gov_params") {
		t.Fatal("gov_params is operator-only")
	}
	if !isKnownRPCMethod("gov_params") {
		t.Fatal("gov_params is not in the table of known methods (its metrics would be labelled \"unknown\")")
	}
}
