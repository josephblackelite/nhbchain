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
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"nhbchain/core/identity"
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
	// defaultBindTokenTTL bounds how long a completed email verification's
	// bind token remains usable (NHB-AUDIT-S6) -- short-lived and
	// single-use, so a leaked/observed token has a small, closing window
	// rather than being valid indefinitely the way a bare VerifiedAt
	// timestamp was.
	defaultBindTokenTTL = 15 * time.Minute
	bindTokenBytes      = 32
	// A six-digit code has a million values and lives ten minutes, so the number of wrong
	// guesses it tolerates must be small (ledger PL-EXT-50).
	defaultMaxVerifyAttempts = 5
	// A live code is not replaced for this long, so a register call for someone else's address
	// cannot kill the code its owner is about to type.
	defaultRegisterCooldown = time.Minute
	// One API key is shared by every app of a deployment: this many register calls per
	// RegisterWindow, whatever the addresses.
	defaultKeyRegisterAttempts = 60
)

var (
	// ErrCodeMismatch indicates the supplied verification code does not match.
	ErrCodeMismatch = errors.New("verification code mismatch")
	// ErrCodeExpired indicates the verification code has expired.
	ErrCodeExpired = errors.New("verification code expired")
	// ErrRateLimited indicates the register endpoint has been throttled for the email hash.
	ErrRateLimited = errors.New("rate limit exceeded")
	// ErrTooManyGuesses indicates the live code was cancelled after too many wrong guesses.
	ErrTooManyGuesses = errors.New("too many wrong verification codes")
	// ErrCodeStillLive indicates a code issued inside the register cooldown is still live and was kept.
	ErrCodeStillLive = errors.New("a verification code was issued moments ago")
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
	chainClient      AliasOwnerLookup
	keys             map[string]apiKeySecret
	emailSalt        []byte
	codeTTL          time.Duration
	registerWindow   time.Duration
	registerAttempts int
	timestampSkew    time.Duration
	idempotencyTTL   time.Duration
	bindTokenTTL     time.Duration
	nowFn            func() time.Time
	codeFn           func() (string, error)
	bindTokenFn      func() (string, error)

	maxVerifyAttempts   int
	registerCooldown    time.Duration
	keyRegisterAttempts int
	keyMu               sync.Mutex
	keyRegisters        map[string][]time.Time
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
	BindTokenTTL     time.Duration
	// MaxVerifyAttempts is how many wrong codes an email record accepts before its code is destroyed.
	MaxVerifyAttempts int
	// RegisterCooldown is how long a live code is kept untouched: a register call for the same
	// address inside it is refused instead of replacing the code.
	RegisterCooldown time.Duration
	// KeyRegisterAttempts bounds the register calls one API key may make inside RegisterWindow.
	KeyRegisterAttempts int
}

