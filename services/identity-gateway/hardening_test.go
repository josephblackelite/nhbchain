package identitygateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Bug-bounty reports of 2026-11-18/19 (ledger PL-EXT-50): the six-digit code could be
// guessed without limit, a register call replaced a live code, register had no limit per
// API key, and a replayed idempotency key answered with another request's response.

type hardeningHarness struct {
	t       *testing.T
	server  *Server
	http    *httptest.Server
	emailer *captureEmailer
	now     time.Time
	code    string
}

func newHardeningHarness(t *testing.T, cfg Config) *hardeningHarness {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"), &bolt.Options{Timeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if cfg.APIKeys == nil {
		cfg.APIKeys = map[string]string{"test": "secret"}
	}
	cfg.EmailSalt = []byte("salt")
	cfg.CodeTTL = 10 * time.Minute
	cfg.RegisterWindow = time.Hour
	cfg.RegisterAttempts = 5
	cfg.TimestampSkew = 24 * time.Hour
	cfg.IdempotencyTTL = time.Hour
	h := &hardeningHarness{t: t, emailer: &captureEmailer{}, now: time.Unix(1_700_000_000, 0).UTC(), code: "483921"}
	server, err := NewServer(store, h.emailer, staticAliasOwner{}, cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server.nowFn = func() time.Time { return h.now }
	server.codeFn = func() (string, error) { return h.code, nil }
	h.server = server
	h.http = httptest.NewServer(server)
	t.Cleanup(h.http.Close)
	return h
}

func (h *hardeningHarness) post(endpoint string, body map[string]any, idem string) (int, map[string]any, string) {
	h.t.Helper()
	resp := doRawRequest(h.t, h.http.Client(), "POST", h.http.URL, endpoint, body, "test", "secret", h.now, idem)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read response: %v", err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded, resp.Header.Get("X-Idempotency-Cache")
}

func (h *hardeningHarness) register(email, idem string) int {
	h.t.Helper()
	status, _, _ := h.post("/identity/email/register", map[string]any{"email": email}, idem)
	return status
}

func (h *hardeningHarness) verify(email, code string) int {
	h.t.Helper()
	status, _, _ := h.post("/identity/email/verify", map[string]any{"email": email, "code": code}, "")
	return status
}

// Report 290: handleVerify answered a wrong code with 400 and left the code alive, so a
// holder of the API key could try every one of the million six-digit codes within the ten
// minutes the code lives. After the allowed number of wrong guesses the code is destroyed.
func TestVerifyDestroysTheCodeAfterTooManyWrongGuesses(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{MaxVerifyAttempts: 5})
	if got := h.register("victim@example.com", ""); got != http.StatusOK {
		t.Fatalf("register: %d", got)
	}
	for i := 1; i <= 4; i++ {
		if got := h.verify("victim@example.com", "00000"+strconv.Itoa(i)); got != http.StatusBadRequest {
			t.Fatalf("wrong guess %d: want 400, got %d", i, got)
		}
	}
	if got := h.verify("victim@example.com", "000005"); got != http.StatusTooManyRequests {
		t.Fatalf("the guess that reaches the limit must answer 429, got %d", got)
	}
	// The real code no longer works: the attacker's budget is spent and the code is gone.
	if got := h.verify("victim@example.com", h.code); got == http.StatusOK {
		t.Fatalf("the code must be destroyed after too many wrong guesses, but the real code was accepted")
	}
}

// The limit must not stop the person who has the code from using it while they are under it.
func TestVerifyAcceptsTheRealCodeBeforeTheLimit(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{MaxVerifyAttempts: 5})
	h.register("owner@example.com", "")
	for i := 1; i <= 4; i++ {
		_ = h.verify("owner@example.com", "00000"+strconv.Itoa(i))
	}
	if got := h.verify("owner@example.com", h.code); got != http.StatusOK {
		t.Fatalf("the real code after four wrong guesses must still verify, got %d", got)
	}
}

// A new code starts with a fresh count: wrong guesses against the old code are not carried over.
func TestANewCodeStartsWithAFreshGuessCount(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{MaxVerifyAttempts: 3})
	h.register("again@example.com", "")
	_ = h.verify("again@example.com", "000001")
	_ = h.verify("again@example.com", "000002")
	h.now = h.now.Add(2 * time.Minute) // past the register cooldown
	h.code = "777777"
	if got := h.register("again@example.com", ""); got != http.StatusOK {
		t.Fatalf("register after the cooldown: %d", got)
	}
	_ = h.verify("again@example.com", "000003")
	_ = h.verify("again@example.com", "000004")
	if got := h.verify("again@example.com", "777777"); got != http.StatusOK {
		t.Fatalf("two wrong guesses against the new code must leave it usable, got %d", got)
	}
}

// Report 294: register replaced the live code of an address on every call, so whoever held an
// API key could kill the code its owner was about to type. Within the cooldown a live code is
// kept, nothing is sent and the hourly budget is not spent.
func TestRegisterDoesNotReplaceALiveCodeWithinTheCooldown(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{RegisterCooldown: time.Minute})
	if got := h.register("victim@example.com", ""); got != http.StatusOK {
		t.Fatalf("first register: %d", got)
	}
	first := h.emailer.lastMsg.Code
	h.now = h.now.Add(10 * time.Second)
	h.code = "222222"
	if got := h.register("victim@example.com", ""); got != http.StatusTooManyRequests {
		t.Fatalf("a second register inside the cooldown must answer 429, got %d", got)
	}
	if h.emailer.sent != 1 {
		t.Fatalf("no second mail may be sent inside the cooldown, sent %d", h.emailer.sent)
	}
	if got := h.verify("victim@example.com", first); got != http.StatusOK {
		t.Fatalf("the first code must still verify, got %d", got)
	}
}

