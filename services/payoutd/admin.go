package payoutd

import (
	"encoding/json"
	"net/http"
)

// AdminServer exposes HTTP endpoints for operator controls.
type AdminServer struct {
	processor *Processor
	mux       *http.ServeMux
}

// NewAdminServer constructs a server wrapping the provided processor.
func NewAdminServer(processor *Processor) *AdminServer {
	mux := http.NewServeMux()
	server := &AdminServer{processor: processor, mux: mux}
	mux.HandleFunc("/pause", server.handlePause)
	mux.HandleFunc("/resume", server.handleResume)
	mux.HandleFunc("/abort", server.handleAbort)
	mux.HandleFunc("/status", server.handleStatus)
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
