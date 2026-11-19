package routes

// The transaction route and the escrow lookup route build the X-Forwarded-For
// value they send to the node with the same helper, so the two stay in step.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestForwardedForValue(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		callerXFF  string
		want       string
	}{
		{name: "observed address only", remoteAddr: "198.51.100.7:4321", want: "198.51.100.7"},
		{name: "caller value then observed", remoteAddr: "198.51.100.7:4321", callerXFF: "203.0.113.9", want: "203.0.113.9, 198.51.100.7"},
		{name: "surrounding space in caller value is trimmed", remoteAddr: "198.51.100.7:4321", callerXFF: "  203.0.113.9 ", want: "203.0.113.9, 198.51.100.7"},
		{name: "ipv6 peer", remoteAddr: "[2001:db8::7]:4321", callerXFF: "203.0.113.9", want: "203.0.113.9, 2001:db8::7"},
		{name: "peer without port", remoteAddr: "198.51.100.7", want: "198.51.100.7"},
		// Without an observed address the caller's own value is not passed on.
		{name: "no observed address", remoteAddr: "", callerXFF: "203.0.113.9", want: ""},
		{name: "nothing at all", remoteAddr: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.callerXFF != "" {
				req.Header.Set("X-Forwarded-For", tc.callerXFF)
			}
			if got := forwardedForValue(req); got != tc.want {
				t.Fatalf("forwardedForValue = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTransactionsSendForwardsObservedAddressLast(t *testing.T) {
	var (
		mu   sync.Mutex
		seen []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Values("X-Forwarded-For")...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x00"}`))
	}))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	tr, err := newTransactionsRoutes(target)
	if err != nil {
		t.Fatalf("newTransactionsRoutes: %v", err)
	}
	router := chi.NewRouter()
	tr.mount(router)

	body := `{"jsonrpc":"2.0","id":1,"method":"nhb_sendTransaction","params":[{"type":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body))
	req.RemoteAddr = "198.51.100.7:4321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "203.0.113.9, 198.51.100.7" {
		t.Fatalf("X-Forwarded-For = %q, want [\"203.0.113.9, 198.51.100.7\"]", seen)
	}
}
