package settlement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPPayoutClientLoginAndPayout(t *testing.T) {
	var loginCalls, payoutCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&loginCalls, 1)
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode login body: %v", err)
		}
		if body["email"] != "ops@example.com" || body["password"] != "secret" {
			t.Fatalf("unexpected login credentials: %+v", body)
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token-1"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&payoutCalls, 1)
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Fatalf("missing/incorrect api key header: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Authorization") != "Bearer jwt-token-1" {
			t.Fatalf("missing/incorrect bearer token: %q", r.Header.Get("Authorization"))
		}
		var body nowPaymentsPayoutRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode payout body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Withdrawals) != 1 || body.Withdrawals[0].Currency != "znhb" {
			t.Errorf("unexpected payout body: %+v", body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-42"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	result, err := client.CreatePayout(context.Background(), PayoutRequest{
		Asset: "ZNHB", Amount: 25.5, Address: "0xabc",
	})
	if err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if result.ExternalRef != "batch-42" {
		t.Fatalf("unexpected external ref: %s", result.ExternalRef)
	}
	if atomic.LoadInt32(&loginCalls) != 1 {
		t.Fatalf("expected exactly one login call, got %d", loginCalls)
	}
	if atomic.LoadInt32(&payoutCalls) != 1 {
		t.Fatalf("expected exactly one payout call, got %d", payoutCalls)
	}

	// Second payout reuses the cached token -- no additional login call.
	if _, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "ZNHB", Amount: 1, Address: "0xdef"}); err != nil {
		t.Fatalf("second payout: %v", err)
	}
	if atomic.LoadInt32(&loginCalls) != 1 {
		t.Fatalf("expected token to be cached, got %d login calls", loginCalls)
	}
}

func TestHTTPPayoutClientSendsCorrectRequestShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		var body nowPaymentsPayoutRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode payout body: %v", err)
		}
		if len(body.Withdrawals) != 1 || body.Withdrawals[0].Address != "0xabc" || body.Withdrawals[0].Currency != "znhb" || body.Withdrawals[0].Amount != 25.5 {
			t.Fatalf("unexpected payout body: %+v", body)
		}
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-42"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "ZNHB", Amount: 25.5, Address: "0xabc"}); err != nil {
		t.Fatalf("create payout: %v", err)
	}
}

func TestHTTPPayoutClientRetriesOnceOn401(t *testing.T) {
	var loginCalls, payoutCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&loginCalls, 1)
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token-" + string(rune('0'+n))})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&payoutCalls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-retry"})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "ZNHB", Amount: 1, Address: "0xabc"})
	if err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if result.ExternalRef != "batch-retry" {
		t.Fatalf("unexpected external ref: %s", result.ExternalRef)
	}
	if atomic.LoadInt32(&loginCalls) != 2 {
		t.Fatalf("expected a forced re-login after 401, got %d login calls", loginCalls)
	}
	if atomic.LoadInt32(&payoutCalls) != 2 {
		t.Fatalf("expected exactly one retry, got %d payout calls", payoutCalls)
	}
}

func TestHTTPPayoutClientRequiresCredentials(t *testing.T) {
	if _, err := NewHTTPPayoutClient(NowPaymentsConfig{}); err == nil {
		t.Fatalf("expected error for missing credentials")
	}
	if _, err := NewHTTPPayoutClient(NowPaymentsConfig{Email: "a@b.com", Password: "x"}); err == nil {
		t.Fatalf("expected error for missing api key")
	}
}

func TestHTTPPayoutClientValidatesRequest(t *testing.T) {
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{Email: "a@b.com", Password: "x", APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "ZNHB", Amount: 1}); err == nil {
		t.Fatalf("expected error for missing address")
	}
	if _, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "ZNHB", Amount: 0, Address: "0xabc"}); err == nil {
		t.Fatalf("expected error for non-positive amount")
	}
}
