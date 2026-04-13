package payoutd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// AdminServer exposes HTTP endpoints for operator controls.
type AdminServer struct {
	processor *Processor
	mux       *http.ServeMux
	auth      *Authenticator
}

// NewAdminServer constructs a server wrapping the provided processor.
func NewAdminServer(processor *Processor, auth *Authenticator) *AdminServer {
	mux := http.NewServeMux()
	server := &AdminServer{processor: processor, mux: mux, auth: auth}
	mux.Handle("/pause", server.requireAuth(http.HandlerFunc(server.handlePause)))
	mux.Handle("/resume", server.requireAuth(http.HandlerFunc(server.handleResume)))
	mux.Handle("/abort", server.requireAuth(http.HandlerFunc(server.handleAbort)))
	mux.Handle("/status", server.requireAuth(http.HandlerFunc(server.handleStatus)))
	mux.Handle("/executions", server.requireAuth(http.HandlerFunc(server.handleExecutions)))
	mux.Handle("/holds", server.requireAuth(http.HandlerFunc(server.handleHolds)))
	mux.Handle("/holds/release", server.requireAuth(http.HandlerFunc(server.handleHoldRelease)))
	mux.Handle("/treasury/reconcile", server.requireAuth(http.HandlerFunc(server.handleTreasuryReconcile)))
	mux.Handle("/treasury/sweep-plan", server.requireAuth(http.HandlerFunc(server.handleTreasurySweepPlan)))
	mux.Handle("/treasury/instructions", server.requireAuth(http.HandlerFunc(server.handleTreasuryInstructions)))
	mux.Handle("/treasury/instructions/review", server.requireAuth(http.HandlerFunc(server.handleTreasuryInstructionReview)))
	return server
}

// ServeHTTP implements http.Handler.
func (s *AdminServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *AdminServer) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.processor.Pause()
	w.WriteHeader(http.StatusNoContent)
}

func (s *AdminServer) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.processor.Resume()
	w.WriteHeader(http.StatusNoContent)
}

type abortRequest struct {
	IntentID string `json:"intent_id"`
	Reason   string `json:"reason"`
}

func (s *AdminServer) handleAbort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req abortRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.processor.Abort(r.Context(), req.IntentID, req.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *AdminServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := s.processor.Status()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func (s *AdminServer) handleExecutions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := parseOptionalInt(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	items, err := s.processor.ListPayoutExecutions(r.URL.Query().Get("status"), r.URL.Query().Get("asset"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

type holdRequest struct {
	Scope     string `json:"scope"`
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	CreatedBy string `json:"created_by"`
}

func (s *AdminServer) handleHolds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		activeOnly := r.URL.Query().Get("active") == "true"
		items, err := s.processor.ListHolds(r.URL.Query().Get("scope"), activeOnly)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	case http.MethodPost:
		var req holdRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		record, err := s.processor.CreateHold(HoldScope(req.Scope), req.Value, req.Reason, req.CreatedBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(record)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type holdReleaseRequest struct {
	ID    string `json:"id"`
	Actor string `json:"actor"`
	Notes string `json:"notes"`
}

func (s *AdminServer) handleHoldRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req holdReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	record, err := s.processor.ReleaseHold(req.ID, req.Actor, req.Notes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(record)
}

func (s *AdminServer) handleTreasuryReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot, err := s.processor.TreasurySnapshot(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshot)
}

func (s *AdminServer) handleTreasurySweepPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	plan, err := s.processor.TreasurySweepPlan(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(plan)
}

type treasuryInstructionRequest struct {
	Action      string `json:"action"`
	Asset       string `json:"asset"`
	Amount      string `json:"amount"`
	RequestedBy string `json:"requested_by"`
	Notes       string `json:"notes"`
}

func (s *AdminServer) handleTreasuryInstructions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.processor.ListTreasuryInstructions(r.URL.Query().Get("status"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	case http.MethodPost:
		var req treasuryInstructionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		amount, err := parseDecimal(req.Amount)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		instruction, err := s.processor.CreateTreasuryInstruction(TreasuryInstructionRequest{
			Action:      req.Action,
			Asset:       req.Asset,
			Amount:      amount,
			RequestedBy: req.RequestedBy,
			Notes:       req.Notes,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(instruction)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type treasuryInstructionReviewRequest struct {
	ID     string `json:"id"`
	Actor  string `json:"actor"`
	Notes  string `json:"notes"`
	Reject bool   `json:"reject"`
}

func (s *AdminServer) handleTreasuryInstructionReview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req treasuryInstructionReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	instruction, err := s.processor.ReviewTreasuryInstruction(TreasuryInstructionDecision{
		ID:     req.ID,
		Actor:  req.Actor,
		Notes:  req.Notes,
		Reject: req.Reject,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(instruction)
}

func (s *AdminServer) requireAuth(next http.Handler) http.Handler {
	if s.auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		})
	}
	return s.auth.Middleware(next)
}

func parseOptionalInt(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	return value, nil
}
