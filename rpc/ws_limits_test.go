package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"

	"nhooyr.io/websocket"
)

// newStreamTestServer serves the two WebSocket stream handlers of an RPC server
// on a test HTTP server. Clients pick the address they appear to come from with
// an X-Forwarded-For header, which the server is configured to trust.
func newStreamTestServer(t *testing.T, cfg ServerConfig) (*Server, string) {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	cfg.TrustProxyHeaders = true
	cfg.ProxyHeaders = ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle}
	srv := newTestServer(t, node, nil, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/pos/finality", srv.handlePOSFinalityWS)
	mux.HandleFunc("/ws/explorer", srv.handleExplorerWS)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, "ws" + strings.TrimPrefix(ts.URL, "http")
}

// dialStream opens a stream as a client at the given address, optionally from a
// browser page at the given origin. It returns the connection (nil when the
// server refused it) and the status of the response.
func dialStream(t *testing.T, base, path, clientIP, origin string) (*websocket.Conn, int) {
	t.Helper()
	header := http.Header{}
	if clientIP != "" {
		header.Set("X-Forwarded-For", clientIP)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, base+path, &websocket.DialOptions{HTTPHeader: header})
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err != nil {
		return nil, status
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "test over") })
	return conn, status
}

func openStreams(srv *Server) int {
	srv.wsMu.Lock()
	defer srv.wsMu.Unlock()
	return srv.wsOpen
}

func waitOpenStreams(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if openStreams(srv) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %d open streams, have %d", want, openStreams(srv))
}

func TestWebSocketStreamsFromOneAddressAreLimited(t *testing.T) {
	srv, base := newStreamTestServer(t, ServerConfig{})

	var conns []*websocket.Conn
	refused := 0
	for i := 0; i < defaultWebSocketMaxPerIP+4; i++ {
		conn, status := dialStream(t, base, "/ws/explorer", "203.0.113.5", "")
		if conn == nil {
			if status != http.StatusTooManyRequests {
				t.Fatalf("stream %d: expected 429 once the address is at its cap, got %d", i, status)
			}
			refused++
			continue
		}
		conns = append(conns, conn)
	}
	if len(conns) != defaultWebSocketMaxPerIP || refused != 4 {
		t.Fatalf("expected %d streams and 4 refusals from one address, got %d and %d", defaultWebSocketMaxPerIP, len(conns), refused)
	}

	// Another address has a cap of its own.
	if conn, status := dialStream(t, base, "/ws/explorer", "203.0.113.6", ""); conn == nil {
		t.Fatalf("a different address must not be limited by the first one, got %d", status)
	}

	// A stream that ends gives its slot back.
	conns[0].Close(websocket.StatusNormalClosure, "done")
	waitOpenStreams(t, srv, defaultWebSocketMaxPerIP)
	if conn, status := dialStream(t, base, "/ws/explorer", "203.0.113.5", ""); conn == nil {
		t.Fatalf("expected the freed slot to be usable, got %d", status)
	}
}

func TestWebSocketStreamsAreLimitedInTotal(t *testing.T) {
	_, base := newStreamTestServer(t, ServerConfig{WebSocketMaxConnections: 5, WebSocketMaxPerIP: 100})

	accepted := 0
	for i := 0; i < 9; i++ {
		ip := "203.0.113." + string(rune('0'+i))
		path := "/ws/explorer"
		if i%2 == 1 {
			path = "/ws/pos/finality"
		}
		if conn, _ := dialStream(t, base, path, ip, ""); conn != nil {
			accepted++
		}
	}
	if accepted != 5 {
		t.Fatalf("expected the two stream kinds together to be held to 5 streams, got %d", accepted)
	}
}

func TestWebSocketSlotIsFreedWhenTheClientGoesAway(t *testing.T) {
	srv, base := newStreamTestServer(t, ServerConfig{WebSocketMaxPerIP: 1})

	for i := 0; i < 3; i++ {
		conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.5", "")
		if conn == nil {
			t.Fatalf("round %d: stream refused with %d", i, status)
		}
		waitOpenStreams(t, srv, 1)
		// Nothing is ever sent on this stream: the server can only know that the
		// client left by reading from the connection.
		conn.Close(websocket.StatusNormalClosure, "bye")
		waitOpenStreams(t, srv, 0)
	}
}

func TestWebSocketOriginsAreRestrictedWhenConfigured(t *testing.T) {
	_, base := newStreamTestServer(t, ServerConfig{WebSocketOrigins: []string{"app.example", "*.app.example"}})

	for _, path := range []string{"/ws/explorer", "/ws/pos/finality"} {
		for _, allowed := range []string{"https://app.example", "https://www.app.example"} {
			if conn, status := dialStream(t, base, path, "203.0.113.5", allowed); conn == nil {
				t.Fatalf("%s: origin %s must be allowed, got %d", path, allowed, status)
			}
		}
		if conn, status := dialStream(t, base, path, "203.0.113.5", "https://evil.example"); conn != nil || status != http.StatusForbidden {
			t.Fatalf("%s: an origin that is not listed must be refused with 403, got conn=%v status=%d", path, conn != nil, status)
		}
		// Clients that are not browsers send no Origin header.
		if conn, status := dialStream(t, base, path, "203.0.113.5", ""); conn == nil {
			t.Fatalf("%s: a client without an Origin header must still be served, got %d", path, status)
		}
	}
}

func TestWebSocketOriginsAreOpenUntilConfigured(t *testing.T) {
	_, base := newStreamTestServer(t, ServerConfig{})
	if conn, status := dialStream(t, base, "/ws/explorer", "203.0.113.5", "https://anywhere.example"); conn == nil {
		t.Fatalf("with no origins configured the public stream stays open to any origin, got %d", status)
	}
}