// NewServer constructs an HTTP server with the supplied dependencies.
// chainClient resolves an alias's real on-chain owner (NHB-AUDIT-S6b) --
// required so handleBindAlias never has to trust a client-supplied
// address in place of it.
func NewServer(store *Store, emailer Emailer, chainClient AliasOwnerLookup, cfg Config) (*Server, error) {
	if store == nil {
		return nil, errors.New("store required")
	}
	if emailer == nil {
		return nil, errors.New("emailer required")
	}
	if chainClient == nil {
		return nil, errors.New("chain client required")
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
		chainClient:      chainClient,
		keys:             secrets,
		emailSalt:        append([]byte(nil), cfg.EmailSalt...),
		codeTTL:          cfg.CodeTTL,
		registerWindow:   cfg.RegisterWindow,
		registerAttempts: cfg.RegisterAttempts,
		timestampSkew:    cfg.TimestampSkew,
		idempotencyTTL:   cfg.IdempotencyTTL,
		bindTokenTTL:     cfg.BindTokenTTL,
		nowFn:            time.Now,

		maxVerifyAttempts:   cfg.MaxVerifyAttempts,
		registerCooldown:    cfg.RegisterCooldown,
		keyRegisterAttempts: cfg.KeyRegisterAttempts,
		keyRegisters:        make(map[string][]time.Time),
	}
	if server.maxVerifyAttempts <= 0 {
		server.maxVerifyAttempts = defaultMaxVerifyAttempts
	}
	if server.registerCooldown <= 0 {
		server.registerCooldown = defaultRegisterCooldown
	}
	if server.keyRegisterAttempts <= 0 {
		server.keyRegisterAttempts = defaultKeyRegisterAttempts
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
	if server.bindTokenTTL <= 0 {
		server.bindTokenTTL = defaultBindTokenTTL
	}
	server.codeFn = server.randomCode
	server.bindTokenFn = randomBindToken
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
	if s.handleReplay(w, r, apiKey.Key, body) {
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
	now := s.now()
	// The API key is shared by every app of a deployment, so the per-address budget below is
	// not enough on its own: one holder could send mail to any number of addresses.
	if !s.allowKeyRegister(apiKey.Key, now) {
		s.writeError(w, http.StatusTooManyRequests, "IDN-429", "too many register calls for this API key", map[string]any{"retryAfter": int(s.registerWindow.Seconds())})
		return
	}
	emailHash := computeEmailHash(normalized, s.emailSalt)
	code, err := s.codeFn()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to generate verification code", nil)
		return
	}
	codeDigest := hashVerificationCode(normalized, code, s.emailSalt)
	expiresAt := now.Add(s.codeTTL)
	_, err = s.store.MutateEmail(emailHash, true, func(rec *EmailRecord) error {
		// A live code that was issued moments ago is kept: replacing it would let whoever holds
		// an API key kill the code the address's owner is about to type. Nothing is written, so
		// the refusal does not spend the hourly budget either.
		if rec.CodeIssuedAt != nil && now.Before(rec.CodeIssuedAt.Add(s.registerCooldown)) {
			return ErrCodeStillLive
		}
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
		issuedAt := now
		rec.CodeIssuedAt = &issuedAt
		rec.FailedGuesses = 0
		if rec.Bindings == nil {
			rec.Bindings = make(map[string]AliasBinding)
		}
		if rec.EmailHash == "" {
			rec.EmailHash = emailHash
		}
		return nil
	})
	if errors.Is(err, ErrCodeStillLive) {
		s.writeError(w, http.StatusTooManyRequests, "IDN-429", "a verification code was just sent to this address; use it or wait before asking for another", map[string]any{"retryAfter": int(s.registerCooldown.Seconds())})
		return
	}
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
	s.persistAndWrite(w, r, apiKey.Key, body, resp, http.StatusOK)
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
	if s.handleReplay(w, r, apiKey.Key, body) {
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
	bindToken, err := s.bindTokenFn()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to generate bind token", nil)
		return
	}
	bindTokenDigest := hashBindToken(bindToken)
	bindTokenExpires := now.Add(s.bindTokenTTL)
	// guessErr carries a wrong-code outcome out of the transaction: the counter has to be
	// written, and a transaction that returns an error is rolled back.
	var guessErr error
	record, err := s.store.MutateEmail(emailHash, false, func(rec *EmailRecord) error {
		if rec.CodeDigest == "" || rec.CodeExpires == nil {
			return ErrCodeMismatch
		}
		if now.After(rec.CodeExpires.UTC()) {
			return ErrCodeExpired
		}
		if rec.CodeDigest != codeDigest {
			// A six-digit code lives ten minutes: count the wrong guesses and destroy the code
			// at the limit, so a caller cannot walk through the million values (ledger PL-EXT-50).
			rec.FailedGuesses++
			guessErr = ErrCodeMismatch
			if rec.FailedGuesses >= s.maxVerifyAttempts {
				rec.CodeDigest = ""
				rec.CodeExpires = nil
				rec.CodeIssuedAt = nil
				rec.FailedGuesses = 0
				guessErr = ErrTooManyGuesses
			}
			return nil
		}
		verifiedAt := now
		rec.VerifiedAt = &verifiedAt
		rec.CodeDigest = ""
		rec.CodeExpires = nil
		rec.CodeIssuedAt = nil
		rec.FailedGuesses = 0
		// NHB-AUDIT-S6: issuing a fresh bind token on every successful
		// verification (and requiring it at bind time, see
		// handleBindAlias) is what actually ties a bind to the party who
		// just proved control of the inbox -- VerifiedAt alone only ever
		// proved that SOMEONE did, at SOME point in the past.
		rec.BindTokenDigest = bindTokenDigest
		expires := bindTokenExpires
		rec.BindTokenExpires = &expires
		return nil
	})
	if err == nil && guessErr != nil {
		err = guessErr
	}
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeError(w, http.StatusNotFound, "IDN-404", "verification session not found", nil)
		return
	case errors.Is(err, ErrTooManyGuesses):
		s.writeError(w, http.StatusTooManyRequests, "IDN-429", "too many wrong codes; the code was cancelled, request a new one", nil)
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
		"bindToken":  bindToken,
	}
	s.persistAndWrite(w, r, apiKey.Key, body, resp, http.StatusOK)
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
	if s.handleReplay(w, r, apiKey.Key, body) {
		return
	}
	var req struct {
		AliasID        string `json:"aliasId"`
		Alias          string `json:"alias"`
		Email          string `json:"email"`
		Consent        bool   `json:"consent"`
		BindToken      string `json:"bindToken"`
		AliasSignature string `json:"aliasSignature"`
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
	// NHB-AUDIT-S6: a bind token is required on every bind -- without one,
	// this endpoint used to trust ANY caller holding a valid gateway API
	// key (shared across every partner/demo app, not scoped to a single
	// end user) to bind ANY alias to ANY email that had EVER been
	// verified by anyone, with no proof this specific request came from
	// whoever actually controls that inbox.
	bindToken := strings.TrimSpace(req.BindToken)
	if bindToken == "" {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "bind token required", nil)
		return
	}
	emailHash := computeEmailHash(normalized, s.emailSalt)
	// NHB-AUDIT-S6b: the bind token above only ever proves control of the
	// EMAIL inbox. It never proved the caller also controls the ALIAS --
	// without this, anyone holding a bind token for a verified email could
	// bind that email to ANY syntactically-valid alias string they do not
	// own or control on-chain. This block requires a second, independent
	// proof: a signature from the alias's own on-chain controlling key
	// over a canonical envelope naming this exact (aliasId, emailHash,
	// bindToken) tuple, checked against the alias's REAL on-chain owner
	// (never a client-supplied address) via s.chainClient. Both proofs are
	// now required together; neither weakens the other -- this check runs
	// (and can fail closed) before s.store.BindAlias ever touches/consumes
	// the bind token, so a bad alias signature can never burn a victim's
	// legitimate token. Missing-required-proof cases use 401 (matching the
	// bind-token-required check above), not 400, so a request missing this
	// new proof is rejected the same way a request missing the email-side
	// proof always was.
	aliasNameRaw := strings.TrimSpace(req.Alias)
	if aliasNameRaw == "" {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "alias required", nil)
		return
	}
	aliasSigHex := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(req.AliasSignature), "0x"), "0X")
	if aliasSigHex == "" {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "alias signature required", nil)
		return
	}
	aliasName, err := identity.NormalizeAlias(aliasNameRaw)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "IDN-400", fmt.Sprintf("invalid alias: %v", err), nil)
		return
	}
	derivedID := identity.DeriveAliasID(aliasName)
	if !strings.EqualFold(aliasID, "0x"+hex.EncodeToString(derivedID[:])) {
		s.writeError(w, http.StatusBadRequest, "IDN-400", "aliasId does not match alias", nil)
		return
	}
	aliasSig, err := hex.DecodeString(aliasSigHex)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "invalid alias signature encoding", nil)
		return
	}
	recoveredSigner, err := RecoverAliasBindSigner(aliasID, emailHash, bindToken, aliasSig)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "alias signature invalid", nil)
		return
	}
	onChainOwner, err := s.chainClient.ResolveOwner(r.Context(), aliasName)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "alias has no resolvable on-chain owner", nil)
		return
	}
	if !hmac.Equal(recoveredSigner[:], onChainOwner[:]) {
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "alias signature does not match on-chain owner", nil)
		return
	}
	tokenDigest := hashBindToken(bindToken)
	now := s.now()
	binding, err := s.store.BindAlias(emailHash, aliasID, req.Consent, tokenDigest, now)
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "email not verified", nil)
		return
	case errors.Is(err, ErrNotVerified):
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "email not verified", nil)
		return
	case errors.Is(err, ErrBindTokenInvalid):
		s.writeError(w, http.StatusUnauthorized, "IDN-401", "bind token invalid or expired", nil)
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
	s.persistAndWrite(w, r, apiKey.Key, body, resp, http.StatusOK)
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

