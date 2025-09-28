package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) registerStableHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/v1/stable/quote", s.handleStableNotImplemented)
	mux.HandleFunc("/v1/stable/reserve", s.handleStableNotImplemented)
	mux.HandleFunc("/v1/stable/cashout", s.handleStableNotImplemented)
	mux.HandleFunc("/v1/stable/status", s.handleStableNotImplemented)
	mux.HandleFunc("/v1/stable/limits", s.handleStableNotImplemented)
}

func (s *Server) handleStableNotImplemented(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(map[string]string{"error": "stable engine not enabled"})
}
