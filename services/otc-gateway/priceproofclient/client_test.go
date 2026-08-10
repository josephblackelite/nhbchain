package priceproofclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"
	swap "nhbchain/native/swap"
)

func TestClientSignsRequestAndDecodesResponse(t *testing.T) {
	now := time.Unix(1_700_700_000, 0).UTC()
	nonce := "nonce-1"
	var capturedBody []byte
	var capturedHeaders http.Header
	var capturedPath string

	sig := strings.Repeat("ab", 65)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capturedBody = append([]byte(nil), body...)
		capturedHeaders = r.Header.Clone()
		capturedPath = gatewayauth.CanonicalRequestPath(r)
		resp := map[string]any{
			"domain":    swap.PriceProofDomainV1,
			"provider":  "nowpayments",
			"pair":      "ZNHB/USD",
			"rate":      "0.100000000000000000",
			"timestamp": now.Unix(),
			"signature": "0x" + sig,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer httpServer.Close()

	client, err := NewClient(Config{
		URL:       httpServer.URL,
		APIKey:    "partner",
		APISecret: "secret",
		Now:       func() time.Time { return now },
		Nonce:     func() (string, error) { return nonce, nil },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	proof, err := client.PriceProof(context.Background(), "ZNHB/USD")
	if err != nil {
		t.Fatalf("price proof: %v", err)
	}
	if proof.Provider != "nowpayments" || proof.Base != "ZNHB" || proof.Quote != "USD" {
		t.Fatalf("unexpected proof: %+v", proof)
	}
	if len(proof.Signature) != 65 {
		t.Fatalf("expected 65-byte signature, got %d", len(proof.Signature))
	}

	if got := capturedHeaders.Get(gatewayauth.HeaderAPIKey); got != "partner" {
		t.Fatalf("expected api key header, got %q", got)
	}
	ts := capturedHeaders.Get(gatewayauth.HeaderTimestamp)
	if ts != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("unexpected timestamp header %q", ts)
	}
	if got := capturedHeaders.Get(gatewayauth.HeaderNonce); got != nonce {
		t.Fatalf("unexpected nonce header %q", got)
	}
	expectedSig := gatewayauth.ComputeSignature("secret", ts, nonce, http.MethodPost, capturedPath, capturedBody)
	if got := capturedHeaders.Get(gatewayauth.HeaderSignature); got != hex.EncodeToString(expectedSig) {
		t.Fatalf("unexpected signature header %q", got)
	}

	var sentBody map[string]string
	if err := json.Unmarshal(capturedBody, &sentBody); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sentBody["pair"] != "ZNHB/USD" {
		t.Fatalf("unexpected sent pair: %s", sentBody["pair"])
	}
}

func TestClientPropagatesServerError(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "partner not authorized", http.StatusForbidden)
	}))
	defer httpServer.Close()

	client, err := NewClient(Config{URL: httpServer.URL, APIKey: "partner", APISecret: "secret"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.PriceProof(context.Background(), "ZNHB/USD"); err == nil {
		t.Fatalf("expected error for forbidden response")
	}
}

func TestClientRejectsEmptyPair(t *testing.T) {
	client, err := NewClient(Config{URL: "http://127.0.0.1", APIKey: "partner", APISecret: "secret"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.PriceProof(context.Background(), "   "); err == nil {
		t.Fatalf("expected error for empty pair")
	}
}
