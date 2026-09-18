package identitygateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	bolt "go.etcd.io/bbolt"

	"nhbchain/core/identity"
)

// staticAliasOwner is a test double for AliasOwnerLookup (NHB-AUDIT-S6b):
// it never makes a network call, just reports a fixed on-chain owner (or a
// fixed error) for whatever alias is asked about, so these tests can
// exercise handleBindAlias's alias-signature verification without needing
// a live chain node.
type staticAliasOwner struct {
	owner [20]byte
	err   error
}

func (s staticAliasOwner) ResolveOwner(_ context.Context, _ string) ([20]byte, error) {
	if s.err != nil {
		return [20]byte{}, s.err
	}
	return s.owner, nil
}

// newAliasKey generates a fresh secp256k1 keypair and its derived address,
// standing in for an alias's real on-chain controlling key in tests.
func newAliasKey(t *testing.T) (*ecdsa.PrivateKey, [20]byte) {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate alias key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey)
	var out [20]byte
	copy(out[:], addr.Bytes())
	return priv, out
}

// aliasFixture normalizes alias the same way handleBindAlias does and
// returns both the normalized name (chainClient.ResolveOwner's input) and
// its on-chain-derived aliasId hex (identity.DeriveAliasID), matching the
// exact value handleBindAlias requires the request's aliasId to equal.
func aliasFixture(t *testing.T, alias string) (name string, aliasIDHex string) {
	t.Helper()
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		t.Fatalf("normalize alias %q: %v", alias, err)
	}
	id := identity.DeriveAliasID(normalized)
	return normalized, "0x" + hex.EncodeToString(id[:])
}

// signAliasBind signs the canonical AliasBindEnvelope for (aliasID,
// emailHash, bindToken) with priv and hex-encodes it the way the wire
// request expects (aliasSignature).
func signAliasBind(t *testing.T, priv *ecdsa.PrivateKey, aliasID, emailHash, bindToken string) string {
	t.Helper()
	sig, err := SignAliasBindEnvelope(aliasID, emailHash, bindToken, priv)
	if err != nil {
		t.Fatalf("sign alias bind envelope: %v", err)
	}
	return "0x" + hex.EncodeToString(sig)
}

type captureEmailer struct {
	mu      sync.Mutex
	lastMsg VerificationMessage
	sent    int
}

func (c *captureEmailer) SendVerification(_ context.Context, msg VerificationMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent++
	c.lastMsg = msg
	return nil
}

