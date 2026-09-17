package rpc

import (
	"encoding/json"
	"net/http"
	"strconv"

	"nhbchain/native/subscriptions"
)

// Every mutating subscriptions action (create/update a Plan, subscribe,
// cancel) is a real signed transaction (TxTypeSubscriptionCreatePlan/
// UpdatePlan/Subscribe/Cancel, core/subscriptions_tx.go) submitted via the
// generic nhb_sendTransaction RPC method like every other signed native
// transaction on this chain -- deliberately NOT a bespoke bearer-JWT
// write RPC method. That was exactly the direct-state-write bug class
// fixed for governance/CreatePool/POTSO-stake this session (see those
// TxType doc comments in core/types/transaction.go); a brand-new module
// should not introduce a fresh instance of it. Everything below is a
// read-only query, requiring no auth, the same class as
// potso_stake_info/gov_list.

type subscriptionsPlanIDParams struct {
	PlanID string `json:"planId"`
}

type subscriptionsSubscriptionIDParams struct {
	SubscriptionID string `json:"subscriptionId"`
}

type subscriptionsMerchantParams struct {
	Merchant string `json:"merchant"`
}

type subscriptionsPayerParams struct {
	Payer string `json:"payer"`
}

type subscriptionsPlanResult struct {
	PlanID             string `json:"planId"`
	Merchant           string `json:"merchant"`
	Name               string `json:"name"`
	PriceWei           string `json:"priceWei"`
	Asset              string `json:"asset"`
	IntervalSeconds    uint64 `json:"intervalSeconds"`
	TrialPeriodSeconds uint64 `json:"trialPeriodSeconds"`
	Active             bool   `json:"active"`
	CreatedAt          uint64 `json:"createdAt"`
}

func newSubscriptionsPlanResult(plan *subscriptions.Plan) subscriptionsPlanResult {
	return subscriptionsPlanResult{
		PlanID:             strconv.FormatUint(uint64(plan.ID), 10),
		Merchant:           formatAddress(plan.Merchant),
		Name:               plan.Name,
		PriceWei:           bigString(plan.PriceWei),
		Asset:              string(plan.Asset),
		IntervalSeconds:    plan.IntervalSeconds,
		TrialPeriodSeconds: plan.TrialPeriodSeconds,
		Active:             plan.Active,
		CreatedAt:          plan.CreatedAt,
	}
}

type subscriptionsSubscriptionResult struct {
	SubscriptionID   string `json:"subscriptionId"`
	PlanID           string `json:"planId"`
	Payer            string `json:"payer"`
	Merchant         string `json:"merchant"`
	PriceWei         string `json:"priceWei"`
	Asset            string `json:"asset"`
	IntervalSeconds  uint64 `json:"intervalSeconds"`
	Status           string `json:"status"`
	StartAt          uint64 `json:"startAt"`
	NextChargeAt     uint64 `json:"nextChargeAt"`
	CycleCount       uint64 `json:"cycleCount"`
	FailedAttempts   uint32 `json:"failedAttempts"`
	LastChargeAt     uint64 `json:"lastChargeAt"`
	LastChargeStatus string `json:"lastChargeStatus"`
	CreatedAt        uint64 `json:"createdAt"`
	CancelledAt      uint64 `json:"cancelledAt,omitempty"`
}

func subscriptionStatusString(status subscriptions.SubscriptionStatus) string {
	switch status {
	case subscriptions.SubscriptionStatusActive:
		return "active"
	case subscriptions.SubscriptionStatusPastDue:
		return "past_due"
	case subscriptions.SubscriptionStatusCancelled:
		return "cancelled"
	case subscriptions.SubscriptionStatusSuspended:
		return "suspended"
	default:
		return "unknown"
	}
}

func chargeStatusString(status subscriptions.ChargeStatus) string {
	if status == subscriptions.ChargeStatusPaid {
		return "paid"
	}
	return "failed"
}

func newSubscriptionsSubscriptionResult(sub *subscriptions.Subscription) subscriptionsSubscriptionResult {
	return subscriptionsSubscriptionResult{
		SubscriptionID:   strconv.FormatUint(uint64(sub.ID), 10),
		PlanID:           strconv.FormatUint(uint64(sub.PlanID), 10),
		Payer:            formatAddress(sub.Payer),
		Merchant:         formatAddress(sub.Merchant),
		PriceWei:         bigString(sub.PriceWei),
		Asset:            string(sub.Asset),
		IntervalSeconds:  sub.IntervalSeconds,
		Status:           subscriptionStatusString(sub.Status),
		StartAt:          sub.StartAt,
		NextChargeAt:     sub.NextChargeAt,
		CycleCount:       sub.CycleCount,
		FailedAttempts:   sub.FailedAttempts,
		LastChargeAt:     sub.LastChargeAt,
		LastChargeStatus: chargeStatusString(sub.LastChargeStatus),
		CreatedAt:        sub.CreatedAt,
		CancelledAt:      sub.CancelledAt,
	}
}

type subscriptionsChargeResult struct {
	SubscriptionID string `json:"subscriptionId"`
	PlanID         string `json:"planId"`
	Payer          string `json:"payer"`
	Merchant       string `json:"merchant"`
	Asset          string `json:"asset"`
	AmountWei      string `json:"amountWei"`
	FeeWei         string `json:"feeWei"`
	Status         string `json:"status"`
	AttemptNumber  uint32 `json:"attemptNumber"`
	ChargedAt      uint64 `json:"chargedAt"`
	FailureReason  string `json:"failureReason,omitempty"`
}

