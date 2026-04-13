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

type potsoParamsResponse struct {
	AlphaStakeBps         uint64 `json:"alphaStakeBps"`
	TxWeightBps           uint64 `json:"txWeightBps"`
	EscrowWeightBps       uint64 `json:"escrowWeightBps"`
	UptimeWeightBps       uint64 `json:"uptimeWeightBps"`
	MaxEngagementPerEpoch uint64 `json:"maxEngagementPerEpoch"`
	MinStakeToWinWei      string `json:"minStakeToWinWei"`
	MinEngagementToWin    uint64 `json:"minEngagementToWin"`
	DecayHalfLifeEpochs   uint64 `json:"decayHalfLifeEpochs"`
	TopKWinners           uint64 `json:"topKWinners"`
	TieBreak              string `json:"tieBreak"`
}

type potsoLeaderboardResponse struct {
	Epoch uint64 `json:"epoch"`
	Total uint64 `json:"total"`
	Items []struct {
		Address            string `json:"addr"`
		WeightBps          uint64 `json:"weightBps"`
		StakeShareBps      uint64 `json:"stakeShareBps"`
		EngagementShareBps uint64 `json:"engShareBps"`
	} `json:"items"`
}

func testAddr(b byte) [20]byte {
	var out [20]byte
	out[19] = b
	return out
}

func TestHandlePotsoParams(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoParams(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp potsoParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	weights := env.node.PotsoWeightConfig()
	if resp.AlphaStakeBps != weights.AlphaStakeBps {
		t.Fatalf("alpha mismatch: %d != %d", resp.AlphaStakeBps, weights.AlphaStakeBps)
	}
	if resp.MinStakeToWinWei != weights.MinStakeToWinWei.String() {
		t.Fatalf("min stake mismatch: %s != %s", resp.MinStakeToWinWei, weights.MinStakeToWinWei.String())
	}
}

func TestHandlePotsoLeaderboard(t *testing.T) {
	env := newTestEnv(t)
	params := potso.WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
		TieBreak:              potso.TieBreakAddrLex,
	}
	inputs := []potso.WeightInput{
		{Address: testAddr(1), Stake: big.NewInt(100), Meter: potso.EngagementMeter{TxCount: 1}},
		{Address: testAddr(2), Stake: big.NewInt(200), Meter: potso.EngagementMeter{TxCount: 2}},
		{Address: testAddr(3), Stake: big.NewInt(300), Meter: potso.EngagementMeter{TxCount: 3}},
	}
	snapshot, err := potso.ComputeWeightSnapshot(4, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	stored := snapshot.ToStored()
	if err := env.node.WithState(func(manager *state.Manager) error {
		if err := manager.PotsoMetricsSetSnapshot(4, stored); err != nil {
			return err
		}
		return manager.PotsoRewardsSetLastProcessedEpoch(4)
	}); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	reqParams := potsoLeaderboardParams{Epoch: uint64Ptr(4), Offset: 1, Limit: 1}
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, reqParams)}}
	recorder := httptest.NewRecorder()
	env.server.handlePotsoLeaderboard(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp potsoLeaderboardResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Epoch != 4 {
		t.Fatalf("expected epoch 4, got %d", resp.Epoch)
	}
	if resp.Total != uint64(len(snapshot.Entries)) {
		t.Fatalf("expected total %d, got %d", len(snapshot.Entries), resp.Total)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(resp.Items))
	}
	expectedAddr := crypto.MustNewAddress(crypto.NHBPrefix, snapshot.Entries[1].Address[:]).String()
	if resp.Items[0].Address != expectedAddr {
		t.Fatalf("unexpected address %s", resp.Items[0].Address)
	}
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}