func TestEmailVerificationFlow(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	aliasPriv, aliasAddr := newAliasKey(t)
	chainClient := staticAliasOwner{owner: aliasAddr}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("super-secret-salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
	}
	server, err := NewServer(store, emailer, chainClient, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	var current time.Time
	current = now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "483921", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	// Register email and ensure code dispatched.
	respBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email":     "Frank.Example@Example.com",
		"aliasHint": "frankrocks",
	}, "test", "secret", current, "register-key")
	if emailer.sent != 1 {
		t.Fatalf("expected verification email to be dispatched")
	}
	var registerResp struct {
		Status    string `json:"status"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(respBody, &registerResp); err != nil {
		t.Fatalf("unmarshal register: %v", err)
	}
	if registerResp.Status != "pending" {
		t.Fatalf("unexpected status %q", registerResp.Status)
	}
	if registerResp.ExpiresIn != 60 {
		t.Fatalf("expected expiresIn=60, got %d", registerResp.ExpiresIn)
	}

	// Replay with idempotency should return cached response.
	current = current.Add(5 * time.Second)
	resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "Frank.Example@Example.com",
	}, "test", "secret", current, "register-key")
	if got := resp.Header.Get("X-Idempotency-Cache"); got != "hit" {
		t.Fatalf("expected idempotency cache hit, got %q", got)
	}

	// Complete verification.
	current = current.Add(10 * time.Second)
	verifyBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/verify", map[string]any{
		"email": "frank.example@example.com",
		"code":  emailer.lastMsg.Code,
	}, "test", "secret", current, "")
	var verifyResp struct {
		Status     string `json:"status"`
		EmailHash  string `json:"emailHash"`
		VerifiedAt string `json:"verifiedAt"`
		BindToken  string `json:"bindToken"`
	}
	if err := json.Unmarshal(verifyBody, &verifyResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}
	if verifyResp.Status != "verified" {
		t.Fatalf("unexpected verify status %q", verifyResp.Status)
	}
	expectedHash := computeEmailHash("frank.example@example.com", cfg.EmailSalt)
	if verifyResp.EmailHash != expectedHash {
		t.Fatalf("expected email hash %s, got %s", expectedHash, verifyResp.EmailHash)
	}
	if verifyResp.BindToken == "" {
		t.Fatalf("expected verify response to include a bind token")
	}

	// Bind alias to verified email. The bind now also requires a signature
	// from the alias's own controlling key (NHB-AUDIT-S6b), checked against
	// chainClient's (fake) on-chain owner for "frankrocks".
	aliasName, aliasIDHex := aliasFixture(t, "frankrocks")
	aliasSig := signAliasBind(t, aliasPriv, aliasIDHex, verifyResp.EmailHash, verifyResp.BindToken)
	current = current.Add(15 * time.Second)
	bindBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":        aliasIDHex,
		"alias":          aliasName,
		"email":          "frank.example@example.com",
		"consent":        true,
		"bindToken":      verifyResp.BindToken,
		"aliasSignature": aliasSig,
	}, "test", "secret", current, "")
	var bindResp struct {
		Status       string `json:"status"`
		AliasID      string `json:"aliasId"`
		EmailHash    string `json:"emailHash"`
		PublicLookup bool   `json:"publicLookup"`
	}
	if err := json.Unmarshal(bindBody, &bindResp); err != nil {
		t.Fatalf("unmarshal bind: %v", err)
	}
	if bindResp.Status != "linked" || !bindResp.PublicLookup {
		t.Fatalf("unexpected bind response: %+v", bindResp)
	}
	if bindResp.EmailHash != expectedHash {
		t.Fatalf("email hash mismatch: %s != %s", bindResp.EmailHash, expectedHash)
	}
}

func TestRegisterRateLimit(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
	}
	server, err := NewServer(store, emailer, staticAliasOwner{}, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	current := now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "123456", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	for i := 0; i < 5; i++ {
		current = now.Add(time.Duration(i) * time.Minute)
		resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
			"email": "rate@example.com",
		}, "test", "secret", current, "rate"+strconv.Itoa(i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
	}

	current = now.Add(10 * time.Minute)
	resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "rate@example.com",
	}, "test", "secret", current, "rate-limit")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 response, got %d", resp.StatusCode)
	}
}

// NHB-AUDIT-S6: binding must fail without a valid bind token, even though
// the target email genuinely has a completed VerifiedAt -- proving the old
// gap (anyone holding a valid API key could bind ANY alias to ANY
// already-verified email, with nothing tying the bind to whoever actually
// verified it) is closed.
func TestBindAliasRequiresBindToken(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
		BindTokenTTL:     time.Minute,
	}
	// staticAliasOwner{} is never consulted by either sub-case below --
	// both fail at the bind-token/alias-required checks before
	// s.chainClient.ResolveOwner is ever called.
	server, err := NewServer(store, emailer, staticAliasOwner{}, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	current := now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "111222", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "victim@example.com",
	}, "test", "secret", current, "reg-1")

	current = current.Add(time.Second)
	verifyBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/verify", map[string]any{
		"email": "victim@example.com",
		"code":  emailer.lastMsg.Code,
	}, "test", "secret", current, "")
	var verifyResp struct {
		BindToken string `json:"bindToken"`
	}
	if err := json.Unmarshal(verifyBody, &verifyResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}

	// No bind token at all -- rejected, even though the email is verified.
	current = current.Add(time.Second)
	noTokenResp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId": "0xdeadbeef",
		"email":   "victim@example.com",
		"consent": true,
	}, "test", "secret", current, "")
	if noTokenResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a bind token, got %d", noTokenResp.StatusCode)
	}

	// Wrong/fabricated bind token -- rejected.
	current = current.Add(time.Second)
	wrongTokenResp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":   "0xdeadbeef",
		"email":     "victim@example.com",
		"consent":   true,
		"bindToken": "0000000000000000000000000000000000000000000000000000000000dead",
	}, "test", "secret", current, "")
	if wrongTokenResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a fabricated bind token, got %d", wrongTokenResp.StatusCode)
	}
}

// NHB-AUDIT-S6b: the bind token alone (NHB-AUDIT-S6, proven sufficient-ish
// by TestBindAliasRequiresBindToken above) only ever proves control of the
// EMAIL inbox. This test proves the newer, complementary requirement: a
// bind also needs a signature from the ALIAS's own on-chain controlling
// key, checked against that alias's real on-chain owner
// (chainClient.ResolveOwner) rather than any client-supplied address.
func TestBindAliasRequiresAliasSignature(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	ownerPriv, ownerAddr := newAliasKey(t)
	attackerPriv, _ := newAliasKey(t) // controls a real key, but NOT the alias's on-chain owner
	chainClient := staticAliasOwner{owner: ownerAddr}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
		BindTokenTTL:     time.Minute,
	}
	server, err := NewServer(store, emailer, chainClient, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	current := now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "555666", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "victim2@example.com",
	}, "test", "secret", current, "reg-1")

	current = current.Add(time.Second)
	verifyBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/verify", map[string]any{
		"email": "victim2@example.com",
		"code":  emailer.lastMsg.Code,
	}, "test", "secret", current, "")
	var verifyResp struct {
		EmailHash string `json:"emailHash"`
		BindToken string `json:"bindToken"`
	}
	if err := json.Unmarshal(verifyBody, &verifyResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}

	aliasName, aliasIDHex := aliasFixture(t, "notyouralias")

	// (1a) Valid bind token, NO alias signature at all -- rejected, even
	// though the bind token itself is genuine and unexpired.
	current = current.Add(time.Second)
	noSigResp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":   aliasIDHex,
		"alias":     aliasName,
		"email":     "victim2@example.com",
		"consent":   true,
		"bindToken": verifyResp.BindToken,
	}, "test", "secret", current, "")
	if noSigResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no alias signature, got %d", noSigResp.StatusCode)
	}

	// (1b) Valid bind token, alias signature from the WRONG key (a real
	// signature, just not the alias's actual on-chain owner) -- rejected.
	current = current.Add(time.Second)
	wrongKeySig := signAliasBind(t, attackerPriv, aliasIDHex, verifyResp.EmailHash, verifyResp.BindToken)
	wrongKeyResp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":        aliasIDHex,
		"alias":          aliasName,
		"email":          "victim2@example.com",
		"consent":        true,
		"bindToken":      verifyResp.BindToken,
		"aliasSignature": wrongKeySig,
	}, "test", "secret", current, "")
	if wrongKeyResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong-key alias signature, got %d", wrongKeyResp.StatusCode)
	}

	// The bind token must still be intact after both rejected attempts --
	// neither a missing nor a wrong-key alias signature may burn it (it is
	// only ever consumed by store.BindAlias, which handleBindAlias must
	// not reach until the alias signature has already checked out).
	rec, ok, err := store.GetEmail(verifyResp.EmailHash)
	if err != nil || !ok {
		t.Fatalf("expected email record to exist: ok=%v err=%v", ok, err)
	}
	if rec.BindTokenDigest == "" {
		t.Fatalf("expected bind token to remain unconsumed after rejected alias-signature attempts")
	}

	// (2) Valid bind token AND a valid alias signature from the real
	// on-chain owner -- succeeds exactly once.
	current = current.Add(time.Second)
	correctSig := signAliasBind(t, ownerPriv, aliasIDHex, verifyResp.EmailHash, verifyResp.BindToken)
	firstBind := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":        aliasIDHex,
		"alias":          aliasName,
		"email":          "victim2@example.com",
		"consent":        true,
		"bindToken":      verifyResp.BindToken,
		"aliasSignature": correctSig,
	}, "test", "secret", current, "")
	if firstBind.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(firstBind.Body)
		t.Fatalf("expected 200 with a valid bind token and alias signature, got %d: %s", firstBind.StatusCode, string(body))
	}

	// Replaying the exact same (now-consumed) bind token -- even with a
	// fresh, otherwise-valid alias signature reusing it -- fails. Proves
	// the alias-signature requirement did not loosen the bind token's own
	// single-use guarantee (NHB-AUDIT-S6).
	current = current.Add(time.Second)
	replaySig := signAliasBind(t, ownerPriv, aliasIDHex, verifyResp.EmailHash, verifyResp.BindToken)
	replayBind := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":        aliasIDHex,
		"alias":          aliasName,
		"email":          "victim2@example.com",
		"consent":        true,
		"bindToken":      verifyResp.BindToken,
		"aliasSignature": replaySig,
	}, "test", "secret", current, "")
	if replayBind.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 replaying a consumed bind token, got %d", replayBind.StatusCode)
	}
}

// NHB-AUDIT-S6: a bind token past its TTL is rejected even though it is
// otherwise a correct, never-used token.
func TestBindAliasRejectsExpiredBindToken(t *testing.T) {
	t.Parallel()

	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	emailer := &captureEmailer{}
	cfg := Config{
		APIKeys:          map[string]string{"test": "secret"},
		EmailSalt:        []byte("salt"),
		CodeTTL:          time.Minute,
		RegisterWindow:   time.Hour,
		RegisterAttempts: 5,
		TimestampSkew:    time.Hour,
		IdempotencyTTL:   time.Hour,
		BindTokenTTL:     time.Minute,
	}
	// staticAliasOwner{} is never consulted -- this request is rejected at
	// the expired-bind-token check before any alias proof is examined.
	server, err := NewServer(store, emailer, staticAliasOwner{}, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	current := now
	server.nowFn = func() time.Time { return current }
	server.codeFn = func() (string, error) { return "333444", nil }

	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)

	doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/register", map[string]any{
		"email": "expiring@example.com",
	}, "test", "secret", current, "reg-1")

	current = current.Add(time.Second)
	verifyBody := doRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/email/verify", map[string]any{
		"email": "expiring@example.com",
		"code":  emailer.lastMsg.Code,
	}, "test", "secret", current, "")
	var verifyResp struct {
		BindToken string `json:"bindToken"`
	}
	if err := json.Unmarshal(verifyBody, &verifyResp); err != nil {
		t.Fatalf("unmarshal verify: %v", err)
	}

	// Advance well past the configured bind-token TTL before attempting the bind.
	current = current.Add(5 * time.Minute)
	resp := doRawRequest(t, httpServer.Client(), "POST", httpServer.URL, "/identity/alias/bind-email", map[string]any{
		"aliasId":   "0xd00d1234",
		"email":     "expiring@example.com",
		"consent":   true,
		"bindToken": verifyResp.BindToken,
	}, "test", "secret", current, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an expired bind token, got %d", resp.StatusCode)
	}
}

func doRequest(t *testing.T, client *http.Client, method, baseURL, endpoint string, body map[string]any, key, secret string, now time.Time, idem string) []byte {
	t.Helper()
	resp := doRawRequest(t, client, method, baseURL, endpoint, body, key, secret, now, idem)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode >= 400 {
		t.Fatalf("unexpected status %d: %s", resp.StatusCode, string(payload))
	}
	return payload
}

func doRawRequest(t *testing.T, client *http.Client, method, baseURL, endpoint string, body map[string]any, key, secret string, now time.Time, idem string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(method, baseURL+endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	signature := computeSignature([]byte(secret), method, endpoint, encoded, ts)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerAPIKey, key)
	req.Header.Set(headerAPISignature, signature)
	req.Header.Set(headerAPITimestamp, ts)
	if idem != "" {
		req.Header.Set(headerIdempotency, idem)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}
