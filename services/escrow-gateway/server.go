package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbcrypto "nhbchain/crypto"
	escrowpkg "nhbchain/native/escrow"
)

const (
	headerIdempotencyKey = "Idempotency-Key"
	headerWalletAddress  = "X-Sig-Addr"
	headerWalletSig      = "X-Sig"
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
	case r.Method == http.MethodPost && r.URL.Path == "/escrow/release":
		s.handleEscrowTransition(w, r, escrowTransitionRelease)
	case r.Method == http.MethodPost && r.URL.Path == "/escrow/refund":
		s.handleEscrowTransition(w, r, escrowTransitionRefund)
	case r.Method == http.MethodPost && r.URL.Path == "/escrow/dispute":
		s.handleEscrowTransition(w, r, escrowTransitionDispute)
	case r.Method == http.MethodPost && r.URL.Path == "/escrow/resolve":
		s.handleEscrowResolve(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/escrow/"):
		s.handleEscrowGet(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/p2p/offers":
		s.handleCreateOffer(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/p2p/offers":
		s.handleListOffers(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/p2p/accept":
		s.handleAcceptOffer(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/p2p/trades/"):
		s.handleGetTrade(w, r)
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

type escrowTransitionKind int

const (
	escrowTransitionRelease escrowTransitionKind = iota + 1
	escrowTransitionRefund
	escrowTransitionDispute
)

func (s *Server) handleEscrowTransition(w http.ResponseWriter, r *http.Request, kind escrowTransitionKind) {
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

	var req EscrowActionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if strings.TrimSpace(req.EscrowID) == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("escrowId is required"))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(`{"error":"missing escrowId"}`))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	esc, err := s.node.EscrowGet(ctx, req.EscrowID)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	allowed := allowedSignersForTransition(kind, esc)
	signer, sigErr := s.verifyWalletSignature(r, body, req.EscrowID, allowed)
	if sigErr != nil {
		s.writeError(w, http.StatusForbidden, sigErr)
		s.audit(r.Context(), principal, r, body, http.StatusForbidden, []byte(fmt.Sprintf(`{"error":"%s"}`, sigErr.Error())))
		return
	}

	var callErr error
	switch kind {
	case escrowTransitionRelease:
		callErr = s.node.EscrowRelease(ctx, req.EscrowID, signer)
	case escrowTransitionRefund:
		callErr = s.node.EscrowRefund(ctx, req.EscrowID, signer)
	case escrowTransitionDispute:
		callErr = s.node.EscrowDispute(ctx, req.EscrowID, signer)
	default:
		callErr = errors.New("unsupported transition")
	}
	if callErr != nil {
		s.writeError(w, http.StatusBadGateway, callErr)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, callErr.Error())))
		return
	}

	payload := []byte(`{"queued":true}`)
	if kind == escrowTransitionDispute {
		payload = []byte(`{"ok":true}`)
	}
	if err := s.store.SaveIdempotency(r.Context(), principal.APIKey, key, requestHash, http.StatusAccepted, payload); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(payload)
	s.audit(r.Context(), principal, r, body, http.StatusAccepted, payload)
}

func allowedSignersForTransition(kind escrowTransitionKind, esc *EscrowState) []string {
	if esc == nil {
		return nil
	}
	payers := []string{}
	switch kind {
	case escrowTransitionRelease:
		payers = append(payers, esc.Payee)
		if esc.Mediator != nil {
			payers = append(payers, *esc.Mediator)
		}
	case escrowTransitionRefund:
		payers = append(payers, esc.Payer)
	case escrowTransitionDispute:
		payers = append(payers, esc.Payer, esc.Payee)
	}
	return payers
}

func (s *Server) handleEscrowResolve(w http.ResponseWriter, r *http.Request) {
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

	var req EscrowResolveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if strings.TrimSpace(req.EscrowID) == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("escrowId is required"))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(`{"error":"missing escrowId"}`))
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(req.Outcome))
	if outcome != "release" && outcome != "refund" {
		s.writeError(w, http.StatusBadRequest, errors.New("outcome must be release or refund"))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(`{"error":"invalid outcome"}`))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	esc, err := s.node.EscrowGet(ctx, req.EscrowID)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	allowed := []string{esc.Payer, esc.Payee}
	if esc.Mediator != nil {
		allowed = append(allowed, *esc.Mediator)
	}
	signer, sigErr := s.verifyWalletSignature(r, body, req.EscrowID, allowed)
	if sigErr != nil {
		s.writeError(w, http.StatusForbidden, sigErr)
		s.audit(r.Context(), principal, r, body, http.StatusForbidden, []byte(fmt.Sprintf(`{"error":"%s"}`, sigErr.Error())))
		return
	}

	if err := s.node.EscrowResolve(ctx, req.EscrowID, signer, outcome); err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	payload := []byte(`{"queued":true}`)
	if err := s.store.SaveIdempotency(r.Context(), principal.APIKey, key, requestHash, http.StatusAccepted, payload); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(payload)
	s.audit(r.Context(), principal, r, body, http.StatusAccepted, payload)
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

