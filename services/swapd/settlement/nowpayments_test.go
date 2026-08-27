package settlement

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPPayoutClientLoginAndPayout(t *testing.T) {
	var loginCalls, payoutCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
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

func TestHTTPPayoutClientVerifiesWithTOTPWhenConfigured(t *testing.T) {
	var verifyCalls int32
	var gotCode string
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-totp"})
	})
	mux.HandleFunc("/payout/batch-totp/verify", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&verifyCalls, 1)
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("missing/incorrect api key header on verify: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Authorization") != "Bearer jwt-token" {
			t.Errorf("missing/incorrect bearer token on verify: %q", r.Header.Get("Authorization"))
		}
		var body nowPaymentsVerifyRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode verify body: %v", err)
		}
		gotCode = body.VerificationCode
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
		TOTPSecret: secret,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.nowFn = func() time.Time { return time.Unix(59, 0).UTC() }

	result, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "USDT", Amount: 18, Address: "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V"})
	if err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if result.ExternalRef != "batch-totp" {
		t.Fatalf("unexpected external ref: %s", result.ExternalRef)
	}
	if atomic.LoadInt32(&verifyCalls) != 1 {
		t.Fatalf("expected exactly one verify call, got %d", verifyCalls)
	}
	// The RFC 6238 vector for t=59 is 287082 (see totp_test.go) -- confirms
	// CreatePayout actually threads the generated code through, not a stub.
	if gotCode != "287082" {
		t.Fatalf("unexpected verification code sent: %s", gotCode)
	}
}

func TestHTTPPayoutClientSkipsVerifyWhenTOTPNotConfigured(t *testing.T) {
	verifyHit := false
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-no-totp"})
	})
	mux.HandleFunc("/payout/batch-no-totp/verify", func(w http.ResponseWriter, r *http.Request) {
		verifyHit = true
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "USDT", Amount: 18, Address: "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V"})
	if err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if result.ExternalRef != "batch-no-totp" {
		t.Fatalf("unexpected external ref: %s", result.ExternalRef)
	}
	if verifyHit {
		t.Fatalf("verify endpoint must never be called when TOTPSecret is unset -- this must not change swapd's existing behavior")
	}
}

func TestHTTPPayoutClientFailsCreatePayoutWhenVerifyRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-bad-code"})
	})
	mux.HandleFunc("/payout/batch-bad-code/verify", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
		TOTPSecret: secret,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.CreatePayout(context.Background(), PayoutRequest{Asset: "USDT", Amount: 18, Address: "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V"})
	if err == nil {
		t.Fatalf("expected CreatePayout to fail when verify is rejected -- a batch NOWPayments won't pay out must never be reported as a success")
	}
}

