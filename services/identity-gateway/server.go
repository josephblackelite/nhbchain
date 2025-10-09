package identitygateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

const (
	maxBodyBytes        = 1 << 16 // 64 KiB
	headerAPIKey        = "X-API-Key"
	headerAPISignature  = "X-API-Signature"
	headerAPITimestamp  = "X-API-Timestamp"
	headerIdempotency   = "Idempotency-Key"
	defaultCodeTTL      = 10 * time.Minute
	defaultRateWindow   = time.Hour
	defaultRateAttempts = 5
	defaultSkew         = 5 * time.Minute
	defaultIdemTTL      = 24 * time.Hour
)

var (
	// ErrCodeMismatch indicates the supplied verification code does not match.
	ErrCodeMismatch = errors.New("verification code mismatch")
	// ErrCodeExpired indicates the verification code has expired.
	ErrCodeExpired = errors.New("verification code expired")
	// ErrRateLimited indicates the register endpoint has been throttled for the email hash.
	ErrRateLimited = errors.New("rate limit exceeded")
)

// apiKeySecret stores secret material for authenticated clients.
type apiKeySecret struct {
	Key    string
	Secret []byte
}

// Server implements the HTTP handlers for the identity gateway.
type Server struct {
	store            *Store
	emailer          Emailer
	keys             map[string]apiKeySecret
	emailSalt        []byte
	codeTTL          time.Duration
	registerWindow   time.Duration
	registerAttempts int
	timestampSkew    time.Duration
	idempotencyTTL   time.Duration
	nowFn            func() time.Time
	codeFn           func() (string, error)
}

// Config describes the runtime configuration for the server.
type Config struct {
	APIKeys          map[string]string
	EmailSalt        []byte
	CodeTTL          time.Duration
	RegisterWindow   time.Duration
	RegisterAttempts int
	TimestampSkew    time.Duration
	IdempotencyTTL   time.Duration
}

// NewServer constructs an HTTP server with the supplied dependencies.
func NewServer(store *Store, emailer Emailer, cfg Config) (*Server, error) {
	if store == nil {
		return nil, errors.New("store required")
	}
	if emailer == nil {
		return nil, errors.New("emailer required")
	}
	if len(cfg.EmailSalt) == 0 {
		return nil, errors.New("email salt required")
	}
	if len(cfg.APIKeys) == 0 {
		return nil, errors.New("at least one API key required")
	}
	secrets := make(map[string]apiKeySecret, len(cfg.APIKeys))
	for key, secret := range cfg.APIKeys {
		trimmedKey := strings.TrimSpace(key)
		trimmedSecret := strings.TrimSpace(secret)
		if trimmedKey == "" || trimmedSecret == "" {
			return nil, fmt.Errorf("invalid API key entry for %q", key)
		}
		secrets[trimmedKey] = apiKeySecret{Key: trimmedKey, Secret: []byte(trimmedSecret)}
	}
	server := &Server{
		store:            store,
		emailer:          emailer,
		keys:             secrets,
		emailSalt:        append([]byte(nil), cfg.EmailSalt...),
		codeTTL:          cfg.CodeTTL,
		registerWindow:   cfg.RegisterWindow,
		registerAttempts: cfg.RegisterAttempts,
		timestampSkew:    cfg.TimestampSkew,
		idempotencyTTL:   cfg.IdempotencyTTL,
		nowFn:            time.Now,
	}
	if server.codeTTL <= 0 {
		server.codeTTL = defaultCodeTTL
	}
	if server.registerWindow <= 0 {
		server.registerWindow = defaultRateWindow
	}
	if server.registerAttempts <= 0 {
		server.registerAttempts = defaultRateAttempts
	}
	if server.timestampSkew <= 0 {
		server.timestampSkew = defaultSkew
	}
	if server.idempotencyTTL <= 0 {
		server.idempotencyTTL = defaultIdemTTL
	}
	server.codeFn = server.randomCode
	return server, nil
}

func (s *Server) now() time.Time {
	if s.nowFn == nil {
		return time.Now().UTC()
	}
	return s.nowFn().UTC()
}

