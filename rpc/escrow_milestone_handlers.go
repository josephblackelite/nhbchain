package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/native/escrow"
)

type milestoneLegParam struct {
	ID          uint64 `json:"id"`
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Token       string `json:"token"`
	Amount      string `json:"amount"`
	Deadline    int64  `json:"deadline"`
}

type milestoneSubscriptionParam struct {
	IntervalSeconds int64 `json:"intervalSeconds"`
	NextReleaseAt   int64 `json:"nextReleaseAt"`
	Active          bool  `json:"active"`
}

type milestoneCreateParams struct {
	Payer        string                      `json:"payer"`
	Payee        string                      `json:"payee"`
	Realm        string                      `json:"realm,omitempty"`
	MetaHex      string                      `json:"meta,omitempty"`
	Legs         []milestoneLegParam         `json:"legs"`
	Subscription *milestoneSubscriptionParam `json:"subscription,omitempty"`
}

type milestoneIDParams struct {
	ID string `json:"id"`
}

type milestoneLegActionParams struct {
	ID     string `json:"id"`
	LegID  uint64 `json:"legId"`
	Caller string `json:"caller"`
}

type milestoneSubscriptionUpdateParams struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
	Active bool   `json:"active"`
}

type milestoneCreateResult struct {
	ID string `json:"id"`
}

type milestoneProjectJSON struct {
	ID           string                      `json:"id"`
	Payer        string                      `json:"payer"`
	Payee        string                      `json:"payee"`
	Realm        string                      `json:"realm"`
	Status       string                      `json:"status"`
	CreatedAt    int64                       `json:"createdAt"`
	UpdatedAt    int64                       `json:"updatedAt"`
	Legs         []milestoneLegJSON          `json:"legs"`
	Meta         string                      `json:"meta"`
	Subscription *milestoneSubscriptionParam `json:"subscription,omitempty"`
}

