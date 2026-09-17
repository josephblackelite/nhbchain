package rpc

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"

	"nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

func addrString(t *testing.T, addr [20]byte) string {
	t.Helper()
	return crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
}

type potsoWeightResponse struct {
	Epoch     uint64 `json:"epoch"`
	Address   string `json:"address"`
	WeightBps uint64 `json:"weightBps"`
}

// TestHandlePotsoGetWeight seeds SnapshotPotsoWeights (the exact key
// native/governance/engine.go's CastVote reads) and confirms the RPC
// reports the same weight for the same address/epoch.
func TestHandlePotsoGetWeight(t *testing.T) {
	env := newTestEnv(t)
	addr := testAddr(7)
	other := testAddr(9)
	snapshot := &potso.StoredWeightSnapshot{
		Entries: []potso.StoredWeightEntry{
			{Address: addr, Stake: big.NewInt(100), WeightBps: 3456},
			{Address: other, Stake: big.NewInt(50), WeightBps: 1000},
		},
	}
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.SetSnapshotPotsoWeights(4, snapshot); err != nil {
			return err
		}
		return manager.PotsoRewardsSetLastProcessedEpoch(4)
	}); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}

	addrStr := addrString(t, addr)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, potsoWeightParams{Address: addrStr})}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoGetWeight(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp potsoWeightResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Epoch != 4 {
		t.Fatalf("expected epoch 4, got %d", resp.Epoch)
	}
	if resp.WeightBps != 3456 {
		t.Fatalf("expected weightBps 3456, got %d", resp.WeightBps)
	}
}

// TestHandlePotsoGetWeightUnknownAddressReturnsZeroNotError confirms a
// snapshot with no entry for the requested address is a passive zero, not
// an RPC error -- unlike CastVote itself, which errors on zero power
// because casting a vote is an action, not a query.
func TestHandlePotsoGetWeightUnknownAddressReturnsZeroNotError(t *testing.T) {
	env := newTestEnv(t)
	known := testAddr(1)
	unknown := testAddr(2)
	snapshot := &potso.StoredWeightSnapshot{
		Entries: []potso.StoredWeightEntry{{Address: known, Stake: big.NewInt(10), WeightBps: 500}},
	}
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.SetSnapshotPotsoWeights(1, snapshot); err != nil {
			return err
		}
		return manager.PotsoRewardsSetLastProcessedEpoch(1)
	}); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, potsoWeightParams{Address: addrString(t, unknown)})}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoGetWeight(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("expected no rpc error for an unknown address, got %+v", rpcErr)
	}
	var resp potsoWeightResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.WeightBps != 0 {
		t.Fatalf("expected weightBps 0 for unknown address, got %d", resp.WeightBps)
	}
}

// TestHandlePotsoGetWeightDoesNotReadLeaderboardKey is the actual
// regression this RPC exists to prevent: seeding ONLY
// PotsoMetricsSetSnapshot (potso_leaderboard's own key) must NOT make
// potso_getWeight report a nonzero weight -- if it did, it would mean this
// handler drifted onto the wrong key and could silently diverge from
// CastVote's real eligibility check the moment the two keys' writers ever
// separate (see SetSnapshotPotsoWeights's own doc comment).
func TestHandlePotsoGetWeightDoesNotReadLeaderboardKey(t *testing.T) {
	env := newTestEnv(t)
	addr := testAddr(5)
	leaderboardOnly := &potso.StoredWeightSnapshot{
		Entries: []potso.StoredWeightEntry{{Address: addr, Stake: big.NewInt(999), WeightBps: 9999}},
	}
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.PotsoMetricsSetSnapshot(2, leaderboardOnly); err != nil {
			return err
		}
		return manager.PotsoRewardsSetLastProcessedEpoch(2)
	}); err != nil {
		t.Fatalf("persist leaderboard-only snapshot: %v", err)
	}

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, potsoWeightParams{Address: addrString(t, addr)})}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoGetWeight(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp potsoWeightResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.WeightBps != 0 {
		t.Fatalf("expected weightBps 0 when only the leaderboard key is populated, got %d -- handler is reading the wrong storage key", resp.WeightBps)
	}
}
