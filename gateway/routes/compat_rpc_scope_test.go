package routes

// A call through /rpc is held to the scopes of the service that would answer it
// (the ones that service's own routes require), and a batch is bounded.

import (
	"net/http"
	"strings"
	"testing"

	"nhbchain/gateway/compat"
	"nhbchain/gateway/middleware"
)

func withScopeGuard(d *compat.Dispatcher) {
	d.SetScopeGuard(compat.ScopeGuard{
		Required: map[string][]string{
			"lendingd": {"lending"},
			"swapd":    {"swap"},
		},
		FromContext: middleware.ScopesFromContext,
	})
}

func TestCompatRPCEnforcesTheScopeOfTheServiceThatAnswers(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, nil, withScopeGuard)
	lendingToken := compatToken(t, "lending")

	rec := postRPC(router, lendingToken, `{"jsonrpc":"2.0","id":1,"method":"swap_limits","params":[]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "insufficient scope") {
		t.Fatalf("a lending token calling a swap method: HTTP %d (%s), want the insufficient scope error", rec.Code, rec.Body.String())
	}
	if calls := up.seen(); len(calls) != 0 {
		t.Fatalf("a call without the scope reached the service: %v", calls)
	}

	if rec := postRPC(router, lendingToken, lendPoolsCall); !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("a lending token calling a lending method: %s", rec.Body.String())
	}
	if rec := postRPC(router, compatToken(t, "lending swap"), `{"jsonrpc":"2.0","id":1,"method":"swap_limits","params":[]}`); !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("a token with both scopes calling a swap method: %s", rec.Body.String())
	}
	if calls := up.seen(); len(calls) != 2 {
		t.Fatalf("the service saw %d calls, want the 2 that were allowed", len(calls))
	}

	// In a batch each call is judged on its own.
	rec = postRPC(router, lendingToken, `[`+lendPoolsCall+`,{"jsonrpc":"2.0","id":2,"method":"swap_limits","params":[]}]`)
	body := rec.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "insufficient scope") {
		t.Fatalf("a batch of one allowed and one refused call: %s", body)
	}
}

func TestCompatRPCBatchIsBounded(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, nil, withScopeGuard)
	token := compatToken(t, "lending")

	calls := make([]string, 0, 11)
	for i := 0; i < 11; i++ {
		calls = append(calls, lendPoolsCall)
	}
	rec := postRPC(router, token, `[`+strings.Join(calls, ",")+`]`)
	if !strings.Contains(rec.Body.String(), "exceeds the limit") {
		t.Fatalf("a batch of 11: %s", rec.Body.String())
	}
	if seen := up.seen(); len(seen) != 0 {
		t.Fatalf("a refused batch reached the service %d times", len(seen))
	}

	rec = postRPC(router, token, `[`+strings.Join(calls[:10], ",")+`]`)
	if strings.Contains(rec.Body.String(), "exceeds the limit") || len(up.seen()) != 10 {
		t.Fatalf("a batch of 10: %s (service saw %d calls)", rec.Body.String(), len(up.seen()))
	}
}
