package main

// What the audit log keeps of requests that failed authentication, how fast it
// writes them, how long it keeps them, and the limits on the HTTP server in
// front of it.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// auditSummary is what the rows added after id `after` add up to.
type auditSummary struct {
	rows        int
	longestPath int
	longestBody int
	longestResp int
}

func lastAuditID(t *testing.T, store *SQLiteStore) int64 {
	t.Helper()
	var id int64
	if err := store.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM audit_log`).Scan(&id); err != nil {
		t.Fatalf("read the last audit id: %v", err)
	}
	return id
}

func auditSince(t *testing.T, store *SQLiteStore, after int64) auditSummary {
	t.Helper()
	var sum auditSummary
	err := store.db.QueryRow(`SELECT COUNT(*),
		COALESCE(MAX(LENGTH(CAST(path AS BLOB))), 0),
		COALESCE(MAX(LENGTH(request_body)), 0),
		COALESCE(MAX(LENGTH(response_body)), 0)
		FROM audit_log WHERE id > ?`, after).Scan(&sum.rows, &sum.longestPath, &sum.longestBody, &sum.longestResp)
	if err != nil {
		t.Fatalf("summarise the audit rows: %v", err)
	}
	return sum
}

// frozenClock stops the server's clock so the limiter does not refill during a test.
func frozenClock(server *Server) time.Time {
	now := time.Unix(1700000000, 0).UTC()
	server.nowFn = func() time.Time { return now }
	return now
}

func TestFailedAuthenticationAuditRowsAreCapped(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	frozenClock(server)
	after := lastAuditID(t, store)

	longPath := "/escrow/" + strings.Repeat("a", 8000)
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, longPath, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %d: HTTP %d, want 401", i, rec.Code)
		}
	}

	sum := auditSince(t, store, after)
	if sum.rows == 0 {
		t.Fatalf("no audit row was written for 200 failed authentications; the first ones should be kept")
	}
	if sum.rows > unauthenticatedAuditBurst {
		t.Fatalf("%d audit rows were written for 200 failed authentications in one instant, want at most the burst of %d", sum.rows, unauthenticatedAuditBurst)
	}
	if sum.longestPath > maxUnauthenticatedAuditBytes {
		t.Fatalf("a stored path is %d bytes, want at most %d", sum.longestPath, maxUnauthenticatedAuditBytes)
	}
}

func TestFailedAuthenticationAuditRowsKeepNoRequestBody(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	frozenClock(server)
	after := lastAuditID(t, store)

	body := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous POST %d: HTTP %d, want 401", i, rec.Code)
		}
	}

	sum := auditSince(t, store, after)
	if sum.rows == 0 {
		t.Fatalf("no audit row was written for failed authentications")
	}
	if sum.longestBody != 0 {
		t.Fatalf("a failed authentication's audit row holds %d bytes of the request body, want none", sum.longestBody)
	}
}

func TestFailedAuthenticationAuditRowsCapTheResponse(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	frozenClock(server)
	after := lastAuditID(t, store)

	// The error text for an unreadable timestamp quotes the header.
	req := httptest.NewRequest(http.MethodGet, "/p2p/offers", nil)
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, strings.Repeat("x", 4000))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP %d, want 401", rec.Code)
	}

	sum := auditSince(t, store, after)
	if sum.rows != 1 {
		t.Fatalf("%d audit rows, want 1", sum.rows)
	}
	if sum.longestResp > maxUnauthenticatedAuditBytes {
		t.Fatalf("a stored response is %d bytes, want at most %d", sum.longestResp, maxUnauthenticatedAuditBytes)
	}
	var stored string
	if err := store.db.QueryRow(`SELECT CAST(response_body AS TEXT) FROM audit_log WHERE id > ?`, after).Scan(&stored); err != nil {
		t.Fatalf("read the stored response: %v", err)
	}
	if !strings.HasSuffix(stored, truncatedMarker) {
		t.Fatalf("a cut response should say so, got %q", stored[len(stored)-40:])
	}
}

// A request that authenticated is recorded in full and is not held to the rate
// of the failed ones, even when those have used the whole allowance.
func TestAuthenticatedAuditRowsAreKeptInFull(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	now := frozenClock(server)

	for i := 0; i < unauthenticatedAuditBurst*2; i++ {
		server.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/p2p/offers", nil))
	}
	after := lastAuditID(t, store)

	// Signed and authenticated, but without the Idempotency-Key a write needs,
	// so the handler answers 400 and records the request.
	body := []byte(`{"escrowId":"0xabc"}`)
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/release", body, now, "nonce-audit-full")
	req := httptest.NewRequest(http.MethodPost, "/escrow/release", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d (%s), want 400 from the handler", rec.Code, rec.Body.String())
	}

	var apiKey string
	var stored []byte
	if err := store.db.QueryRow(`SELECT api_key, request_body FROM audit_log WHERE id > ?`, after).Scan(&apiKey, &stored); err != nil {
		t.Fatalf("an authenticated request left no audit row: %v", err)
	}
	if apiKey != "test" || !bytes.Equal(stored, body) {
		t.Fatalf("audit row has api key %q and body %q, want %q and %q", apiKey, stored, "test", body)
	}
}

func TestAuditLimiterRefillsAndReportsWhatItDropped(t *testing.T) {
	var logged bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	defer slog.SetDefault(original)

	limiter := newAuditLimiter(2, 3)
	now := time.Unix(1700000000, 0).UTC()
	for i := 0; i < 3; i++ {
		if !limiter.allow(now) {
			t.Fatalf("row %d of the burst was refused", i)
		}
	}
	if limiter.allow(now) {
		t.Fatalf("a row beyond the burst was allowed")
	}
	if logged.Len() != 0 {
		t.Fatalf("a refusal was logged straight away: %s", logged.String())
	}

	// Two tokens a second: after a second, two more rows, not three.
	now = now.Add(time.Second)
	if !limiter.allow(now) || !limiter.allow(now) {
		t.Fatalf("the bucket did not refill")
	}
	if limiter.allow(now) {
		t.Fatalf("the bucket refilled faster than its rate")
	}

	// A long quiet spell refills only up to the burst.
	now = now.Add(time.Hour)
	allowed := 0
	for i := 0; i < 10; i++ {
		if limiter.allow(now) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("%d rows allowed after a quiet spell, want the burst of 3", allowed)
	}
	if !strings.Contains(logged.String(), "notWritten") {
		t.Fatalf("the rows that were not written should have been reported once the report interval had passed, got: %q", logged.String())
	}
}

func TestTruncateAuditCutsOnACharacterBoundary(t *testing.T) {
	if got := truncateAudit("short", 512); got != "short" {
		t.Fatalf("a short value was changed: %q", got)
	}
	exact := strings.Repeat("a", 512)
	if got := truncateAudit(exact, 512); got != exact {
		t.Fatalf("a value of exactly the cap was changed")
	}
	long := strings.Repeat("a", 600)
	got := truncateAudit(long, 512)
	if len(got) != 512 || !strings.HasSuffix(got, truncatedMarker) {
		t.Fatalf("got %d bytes ending %q, want 512 bytes ending in the marker", len(got), got[len(got)-20:])
	}
	multi := strings.Repeat("é", 600) // two bytes each
	got = truncateAudit(multi, 512)
	if len(got) > 512 || !utf8.ValidString(got) || !strings.HasSuffix(got, truncatedMarker) {
		t.Fatalf("a multi-byte value was cut badly: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}

func insertAudit(t *testing.T, store *SQLiteStore, apiKey, path string, at time.Time) {
	t.Helper()
	if err := store.InsertAuditLog(context.Background(), AuditEntry{APIKey: apiKey, Method: http.MethodGet, Path: path, ResponseStatus: http.StatusUnauthorized, Timestamp: at.UTC()}); err != nil {
		t.Fatalf("insert audit row: %v", err)
	}
}

func auditPaths(t *testing.T, store *SQLiteStore) map[string]bool {
	t.Helper()
	rows, err := store.db.Query(`SELECT path FROM audit_log`)
	if err != nil {
		t.Fatalf("list audit rows: %v", err)
	}
	defer rows.Close()
	paths := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		paths[p] = true
	}
	return paths
}

func TestPruneUnauthenticatedAuditKeepsAuthenticatedRows(t *testing.T) {
	_, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	if _, err := store.db.Exec(`DELETE FROM audit_log`); err != nil {
		t.Fatalf("clear: %v", err)
	}

	now := time.Unix(1700000000, 0).UTC()
	cutoff := now.Add(-24 * time.Hour)
	insertAudit(t, store, "", "/old-anonymous", cutoff.Add(-time.Hour))
	insertAudit(t, store, "", "/new-anonymous", cutoff.Add(time.Hour))
	insertAudit(t, store, "merchant", "/old-authenticated", cutoff.Add(-72*time.Hour))
	insertAudit(t, store, "merchant", "/new-authenticated", cutoff.Add(time.Hour))

	removed, err := store.PruneUnauthenticatedAudit(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed %d rows, want 1", removed)
	}
	paths := auditPaths(t, store)
	for _, want := range []string{"/new-anonymous", "/old-authenticated", "/new-authenticated"} {
		if !paths[want] {
			t.Fatalf("row %s was deleted; remaining: %v", want, paths)
		}
	}
	if paths["/old-anonymous"] {
		t.Fatalf("the old anonymous row was kept; remaining: %v", paths)
	}
}

func TestAuditPrunerRunsAtStartAndHonoursZeroRetention(t *testing.T) {
	_, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	if _, err := store.db.Exec(`DELETE FROM audit_log`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()
	insertAudit(t, store, "", "/ancient", now.Add(-90*24*time.Hour))
	nowFn := func() time.Time { return now }

	// Zero retention starts nothing and deletes nothing.
	stop := startAuditPruner(store, 0, time.Hour, nowFn)
	stop()
	if !auditPaths(t, store)["/ancient"] {
		t.Fatalf("a zero retention deleted a row")
	}

	stop = startAuditPruner(store, 30*24*time.Hour, time.Hour, nowFn)
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	for auditPaths(t, store)["/ancient"] {
		if time.Now().After(deadline) {
			t.Fatalf("the pruner did not remove a row older than the retention")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHTTPServerHasLimits(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	srv := newHTTPServer(":0", handler)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("a timeout is unset: header %s, read %s, write %s, idle %s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout < srv.ReadTimeout+20*time.Second {
		t.Fatalf("the write timeout %s leaves no room for a 20 second node call after reading a body for up to %s", srv.WriteTimeout, srv.ReadTimeout)
	}
	if srv.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes %d, want %d", srv.MaxHeaderBytes, httpMaxHeaderBytes)
	}

	ts := httptest.NewUnstartedServer(nil)
	ts.Config = srv
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/escrow/abc")
	if err != nil {
		t.Fatalf("ordinary request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("an ordinary request got HTTP %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/escrow/" + strings.Repeat("a", 32<<10))
	if err != nil {
		t.Fatalf("oversized request line: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("a 32 KiB request line got HTTP %d, want %d", resp.StatusCode, http.StatusRequestHeaderFieldsTooLarge)
	}
}

func TestAuditRetentionConfig(t *testing.T) {
	t.Setenv("ESCROW_GATEWAY_NODE_URL", "http://127.0.0.1:1")
	t.Setenv("ESCROW_GATEWAY_RELAYER_KMS_ENV", "TEST_RELAYER_KEY_VAR")
	t.Setenv("ESCROW_GATEWAY_API_KEYS", `[{"key":"test","secret":"secret"}]`)

	load := func(value string) (Config, error) {
		t.Setenv("ESCROW_GATEWAY_AUDIT_UNAUTHENTICATED_RETENTION", value)
		return LoadConfigFromEnv()
	}
	cfg, err := load("")
	if err != nil || cfg.UnauthenticatedAuditRetention != defaultUnauthenticatedAuditRetention {
		t.Fatalf("default: retention %s, err %v; want %s", cfg.UnauthenticatedAuditRetention, err, defaultUnauthenticatedAuditRetention)
	}
	if cfg, err = load("48h"); err != nil || cfg.UnauthenticatedAuditRetention != 48*time.Hour {
		t.Fatalf("48h: retention %s, err %v", cfg.UnauthenticatedAuditRetention, err)
	}
	if cfg, err = load("0"); err != nil || cfg.UnauthenticatedAuditRetention != 0 {
		t.Fatalf("0 should keep the rows for good: retention %s, err %v", cfg.UnauthenticatedAuditRetention, err)
	}
	if _, err = load("-1h"); err == nil {
		t.Fatalf("a negative retention was accepted")
	}
	if _, err = load("soon"); err == nil {
		t.Fatalf("an unreadable retention was accepted")
	}
}
