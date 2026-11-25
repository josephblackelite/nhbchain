package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Ledger PL-EXT-90: verifyWalletSignature signed a nonce into every wallet signature and
// never checked it, so a captured request could be sent again under another valid API key
// with the victim's wallet signature unchanged.
func TestWalletSignatureNonceCannotBeUsedTwice(t *testing.T) {
	priv, addr := newWallet(t)
	server, store, _ := newTestServer(t, &mockNodeClient{}, nil)
	defer store.Close()
	now := time.Unix(1700000000, 0).UTC()
	server.nowFn = func() time.Time { return now }

	body := []byte(`{"offer":"x"}`)
	const path = "/p2p/offers"
	request := func(nonce string) *http.Request {
		timestamp := "1700000000"
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set(headerTimestamp, timestamp)
		req.Header.Set(headerNonce, nonce)
		req.Header.Set(headerWalletAddress, addr)
		req.Header.Set(headerWalletSig, signWalletRequest(t, priv, http.MethodPost, path, body, timestamp, nonce, ""))
		return req
	}

	if _, err := server.verifyWalletSignature(request("n-1"), body, "", nil); err != nil {
		t.Fatalf("first use of a nonce was refused: %v", err)
	}
	if _, err := server.verifyWalletSignature(request("n-1"), body, "", nil); !errors.Is(err, errWalletNonceReused) {
		t.Fatalf("the same signed request a second time: error %v, want errWalletNonceReused", err)
	}
	if _, err := server.verifyWalletSignature(request("n-2"), body, "", nil); err != nil {
		t.Fatalf("a new nonce was refused: %v", err)
	}
	// A request with a bad signature must not use up a nonce.
	bad := request("n-3")
	bad.Header.Set(headerWalletSig, "0x"+string(bytes.Repeat([]byte("00"), 65)))
	if _, err := server.verifyWalletSignature(bad, body, "", nil); err == nil {
		t.Fatalf("a bad signature was accepted")
	}
	if _, err := server.verifyWalletSignature(request("n-3"), body, "", nil); err != nil {
		t.Fatalf("a refused request used up its nonce: %v", err)
	}
	// After the memory window the same nonce is forgotten (the API-key gate refuses the old timestamp long before).
	now = now.Add(walletNonceTTL + time.Second)
	if _, err := server.verifyWalletSignature(request("n-1"), body, "", nil); err != nil {
		t.Fatalf("a nonce older than the window was still refused: %v", err)
	}
}

func TestWalletNonceGuardRefusesWhenFullOfFreshEntries(t *testing.T) {
	g := newWalletNonceGuard()
	now := time.Unix(1700000000, 0).UTC()
	for i := 0; i < walletNonceCapacity; i++ {
		g.entries[fmt.Sprintf("signer-%d|n", i)] = now
	}
	if err := g.use("a", "new", now); !errors.Is(err, errWalletNonceFull) {
		t.Fatalf("a full table of fresh entries: error %v, want errWalletNonceFull", err)
	}
	if err := g.use("a", "new", now.Add(walletNonceTTL+time.Second)); err != nil {
		t.Fatalf("a table of expired entries refused a new nonce: %v", err)
	}
	var nilGuard *walletNonceGuard
	if err := nilGuard.use("a", "n", now); err != nil {
		t.Fatalf("a nil guard must allow: %v", err)
	}
}