func (s *Server) verifyWalletSignature(r *http.Request, body []byte, resourceID string, allowed []string) (string, error) {
	sigAddr := strings.TrimSpace(r.Header.Get(headerWalletAddress))
	if sigAddr == "" {
		return "", errors.New("missing X-Sig-Addr header")
	}
	sigHex := strings.TrimSpace(r.Header.Get(headerWalletSig))
	if sigHex == "" {
		return "", errors.New("missing X-Sig header")
	}
	timestamp := strings.TrimSpace(r.Header.Get(headerTimestamp))
	if timestamp == "" {
		return "", errors.New("missing X-Timestamp header")
	}
	payload := strings.Join([]string{strings.ToUpper(r.Method), canonicalRequestPath(r), string(body), timestamp, strings.ToLower(strings.TrimSpace(resourceID))}, "|")
	msgHash := ethcrypto.Keccak256([]byte(payload))
	digest := accounts.TextHash(msgHash)

	cleanedSig := strings.TrimPrefix(strings.TrimPrefix(sigHex, "0x"), "0X")
	sigBytes, err := hexutil.Decode("0x" + cleanedSig)
	if err != nil {
		return "", fmt.Errorf("invalid signature encoding: %w", err)
	}
	if len(sigBytes) != 65 {
		return "", fmt.Errorf("signature must be 65 bytes, got %d", len(sigBytes))
	}
	if sigBytes[64] >= 27 {
		sigBytes[64] -= 27
	}
	pubKey, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return "", fmt.Errorf("signature verification failed: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey).Bytes()
	decodedAddr, err := nhbcrypto.DecodeAddress(sigAddr)
	if err != nil {
		return "", fmt.Errorf("invalid signer address: %w", err)
	}
	if subtle.ConstantTimeCompare(recovered, decodedAddr.Bytes()) != 1 {
		return "", errors.New("signature does not match supplied address")
	}
	if len(allowed) == 0 {
		return sigAddr, nil
	}
	for _, candidate := range allowed {
		if strings.EqualFold(candidate, sigAddr) {
			return sigAddr, nil
		}
	}
	return "", errors.New("signer is not authorised for this action")
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
	if req.Nonce == 0 {
		return errors.New("nonce is required")
	}
	if trimmed := strings.TrimSpace(req.Realm); len(trimmed) > 64 {
		return errors.New("realm must be <= 64 characters")
	}
	return nil
}

func hashRequest(method, path string, body []byte) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{strings.ToUpper(method), path, string(body)}, "\n")))
	return fmt.Sprintf("%x", sum[:])
}

// --- P2P Offer + Trade handlers ---

func (s *Server) handleCreateOffer(w http.ResponseWriter, r *http.Request) {
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

	var req P2POfferRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if err := validateOfferRequest(req); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}

	offerID, err := generateOfferID()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	signer, sigErr := s.verifyWalletSignature(r, body, "", []string{req.Seller})
	if sigErr != nil {
		s.writeError(w, http.StatusForbidden, sigErr)
		s.audit(r.Context(), principal, r, body, http.StatusForbidden, []byte(fmt.Sprintf(`{"error":"%s"}`, sigErr.Error())))
		return
	}
	_ = signer // ensures signature validated even if we do not use return value further

	offer := P2POffer{
		ID:          offerID,
		Seller:      req.Seller,
		BaseToken:   strings.ToUpper(strings.TrimSpace(req.BaseToken)),
		BaseAmount:  req.BaseAmount,
		QuoteToken:  strings.ToUpper(strings.TrimSpace(req.QuoteToken)),
		QuoteAmount: req.QuoteAmount,
		MinQuote:    req.MinQuote,
		MaxQuote:    req.MaxQuote,
		Terms:       req.Terms,
		Active:      true,
		CreatedAt:   s.nowFn().UTC(),
	}
	if err := s.store.InsertOffer(r.Context(), offer); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	payload, err := json.Marshal(offer)
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(payload)
	s.audit(r.Context(), principal, r, body, http.StatusCreated, payload)
}

