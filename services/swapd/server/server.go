package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/services/swapd/priceproof"
	"nhbchain/services/swapd/settlement"
	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

// Config defines HTTP server parameters.
type Config struct {
	ListenAddress string
	PolicyID      string
	TLS           TLSConfig
}

// TLSConfig describes TLS settings for the admin server.
type TLSConfig struct {
	Disabled bool
	CertFile string
	KeyFile  string
	Config   *tls.Config
}

// StableRuntime configures the optional stable engine wiring.
type StableRuntime struct {
	Enabled    bool
	Engine     *stable.Engine
	Limits     stable.Limits
	Assets     []stable.Asset
	Now        func() time.Time
	Partners   []Partner
	Settlement *settlement.Manager
}

// Server hosts admin and health endpoints for swapd.
type Server struct {
	cfg         Config
	storage     *storage.Storage
	policyMu    sync.RWMutex
	policy      storage.Policy
	logger      *log.Logger
	adminAuth   *Authenticator
	partnerAuth *PartnerAuthenticator

	tls struct {
		disabled bool
		certFile string
		keyFile  string
		config   *tls.Config
	}

	stable struct {
		enabled bool
		engine  *stable.Engine
		limits  stable.Limits
		assets  map[string]stable.Asset
	}
	stableNow  func() time.Time
	settlement *settlement.Manager

	// reservationOwners maps a live reservation ID to the partner ID that
	// created it via /v1/stable/reserve. The stable engine itself has no
	// concept of partners at all (by design -- see StableRuntime), so
	// nothing at that layer stops one authenticated partner from cashing
	// out a reservation another partner created, since reservation IDs are
	// returned in the reserve response and are not inherently secret. This
	// map closes that gap at the HTTP layer, where partner identity is
	// actually available: handleStableReserve records ownership,
	// handleStableCashOut checks it before ever calling into the engine.
	reservationOwnersMu sync.Mutex
	reservationOwners   map[string]string

	// priceProofService/priceProofAuth back the optional POST
	// /v1/price-proof endpoint (see SetPriceProofRuntime). priceProofAuth is
	// deliberately a SEPARATE PartnerAuthenticator from partnerAuth -- an
	// operator can grant a caller price-proof access without also granting
	// it stable-engine trading access, and the endpoint fails closed
	// (unlike requirePartner's anonymous fallback) if it is ever enabled
	// without partner credentials configured.
	priceProofService *priceproof.Service
	priceProofAuth    *PartnerAuthenticator
}

// PriceProofRuntime configures the optional price-proof signing endpoint.
type PriceProofRuntime struct {
	Service  *priceproof.Service
	Partners []Partner
}

// SetPriceProofRuntime wires the price-proof signing endpoint (POST
// /v1/price-proof) into the server. It must be called before Run. Partners
// must be non-empty -- this endpoint gates real ZNHB minting downstream (via
// TxTypeSwapVoucherMint's mandatory price-proof signature check), so it must
// never silently run unauthenticated.
func (s *Server) SetPriceProofRuntime(rt PriceProofRuntime) error {
	if s == nil {
		return fmt.Errorf("server not configured")
	}
	if rt.Service == nil {
		return nil
	}
	if len(rt.Partners) == 0 {
		return fmt.Errorf("price proof runtime requires partner configuration")
	}
	auth, err := NewPartnerAuthenticator(rt.Partners, nil, s.storage)
	if err != nil {
		return fmt.Errorf("configure price proof partner auth: %w", err)
	}
	if err := auth.Hydrate(context.Background()); err != nil && s.logger != nil {
		s.logger.Printf("swapd: hydrate price proof partner auth: %v", err)
	}
	s.priceProofAuth = auth
	s.priceProofService = rt.Service
	return nil
}

