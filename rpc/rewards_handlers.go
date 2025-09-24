package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nhbchain/core/genesis"
	"nhbchain/core/rewards"
)

type rewardEpochResult struct {
	Epoch             uint64               `json:"epoch"`
	Height            uint64               `json:"height"`
	ClosedAt          int64                `json:"closedAt"`
	Blocks            uint64               `json:"blocks"`
	PlannedTotal      string               `json:"plannedTotal"`
	PaidTotal         string               `json:"paidTotal"`
	ValidatorsPlanned string               `json:"validatorsPlanned"`
	ValidatorsPaid    string               `json:"validatorsPaid"`
	StakersPlanned    string               `json:"stakersPlanned"`
	StakersPaid       string               `json:"stakersPaid"`
	EngagementPlanned string               `json:"engagementPlanned"`
	EngagementPaid    string               `json:"engagementPaid"`
	UnusedTotal       string               `json:"unusedTotal"`
	UnusedValidators  string               `json:"unusedValidators"`
	UnusedStakers     string               `json:"unusedStakers"`
	UnusedEngagement  string               `json:"unusedEngagement"`
	Payouts           []rewardPayoutResult `json:"payouts"`
}

type rewardPayoutResult struct {
	Account    string `json:"account"`
	Total      string `json:"total"`
	Validators string `json:"validators"`
	Stakers    string `json:"stakers"`
	Engagement string `json:"engagement"`
}

type rewardPayoutResponse struct {
	Epoch  uint64             `json:"epoch"`
	Payout rewardPayoutResult `json:"payout"`
}

type rewardPayoutParams struct {
	Epoch   *uint64 `json:"epoch,omitempty"`
	Account string  `json:"account"`
}

func (s *Server) handleGetRewardEpoch(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	var epochNumber uint64
	var haveEpoch bool
	if len(req.Params) > 0 {
		value, ok, err := parseEpochParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
			return
		}
		if ok {
			epochNumber = value
			haveEpoch = true
		}
	}
	var (
		settlement *rewards.EpochSettlement
		exists     bool
	)
	if haveEpoch {
		settlement, exists = s.node.RewardEpochSettlement(epochNumber)
	} else {
		settlement, exists = s.node.LatestRewardEpochSettlement()
	}
	if !exists || settlement == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "reward epoch not found", nil)
		return
	}
	result := rewardEpochResult{
		Epoch:             settlement.Epoch,
		Height:            settlement.Height,
		ClosedAt:          settlement.ClosedAt,
		Blocks:            settlement.Blocks,
		PlannedTotal:      settlement.PlannedTotal.String(),
		PaidTotal:         settlement.PaidTotal.String(),
		ValidatorsPlanned: settlement.ValidatorsPlanned.String(),
		ValidatorsPaid:    settlement.ValidatorsPaid.String(),
		StakersPlanned:    settlement.StakersPlanned.String(),
		StakersPaid:       settlement.StakersPaid.String(),
		EngagementPlanned: settlement.EngagementPlanned.String(),
		EngagementPaid:    settlement.EngagementPaid.String(),
		UnusedTotal:       settlement.UnusedTotal().String(),
		UnusedValidators:  settlement.UnusedValidators().String(),
		UnusedStakers:     settlement.UnusedStakers().String(),
		UnusedEngagement:  settlement.UnusedEngagement().String(),
		Payouts:           make([]rewardPayoutResult, len(settlement.Payouts)),
	}
	for i := range settlement.Payouts {
		payout := settlement.Payouts[i]
		result.Payouts[i] = rewardPayoutResult{
			Account:    "0x" + hex.EncodeToString(payout.Account),
			Total:      payout.Total.String(),
			Validators: payout.Validators.String(),
			Stakers:    payout.Stakers.String(),
			Engagement: payout.Engagement.String(),
		}
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleGetRewardPayout(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "account parameter required", nil)
		return
	}
	var params rewardPayoutParams
	raw := req.Params[0]
	if err := json.Unmarshal(raw, &params); err != nil {
		var direct string
		if err2 := json.Unmarshal(raw, &direct); err2 == nil {
			params.Account = direct
		} else {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", nil)
			return
		}
	}
	addrBytes, err := parseRewardAccount(params.Account)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid account", err.Error())
		return
	}
	var (
		settlement *rewards.EpochSettlement
		exists     bool
	)
	if params.Epoch != nil {
		settlement, exists = s.node.RewardEpochSettlement(*params.Epoch)
	} else {
		settlement, exists = s.node.LatestRewardEpochSettlement()
	}
	if !exists || settlement == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "reward epoch not found", nil)
		return
	}
	hexAccount := "0x" + hex.EncodeToString(addrBytes)
	for i := range settlement.Payouts {
		payout := settlement.Payouts[i]
		if bytesEqual(payout.Account, addrBytes) {
			result := rewardPayoutResponse{
				Epoch: settlement.Epoch,
				Payout: rewardPayoutResult{
					Account:    hexAccount,
					Total:      payout.Total.String(),
					Validators: payout.Validators.String(),
					Stakers:    payout.Stakers.String(),
					Engagement: payout.Engagement.String(),
				},
			}
			writeResult(w, req.ID, result)
			return
		}
	}
	writeError(w, http.StatusNotFound, req.ID, codeServerError, "payout not found", nil)
}

func parseRewardAccount(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("account required")
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
		return hex.DecodeString(cleaned)
	}
	addr, err := genesis.ParseBech32Account(trimmed)
	if err != nil {
		return nil, err
	}
	return addr[:], nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