func (s *Server) handleListOffers(w http.ResponseWriter, r *http.Request) {
	offers, err := s.store.ListOffers(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload, err := json.Marshal(offers)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (s *Server) handleAcceptOffer(w http.ResponseWriter, r *http.Request) {
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

	var req P2PAcceptRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err))
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if err := validateAcceptRequest(req, s.nowFn()); err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	offer, err := s.store.GetOffer(r.Context(), req.OfferID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err)
		s.audit(r.Context(), principal, r, body, http.StatusNotFound, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if !offer.Active {
		s.writeError(w, http.StatusConflict, errors.New("offer is inactive"))
		s.audit(r.Context(), principal, r, body, http.StatusConflict, []byte(`{"error":"offer inactive"}`))
		return
	}
	signer, sigErr := s.verifyWalletSignature(r, body, offer.ID, []string{req.Buyer})
	if sigErr != nil {
		s.writeError(w, http.StatusForbidden, sigErr)
		s.audit(r.Context(), principal, r, body, http.StatusForbidden, []byte(fmt.Sprintf(`{"error":"%s"}`, sigErr.Error())))
		return
	}
	_ = signer

	quoteAmount := offer.QuoteAmount
	if strings.TrimSpace(req.QuoteAmount) != "" {
		if !amountEquals(req.QuoteAmount, offer.QuoteAmount) {
			s.writeError(w, http.StatusBadRequest, errors.New("custom quoteAmount not supported for this offer"))
			s.audit(r.Context(), principal, r, body, http.StatusBadRequest, []byte(`{"error":"unsupported amount"}`))
			return
		}
		quoteAmount = req.QuoteAmount
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	nodeReq := P2PAcceptRequest{
		OfferID:     offer.ID,
		Buyer:       req.Buyer,
		Seller:      offer.Seller,
		BaseToken:   offer.BaseToken,
		BaseAmount:  offer.BaseAmount,
		QuoteToken:  offer.QuoteToken,
		QuoteAmount: quoteAmount,
		Deadline:    req.Deadline,
		SlippageBps: req.SlippageBps,
	}
	nodeResp, err := s.node.P2PCreateTrade(ctx, nodeReq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, err)
		s.audit(r.Context(), principal, r, body, http.StatusBadGateway, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	now := s.nowFn().UTC()
	trade := P2PTrade{
		ID:            nodeResp.TradeID,
		OfferID:       offer.ID,
		Buyer:         req.Buyer,
		Seller:        offer.Seller,
		BaseToken:     offer.BaseToken,
		BaseAmount:    offer.BaseAmount,
		QuoteToken:    offer.QuoteToken,
		QuoteAmount:   quoteAmount,
		EscrowBaseID:  nodeResp.EscrowBaseID,
		EscrowQuoteID: nodeResp.EscrowQuoteID,
		Status:        "created",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.InsertTrade(r.Context(), trade); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if err := s.store.LinkEscrowToTrade(r.Context(), nodeResp.EscrowBaseID, trade.ID); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	if err := s.store.LinkEscrowToTrade(r.Context(), nodeResp.EscrowQuoteID, trade.ID); err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		s.audit(r.Context(), principal, r, body, http.StatusInternalServerError, []byte(fmt.Sprintf(`{"error":"%s"}`, err.Error())))
		return
	}
	respBody := map[string]interface{}{
		"tradeId":       nodeResp.TradeID,
		"escrowBaseId":  nodeResp.EscrowBaseID,
		"escrowQuoteId": nodeResp.EscrowQuoteID,
		"payIntents":    nodeResp.PayIntents,
	}
	payload, err := json.Marshal(respBody)
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(payload)
	s.audit(r.Context(), principal, r, body, http.StatusCreated, payload)
}

func (s *Server) handleGetTrade(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/p2p/trades/")
	if strings.TrimSpace(id) == "" {
		s.writeError(w, http.StatusBadRequest, errors.New("trade id required"))
		return
	}
	trade, err := s.store.GetTrade(r.Context(), id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err)
		return
	}
	payload, err := json.Marshal(trade)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func validateOfferRequest(req P2POfferRequest) error {
	if strings.TrimSpace(req.Seller) == "" {
		return errors.New("seller is required")
	}
	if _, err := nhbcrypto.DecodeAddress(req.Seller); err != nil {
		return fmt.Errorf("invalid seller address: %w", err)
	}
	if _, err := escrowpkg.NormalizeToken(req.BaseToken); err != nil {
		return fmt.Errorf("invalid baseToken: %w", err)
	}
	if _, err := escrowpkg.NormalizeToken(req.QuoteToken); err != nil {
		return fmt.Errorf("invalid quoteToken: %w", err)
	}
	if err := requirePositiveBigInt(req.BaseAmount); err != nil {
		return fmt.Errorf("baseAmount %w", err)
	}
	if err := requirePositiveBigInt(req.QuoteAmount); err != nil {
		return fmt.Errorf("quoteAmount %w", err)
	}
	if strings.TrimSpace(req.MinQuote) != "" {
		if err := requirePositiveBigInt(req.MinQuote); err != nil {
			return fmt.Errorf("minAmount %w", err)
		}
	}
	if strings.TrimSpace(req.MaxQuote) != "" {
		if err := requirePositiveBigInt(req.MaxQuote); err != nil {
			return fmt.Errorf("maxAmount %w", err)
		}
	}
	return nil
}

func validateAcceptRequest(req P2PAcceptRequestBody, now time.Time) error {
	if strings.TrimSpace(req.OfferID) == "" {
		return errors.New("offerId is required")
	}
	if strings.TrimSpace(req.Buyer) == "" {
		return errors.New("buyer is required")
	}
	if _, err := nhbcrypto.DecodeAddress(req.Buyer); err != nil {
		return fmt.Errorf("invalid buyer address: %w", err)
	}
	if req.Deadline <= now.Unix() {
		return errors.New("deadline must be in the future")
	}
	if strings.TrimSpace(req.QuoteAmount) != "" {
		if err := requirePositiveBigInt(req.QuoteAmount); err != nil {
			return fmt.Errorf("quoteAmount %w", err)
		}
	}
	if req.SlippageBps > 10_000 {
		return errors.New("slippageBps must be <= 10000")
	}
	return nil
}

func requirePositiveBigInt(v string) error {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return errors.New("must be provided")
	}
	amount, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return errors.New("must be a base-10 integer")
	}
	if amount.Sign() <= 0 {
		return errors.New("must be greater than zero")
	}
	return nil
}

func amountEquals(a, b string) bool {
	aa, okA := new(big.Int).SetString(strings.TrimSpace(a), 10)
	bb, okB := new(big.Int).SetString(strings.TrimSpace(b), 10)
	if !okA || !okB {
		return false
	}
	return aa.Cmp(bb) == 0
}

func generateOfferID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "OFF_" + strings.ToUpper(hex.EncodeToString(buf)), nil
}

type EscrowActionRequest struct {
	EscrowID string `json:"escrowId"`
	Reason   string `json:"reason,omitempty"`
}

type EscrowResolveRequest struct {
	EscrowID string `json:"escrowId"`
	Outcome  string `json:"outcome"`
}

type P2POfferRequest struct {
	Seller      string `json:"seller"`
	BaseToken   string `json:"baseToken"`
	BaseAmount  string `json:"baseAmount"`
	QuoteToken  string `json:"quoteToken"`
	QuoteAmount string `json:"quoteAmount"`
	MinQuote    string `json:"minAmount,omitempty"`
	MaxQuote    string `json:"maxAmount,omitempty"`
	Terms       string `json:"terms,omitempty"`
}

type P2PAcceptRequestBody struct {
	OfferID     string `json:"offerId"`
	Buyer       string `json:"buyer"`
	QuoteAmount string `json:"quoteAmount,omitempty"`
	Deadline    int64  `json:"deadline"`
	SlippageBps uint32 `json:"slippageBps,omitempty"`
}
