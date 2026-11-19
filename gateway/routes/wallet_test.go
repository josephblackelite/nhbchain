package routes

// The escrow lookup route relays the caller's address to the node the same way
// the transaction route does: whatever X-Forwarded-For the caller sent is kept,
// and the address the gateway itself observed is appended last.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

type walletUpstreamCall struct {
	method    string
	forwarded []string
}

// walletUpstream stands in for the node's JSON-RPC listener: it answers
// escrow_get and identity_reverse and records the X-Forwarded-For header lines
// each call arrived with.
type walletUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	calls []walletUpstreamCall
}

func newWalletUpstream(t *testing.T) *walletUpstream {
	t.Helper()
	up := &walletUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req walletRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		up.mu.Lock()
		up.calls = append(up.calls, walletUpstreamCall{
			method:    req.Method,
			forwarded: append([]string(nil), r.Header.Values("X-Forwarded-For")...),
		})
		up.mu.Unlock()

		var result interface{}
		switch req.Method {
		case "escrow_get":
			result = walletEscrowResult{ID: "escrow-1", Payer: "nhb1payer", Payee: "nhb1payee", Status: "funded", Token: "NHB", Amount: "10"}
		case "identity_reverse":
			result = walletIdentityResult{Alias: "alice", AliasID: "alias-1"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(up.Close)
	return up
}

func (u *walletUpstream) seen() []walletUpstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]walletUpstreamCall(nil), u.calls...)
}

func newWalletTestRouter(t *testing.T, up *walletUpstream) http.Handler {
	t.Helper()
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	wr, err := newWalletRoutes(target)
	if err != nil {
		t.Fatalf("newWalletRoutes: %v", err)
	}
	router := chi.NewRouter()
	wr.mount(router)
	return router
}

func TestWalletEscrowForwardsObservedAddressLast(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		callerXFF  string
		want       string
	}{
		{
			name:       "no caller header",
			remoteAddr: "198.51.100.7:4321",
			want:       "198.51.100.7",
		},
		{
			name:       "caller header is kept and the observed address follows",
			remoteAddr: "198.51.100.7:4321",
			callerXFF:  "203.0.113.9",
			want:       "203.0.113.9, 198.51.100.7",
		},
		{
			name:       "caller chain is kept in order",
			remoteAddr: "198.51.100.7:4321",
			callerXFF:  "203.0.113.9, 192.0.2.44",
			want:       "203.0.113.9, 192.0.2.44, 198.51.100.7",
		},
		{
			name:       "ipv6 peer with port",
			remoteAddr: "[2001:db8::7]:4321",
			want:       "2001:db8::7",
		},
		{
			name:       "peer address without a port",
			remoteAddr: "198.51.100.7",
			callerXFF:  "203.0.113.9",
			want:       "203.0.113.9, 198.51.100.7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newWalletUpstream(t)
			router := newWalletTestRouter(t, up)

			req := httptest.NewRequest(http.MethodGet, "/wallet/escrows/escrow-1", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.callerXFF != "" {
				req.Header.Set("X-Forwarded-For", tc.callerXFF)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}

			calls := up.seen()
			if len(calls) != 2 || calls[0].method != "escrow_get" || calls[1].method != "identity_reverse" {
				t.Fatalf("upstream calls = %+v, want escrow_get then identity_reverse", calls)
			}
			for _, call := range calls {
				if len(call.forwarded) != 1 || call.forwarded[0] != tc.want {
					t.Fatalf("%s X-Forwarded-For = %q, want [%q]", call.method, call.forwarded, tc.want)
				}
			}
		})
	}
}
