package rpc

// Who may read the swap voucher ledger and the peer list, and how much of the
// ledger one call may ask for. The ledger holds every partner order and the peer
// list names every peer with its score and ban state; on a server with no partner
// authentication configured (the shipped configurations leave it unset) they used
// to be readable by any client that could reach the port. They go through the real
// dispatch.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	swap "nhbchain/native/swap"
	"nhbchain/p2p"
)

// bearerOnlyServer is a server on env's node with no partner authentication
// configured and the JWT clock pinned to env's.
func bearerOnlyServer(t *testing.T, env *testEnv, net NetworkService, cfg ServerConfig) *Server {
	t.Helper()
	srv := newTestServer(t, env.node, net, cfg)
	if srv.jwtVerifier != nil {
		srv.jwtVerifier.now = func() time.Time { return env.now }
	}
	return srv
}

func httptestPost(body []byte) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
}

func serveRequest(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func seedVouchers(t *testing.T, env *testEnv, n int) {
	t.Helper()
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		for i := 0; i < n; i++ {
			record := &swap.VoucherRecord{
				Provider:      "test-provider",
				ProviderTxID:  fmt.Sprintf("PTX-%04d", i),
				MintAmountWei: big.NewInt(1),
				CreatedAt:     env.now.Unix() + int64(i),
				Status:        swap.VoucherStatusMinted,
			}
			if err := ledger.Put(record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed vouchers: %v", err)
	}
}

func TestVoucherLedgerReadsNeedTheBearerCredentialWithoutPartnerAuth(t *testing.T) {
	env := newTestEnv(t)
	seedVouchers(t, env, 1)
	srv := bearerOnlyServer(t, env, nil, ServerConfig{})

	calls := []struct {
		method string
		params []any
		want   int // status once the caller is authenticated
	}{
		{"swap_voucher_list", []any{0, 0}, http.StatusOK},
		{"swap_voucher_export", []any{0, 0}, http.StatusOK},
		{"swap_voucher_get", []any{"PTX-0000"}, http.StatusOK},
	}
	for _, call := range calls {
		rec, rpcErr := serveBearer(t, srv, "", call.method, call.params...)
		if rec.Code != http.StatusUnauthorized || rpcErr == nil || rpcErr.Code != codeUnauthorized {
			t.Fatalf("%s without a credential: HTTP %d, error %+v; want 401 unauthorized (body %s)", call.method, rec.Code, rpcErr, rec.Body.String())
		}
		rec, rpcErr = serveBearer(t, srv, "not-a-token", call.method, call.params...)
		if rec.Code != http.StatusUnauthorized || rpcErr == nil {
			t.Fatalf("%s with a bad token: HTTP %d, error %+v; want 401", call.method, rec.Code, rpcErr)
		}
		rec, rpcErr = serveBearer(t, srv, env.token, call.method, call.params...)
		if rec.Code != call.want || rpcErr != nil {
			t.Fatalf("%s with the bearer token: HTTP %d, error %+v; want %d (body %s)", call.method, rec.Code, rpcErr, call.want, rec.Body.String())
		}
	}
}

// With partner authentication configured, a signed partner request is the
// credential for the ledger reads: no bearer token is asked of it on top, and a
// request with neither is refused.
func TestVoucherLedgerReadsAcceptTheSignedPartnerRequest(t *testing.T) {
	env := newTestEnv(t)
	seedVouchers(t, env, 1)
	params := map[string][]json.RawMessage{
		"swap_voucher_list":   {marshalParam(t, 0), marshalParam(t, 0)},
		"swap_voucher_export": {marshalParam(t, 0), marshalParam(t, 0)},
		"swap_voucher_get":    {marshalParam(t, "PTX-0000")},
	}
	n := 0
	for method, p := range params {
		body := buildRPCRequestBody(t, 1, method, p)

		// The partner scheme wants each request's timestamp to be later than the last.
		env.now = env.now.Add(time.Second)
		signed := httptestPost(body)
		n++
		env.signSwapRequest(t, signed, body, fmt.Sprintf("nonce-%d", n))
		rec := serveRequest(env.server, signed)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with a signed partner request: HTTP %d (%s)", method, rec.Code, rec.Body.String())
		}

		unsigned := httptestPost(body)
		rec = serveRequest(env.server, unsigned)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with no credential at all: HTTP %d, want 401", method, rec.Code)
		}
	}
}

