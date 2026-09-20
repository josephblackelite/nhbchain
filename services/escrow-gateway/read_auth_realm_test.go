package main

// Who may read from the gateway, and what a merchant's realm settings can
// actually be checked against.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// signedGet builds a GET request carrying the API key and HMAC headers the
// gateway asks of every call, timestamped at unix (the scheme wants each
// request's timestamp to be later than the last one the key made).
func signedGet(path string, unix int64, nonce string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	timestamp, nonce, sig := signHeaders("secret", http.MethodGet, path, nil, time.Unix(unix, 0).UTC(), nonce)
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	return req
}

func TestReadsRequireTheAPIKeyAndSignature(t *testing.T) {
	node := &mockNodeClient{getResp: &EscrowState{ID: "0xabc", Payer: "nhb1payer", Payee: "nhb1payee", Status: "init"}}
	server, store, _ := newTestServer(t, node, nil)
	defer store.Close()

	cases := []struct {
		path string
		want int // once authenticated
	}{
		{"/escrow/0xabc", http.StatusOK},
		{"/p2p/offers", http.StatusOK},
		{"/p2p/trades/0xtrade", http.StatusNotFound}, // no such trade: reached the store, so past authentication
	}
	unix := int64(1700000001)
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s with no credentials: HTTP %d (%s), want 401", tc.path, rec.Code, rec.Body.String())
		}

		forged := signedGet(tc.path, unix, "nonce-forged-"+tc.path)
		forged.Header.Set(headerSignature, strings.Repeat("00", 32))
		rec = httptest.NewRecorder()
		server.ServeHTTP(rec, forged)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s with a wrong signature: HTTP %d, want 401", tc.path, rec.Code)
		}

		unix++
		rec = httptest.NewRecorder()
		server.ServeHTTP(rec, signedGet(tc.path, unix, "nonce-ok-"+tc.path))
		if rec.Code != tc.want {
			t.Fatalf("GET %s with a signed request: HTTP %d (%s), want %d", tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if node.getCalls != 1 {
		t.Fatalf("the node was asked for the escrow %d times; only the signed request should have got that far", node.getCalls)
	}
}

// createWithRealm sends a fully signed create for realm and reports the status.
func createWithRealm(t *testing.T, merchants map[string]MerchantConfig, realmResp *EscrowRealm, realm string) (int, *mockNodeClient) {
	t.Helper()
	payerPriv, payerAddr := newWallet(t)
	_, payeeAddr := newWallet(t)
	node := &mockNodeClient{createResp: &EscrowCreateResponse{ID: "0xabc"}, realmResp: realmResp}
	server, store, _ := newTestServer(t, node, merchants)
	defer store.Close()

	payload := EscrowCreateRequest{
		Payer:    payerAddr,
		Payee:    payeeAddr,
		Token:    "NHB",
		Amount:   "10",
		Deadline: 1700000500,
		Nonce:    1,
		Realm:    realm,
	}
	body, _ := json.Marshal(payload)
	timestamp, nonce, sig := signHeaders("secret", http.MethodPost, "/escrow/create", body, time.Unix(1700000000, 0).UTC(), "nonce-create-realm")
	req := httptest.NewRequest(http.MethodPost, "/escrow/create", bytes.NewReader(body))
	req.Header.Set(headerAPIKey, "test")
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, sig)
	req.Header.Set(headerIdempotencyKey, "idem-realm")
	req.Header.Set(headerWalletAddress, payerAddr)
	req.Header.Set(headerWalletSig, signEscrowCreateEnvelope(t, payerPriv, payload))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec.Code, node
}

// The node's realm result carries no type, so a merchant that configured one
// (or an identity match, which implies "private") could never create an escrow.
// A private realm is the one named after the merchant; a public one is checked
// by scope.
func TestRealmTypeIsCheckedThroughTheIdentityMatch(t *testing.T) {
	realm := func(id, scope string) *EscrowRealm {
		return &EscrowRealm{ID: id, Metadata: &EscrowRealmMetadata{Scope: scope}}
	}
	private := map[string]MerchantConfig{"test": {Identity: "merchant-xyz", Realm: MerchantRealmConfig{Type: "private"}}}
	enforced := map[string]MerchantConfig{"test": {Identity: "merchant-xyz", Realm: MerchantRealmConfig{EnforceIdentityMatch: true}}}
	public := map[string]MerchantConfig{"test": {Identity: "merchant-xyz", Realm: MerchantRealmConfig{Type: "public", Scope: "marketplace"}}}

	if code, node := createWithRealm(t, private, realm("merchant-xyz", "marketplace"), "merchant-xyz"); code != http.StatusCreated || node.createCalls != 1 {
		t.Fatalf("private merchant, its own realm: HTTP %d, %d creates; want 201 and 1", code, node.createCalls)
	}
	if code, node := createWithRealm(t, private, realm("someone-else", "marketplace"), "someone-else"); code != http.StatusBadRequest || node.createCalls != 0 {
		t.Fatalf("private merchant, another realm: HTTP %d, %d creates; want 400 and none", code, node.createCalls)
	}
	if code, node := createWithRealm(t, enforced, realm("merchant-xyz", "platform"), "merchant-xyz"); code != http.StatusCreated || node.createCalls != 1 {
		t.Fatalf("identity match, its own realm: HTTP %d, %d creates; want 201 and 1", code, node.createCalls)
	}
	if code, node := createWithRealm(t, enforced, realm("someone-else", "platform"), "someone-else"); code != http.StatusBadRequest || node.createCalls != 0 {
		t.Fatalf("identity match, another realm: HTTP %d, %d creates; want 400 and none", code, node.createCalls)
	}
	if code, node := createWithRealm(t, public, realm("core", "marketplace"), "core"); code != http.StatusCreated || node.createCalls != 1 {
		t.Fatalf("public merchant, a realm of the right scope: HTTP %d, %d creates; want 201 and 1", code, node.createCalls)
	}
	if code, node := createWithRealm(t, public, realm("core", "platform"), "core"); code != http.StatusBadRequest || node.createCalls != 0 {
		t.Fatalf("public merchant, a realm of the wrong scope: HTTP %d, %d creates; want 400 and none", code, node.createCalls)
	}
}