// requestHash is the sha256 (hex) of a request body: the part of a request an idempotency key
// must be bound to (ledger PL-EXT-50, report of 2026-11-19).
func requestHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// handleReplay answers a request that carries an Idempotency-Key already seen. It reports
// whether it wrote a response: the cached one for the same body, or 409 when the key comes
// back with another body (the escrow gateway does the same). A record stored before the body
// hash existed has none and is replayed as before.
func (s *Server) handleReplay(w http.ResponseWriter, r *http.Request, apiKey string, body []byte) bool {
	idemKey := strings.TrimSpace(r.Header.Get(headerIdempotency))
	if idemKey == "" {
		return false
	}
	key := idempotencyKey(apiKey, r.Method, r.URL.Path, idemKey)
	record, found, err := s.store.GetIdempotency(key, s.now())
	if err != nil || !found {
		return false
	}
	if record.RequestHash != "" && record.RequestHash != requestHash(body) {
		s.writeError(w, http.StatusConflict, "IDN-409", "this Idempotency-Key was already used with a different request", nil)
		return true
	}
	s.writeCachedResponse(w, record)
	return true
}

// allowKeyRegister is a sliding window per API key over RegisterWindow: the key is shared by
// every app of a deployment, so the per-address budget alone does not bound the mail one
// holder can cause.
func (s *Server) allowKeyRegister(apiKey string, now time.Time) bool {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	cutoff := now.Add(-s.registerWindow)
	kept := s.keyRegisters[apiKey][:0]
	for _, ts := range s.keyRegisters[apiKey] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= s.keyRegisterAttempts {
		s.keyRegisters[apiKey] = kept
		return false
	}
	s.keyRegisters[apiKey] = append(kept, now)
	return true
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

func (s *Server) persistAndWrite(w http.ResponseWriter, r *http.Request, apiKey string, reqBody []byte, payload map[string]any, status int) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "IDN-500", "failed to marshal response", nil)
		return
	}
	if idem := strings.TrimSpace(r.Header.Get(headerIdempotency)); idem != "" {
		key := idempotencyKey(apiKey, r.Method, r.URL.Path, idem)
		_ = s.store.PutIdempotency(key, IdempotencyRecord{
			StatusCode:  status,
			Body:        body,
			StoredAt:    s.now(),
			ExpiresAt:   s.now().Add(s.idempotencyTTL),
			RequestHash: requestHash(reqBody),
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

// randomBindToken generates the NHB-AUDIT-S6 bind token: 256 bits from
// crypto/rand, hex-encoded. High-entropy and unguessable by construction,
// unlike the 6-digit email code, so no rate limiting is needed on its use
// the way there is on code attempts.
func randomBindToken() (string, error) {
	buf := make([]byte, bindTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hashBindToken mirrors hashVerificationCode's own reasoning: never persist
// the raw secret at rest, only a digest a database read can't be replayed
// from.
func hashBindToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
