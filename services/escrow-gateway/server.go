package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	headerIdempotencyKey = "Idempotency-Key"
	maxRequestBody       = 1 << 20 // 1 MiB
)

// Server is the HTTP front-end for escrow interactions.
type Server struct {
	authenticator *Authenticator
	node          NodeClient
	store         *SQLiteStore
	queue         *WebhookQueue
	intents       *PayIntentBuilder
	nowFn         func() time.Time
}

func NewServer(auth *Authenticator, node NodeClient, store *SQLiteStore, queue *WebhookQueue, intents *PayIntentBuilder) *Server {
	if auth == nil {
		panic("authenticator required")
	}
	if node == nil {
		panic("node client required")
	}
	if store == nil {
		panic("sqlite store required")
	}
	if queue == nil {
		queue = NewWebhookQueue()
	}
	if intents == nil {
		intents = NewPayIntentBuilder()
	}
	return &Server{
		authenticator: auth,
		node:          node,
		store:         store,
		queue:         queue,
		intents:       intents,
		nowFn:         time.Now,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/escrow/create":
		s.handleEscrowCreate(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/escrow/"):
		s.handleEscrowGet(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleEscrowCreate(w http.ResponseWriter, r *http.Request) {
	body, err := s.readRequestBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	principal, err := s.authenticator.Authenticate(r, body)
	if err != nil {
		s.writeAuthError(w, err)
		s.audit(r.Context(), principal, r, body, http.StatusUnauthorized, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("missing Idempotency-Key header"))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(`{"error":"missing idempotency key"}`))
		return
	}
	requestHash := hashRequest(r.Method, canonicalRequestPath(r), body)
	if cached, cacheErr := s.store.LookupIdempotency(r.Context(), principal.APIKey, key, requestHash); cacheErr == nil && cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cached.Status)
		_, _ = w.Write(cached.Body)
		s.audit(r.Context(), principal, r, body, cached.Status, cached.Body)
		return
	} else if cacheErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(cacheErr, ErrIdempotencyMismatch) {
			status = http.StatusConflict
		}
		s.writeError(w, status, cacheErr)
		s.audit(r.Context(), principal, r, body, status, []byte(fmt.Sprintf(`{"error":"%s"}`, cacheErr.Error())))
		return
	}

	var req EscrowCreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if validationErr := validateEscrowCreate(req); validationErr != nil {
		s.writeError(w, http.StatusBadRequest, validationErr)
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, validationErr.Error())))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	created, err := s.node.EscrowCreate(ctx, req)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	intent, err := s.intents.Build(req.Token, req.Amount, created.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	resp := map[string]interface{}{
		"escrowId":  created.ID,
		"payIntent": intent,
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	if err := s.store.SaveIdempotency(r.Context(), principal.APIKey, key, requestHash, http.StatusCreated, payload); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	s.queue.Enqueue(WebhookEvent{Type: "escrow.created", EscrowID: created.ID, CreatedAt: s.nowFn()})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(payload)
	s.audit(r.Context(), principal, r, body, http.StatusCreated, payload)
}

func (s *Server) handleEscrowGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/escrow/")
	if id == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("escrow id required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	esc, err := s.node.EscrowGet(ctx, id)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		return
	}
	payload, err := json.Marshal(esc)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (s *Server) readRequestBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, maxRequestBody+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxRequestBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxRequestBody)
	}
	return data, nil
}

func (s *Server) writeError(w http.ResponseWriter, status int, err error) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	msg := strings.ReplaceAll(err.Error(), "\"", "'")
	payload := fmt.Sprintf(`{"error":"%s"}`, msg)
	_, _ = w.Write([]byte(payload))
}

func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	payload := fmt.Sprintf(`{"error":"%s"}`, strings.ReplaceAll(err.Error(), "\"", "'"))
	_, _ = w.Write([]byte(payload))
}

func (s *Server) audit(ctx context.Context, principal *Principal, r *http.Request, requestBody []byte, status int, responseBody []byte) {
	apiKey := ""
	if principal != nil {
		apiKey = principal.APIKey
	}
	entry := AuditEntry{
		APIKey:         apiKey,
		Method:         r.Method,
		Path:           canonicalRequestPath(r),
		RequestBody:    append([]byte(nil), requestBody...),
		ResponseBody:   append([]byte(nil), responseBody...),
		ResponseStatus: status,
		Timestamp:      s.nowFn().UTC(),
	}
	_ = s.store.InsertAuditLog(ctx, entry)
}

func validateEscrowCreate(req EscrowCreateRequest) error {
	if strings.TrimSpace(req.Payer) == "" {
		return errors.New("payer is required")
	}
	if strings.TrimSpace(req.Payee) == "" {
		return errors.New("payee is required")
	}
	if strings.TrimSpace(req.Token) == "" {
		return errors.New("token is required")
	}
	if strings.TrimSpace(req.Amount) == "" {
		return errors.New("amount is required")
	}
	if req.Deadline == 0 {
		return errors.New("deadline is required")
	}
	return nil
}

func hashRequest(method, path string, body []byte) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{strings.ToUpper(method), path, string(body)}, "\n")))
	return fmt.Sprintf("%x", sum[:])
}
