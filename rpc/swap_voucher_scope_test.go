package rpc

// Which partner may read which vouchers (PL-EXT-32). The ledger holds every
// partner's orders. swap_voucher_get, swap_voucher_list and swap_voucher_export
// used to check that the caller was SOME authenticated partner and then hand
// over the whole ledger, so one partner could read every other partner's
// recipients, amounts and provider transaction ids. A partner's key now reads
// the providers it is mapped to in RPCSwapAuth.PartnerProviders and nothing
// else; the operator's bearer credential, on a node with no partner
// authentication, still sees the whole ledger.
//
// Every call here goes through the real dispatch with a really signed request.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"

	nhbstate "nhbchain/core/state"
	swap "nhbchain/native/swap"
)

const (
	keyAlpha = "key-alpha"
	keyBeta  = "key-beta"
	keyNone  = "key-none"
	keyAll   = "key-all"
)

// newPartnerRequest is a request as the handlers of the voucher ledger reads see
// it once the dispatch has accepted env's partner key: the same context the
// dispatch sets, for tests that call a handler directly.
func (env *testEnv) newPartnerRequest() *http.Request {
	req := env.newRequest()
	return req.WithContext(context.WithValue(req.Context(), swapPartnerContextKey, env.swapKey))
}

type scopeEnv struct {
	env     *testEnv
	srv     *Server
	now     time.Time
	nonce   int
	secrets map[string]string
}

// newScopeEnv builds a server with four partner keys on one node:
// key-alpha reads provider "alpha", key-beta reads "beta" (written in the
// config with stray case and space), key-all reads everything ("*") and
// key-none has a secret but no entry, so it reads nothing.
func newScopeEnv(t *testing.T) *scopeEnv {
	t.Helper()
	env := newTestEnv(t)
	se := &scopeEnv{
		env: env,
		now: time.Unix(1700000000, 0).UTC(),
		secrets: map[string]string{
			keyAlpha: "secret-alpha",
			keyBeta:  "secret-beta",
			keyNone:  "secret-none",
			keyAll:   "secret-all",
		},
	}
	se.srv = newTestServer(t, env.node, nil, ServerConfig{
		SwapAuth: SwapAuthConfig{
			Secrets:              se.secrets,
			AllowedTimestampSkew: time.Minute,
			NonceTTL:             2 * time.Minute,
			NonceCapacity:        1024,
			RateLimitWindow:      time.Minute,
			PartnerProviders: map[string][]string{
				keyAlpha: {"alpha"},
				keyBeta:  {" Beta "},
				keyAll:   {"*"},
			},
			Now: func() time.Time { return se.now },
		},
	})
	if se.srv.jwtVerifier != nil {
		se.srv.jwtVerifier.now = func() time.Time { return se.now }
	}
	seedScopedVouchers(t, env, se.now)
	return se
}

// Creation order: A-1 (alpha), B-1 (beta), A-2 ("Alpha " as stored), B-2, A-3.
func seedScopedVouchers(t *testing.T, env *testEnv, now time.Time) {
	t.Helper()
	seeds := []struct {
		id, provider string
		amount       int64
	}{
		{"A-1", "alpha", 1},
		{"B-1", "beta", 10},
		{"A-2", "Alpha ", 100},
		{"B-2", "beta", 1000},
		{"A-3", "alpha", 10000},
	}
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		for i, seed := range seeds {
			rec := &swap.VoucherRecord{
				Provider:      seed.provider,
				ProviderTxID:  seed.id,
				MintAmountWei: big.NewInt(seed.amount),
				Username:      "user-of-" + seed.id,
				CreatedAt:     now.Unix() + int64(i),
				Status:        swap.VoucherStatusMinted,
			}
			if err := ledger.Put(rec); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed vouchers: %v", err)
	}
}