func TestVoucherListPageIsCapped(t *testing.T) {
	env := newTestEnv(t)
	const total = 205
	seedVouchers(t, env, total)
	srv := bearerOnlyServer(t, env, nil, ServerConfig{})

	list := func(cursor string) (vouchers []map[string]any, next string) {
		t.Helper()
		rec, rpcErr := serveBearer(t, srv, env.token, "swap_voucher_list", 0, 0, cursor, 1_000_000)
		if rec.Code != http.StatusOK || rpcErr != nil {
			t.Fatalf("swap_voucher_list: HTTP %d, error %+v", rec.Code, rpcErr)
		}
		result, _ := decodeRPCResponse(t, rec)
		var page struct {
			Vouchers   []map[string]any `json:"vouchers"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			t.Fatalf("decode page: %v", err)
		}
		return page.Vouchers, page.NextCursor
	}

	first, next := list("")
	if len(first) != 200 {
		t.Fatalf("a call asking for a million records got %d; a page must not exceed 200", len(first))
	}
	if next == "" {
		t.Fatalf("a capped page must carry the cursor to the rest")
	}
	rest, next := list(next)
	if len(rest) != total-200 || next != "" {
		t.Fatalf("the second page has %d records and cursor %q; want %d and none", len(rest), next, total-200)
	}
}

// The ledger reads scan every record of the range while the node's state lock is
// held, so they take a slot in the query pool like the other heavy reads: while
// the pool is full they are refused before touching the state.
func TestVoucherListAndExportAreRefusedWhileThePoolIsFull(t *testing.T) {
	env := newTestEnv(t)
	seedVouchers(t, env, 3)
	srv := bearerOnlyServer(t, env, nil, ServerConfig{QueryMaxConcurrent: 1, QueryQueueDepth: 1, QueryQueueWait: 20 * time.Millisecond})
	held, err := srv.queryGate.acquire(context.Background(), "someone-else", false)
	if err != nil {
		t.Fatalf("hold the pool: %v", err)
	}
	for _, call := range []struct {
		method string
		params []any
	}{
		{"swap_voucher_list", []any{0, 0}},
		{"swap_voucher_export", []any{0, 0}},
	} {
		rec, rpcErr := serveBearer(t, srv, env.token, call.method, call.params...)
		if rec.Code != http.StatusTooManyRequests || rpcErr == nil || rpcErr.Code != codeRateLimited {
			t.Fatalf("%s while the pool is full: HTTP %d, error %+v; want 429 rate limited", call.method, rec.Code, rpcErr)
		}
	}
	held()
	for _, call := range []struct {
		method string
		params []any
	}{
		{"swap_voucher_list", []any{0, 0}},
		{"swap_voucher_export", []any{0, 0}},
	} {
		if rec, rpcErr := serveBearer(t, srv, env.token, call.method, call.params...); rec.Code != http.StatusOK || rpcErr != nil {
			t.Fatalf("%s once the pool has room: HTTP %d, error %+v", call.method, rec.Code, rpcErr)
		}
	}
	if running, waiting := srv.queryGate.load(); running != 0 || waiting != 0 {
		t.Fatalf("slots leaked: %d running, %d waiting", running, waiting)
	}
}

func TestPeerListNeedsTheBearerCredential(t *testing.T) {
	env := newTestEnv(t)
	net := &stubNetwork{peers: []p2p.PeerNetInfo{{NodeID: "peer-1", Address: "203.0.113.9:6001", State: "connected"}}}
	srv := bearerOnlyServer(t, env, net, ServerConfig{})

	for _, method := range []string{"net_peers", "p2p_peers"} {
		rec, rpcErr := serveBearer(t, srv, "", method)
		if rec.Code != http.StatusUnauthorized || rpcErr == nil || rpcErr.Code != codeUnauthorized {
			t.Fatalf("%s without a credential: HTTP %d, error %+v; want 401 unauthorized (body %s)", method, rec.Code, rpcErr, rec.Body.String())
		}
		rec, rpcErr = serveBearer(t, srv, env.token, method)
		if rec.Code != http.StatusOK || rpcErr != nil {
			t.Fatalf("%s with the bearer token: HTTP %d, error %+v", method, rec.Code, rpcErr)
		}
		result, _ := decodeRPCResponse(t, rec)
		var peers []map[string]any
		if err := json.Unmarshal(result, &peers); err != nil || len(peers) != 1 {
			t.Fatalf("%s returned %s (%v); want the one peer", method, result, err)
		}
	}

	// net_info stays what the operator dashboards read.
	if rec, rpcErr := serveBearer(t, srv, "", "net_info"); rec.Code != http.StatusOK || rpcErr != nil {
		t.Fatalf("net_info without a credential: HTTP %d, error %+v; it is public", rec.Code, rpcErr)
	}
}