// TestHTTPPayoutClientTreatsVerifyErrorAsSuccessWhenBatchAlreadyProgressed
// covers a narrow but real failure mode: NOWPayments receives and processes
// the verify call for real, but the response is lost to us (timeout,
// connection reset). Naively reporting CreatePayout as failed here would let
// a caller retry and submit a genuinely new, second real payout for the same
// redemption. A batch status past NEW/CREATING is proof verification
// actually went through, so CreatePayout must succeed in that case despite
// the verify call itself erroring.
func TestHTTPPayoutClientTreatsVerifyErrorAsSuccessWhenBatchAlreadyProgressed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-lost-response"})
	})
	mux.HandleFunc("/payout/batch-lost-response/verify", func(w http.ResponseWriter, r *http.Request) {
		// Simulate NOWPayments having processed the request but the
		// response never reaching us cleanly.
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/payout/batch-lost-response", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutStatusResponse{
			ID: "batch-lost-response",
			Withdrawals: []nowPaymentsWithdrawalStatus{
				{BatchWithdrawalID: "batch-lost-response", Status: "PROCESSING"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
		TOTPSecret: secret,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	result, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "USDT", Amount: 18, Address: "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V"})
	if err != nil {
		t.Fatalf("expected CreatePayout to succeed once status polling shows the batch already progressed, got: %v", err)
	}
	if result.ExternalRef != "batch-lost-response" {
		t.Fatalf("unexpected external ref: %s", result.ExternalRef)
	}
}

// TestHTTPPayoutClientVerifyErrorStaysFailedWhenBatchNeverProgressed is the
// contrast case: if the status check shows the batch is still stuck at
// NEW/CREATING (verification genuinely never happened), CreatePayout must
// still report failure -- the fallback in the test above must not become a
// blanket "always succeed" path.
func TestHTTPPayoutClientVerifyErrorStaysFailedWhenBatchNeverProgressed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutResponse{ID: "batch-never-verified"})
	})
	mux.HandleFunc("/payout/batch-never-verified/verify", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	mux.HandleFunc("/payout/batch-never-verified", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(nowPaymentsPayoutStatusResponse{
			ID: "batch-never-verified",
			Withdrawals: []nowPaymentsWithdrawalStatus{
				{BatchWithdrawalID: "batch-never-verified", Status: "CREATING"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
		TOTPSecret: secret,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.CreatePayout(context.Background(), PayoutRequest{Asset: "USDT", Amount: 18, Address: "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V"}); err == nil {
		t.Fatalf("expected CreatePayout to still fail when the batch never progressed past CREATING")
	}
}

func TestHTTPPayoutClientGetPayoutStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout/batch-status-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Errorf("missing/incorrect api key header: %q", r.Header.Get("x-api-key"))
		}
		json.NewEncoder(w).Encode(nowPaymentsPayoutStatusResponse{
			ID: "batch-status-1",
			Withdrawals: []nowPaymentsWithdrawalStatus{
				{BatchWithdrawalID: "batch-status-1", Status: "REJECTED"},
			},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "test-api-key", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	status, err := client.GetPayoutStatus(context.Background(), "batch-status-1")
	if err != nil {
		t.Fatalf("get payout status: %v", err)
	}
	if status != "REJECTED" {
		t.Fatalf("unexpected status: %s", status)
	}
}

func TestHTTPPayoutClientGetPayoutFeeEstimate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "jwt-token"})
	})
	mux.HandleFunc("/payout/fee", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("currency") != "usdttrc20" {
			t.Errorf("unexpected currency query param: %q", r.URL.Query().Get("currency"))
		}
		if r.URL.Query().Get("amount") != "17" {
			t.Errorf("unexpected amount query param: %q", r.URL.Query().Get("amount"))
		}
		json.NewEncoder(w).Encode(nowPaymentsFeeResponse{Currency: "usdttrc20", Fee: 5.58794178})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, err := NewHTTPPayoutClient(NowPaymentsConfig{Email: "a@b.com", Password: "x", APIKey: "key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	fee, err := client.GetPayoutFeeEstimate(context.Background(), "USDTTRC20", 17)
	if err != nil {
		t.Fatalf("get payout fee estimate: %v", err)
	}
	if fee != 5.58794178 {
		t.Fatalf("unexpected fee: %v", fee)
	}
}

func TestHTTPPayoutClientGetPayoutFeeEstimateValidatesInput(t *testing.T) {
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{Email: "a@b.com", Password: "x", APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.GetPayoutFeeEstimate(context.Background(), "", 17); err == nil {
		t.Fatalf("expected error for empty currency")
	}
	if _, err := client.GetPayoutFeeEstimate(context.Background(), "USDTTRC20", 0); err == nil {
		t.Fatalf("expected error for non-positive amount")
	}
}

func TestHTTPPayoutClientGetPayoutStatusRequiresBatchID(t *testing.T) {
	client, err := NewHTTPPayoutClient(NowPaymentsConfig{Email: "a@b.com", Password: "x", APIKey: "key"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.GetPayoutStatus(context.Background(), ""); err == nil {
		t.Fatalf("expected error for empty batch id")
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
