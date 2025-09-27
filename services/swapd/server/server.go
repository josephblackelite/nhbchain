package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/services/swapd/storage"
)

// Config defines HTTP server parameters.
type Config struct {
	ListenAddress string
	PolicyID      string
}

// Server hosts admin and health endpoints for swapd.
type Server struct {
	cfg      Config
	storage  *storage.Storage
	policyMu sync.RWMutex
	policy   storage.Policy
	logger   *log.Logger
}

// New constructs a new HTTP server.
func New(cfg Config, store *storage.Storage, logger *log.Logger) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("storage required")
	}
	if logger == nil {
		logger = log.Default()
	}
	if strings.TrimSpace(cfg.PolicyID) == "" {
		cfg.PolicyID = "default"
	}
	srv := &Server{cfg: cfg, storage: store, logger: logger}
	if policy, err := store.GetPolicy(context.Background(), cfg.PolicyID); err == nil {
		srv.setPolicy(policy)
	}
	return srv, nil
}

// Run starts the HTTP server and blocks until context cancellation.
func (s *Server) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("server not configured")
	}
	mux := http.NewServeMux()
	mux.Handle("/healthz", otelhttp.NewHandler(http.HandlerFunc(s.handleHealth), "swapd.health"))
	mux.Handle("/admin/policy", otelhttp.NewHandler(http.HandlerFunc(s.handlePolicy), "swapd.policy"))
	mux.Handle("/admin/throttle/check", otelhttp.NewHandler(http.HandlerFunc(s.handleThrottleCheck), "swapd.throttle"))

	srv := &http.Server{Addr: s.cfg.ListenAddress, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	s.logger.Printf("swapd: http server listening on %s", s.cfg.ListenAddress)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getPolicy(w, r)
	case http.MethodPut:
		s.putPolicy(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleThrottleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	policy := s.currentPolicy()
	now := time.Now()
	var (
		allowed bool
		err     error
	)
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "mint":
		allowed, err = s.storage.CheckThrottle(r.Context(), policy.ID, storage.ActionMint, policy.MintLimit, policy.Window, now)
	case "redeem":
		allowed, err = s.storage.CheckThrottle(r.Context(), policy.ID, storage.ActionRedeem, policy.RedeemLimit, policy.Window, now)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.logger.Printf("swapd: throttle error: %v", err)
		http.Error(w, "throttle error", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"allowed": allowed})
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	policy := s.currentPolicy()
	if policy.Window == 0 {
		// attempt load from storage
		stored, err := s.storage.GetPolicy(r.Context(), policy.ID)
		if err == nil {
			policy = stored
			s.setPolicy(policy)
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id":             policy.ID,
		"mint_limit":     policy.MintLimit,
		"redeem_limit":   policy.RedeemLimit,
		"window_seconds": int(policy.Window.Seconds()),
	})
}

func (s *Server) putPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MintLimit   int `json:"mint_limit"`
		RedeemLimit int `json:"redeem_limit"`
		Window      int `json:"window_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if req.Window <= 0 {
		http.Error(w, "window_seconds must be positive", http.StatusBadRequest)
		return
	}
	policy := storage.Policy{
		ID:          s.cfg.PolicyID,
		MintLimit:   req.MintLimit,
		RedeemLimit: req.RedeemLimit,
		Window:      time.Duration(req.Window) * time.Second,
	}
	if err := s.storage.SavePolicy(r.Context(), policy); err != nil {
		s.logger.Printf("swapd: save policy: %v", err)
		http.Error(w, "failed to persist policy", http.StatusInternalServerError)
		return
	}
	s.setPolicy(policy)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) currentPolicy() storage.Policy {
	s.policyMu.RLock()
	policy := s.policy
	s.policyMu.RUnlock()
	if policy.ID == "" {
		policy.ID = s.cfg.PolicyID
	}
	if policy.Window == 0 {
		policy.Window = time.Hour
	}
	return policy
}

func (s *Server) setPolicy(policy storage.Policy) {
	s.policyMu.Lock()
	s.policy = policy
	s.policyMu.Unlock()
}