// ServeHTTP dispatches to the relevant handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/identity/email/register":
		s.handleRegister(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/identity/email/verify":
		s.handleVerify(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/identity/alias/bind-email":
		s.handleBindAlias(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	apiKey, err := s.authenticateRequest(r, body)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", err.Error(), nil)
		return
	}
	if resp, ok := s.tryReplay(r, apiKey.Key, body); ok {
		s.writeCachedResponse(w, resp)
		return
	}
	var req struct {
		Email     string `json:"email"`
		AliasHint string `json:"aliasHint"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", "invalid JSON payload", nil)
		return
	}
	normalized, err := normalizeEmail(req.Email)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	emailHash := computeEmailHash(normalized, s.emailSalt)
	code, err := s.codeFn()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to generate verification code", nil)
		return
	}
	codeDigest := hashVerificationCode(normalized, code, s.emailSalt)
	now := s.now()
	expiresAt := now.Add(s.codeTTL)
	_, err = s.store.MutateEmail(emailHash, true, func(rec *EmailRecord) error {
		// Prune stale attempts.
		cutoff := now.Add(-s.registerWindow)
		pruned := rec.Attempts[:0]
		for _, ts := range rec.Attempts {
			if ts.After(cutoff) {
				pruned = append(pruned, ts)
			}
		}
		if len(pruned) >= s.registerAttempts {
			return ErrRateLimited
		}
		rec.Attempts = append(pruned, now)
		rec.CodeDigest = codeDigest
		rec.CodeExpires = &expiresAt
		if rec.Bindings == nil {
			rec.Bindings = make(map[string]AliasBinding)
		}
		if rec.EmailHash == "" {
			rec.EmailHash = emailHash
		}
		return nil
	})
	if errors.Is(err, ErrRateLimited) {
		s.writeError(w, http.StatusTooManyRequests, "IDN-429", "too many verification attempts", map[string]any{"retryAfter": int(s.registerWindow.Seconds())})
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to persist verification state", nil)
		return
	}
	message := VerificationMessage{
		Email:     strings.TrimSpace(req.Email),
		Code:      code,
		AliasHint: strings.TrimSpace(req.AliasHint),
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}
	if err := s.emailer.SendVerification(r.Context(), message); err != nil {
		s.writeError(w, http.StatusBadGateway, "IDN-502", "failed to dispatch verification", nil)
		return
	}
	resp := map[string]any{
		"status":    "pending",
		"expiresIn": int(s.codeTTL.Seconds()),
	}
	s.persistAndWrite(w, r, apiKey.Key, resp, http.StatusOK)
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	apiKey, err := s.authenticateRequest(r, body)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", err.Error(), nil)
		return
	}
	if resp, ok := s.tryReplay(r, apiKey.Key, body); ok {
		s.writeCachedResponse(w, resp)
		return
	}
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", "invalid JSON payload", nil)
		return
	}
	normalized, err := normalizeEmail(req.Email)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		s.writeError(w, http.StatusBadRequest, "IDN-400", "code required", nil)
		return
	}
	emailHash := computeEmailHash(normalized, s.emailSalt)
	codeDigest := hashVerificationCode(normalized, strings.TrimSpace(req.Code), s.emailSalt)
	now := s.now()
	record, err := s.store.MutateEmail(emailHash, false, func(rec *EmailRecord) error {
		if rec.CodeDigest == "" || rec.CodeExpires == nil {
			return ErrCodeMismatch
		}
		if now.After(rec.CodeExpires.UTC()) {
			return ErrCodeExpired
		}
		if rec.CodeDigest != codeDigest {
			return ErrCodeMismatch
		}
		verifiedAt := now
		rec.VerifiedAt = &verifiedAt
		rec.CodeDigest = ""
		rec.CodeExpires = nil
		return nil
	})
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeError(w, http.StatusNotFound, "IDN-404", "verification session not found", nil)
		return
	case errors.Is(err, ErrCodeExpired):
		s.writeError(w, http.StatusBadRequest, "IDN-409", "verification code expired", nil)
		return
	case errors.Is(err, ErrCodeMismatch):
		s.writeError(w, http.StatusBadRequest, "IDN-400", "invalid verification code", nil)
		return
	case err != nil:
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to update verification state", nil)
		return
	}
	resp := map[string]any{
		"status":     "verified",
		"verifiedAt": record.VerifiedAt.UTC().Format(time.RFC3339),
		"emailHash":  emailHash,
	}
	s.persistAndWrite(w, r, apiKey.Key, resp, http.StatusOK)
}

func (s *Server) handleBindAlias(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	apiKey, err := s.authenticateRequest(r, body)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", err.Error(), nil)
		return
	}
	if resp, ok := s.tryReplay(r, apiKey.Key, body); ok {
		s.writeCachedResponse(w, resp)
		return
	}
	var req struct {
		AliasID string `json:"aliasId"`
		Email   string `json:"email"`
		Consent bool   `json:"consent"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", "invalid JSON payload", nil)
		return
	}
	aliasID := strings.TrimSpace(req.AliasID)
	if err := validateAliasID(aliasID); err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	normalized, err := normalizeEmail(req.Email)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", err.Error(), nil)
		return
	}
	emailHash := computeEmailHash(normalized, s.emailSalt)
	now := s.now()
	binding, err := s.store.BindAlias(emailHash, aliasID, req.Consent, now)
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "email not verified", nil)
		return
	case errors.Is(err, ErrNotVerified):
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "email not verified", nil)
		return
	case errors.Is(err, ErrAliasConflict):
		s.writeError(w, http.StatusConflict, "IDN-409", "alias already linked to another email", nil)
		return
	case err != nil:
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to persist alias binding", nil)
		return
	}
	resp := map[string]any{
		"status":       "linked",
		"aliasId":      binding.AliasID,
		"emailHash":    emailHash,
		"publicLookup": binding.PublicLookup,
	}
	s.persistAndWrite(w, r, apiKey.Key, resp, http.StatusOK)
}

