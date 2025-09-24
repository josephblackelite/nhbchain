package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/crypto"
)

type potsoEpochInfoParams struct {
	Epoch *uint64 `json:"epoch,omitempty"`
}

type potsoEpochInfoResult struct {
	Epoch           uint64 `json:"epoch"`
	Day             string `json:"day"`
	StakeTotal      string `json:"stakeTotal"`
	EngagementTotal string `json:"engagementTotal"`
	AlphaBps        uint64 `json:"alphaBps"`
	Emission        string `json:"emission"`
	Budget          string `json:"budget"`
	TotalPaid       string `json:"totalPaid"`
	Remainder       string `json:"remainder"`
	Winners         uint64 `json:"winners"`
}

type potsoEpochPayoutsParams struct {
	Epoch  uint64 `json:"epoch"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type potsoEpochPayoutEntry struct {
	User   string `json:"user"`
	Amount string `json:"amount"`
}

type potsoEpochPayoutsResult struct {
	Epoch   uint64                  `json:"epoch"`
	Payouts []potsoEpochPayoutEntry `json:"payouts"`
}

func (s *Server) handlePotsoEpochInfo(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var params potsoEpochInfoParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
			return
		}
	}

	var (
		epoch uint64
		have  bool
		err   error
	)
	if params.Epoch != nil {
		epoch = *params.Epoch
		have = true
	} else {
		var ok bool
		epoch, ok, err = s.node.PotsoLatestRewardEpoch()
		if err != nil {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load latest epoch", err.Error())
			return
		}
		have = ok
	}
	if !have {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch not found", nil)
		return
	}
	meta, ok, err := s.node.PotsoRewardEpochInfo(epoch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load epoch info", err.Error())
		return
	}
	if !ok || meta == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch not found", nil)
		return
	}
	result := potsoEpochInfoResult{
		Epoch:           meta.Epoch,
		Day:             meta.Day,
		StakeTotal:      bigIntString(meta.StakeTotal),
		EngagementTotal: bigIntString(meta.EngagementTotal),
		AlphaBps:        meta.AlphaBps,
		Emission:        bigIntString(meta.Emission),
		Budget:          bigIntString(meta.Budget),
		TotalPaid:       bigIntString(meta.TotalPaid),
		Remainder:       bigIntString(meta.Remainder),
		Winners:         meta.Winners,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoEpochPayouts(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "payouts requires parameter object", nil)
		return
	}
	var params potsoEpochPayoutsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	var cursorPtr *[20]byte
	trimmedCursor := strings.TrimSpace(params.Cursor)
	if trimmedCursor != "" {
		addr, err := decodeBech32(trimmedCursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid cursor", err.Error())
			return
		}
		cursorPtr = &addr
	}
	payouts, err := s.node.PotsoRewardEpochPayouts(params.Epoch, cursorPtr, params.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load payouts", err.Error())
		return
	}
	result := potsoEpochPayoutsResult{
		Epoch:   params.Epoch,
		Payouts: make([]potsoEpochPayoutEntry, len(payouts)),
	}
	for i, payout := range payouts {
		user := crypto.NewAddress(crypto.NHBPrefix, payout.Address[:]).String()
		result.Payouts[i] = potsoEpochPayoutEntry{
			User:   user,
			Amount: bigIntString(payout.Amount),
		}
	}
	writeResult(w, req.ID, result)
}

func bigIntString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}
