package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"nhbchain/core"
	"nhbchain/crypto"
)

type feesMonthlyStatusResult struct {
	Window       string `json:"window_yyyymm"`
	Used         uint64 `json:"used"`
	Remaining    uint64 `json:"remaining"`
	LastRollover string `json:"last_rollover_yyyymm"`
}

type feesTransferStatusParams struct {
	Address string `json:"address"`
}

type feesTransferStatusResult struct {
	Window        string `json:"window"`
	WindowKey     string `json:"window_key"`
	SpentWei      string `json:"spentWei"`
	FreeLimitWei  string `json:"freeLimitWei"`
	RemainingWei  string `json:"remainingWei"`
	Eligible      bool   `json:"eligible"`
	NextResetUnix int64  `json:"nextResetUnix,omitempty"`
	// StrictAllowance is true when the next block charges the strict allowance: only the part of a transfer above
	// remainingWei pays the fee. False: a transfer is free in full while remainingWei is positive.
	StrictAllowance bool `json:"strictAllowance"`
}

type feesTransferQuoteParams struct {
	Address   string `json:"address"`
	Asset     string `json:"asset"`
	AmountWei string `json:"amountWei"`
}

type feesTransferQuoteResult struct {
	// Eligible is true when the fee is zero because the allowance covers the whole transfer.
	Eligible bool   `json:"eligible"`
	FeeWei   string `json:"feeWei"`
	FeeBps   uint32 `json:"feeBps"`
	// FreeWei is the part of the amount the allowance covers and ChargeableWei the rest, the part the fee is charged on.
	FreeWei         string `json:"freeWei"`
	ChargeableWei   string `json:"chargeableWei"`
	StrictAllowance bool   `json:"strictAllowance"`
}

func (s *Server) handleFeesGetMonthlyStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	status, err := s.node.FeesMonthlyStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load monthly status", err.Error())
		return
	}
	result := feesMonthlyStatusResult{
		Window:       status.Window,
		Used:         status.Used,
		Remaining:    status.Remaining,
		LastRollover: status.LastRollover,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleFeesGetTransferStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var params feesTransferStatusParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid params", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	status, err := s.node.TransferGasStatus(addr.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load transfer status", err.Error())
		return
	}
	strict, _ := s.node.TransferFeeStrictAllowanceNextBlock()
	result := feesTransferStatusResult{
		Window:          status.Window,
		WindowKey:       status.WindowKey,
		SpentWei:        status.Spent.String(),
		FreeLimitWei:    status.FreeLimit.String(),
		RemainingWei:    status.Remaining.String(),
		Eligible:        status.Eligible,
		StrictAllowance: strict,
	}
	if !status.NextReset.IsZero() {
		result.NextResetUnix = status.NextReset.UTC().Unix()
	}
	writeResult(w, req.ID, result)
}

// handleFeesGetTransferQuote returns the fee that would actually be charged
// for a transfer of amountWei in the given asset from address, reusing the
// same free-tier eligibility check as fees_getTransferStatus. When the
// wallet is within its free tier the quoted fee is zero; otherwise the fee
// is computed from the live protocol-enforced rate for the requested asset
// (TransferGasPolicy.FeeBps for NHB, TransferGasPolicy.FeeBpsZNHB for ZNHB --
// see TransferGasPolicy.FeeBpsForAsset). The response's FeeBps field always
// echoes back whichever rate actually applied to this request's asset.
func (s *Server) handleFeesGetTransferQuote(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address, asset, and amountWei parameters required", nil)
		return
	}
	var params feesTransferQuoteParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid params", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(params.Asset))
	if asset != "NHB" && asset != "ZNHB" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid asset", "asset must be NHB or ZNHB")
		return
	}
	amount, err := parsePositiveBigInt(params.AmountWei)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid amountWei", err.Error())
		return
	}
	// The fee the next block will charge: the governed schedule once it is in force, and the strict allowance once its
	// height is reached (core.Node.TransferFeeQuote uses the same function as the block path).
	quote, err := s.node.TransferFeeQuote(addr.Bytes(), asset, amount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load transfer fee quote", err.Error())
		return
	}
	writeResult(w, req.ID, feesTransferQuoteResult{
		Eligible:        quote.Eligible,
		FeeWei:          quote.FeeWei.String(),
		FeeBps:          quote.FeeBps,
		FreeWei:         quote.FreeWei.String(),
		ChargeableWei:   quote.ChargeableWei.String(),
		StrictAllowance: quote.StrictAllowance,
	})
}