type milestoneLegJSON struct {
	ID       uint64 `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	Deadline int64  `json:"deadline"`
	Status   string `json:"status"`
}

func (s *Server) handleEscrowMilestoneCreate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneCreateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	payer, err := parseBech32Address(params.Payer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	payee, err := parseBech32Address(params.Payee)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	project := &escrow.MilestoneProject{
		Payer:   payer,
		Payee:   payee,
		RealmID: strings.TrimSpace(params.Realm),
	}
	metaBytes, err := parseMilestoneMeta(params.MetaHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	project.Metadata = metaBytes
	legs, err := parseMilestoneLegs(params.Legs)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	project.Legs = legs
	if params.Subscription != nil {
		project.Subscription = &escrow.MilestoneSubscription{
			IntervalSeconds: params.Subscription.IntervalSeconds,
			NextReleaseAt:   params.Subscription.NextReleaseAt,
			Active:          params.Subscription.Active,
		}
	}
	created, err := s.node.EscrowMilestoneCreate(project)
	if err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, milestoneCreateResult{ID: formatEscrowID(created.ID)})
}

func (s *Server) handleEscrowMilestoneGet(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	project, err := s.node.EscrowMilestoneGet(id)
	if err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatMilestoneJSON(project))
}

func (s *Server) handleEscrowMilestoneFund(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneLegActionParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.LegID == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "legId must be > 0")
		return
	}
	if err := s.node.EscrowMilestoneFund(id, params.LegID, caller); err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]string{"status": "funded"})
}

func (s *Server) handleEscrowMilestoneRelease(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneLegActionParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.LegID == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "legId must be > 0")
		return
	}
	if err := s.node.EscrowMilestoneRelease(id, params.LegID, caller); err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]string{"status": "released"})
}

func (s *Server) handleEscrowMilestoneCancel(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneLegActionParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.LegID == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "legId must be > 0")
		return
	}
	if err := s.node.EscrowMilestoneCancel(id, params.LegID, caller); err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]string{"status": "cancelled"})
}

func (s *Server) handleEscrowMilestoneSubscriptionUpdate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params milestoneSubscriptionUpdateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	project, err := s.node.EscrowMilestoneSubscriptionUpdate(id, caller, params.Active)
	if err != nil {
		writeMilestoneError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatMilestoneJSON(project))
}

func parseMilestoneMeta(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		return nil, fmt.Errorf("meta must be 0x-prefixed")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned)%2 != 0 {
		return nil, fmt.Errorf("meta hex length must be even")
	}
	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return nil, err
	}
	if len(decoded) > 96 {
		return nil, fmt.Errorf("meta must be <= 96 bytes")
	}
	return decoded, nil
}

func parseMilestoneLegs(input []milestoneLegParam) ([]*escrow.MilestoneLeg, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("at least one leg required")
	}
	legs := make([]*escrow.MilestoneLeg, 0, len(input))
	seen := make(map[uint64]struct{})
	now := time.Now().Unix()
	for _, leg := range input {
		if leg.ID == 0 {
			return nil, fmt.Errorf("leg id must be > 0")
		}
		if _, ok := seen[leg.ID]; ok {
			return nil, fmt.Errorf("duplicate leg id %d", leg.ID)
		}
		seen[leg.ID] = struct{}{}
		legType, err := parseMilestoneLegType(leg.Type)
		if err != nil {
			return nil, err
		}
		token := strings.ToUpper(strings.TrimSpace(leg.Token))
		if token == "" {
			return nil, fmt.Errorf("leg token required")
		}
		amount, err := parsePositiveBigInt(leg.Amount)
		if err != nil {
			return nil, err
		}
		if leg.Deadline <= now-deadlineSkewSeconds {
			return nil, fmt.Errorf("leg deadline must be in the future")
		}
		legs = append(legs, &escrow.MilestoneLeg{
			ID:          leg.ID,
			Type:        legType,
			Title:       strings.TrimSpace(leg.Title),
			Description: strings.TrimSpace(leg.Description),
			Token:       token,
			Amount:      amount,
			Deadline:    leg.Deadline,
			Status:      escrow.MilestoneLegPending,
		})
	}
	return legs, nil
}

func parseMilestoneLegType(value string) (escrow.MilestoneLegType, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "deliverable":
		return escrow.MilestoneLegTypeDeliverable, nil
	case "timebox", "subscription":
		return escrow.MilestoneLegTypeTimebox, nil
	default:
		return escrow.MilestoneLegTypeUnspecified, fmt.Errorf("unsupported leg type %s", value)
	}
}

func formatMilestoneJSON(project *escrow.MilestoneProject) milestoneProjectJSON {
	if project == nil {
		return milestoneProjectJSON{}
	}
	legs := make([]milestoneLegJSON, 0, len(project.Legs))
	for _, leg := range project.Legs {
		if leg == nil {
			continue
		}
		legs = append(legs, milestoneLegJSON{
			ID:       leg.ID,
			Type:     formatMilestoneLegType(leg.Type),
			Title:    leg.Title,
			Token:    leg.Token,
			Amount:   leg.Amount.String(),
			Deadline: leg.Deadline,
			Status:   formatMilestoneLegStatus(leg.Status),
		})
	}
	meta := ""
	if len(project.Metadata) > 0 {
		meta = "0x" + hex.EncodeToString(project.Metadata)
	}
	var subscription *milestoneSubscriptionParam
	if project.Subscription != nil {
		subscription = &milestoneSubscriptionParam{
			IntervalSeconds: project.Subscription.IntervalSeconds,
			NextReleaseAt:   project.Subscription.NextReleaseAt,
			Active:          project.Subscription.Active,
		}
	}
	return milestoneProjectJSON{
		ID:           formatEscrowID(project.ID),
		Payer:        formatAddress(project.Payer),
		Payee:        formatAddress(project.Payee),
		Realm:        project.RealmID,
		Status:       formatMilestoneStatus(project.Status),
		CreatedAt:    project.CreatedAt,
		UpdatedAt:    project.UpdatedAt,
		Legs:         legs,
		Meta:         meta,
		Subscription: subscription,
	}
}

func formatMilestoneStatus(status escrow.MilestoneStatus) string {
	switch status {
	case escrow.MilestoneStatusDraft:
		return "draft"
	case escrow.MilestoneStatusActive:
		return "active"
	case escrow.MilestoneStatusCompleted:
		return "completed"
	case escrow.MilestoneStatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

func formatMilestoneLegStatus(status escrow.MilestoneLegStatus) string {
	switch status {
	case escrow.MilestoneLegPending:
		return "pending"
	case escrow.MilestoneLegFunded:
		return "funded"
	case escrow.MilestoneLegReleased:
		return "released"
	case escrow.MilestoneLegCancelled:
		return "cancelled"
	case escrow.MilestoneLegExpired:
		return "expired"
	default:
		return "unknown"
	}
}

func formatMilestoneLegType(t escrow.MilestoneLegType) string {
	switch t {
	case escrow.MilestoneLegTypeDeliverable:
		return "deliverable"
	case escrow.MilestoneLegTypeTimebox:
		return "timebox"
	default:
		return "unknown"
	}
}

func writeMilestoneError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeEscrowInternal
	message := "internal_error"
	data := err.Error()
	switch {
	case strings.Contains(err.Error(), core.ErrEscrowNotFound.Error()):
		status = http.StatusNotFound
		code = codeEscrowNotFound
		message = "not_found"
	case strings.Contains(strings.ToLower(err.Error()), "forbidden"):
		status = http.StatusForbidden
		code = codeEscrowForbidden
		message = "forbidden"
	case errors.Is(err, core.ErrMilestoneUnsupported):
		status = http.StatusConflict
		code = codeEscrowConflict
		message = "conflict"
	}
	writeError(w, status, id, code, message, data)
}

func formatAddress(addr [20]byte) string {
	if addr == ([20]byte{}) {
		return ""
	}
	return "0x" + hex.EncodeToString(addr[:])
}