func (s *Server) readBody(r *http.Request) ([]byte, error) {
	reader := io.LimitReader(r.Body, maxBodyBytes)
	defer r.Body.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

func (s *Server) authenticateRequest(r *http.Request, body []byte) (apiKeySecret, error) {
	key := strings.TrimSpace(r.Header.Get(headerAPIKey))
	signature := strings.TrimSpace(r.Header.Get(headerAPISignature))
	tsRaw := strings.TrimSpace(r.Header.Get(headerAPITimestamp))
	if key == "" || signature == "" || tsRaw == "" {
		return apiKeySecret{}, errors.New("missing authentication headers")
	}
	secret, ok := s.keys[key]
	if !ok {
		return apiKeySecret{}, errors.New("unknown API key")
	}
	tsUnix, err := parseUnixTimestamp(tsRaw)
	if err != nil {
		return apiKeySecret{}, err
	}
	now := s.now()
	skew := s.timestampSkew
	if tsUnix.Before(now.Add(-skew)) || tsUnix.After(now.Add(skew)) {
		return apiKeySecret{}, errors.New("timestamp outside acceptable skew")
	}
	expected := computeSignature(secret.Secret, r.Method, r.URL.Path, body, tsRaw)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return apiKeySecret{}, errors.New("invalid signature")
	}
	return secret, nil
}

func (s *Server) tryReplay(r *http.Request, apiKey string, body []byte) (IdempotencyRecord, bool) {
	idemKey := strings.TrimSpace(r.Header.Get(headerIdempotency))
	if idemKey == "" {
		return IdempotencyRecord{}, false
	}
	key := idempotencyKey(apiKey, r.Method, r.URL.Path, idemKey)
	record, found, err := s.store.GetIdempotency(key, s.now())
	if err != nil {
		return IdempotencyRecord{}, false
	}
	return record, found
}

func (s *Server) writeCachedResponse(w http.ResponseWriter, record IdempotencyRecord) {
	for k, v := range map[string]string{
		"Content-Type":        "application/json",
		"X-Idempotency-Cache": "hit",
	} {
		w.Header().Set(k, v)
	}
	w.WriteHeader(record.StatusCode)
	_, _ = w.Write(record.Body)
}

func (s *Server) persistAndWrite(w http.ResponseWriter, r *http.Request, apiKey string, payload map[string]any, status int) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to marshal response", nil)
		return
	}
	if idem := strings.TrimSpace(r.Header.Get(headerIdempotency)); idem != "" {
		key := idempotencyKey(apiKey, r.Method, r.URL.Path, idem)
		_ = s.store.PutIdempotency(key, IdempotencyRecord{
			StatusCode: status,
			Body:       body,
			StoredAt:   s.now(),
			ExpiresAt:  s.now().Add(s.idempotencyTTL),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	payload := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": details,
		},
	}
	body, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) randomCode() (string, error) {
	nBig, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", nBig.Int64()), nil
}

func computeSignature(secret []byte, method, path string, body []byte, ts string) string {
	sum := sha256.Sum256(body)
	message := fmt.Sprintf("%s\n%s\n%s\n%s", method, path, hex.EncodeToString(sum[:]), ts)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func idempotencyKey(apiKey, method, path, idem string) string {
	return fmt.Sprintf("%s|%s|%s|%s", apiKey, method, path, idem)
}

func parseUnixTimestamp(raw string) (time.Time, error) {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp: %w", err)
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func normalizeEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return "", errors.New("email required")
	}
	normalized := norm.NFKC.String(strings.ToLower(trimmed))
	if _, err := mail.ParseAddress(normalized); err != nil {
		return "", fmt.Errorf("invalid email: %w", err)
	}
	return normalized, nil
}

func computeEmailHash(normalized string, salt []byte) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(normalized))
	return "0x" + hex.EncodeToString(mac.Sum(nil))
}

func hashVerificationCode(normalized, code string, salt []byte) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte("code:"))
	mac.Write([]byte(normalized))
	mac.Write([]byte(":"))
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

func validateAliasID(aliasID string) error {
	if aliasID == "" {
		return errors.New("aliasId required")
	}
	if !strings.HasPrefix(aliasID, "0x") || len(aliasID) <= 2 {
		return errors.New("aliasId must be 0x-prefixed hex")
	}
	if _, err := hex.DecodeString(aliasID[2:]); err != nil {
		return fmt.Errorf("invalid aliasId: %w", err)
	}
	return nil
}
