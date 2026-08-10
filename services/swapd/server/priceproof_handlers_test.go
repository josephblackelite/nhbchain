package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
	swap "nhbchain/native/swap"
	"nhbchain/services/swapd/priceproof"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type stubQuoteSource struct {
	rate    *big.Rat
	feeders []string
	ts      time.Time
	err     error
}

func (s *stubQuoteSource) Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error) {
	if s.err != nil {
		return nil, nil, time.Time{}, s.err
	}
	return s.rate, s.feeders, s.ts, nil
}

type keySigner struct {
	key *crypto.PrivateKey
}

func (s *keySigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	sig, err := ethcrypto.Sign(digest, s.key.PrivateKey)
	if err != nil {
		return nil, "", err
	}
	return sig, "CN=test-price-signer", nil
}

func newPriceProofTestServer(t *testing.T, service *priceproof.Service, partners []Partner) *Server {
	t.Helper()
	store := openStableTestStore(t, "price_proof_"+t.Name())
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if service != nil {
		if err := srv.SetPriceProofRuntime(PriceProofRuntime{Service: service, Partners: partners}); err != nil {
			t.Fatalf("set price proof runtime: %v", err)
		}
	}
	return srv
}

func TestHandlePriceProofSuccess(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ts := time.Unix(1_700_600_000, 0).UTC()
	source := &stubQuoteSource{rate: big.NewRat(1, 10), feeders: []string{"nowpayments"}, ts: ts}
	service, err := priceproof.New(source, &keySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}
	creds := partnerCreds{id: "otc-gateway", apiKey: "test-key", secret: "test-secret"}
	srv := newPriceProofTestServer(t, service, []Partner{{ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret}})

	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)

	body := `{"pair":"ZNHB/USD"}`
	resp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/price-proof", body, &creds)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}

	var decoded struct {
		Domain    string `json:"domain"`
		Provider  string `json:"provider"`
		Pair      string `json:"pair"`
		Rate      string `json:"rate"`
		Timestamp int64  `json:"timestamp"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Domain != swap.PriceProofDomainV1 {
		t.Fatalf("unexpected domain: %s", decoded.Domain)
	}
	if decoded.Provider != "nowpayments" {
		t.Fatalf("unexpected provider: %s", decoded.Provider)
	}
	if decoded.Pair != "ZNHB/USD" {
		t.Fatalf("unexpected pair: %s", decoded.Pair)
	}
	if decoded.Timestamp != ts.Unix() {
		t.Fatalf("unexpected timestamp: %d", decoded.Timestamp)
	}
	if !strings.HasPrefix(decoded.Signature, "0x") || len(decoded.Signature) != 2+65*2 {
		t.Fatalf("unexpected signature encoding: %s", decoded.Signature)
	}
}

func TestHandlePriceProofDefaultsToZNHBUSD(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &stubQuoteSource{rate: big.NewRat(1, 10), feeders: []string{"nowpayments"}, ts: time.Now()}
	service, err := priceproof.New(source, &keySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}
	creds := partnerCreds{id: "otc-gateway", apiKey: "test-key", secret: "test-secret"}
	srv := newPriceProofTestServer(t, service, []Partner{{ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret}})

	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)

	resp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/price-proof", "", &creds)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.Code, resp.Body.String())
	}
	if got := extractField(t, resp.Body.Bytes(), "pair"); got != "ZNHB/USD" {
		t.Fatalf("expected default pair ZNHB/USD, got %s", got)
	}
}

func TestHandlePriceProofRejectsUnauthenticatedRequest(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &stubQuoteSource{rate: big.NewRat(1, 10), ts: time.Now()}
	service, err := priceproof.New(source, &keySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}
	srv := newPriceProofTestServer(t, service, []Partner{{ID: "otc-gateway", APIKey: "test-key", Secret: "test-secret"}})

	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/price-proof", strings.NewReader(`{"pair":"ZNHB/USD"}`))
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code == http.StatusOK {
		t.Fatalf("expected unauthenticated request to be rejected, got 200")
	}
}

// TestHandlePriceProofFailsClosedWithoutRuntime proves the endpoint does
// NOT silently fall back to requirePartner's anonymous bypass when
// SetPriceProofRuntime was never called (registerPriceProofHandlers simply
// does not register the route at all in that case).
func TestHandlePriceProofFailsClosedWithoutRuntime(t *testing.T) {
	srv := newPriceProofTestServer(t, nil, nil)
	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)

	req := httptest.NewRequest(http.MethodPost, "/v1/price-proof", strings.NewReader(`{}`))
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when price proof runtime never configured, got %d", resp.Code)
	}
}

func TestSetPriceProofRuntimeRequiresPartners(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &stubQuoteSource{rate: big.NewRat(1, 10), ts: time.Now()}
	service, err := priceproof.New(source, &keySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}
	store := openStableTestStore(t, "price_proof_no_partners")
	auth, err := NewAuthenticator(AuthConfig{BearerToken: "test-token"})
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	srv, err := New(Config{ListenAddress: ":0", PolicyID: "default"}, store, log.New(io.Discard, "", 0), StableRuntime{}, auth)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if err := srv.SetPriceProofRuntime(PriceProofRuntime{Service: service}); err == nil {
		t.Fatalf("expected error when partners are not configured")
	}
}

func TestHandlePriceProofUpstreamErrorReturnsBadGateway(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &stubQuoteSource{err: fmt.Errorf("oracle unavailable")}
	service, err := priceproof.New(source, &keySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}
	creds := partnerCreds{id: "otc-gateway", apiKey: "test-key", secret: "test-secret"}
	srv := newPriceProofTestServer(t, service, []Partner{{ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret}})

	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)

	resp := doStableRequest(t, mux, context.Background(), http.MethodPost, "/v1/price-proof", `{"pair":"ZNHB/USD"}`, &creds)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", resp.Code, resp.Body.String())
	}
}