// New constructs a new HTTP server.
func New(cfg Config, store *storage.Storage, logger *log.Logger, stableRuntime StableRuntime, auth *Authenticator) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("storage required")
	}
	if auth == nil {
		return nil, fmt.Errorf("admin authenticator required")
	}
	if logger == nil {
		logger = log.Default()
	}
	if strings.TrimSpace(cfg.PolicyID) == "" {
		cfg.PolicyID = "default"
	}
	srv := &Server{cfg: cfg, storage: store, logger: logger, adminAuth: auth, reservationOwners: make(map[string]string)}
	srv.tls.disabled = cfg.TLS.Disabled
	srv.tls.certFile = strings.TrimSpace(cfg.TLS.CertFile)
	srv.tls.keyFile = strings.TrimSpace(cfg.TLS.KeyFile)
	srv.tls.config = cfg.TLS.Config
	srv.stableNow = stableRuntime.Now
	if srv.stableNow == nil {
		srv.stableNow = time.Now
	}
	srv.stable.assets = make(map[string]stable.Asset)
	if stableRuntime.Engine != nil && stableRuntime.Enabled {
		srv.stable.enabled = true
		srv.stable.engine = stableRuntime.Engine
		srv.stable.limits = stableRuntime.Limits
		for _, asset := range stableRuntime.Assets {
			symbol := strings.ToUpper(strings.TrimSpace(asset.Symbol))
			if symbol == "" {
				continue
			}
			srv.stable.assets[symbol] = asset
		}
		if len(stableRuntime.Partners) == 0 {
			return nil, fmt.Errorf("stable runtime requires partner configuration")
		}
		partnerAuth, err := NewPartnerAuthenticator(stableRuntime.Partners, nil, store)
		if err != nil {
			return nil, fmt.Errorf("configure partner auth: %w", err)
		}
		if err := partnerAuth.Hydrate(context.Background()); err != nil && logger != nil {
			logger.Printf("swapd: hydrate partner auth: %v", err)
		}
		srv.partnerAuth = partnerAuth
		srv.settlement = stableRuntime.Settlement
	}
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
	mux.Handle("/admin/policy", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handlePolicy)), "swapd.policy"))
	mux.Handle("/admin/throttle/check", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleThrottleCheck)), "swapd.throttle"))
	mux.Handle("/admin/audit/events", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleAuditEvents)), "swapd.audit"))
	mux.Handle("GET /admin/settlements", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleListSettlements)), "swapd.settlements.list"))
	mux.Handle("POST /admin/settlements/{id}/confirm", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleConfirmSettlement)), "swapd.settlements.confirm"))
	mux.Handle("POST /admin/settlements/{id}/retry", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleRetrySettlement)), "swapd.settlements.retry"))
	mux.Handle("POST /admin/settlements/{id}/fail", otelhttp.NewHandler(s.requireAdmin(http.HandlerFunc(s.handleFailSettlement)), "swapd.settlements.fail"))
	s.registerStableHandlers(mux)
	s.registerPriceProofHandlers(mux)

	srv := &http.Server{Addr: s.cfg.ListenAddress, Handler: mux, TLSConfig: s.tls.config}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	s.logger.Printf("swapd: http server listening on %s", s.cfg.ListenAddress)
	var err error
	if s.tls.disabled {
		err = srv.ListenAndServe()
	} else {
		err = srv.ListenAndServeTLS(s.tls.certFile, s.tls.keyFile)
	}
	if err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	if s.adminAuth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "authentication unavailable", http.StatusInternalServerError)
		})
	}
	return s.adminAuth.Middleware(next)
}

func (s *Server) requirePriceProofPartner(next http.Handler) http.Handler {
	if s.priceProofAuth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "price proof authentication unavailable", http.StatusInternalServerError)
		})
	}
	return s.priceProofAuth.Middleware(next)
}

func (s *Server) requirePartner(next http.Handler) http.Handler {
	if s.partnerAuth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), partnerContextKey{}, &PartnerPrincipal{ID: "anonymous"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	return s.partnerAuth.Middleware(next)
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
		Amount string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	amountStr := strings.TrimSpace(req.Amount)
	if amountStr == "" {
		http.Error(w, "amount required", http.StatusBadRequest)
		return
	}
	amount := new(big.Int)
	if _, ok := amount.SetString(amountStr, 10); !ok {
		http.Error(w, "invalid amount", http.StatusBadRequest)
		return
	}
	if amount.Sign() <= 0 {
		http.Error(w, "amount must be positive", http.StatusBadRequest)
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
		allowed, err = s.storage.CheckThrottle(r.Context(), policy.ID, storage.ActionMint, policy.MintLimit, policy.Window, amount, now)
	case "redeem":
		allowed, err = s.storage.CheckThrottle(r.Context(), policy.ID, storage.ActionRedeem, policy.RedeemLimit, policy.Window, amount, now)
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

func (s *Server) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.storage == nil {
		http.Error(w, "storage unavailable", http.StatusInternalServerError)
		return
	}
	query := r.URL.Query()
	partnerID := strings.TrimSpace(query.Get("partner_id"))
	limit := 100
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	events, err := s.storage.ListAuditEvents(r.Context(), partnerID, limit)
	if err != nil {
		s.logger.Printf("swapd: list audit events: %v", err)
		http.Error(w, "failed to load audit events", http.StatusInternalServerError)
		return
	}
	response := make([]map[string]any, 0, len(events))
	for _, event := range events {
		response = append(response, map[string]any{
			"id":          event.ID,
			"event_type":  event.EventType,
			"partner_id":  event.PartnerID,
			"subject_id":  event.SubjectID,
			"outcome":     event.Outcome,
			"detail":      json.RawMessage(event.Detail),
			"trace_id":    event.TraceID,
			"occurred_at": event.OccurredAt.UTC().Format(time.RFC3339),
		})
	}
	json.NewEncoder(w).Encode(map[string]any{"events": response})
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