// call sends one really signed request as the partner with the given key.
func (se *scopeEnv) call(t *testing.T, key, method string, params ...any) (*httptest.ResponseRecorder, json.RawMessage, *RPCError) {
	t.Helper()
	raw := make([]json.RawMessage, len(params))
	for i, p := range params {
		raw[i] = marshalParam(t, p)
	}
	body := buildRPCRequestBody(t, 1, method, raw)
	// The partner scheme wants each request's timestamp to be later than the last.
	se.now = se.now.Add(time.Second)
	se.nonce++
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	timestamp := strconv.FormatInt(se.now.Unix(), 10)
	nonce := fmt.Sprintf("scope-nonce-%d", se.nonce)
	sig := gatewayauth.ComputeSignature(se.secrets[key], timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), body)
	req.Header.Set(gatewayauth.HeaderAPIKey, key)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(sig))
	rec := httptest.NewRecorder()
	se.srv.ServeHTTP(rec, req)
	result, rpcErr := decodeRPCResponse(t, rec)
	return rec, result, rpcErr
}

type listResult struct {
	Vouchers   []map[string]interface{} `json:"vouchers"`
	NextCursor string                   `json:"nextCursor"`
}

func (se *scopeEnv) list(t *testing.T, key string, params ...any) ([]string, string) {
	t.Helper()
	rec, result, rpcErr := se.call(t, key, "swap_voucher_list", params...)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("%s list: HTTP %d, error %+v (%s)", key, rec.Code, rpcErr, rec.Body.String())
	}
	var out listResult
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	got := make([]string, 0, len(out.Vouchers))
	for _, v := range out.Vouchers {
		got = append(got, v["providerTxId"].(string))
	}
	return got, out.NextCursor
}

func decodeBase64(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return string(raw)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPartnerGetReadsItsOwnVoucherOnly(t *testing.T) {
	se := newScopeEnv(t)

	rec, result, rpcErr := se.call(t, keyAlpha, "swap_voucher_get", "A-1")
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("alpha reads its own voucher: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	var own map[string]interface{}
	if err := json.Unmarshal(result, &own); err != nil || own["providerTxId"] != "A-1" {
		t.Fatalf("unexpected own voucher %s (%v)", result, err)
	}
	// "Alpha " as the ledger stored it is still alpha's.
	if rec, _, rpcErr = se.call(t, keyAlpha, "swap_voucher_get", "A-2"); rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("alpha reads A-2 (provider stored as %q): HTTP %d, error %+v", "Alpha ", rec.Code, rpcErr)
	}
	// beta's key is written " Beta " in the config and reads provider "beta".
	if rec, _, rpcErr = se.call(t, keyBeta, "swap_voucher_get", "B-1"); rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("beta reads its own voucher: HTTP %d, error %+v", rec.Code, rpcErr)
	}

	// Another provider's voucher must be answered exactly as a voucher that does
	// not exist, or the answer tells the caller which ids exist.
	foreignRec, _, foreignErr := se.call(t, keyAlpha, "swap_voucher_get", "B-1")
	absentRec, _, absentErr := se.call(t, keyAlpha, "swap_voucher_get", "Z-9")
	if foreignRec.Code != http.StatusNotFound || foreignErr == nil {
		t.Fatalf("alpha reads beta's voucher: HTTP %d, error %+v; want 404 not found", foreignRec.Code, foreignErr)
	}
	if absentRec.Code != foreignRec.Code || absentErr == nil || absentErr.Code != foreignErr.Code || absentErr.Message != foreignErr.Message {
		t.Fatalf("a foreign voucher is answered %d/%+v, an absent one %d/%+v: they must look the same", foreignRec.Code, foreignErr, absentRec.Code, absentErr)
	}
	if strings.Contains(foreignRec.Body.String(), "user-of-B-1") {
		t.Fatalf("the refusal carries the other partner's record: %s", foreignRec.Body.String())
	}
}

func TestPartnerListSeesOnlyItsOwnVouchers(t *testing.T) {
	se := newScopeEnv(t)
	got, cursor := se.list(t, keyAlpha, 0, 0)
	if want := []string{"A-1", "A-2", "A-3"}; !sameStrings(got, want) || cursor != "" {
		t.Fatalf("alpha lists %v (cursor %q), want %v", got, cursor, want)
	}
	got, cursor = se.list(t, keyBeta, 0, 0)
	if want := []string{"B-1", "B-2"}; !sameStrings(got, want) || cursor != "" {
		t.Fatalf("beta lists %v (cursor %q), want %v", got, cursor, want)
	}
}