func TestRegisterCooldownDoesNotSpendTheHourlyBudget(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{RegisterCooldown: time.Minute})
	h.register("budget@example.com", "")
	for i := 0; i < 20; i++ {
		h.now = h.now.Add(time.Second)
		_ = h.register("budget@example.com", "")
	}
	// 21 seconds in, 20 refused calls: the budget of 5 per hour must still hold four.
	h.now = h.now.Add(2 * time.Minute)
	h.code = "555555"
	if got := h.register("budget@example.com", ""); got != http.StatusOK {
		t.Fatalf("a register after the cooldown must still be allowed, got %d", got)
	}
}

// Ledger PL-EXT-81: a register call refused by the per-address cooldown sent no mail but still
// used up a slot of the API key's budget, so one address calling inside its cooldown could lock
// out every other user of the same key. A refused call must leave the key's budget alone.
func TestRegisterRefusedByTheCooldownDoesNotSpendTheAPIKeyBudget(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{KeyRegisterAttempts: 3, RegisterCooldown: time.Minute})
	if got := h.register("victim@example.com", ""); got != http.StatusOK {
		t.Fatalf("first register: %d", got)
	}
	for i := 0; i < 10; i++ {
		h.now = h.now.Add(time.Second)
		if got := h.register("victim@example.com", ""); got != http.StatusTooManyRequests {
			t.Fatalf("a register inside the cooldown must answer 429, got %d", got)
		}
	}
	// The key has spent one slot of three on a mail that was sent. Two other users must still get theirs.
	for i := 0; i < 2; i++ {
		if got := h.register("other"+strconv.Itoa(i)+"@example.com", ""); got != http.StatusOK {
			t.Fatalf("user %d of the same key was refused (%d) after another address was refused by its cooldown ten times", i, got)
		}
	}
	if h.emailer.sent != 3 {
		t.Fatalf("mails sent = %d, want 3", h.emailer.sent)
	}
	if got := h.register("third@example.com", ""); got != http.StatusTooManyRequests {
		t.Fatalf("the key's own budget of three must still hold, got %d", got)
	}
}

// The per-address budget of the register endpoint ages out: after the window an address can
// ask again (the budget of five per hour is a rate, not a lifetime cap).
func TestRegisterBudgetOfAnAddressAgesOut(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{})
	start := h.now
	for i := 0; i < 5; i++ {
		h.now = start.Add(time.Duration(2*i) * time.Minute)
		if got := h.register("aging@example.com", ""); got != http.StatusOK {
			t.Fatalf("register %d: %d", i, got)
		}
	}
	h.now = start.Add(10 * time.Minute)
	if got := h.register("aging@example.com", ""); got != http.StatusTooManyRequests {
		t.Fatalf("the sixth register inside the hour must answer 429, got %d", got)
	}
	h.now = start.Add(61 * time.Minute)
	if got := h.register("aging@example.com", ""); got != http.StatusOK {
		t.Fatalf("once the oldest call is outside the window the address may ask again, got %d", got)
	}
}

// Report 293: register had no limit per API key, and the key is shared by every app.
func TestRegisterIsLimitedPerAPIKey(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{KeyRegisterAttempts: 3})
	for i := 0; i < 3; i++ {
		if got := h.register("user"+strconv.Itoa(i)+"@example.com", ""); got != http.StatusOK {
			t.Fatalf("register %d: %d", i, got)
		}
	}
	if got := h.register("user3@example.com", ""); got != http.StatusTooManyRequests {
		t.Fatalf("the fourth register of the same key inside the window must answer 429, got %d", got)
	}
	if h.emailer.sent != 3 {
		t.Fatalf("a refused register must not send a mail, sent %d", h.emailer.sent)
	}
	h.now = h.now.Add(61 * time.Minute)
	if got := h.register("user4@example.com", ""); got != http.StatusOK {
		t.Fatalf("after the window the key may register again, got %d", got)
	}
}

// Report 296: the cache key ignored the request body, so a reused Idempotency-Key answered with
// another request's response. The same key with the same body is still a cache hit.
func TestIdempotencyKeyIsBoundToTheRequestBody(t *testing.T) {
	t.Parallel()
	h := newHardeningHarness(t, Config{})
	body := map[string]any{"email": "first@example.com"}
	if status, _, _ := h.post("/identity/email/register", body, "same-key"); status != http.StatusOK {
		t.Fatalf("first request: %d", status)
	}
	if status, _, cache := h.post("/identity/email/register", body, "same-key"); status != http.StatusOK || cache != "hit" {
		t.Fatalf("the same key with the same body must be a cache hit, got %d cache=%q", status, cache)
	}
	status, decoded, _ := h.post("/identity/email/register", map[string]any{"email": "other@example.com"}, "same-key")
	if status != http.StatusConflict {
		t.Fatalf("the same key with another body must answer 409, got %d %v", status, decoded)
	}
	if h.emailer.sent != 1 {
		t.Fatalf("the conflicting request must not run, mails sent %d", h.emailer.sent)
	}
}
