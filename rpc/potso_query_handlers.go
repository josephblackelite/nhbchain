package rpc

import (
	"encoding/json"
	"net/http"

	"nhbchain/crypto"
)

type potsoLeaderboardParams struct {
	Epoch  *uint64 `json:"epoch,omitempty"`
	Offset int     `json:"offset,omitempty"`
	Limit  int     `json:"limit,omitempty"`
}

type potsoLeaderboardItem struct {
	Address            string `json:"addr"`
	WeightBps          uint64 `json:"weightBps"`
	StakeShareBps      uint64 `json:"stakeShareBps"`
	EngagementShareBps uint64 `json:"engShareBps"`
}

type potsoLeaderboardResult struct {
	Epoch uint64                 `json:"epoch"`
	Total uint64                 `json:"total"`
	Items []potsoLeaderboardItem `json:"items"`
}

type potsoParamsResult struct {
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

func (s *Server) handlePotsoLeaderboard(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var params potsoLeaderboardParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
			return
		}
	}
	epoch := uint64(0)
	if params.Epoch != nil {
		epoch = *params.Epoch
	}
	actualEpoch, total, entries, err := s.node.PotsoLeaderboard(epoch, params.Offset, params.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load leaderboard", err.Error())
		return
	}
	result := potsoLeaderboardResult{
		Epoch: actualEpoch,
		Total: total,
		Items: make([]potsoLeaderboardItem, len(entries)),
	}
	for i, entry := range entries {
		addr := crypto.NewAddress(crypto.NHBPrefix, entry.Address[:]).String()
		result.Items[i] = potsoLeaderboardItem{
			Address:            addr,
			WeightBps:          entry.WeightBps,
			StakeShareBps:      entry.StakeShareBps,
			EngagementShareBps: entry.EngagementShareBps,
		}
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoParams(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	weights := s.node.PotsoWeightConfig()
	minStake := "0"
	if weights.MinStakeToWinWei != nil {
		minStake = weights.MinStakeToWinWei.String()
	}
	result := potsoParamsResult{
		AlphaStakeBps:         weights.AlphaStakeBps,
		TxWeightBps:           weights.TxWeightBps,
		EscrowWeightBps:       weights.EscrowWeightBps,
		UptimeWeightBps:       weights.UptimeWeightBps,
		MaxEngagementPerEpoch: weights.MaxEngagementPerEpoch,
		MinStakeToWinWei:      minStake,
		MinEngagementToWin:    weights.MinEngagementToWin,
		DecayHalfLifeEpochs:   weights.DecayHalfLifeEpochs,
		TopKWinners:           weights.TopKWinners,
		TieBreak:              string(weights.TieBreak),
	}
	writeResult(w, req.ID, result)
}