func TestPartnerListPagesThroughItsOwnVouchersOnly(t *testing.T) {
	se := newScopeEnv(t)
	page1, cursor := se.list(t, keyAlpha, 0, 0, "", 2)
	if want := []string{"A-1", "A-2"}; !sameStrings(page1, want) || cursor != "A-2" {
		t.Fatalf("page 1 = %v (cursor %q), want %v and cursor A-2", page1, cursor, want)
	}
	page2, cursor := se.list(t, keyAlpha, 0, 0, cursor, 2)
	if want := []string{"A-3"}; !sameStrings(page2, want) || cursor != "" {
		t.Fatalf("page 2 = %v (cursor %q), want %v and no cursor", page2, cursor, want)
	}
	// beta's last voucher is followed by nothing of beta's: no cursor.
	got, cursor := se.list(t, keyBeta, 0, 0, "", 2)
	if want := []string{"B-1", "B-2"}; !sameStrings(got, want) || cursor != "" {
		t.Fatalf("beta page = %v (cursor %q), want %v and no cursor", got, cursor, want)
	}
}

func TestPartnerCursorOfAnotherPartnerIsIgnored(t *testing.T) {
	se := newScopeEnv(t)
	foreign, foreignCursor := se.list(t, keyAlpha, 0, 0, "B-1", 2)
	unknown, unknownCursor := se.list(t, keyAlpha, 0, 0, "Z-9", 2)
	if !sameStrings(foreign, unknown) || foreignCursor != unknownCursor {
		t.Fatalf("a foreign cursor answers %v/%q, an unknown one %v/%q: they must be the same", foreign, foreignCursor, unknown, unknownCursor)
	}
}

