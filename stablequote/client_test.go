package stablequote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// NHB-AUDIT-S4: Status used to swallow any transport/HTTP/decode error
// into a bare zero-value Status, identical to a genuinely healthy, idle
// engine's own zero-value response. Down distinguishes the two.
func TestHTTPClientStatusSetsDownOnTransportFailure(t *testing.T) {
	client := NewHTTPClient("http://127.0.0.1:1", time.Second)
	status := client.Status(context.Background())
	if !status.Down {
		t.Fatalf("expected Down=true when the upstream is unreachable, got %+v", status)
	}
}

func TestHTTPClientStatusSetsDownOnServiceError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, time.Second)
	status := client.Status(context.Background())
	if !status.Down {
		t.Fatalf("expected Down=true when the upstream returns an error status, got %+v", status)
	}
}

func TestHTTPClientStatusReportsHealthyWithoutDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Quotes":2,"Reservations":1,"Assets":3}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, time.Second)
	status := client.Status(context.Background())
	if status.Down {
		t.Fatalf("expected Down=false for a healthy response, got %+v", status)
	}
	if status.Quotes != 2 || status.Reservations != 1 || status.Assets != 3 {
		t.Fatalf("expected decoded status fields to survive, got %+v", status)
	}
}