func newSubscriptionsChargeResult(c subscriptions.Charge) subscriptionsChargeResult {
	return subscriptionsChargeResult{
		SubscriptionID: strconv.FormatUint(uint64(c.SubscriptionID), 10),
		PlanID:         strconv.FormatUint(uint64(c.PlanID), 10),
		Payer:          formatAddress(c.Payer),
		Merchant:       formatAddress(c.Merchant),
		Asset:          string(c.Asset),
		AmountWei:      bigString(c.AmountWei),
		FeeWei:         bigString(c.FeeWei),
		Status:         chargeStatusString(c.Status),
		AttemptNumber:  c.AttemptNumber,
		ChargedAt:      c.ChargedAt,
		FailureReason:  c.FailureReason,
	}
}

type subscriptionsConfigResult struct {
	ManagementFeeBps     uint32 `json:"managementFeeBps"`
	ManagementFeeCapBps  uint32 `json:"managementFeeCapBps"`
	Treasury             string `json:"treasury,omitempty"`
	MaxRetries           uint32 `json:"maxRetries"`
	RetryIntervalSeconds uint64 `json:"retryIntervalSeconds"`
	Configured           bool   `json:"configured"`
}

func parseSubscriptionsPlanID(raw string) (subscriptions.PlanID, error) {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return subscriptions.PlanID(parsed), nil
}

func parseSubscriptionsSubscriptionID(raw string) (subscriptions.SubscriptionID, error) {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return subscriptions.SubscriptionID(parsed), nil
}

func (s *Server) handleSubscriptionsGetPlan(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsPlanIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	planID, err := parseSubscriptionsPlanID(params.PlanID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid planId", err.Error())
		return
	}
	plan, ok := s.node.SubscriptionPlanByID(planID)
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "plan not found", nil)
		return
	}
	writeResult(w, req.ID, newSubscriptionsPlanResult(plan))
}

func (s *Server) handleSubscriptionsListPlansByMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsMerchantParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	merchantAddr, err := decodeBech32(params.Merchant)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid merchant address", err.Error())
		return
	}
	ids, err := s.node.SubscriptionPlansByMerchant(merchantAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list plans", err.Error())
		return
	}
	results := make([]subscriptionsPlanResult, 0, len(ids))
	for _, id := range ids {
		if plan, ok := s.node.SubscriptionPlanByID(id); ok {
			results = append(results, newSubscriptionsPlanResult(plan))
		}
	}
	writeResult(w, req.ID, results)
}

func (s *Server) handleSubscriptionsGetSubscription(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsSubscriptionIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	subID, err := parseSubscriptionsSubscriptionID(params.SubscriptionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid subscriptionId", err.Error())
		return
	}
	sub, ok := s.node.SubscriptionByID(subID)
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "subscription not found", nil)
		return
	}
	writeResult(w, req.ID, newSubscriptionsSubscriptionResult(sub))
}

func (s *Server) handleSubscriptionsListByPayer(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsPayerParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	payerAddr, err := decodeBech32(params.Payer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payer address", err.Error())
		return
	}
	ids, err := s.node.SubscriptionsByPayer(payerAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list subscriptions", err.Error())
		return
	}
	results := make([]subscriptionsSubscriptionResult, 0, len(ids))
	for _, id := range ids {
		if sub, ok := s.node.SubscriptionByID(id); ok {
			results = append(results, newSubscriptionsSubscriptionResult(sub))
		}
	}
	writeResult(w, req.ID, results)
}

func (s *Server) handleSubscriptionsListByMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsMerchantParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	merchantAddr, err := decodeBech32(params.Merchant)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid merchant address", err.Error())
		return
	}
	ids, err := s.node.SubscriptionsByMerchant(merchantAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list subscriptions", err.Error())
		return
	}
	results := make([]subscriptionsSubscriptionResult, 0, len(ids))
	for _, id := range ids {
		if sub, ok := s.node.SubscriptionByID(id); ok {
			results = append(results, newSubscriptionsSubscriptionResult(sub))
		}
	}
	writeResult(w, req.ID, results)
}

func (s *Server) handleSubscriptionsListCharges(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	var params subscriptionsSubscriptionIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	subID, err := parseSubscriptionsSubscriptionID(params.SubscriptionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid subscriptionId", err.Error())
		return
	}
	charges, err := s.node.SubscriptionCharges(subID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list charges", err.Error())
		return
	}
	results := make([]subscriptionsChargeResult, len(charges))
	for i, c := range charges {
		results[i] = newSubscriptionsChargeResult(c)
	}
	writeResult(w, req.ID, results)
}

func (s *Server) handleSubscriptionsGetConfig(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	cfg, ok := s.node.SubscriptionsEngineConfig()
	result := subscriptionsConfigResult{
		ManagementFeeBps:     cfg.ManagementFeeBps,
		ManagementFeeCapBps:  cfg.ManagementFeeCapBps,
		MaxRetries:           cfg.MaxRetries,
		RetryIntervalSeconds: cfg.RetryIntervalSeconds,
		Configured:           ok,
	}
	if ok && cfg.Treasury != ([20]byte{}) {
		result.Treasury = formatAddress(cfg.Treasury)
	}
	writeResult(w, req.ID, result)
}