func TestPartnerExportCoversItsOwnVouchersOnly(t *testing.T) {
	se := newScopeEnv(t)
	rec, result, rpcErr := se.call(t, keyAlpha, "swap_voucher_export", 0, 0)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("alpha export: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	var out struct {
		CSVBase64    string `json:"csvBase64"`
		Count        int    `json:"count"`
		TotalMintWei string `json:"totalMintWei"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if out.Count != 3 || out.TotalMintWei != "10101" {
		t.Fatalf("alpha export count %d total %s, want 3 and 10101 (beta's 1010 must not be in it)", out.Count, out.TotalMintWei)
	}
	csvText := decodeBase64(t, out.CSVBase64)
	for _, leaked := range []string{"B-1", "B-2", "user-of-B"} {
		if strings.Contains(csvText, leaked) {
			t.Fatalf("alpha's export holds %q:\n%s", leaked, csvText)
		}
	}
	if !strings.Contains(csvText, "A-1") || !strings.Contains(csvText, "A-3") {
		t.Fatalf("alpha's export lacks its own vouchers:\n%s", csvText)
	}
}

func TestPartnerWithNoProviderEntryReadsNothing(t *testing.T) {
	se := newScopeEnv(t)
	if rec, _, rpcErr := se.call(t, keyNone, "swap_voucher_get", "A-1"); rec.Code != http.StatusNotFound || rpcErr == nil {
		t.Fatalf("an unmapped key reads A-1: HTTP %d, error %+v; want 404", rec.Code, rpcErr)
	}
	if got, cursor := se.list(t, keyNone, 0, 0); len(got) != 0 || cursor != "" {
		t.Fatalf("an unmapped key lists %v (cursor %q); want nothing", got, cursor)
	}
	rec, result, rpcErr := se.call(t, keyNone, "swap_voucher_export", 0, 0)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("an unmapped key's export: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	var out struct {
		Count        int    `json:"count"`
		TotalMintWei string `json:"totalMintWei"`
	}
	if err := json.Unmarshal(result, &out); err != nil || out.Count != 0 || out.TotalMintWei != "0" {
		t.Fatalf("an unmapped key's export = %s (%v); want count 0 and total 0", result, err)
	}
}

func TestPartnerMappedToEverythingSeesTheWholeLedger(t *testing.T) {
	se := newScopeEnv(t)
	got, _ := se.list(t, keyAll, 0, 0)
	if want := []string{"A-1", "B-1", "A-2", "B-2", "A-3"}; !sameStrings(got, want) {
		t.Fatalf("the all-providers key lists %v, want %v", got, want)
	}
	if rec, _, rpcErr := se.call(t, keyAll, "swap_voucher_get", "B-2"); rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("the all-providers key reads B-2: HTTP %d, error %+v", rec.Code, rpcErr)
	}
}

// The handlers must not trust the dispatch to have authenticated the caller: if
// partner authentication is on and no partner identity reached them they hand
// over nothing, rather than the whole ledger.
func TestVoucherReadHandlersRefuseACallerWithNoPartnerIdentity(t *testing.T) {
	se := newScopeEnv(t)
	cases := []struct {
		method string
		params []any
		run    func(*httptest.ResponseRecorder, *RPCRequest)
	}{
		{"swap_voucher_get", []any{"A-1"}, func(w *httptest.ResponseRecorder, r *RPCRequest) {
			se.srv.handleSwapVoucherGet(w, se.env.newRequest(), r)
		}},
		{"swap_voucher_list", []any{0, 0}, func(w *httptest.ResponseRecorder, r *RPCRequest) {
			se.srv.handleSwapVoucherList(w, se.env.newRequest(), r)
		}},
		{"swap_voucher_export", []any{0, 0}, func(w *httptest.ResponseRecorder, r *RPCRequest) {
			se.srv.handleSwapVoucherExport(w, se.env.newRequest(), r)
		}},
	}
	for _, c := range cases {
		params := make([]json.RawMessage, len(c.params))
		for i, p := range c.params {
			params[i] = marshalParam(t, p)
		}
		rec := httptest.NewRecorder()
		c.run(rec, &RPCRequest{ID: 1, Params: params})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with no partner identity: HTTP %d, want 401 (body %s)", c.method, rec.Code, rec.Body.String())
		}
		for _, leaked := range []string{"A-1", "B-1", "user-of"} {
			if strings.Contains(rec.Body.String(), leaked) {
				t.Fatalf("%s with no partner identity leaks %q: %s", c.method, leaked, rec.Body.String())
			}
		}
	}
}

// On a node with no partner authentication the ledger reads take the operator's
// bearer credential, and that credential still sees every provider.
func TestOperatorCredentialStillSeesTheWholeLedger(t *testing.T) {
	env := newTestEnv(t)
	seedScopedVouchers(t, env, env.now)
	srv := bearerOnlyServer(t, env, nil, ServerConfig{})

	rec, rpcErr := serveBearer(t, srv, env.token, "swap_voucher_list", 0, 0)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("operator list: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	result, _ := decodeRPCResponse(t, rec)
	var out listResult
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Vouchers) != 5 {
		t.Fatalf("the operator sees %d vouchers, want all 5", len(out.Vouchers))
	}
	rec, rpcErr = serveBearer(t, srv, env.token, "swap_voucher_get", "B-1")
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("operator get of B-1: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	rec, rpcErr = serveBearer(t, srv, env.token, "swap_voucher_export", 0, 0)
	if rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("operator export: HTTP %d, error %+v", rec.Code, rpcErr)
	}
	result, _ = decodeRPCResponse(t, rec)
	var exp struct {
		Count        int    `json:"count"`
		TotalMintWei string `json:"totalMintWei"`
	}
	if err := json.Unmarshal(result, &exp); err != nil || exp.Count != 5 || exp.TotalMintWei != "11111" {
		t.Fatalf("operator export = %s (%v), want count 5 and total 11111", result, err)
	}
}

func TestSwapAuthConfigNormalisesPartnerProviders(t *testing.T) {
	se := newScopeEnv(t)
	got := se.srv.swapPartnerProviders
	if _, ok := got[keyBeta]["beta"]; !ok {
		t.Fatalf("the provider list of %s is %v; want \"beta\" (config wrote \" Beta \")", keyBeta, got[keyBeta])
	}
	if _, ok := got[keyNone]; ok {
		t.Fatalf("a key with no entry must have no scope entry, got %v", got[keyNone])
	}
}