type feesTransferScheduleEntry struct {
	NHBFeeBps        uint32 `json:"nhbFeeBps"`
	ZNHBFeeBps       uint32 `json:"znhbFeeBps"`
	FreeAllowanceWei string `json:"freeAllowanceWei"`
	// Source is "config" (the values in the validators' configuration) or "governance" (a schedule a
	// passed policy.feeParams proposal set).
	Source     string `json:"source"`
	ProposalID uint64 `json:"proposalId,omitempty"`
	FromHeight uint64 `json:"fromHeight,omitempty"`
}

type feesTransferScheduleResult struct {
	NextBlockHeight    uint64                     `json:"nextBlockHeight"`
	InForce            feesTransferScheduleEntry  `json:"inForce"`
	Pending            *feesTransferScheduleEntry `json:"pending"`
	BlocksUntilPending uint64                     `json:"blocksUntilPending"`
	UniformDomainFee   struct {
		Active           bool   `json:"active"`
		ActivationHeight uint64 `json:"activationHeight"`
	} `json:"uniformDomainFee"`
	// StrictAllowance: active means the next block charges the fee on the part of a transfer above what is left of the
	// free allowance; inactive means a transfer is free in full while any allowance is left.
	StrictAllowance struct {
		Active           bool   `json:"active"`
		ActivationHeight uint64 `json:"activationHeight"`
	} `json:"strictAllowance"`
	Governance struct {
		Active              bool   `json:"active"`
		ActivationHeight    uint64 `json:"activationHeight"`
		DelayBlocks         uint64 `json:"delayBlocks"`
		MaxFeeBps           uint32 `json:"maxFeeBps"`
		MaxFreeAllowanceWei string `json:"maxFreeAllowanceWei"`
	} `json:"governance"`
}

func feesScheduleEntry(e core.TransferFeeScheduleEntry) feesTransferScheduleEntry {
	return feesTransferScheduleEntry{
		NHBFeeBps:        e.NHBFeeBps,
		ZNHBFeeBps:       e.ZNHBFeeBps,
		FreeAllowanceWei: e.FreeAllowanceWei,
		Source:           e.Source,
		ProposalID:       e.ProposalID,
		FromHeight:       e.FromHeight,
	}
}

// handleFeesGetTransferSchedule reports the transfer fee schedule the next block will charge (the free
// allowance per wallet and asset, the NHB and ZNHB rates in basis points), where it comes from, and a
// change that has passed governance and is waiting for its block, with the blocks left until it takes
// effect. It also reports the two chain rules (the uniform fee for transfers that name a fee domain and the
// governed schedule). It reads chain state and changes nothing; it takes no parameters.
func (s *Server) handleFeesGetTransferSchedule(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	view, err := s.node.TransferFeeSchedule()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load transfer fee schedule", err.Error())
		return
	}
	result := feesTransferScheduleResult{
		NextBlockHeight:    view.NextBlockHeight,
		InForce:            feesScheduleEntry(view.InForce),
		BlocksUntilPending: view.BlocksUntilPending,
	}
	if view.Pending != nil {
		pending := feesScheduleEntry(*view.Pending)
		result.Pending = &pending
	}
	result.UniformDomainFee.Active = view.UniformDomainFeeActive
	result.UniformDomainFee.ActivationHeight = view.UniformDomainFeeActivationHeight
	result.StrictAllowance.Active = view.StrictAllowanceActive
	result.StrictAllowance.ActivationHeight = view.StrictAllowanceActivationHeight
	result.Governance.Active = view.GovernanceActive
	result.Governance.ActivationHeight = view.GovernanceActivationHeight
	result.Governance.DelayBlocks = view.GovernanceDelayBlocks
	result.Governance.MaxFeeBps = view.MaxFeeBps
	result.Governance.MaxFreeAllowanceWei = view.MaxFreeAllowanceWei
	writeResult(w, req.ID, result)
}
