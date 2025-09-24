package rpc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/crypto"
	"nhbchain/native/potso"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
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

type potsoRewardClaimParams struct {
	Epoch     uint64 `json:"epoch"`
	Address   string `json:"address"`
	Signature string `json:"signature"`
}

type potsoRewardClaimResult struct {
	Paid   bool   `json:"paid"`
	Amount string `json:"amount"`
}

type potsoRewardHistoryParams struct {
	Address string `json:"address"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type potsoRewardHistoryEntry struct {
	Epoch  uint64 `json:"epoch"`
	Amount string `json:"amount"`
	Mode   string `json:"mode"`
}

type potsoRewardHistoryResult struct {
	Address    string                    `json:"address"`
	Entries    []potsoRewardHistoryEntry `json:"entries"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}

type potsoRewardExportParams struct {
	Epoch uint64 `json:"epoch"`
}

type potsoRewardExportResult struct {
	Epoch     uint64 `json:"epoch"`
	CSVBase64 string `json:"csvBase64"`
	TotalPaid string `json:"totalPaid"`
	Winners   int    `json:"winners"`
}

func rewardClaimDigest(epoch uint64, addr string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(addr))
	payload := fmt.Sprintf("potso_reward_claim|%d|%s", epoch, normalized)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
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

func (s *Server) handlePotsoRewardClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "claim requires parameter object", nil)
		return
	}
	var params potsoRewardClaimParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.Address == "" || params.Signature == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address and signature are required", nil)
		return
	}
	addr, err := decodeBech32(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	sig, err := decodeHexBytes(params.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	if len(sig) != 65 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature must be 65 bytes", nil)
		return
	}
	digest := rewardClaimDigest(params.Epoch, params.Address)
	pubKey, err := ethcrypto.SigToPub(digest, sig)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if !strings.EqualFold(recovered.Hex()[2:], hex.EncodeToString(addr[:])) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature does not match address", nil)
		return
	}

	paid, amount, claimErr := s.node.PotsoRewardClaim(params.Epoch, addr)
	if claimErr != nil {
		switch {
		case errors.Is(claimErr, potso.ErrRewardNotFound):
			writeError(w, http.StatusNotFound, req.ID, codeServerError, "reward not found", nil)
		case errors.Is(claimErr, potso.ErrClaimingDisabled):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "claiming disabled", nil)
		case errors.Is(claimErr, potso.ErrInsufficientTreasury):
			writeError(w, http.StatusConflict, req.ID, codeServerError, "INSUFFICIENT_TREASURY", nil)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to claim reward", claimErr.Error())
		}
		return
	}
	result := potsoRewardClaimResult{Paid: paid, Amount: bigIntString(amount)}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoRewardsHistory(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "history requires parameter object", nil)
		return
	}
	var params potsoRewardHistoryParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.Address == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	entries, nextCursor, histErr := s.node.PotsoRewardsHistory(addr, params.Cursor, params.Limit)
	if histErr != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load history", histErr.Error())
		return
	}
	result := potsoRewardHistoryResult{Address: params.Address, Entries: make([]potsoRewardHistoryEntry, len(entries)), NextCursor: nextCursor}
	for i, entry := range entries {
		amount := "0"
		if entry.Amount != nil {
			amount = entry.Amount.String()
		}
		result.Entries[i] = potsoRewardHistoryEntry{Epoch: entry.Epoch, Amount: amount, Mode: string(entry.Mode.Normalise())}
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoExportEpoch(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "export requires parameter object", nil)
		return
	}
	var params potsoRewardExportParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	data, total, winners, err := s.node.PotsoExportEpoch(params.Epoch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to export epoch", err.Error())
		return
	}
	result := potsoRewardExportResult{
		Epoch:     params.Epoch,
		CSVBase64: base64.StdEncoding.EncodeToString(data),
		TotalPaid: bigIntString(total),
		Winners:   winners,
	}
	writeResult(w, req.ID, result)
}

func bigIntString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}
