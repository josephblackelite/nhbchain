package main

// Reads share a second with each other and with writes: the rule that a key's
// timestamp must move forward holds between writes, not for reads.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// signedWriteProbe is a signed POST that gets past authentication and then stops
// at the handler's missing Idempotency-Key check: 400 means the key was
// accepted, 401 means it was not.
func signedWriteProbe(unix int64, nonce string) *http.Request {
	body := []byte(`{"escrowId":"0xabc"}`)
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/release", body, time.Unix(unix, 0).UTC(), nonce)
	req := httptest.NewRequest(http.MethodPost, "/escrow/release", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	return req
}

func serve(server *Server, req *http.Request) int {
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec.Code
}

func TestSignedReadsInTheSameSecondAreAccepted(t *testing.T) {
	node := &mockNodeClient{getResp: &EscrowState{ID: "0xabc", Payer: "nhb1payer", Payee: "nhb1payee", Status: "init"}}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	const unix = int64(1700000000)
	for i, path := range []string{"/escrow/0xabc", "/escrow/0xabc", "/p2p/offers", "/p2p/offers", "/escrow/0xabc"} {
		if code := serve(server, signedGet(path, unix, fmt.Sprintf("nonce-same-second-%d", i))); code != http.StatusOK {
			t.Fatalf("read %d (%s) in the same second as the others: HTTP %d, want 200", i, path, code)
		}
	}
}

func TestAReadDoesNotMakeAWriteInTheSameSecondFail(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()

	const unix = int64(1700000000)
	if code := serve(server, signedGet("/p2p/offers", unix, "nonce-read-first")); code != http.StatusOK {
		t.Fatalf("read: HTTP %d, want 200", code)
	}
	if code := serve(server, signedWriteProbe(unix, "nonce-write-after-read")); code != http.StatusBadRequest {
		t.Fatalf("a write in the second of an earlier read: HTTP %d, want 400 (authenticated, then no Idempotency-Key)", code)
	}
	// Writes keep their own rule: the same second again is refused.
	if code := serve(server, signedWriteProbe(unix, "nonce-write-twice")); code != http.StatusUnauthorized {
		t.Fatalf("a second write in the same second: HTTP %d, want 401", code)
	}
}

func TestAReadOlderThanTheLastWriteIsRefusedAndASameSecondOneIsNot(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()

	const written = int64(1700000005)
	if code := serve(server, signedWriteProbe(written, "nonce-write-ahead")); code != http.StatusBadRequest {
		t.Fatalf("write: HTTP %d, want 400", code)
	}
	if code := serve(server, signedGet("/p2p/offers", written-1, "nonce-read-behind")); code != http.StatusUnauthorized {
		t.Fatalf("a read signed before the last write: HTTP %d, want 401", code)
	}
	if code := serve(server, signedGet("/p2p/offers", written, "nonce-read-level")); code != http.StatusOK {
		t.Fatalf("a read signed in the second of the last write: HTTP %d, want 200", code)
	}
	if code := serve(server, signedGet("/p2p/offers", written+1, "nonce-read-ahead")); code != http.StatusOK {
		t.Fatalf("a read signed after the last write: HTTP %d, want 200", code)
	}
}

func TestAReadWithAReusedNonceIsRefused(t *testing.T) {
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()

	const unix = int64(1700000000)
	if code := serve(server, signedGet("/p2p/offers", unix, "nonce-once")); code != http.StatusOK {
		t.Fatalf("first read: HTTP %d, want 200", code)
	}
	if code := serve(server, signedGet("/p2p/offers", unix, "nonce-once")); code != http.StatusUnauthorized {
		t.Fatalf("the same read again: HTTP %d, want 401", code)
	}
}
