package rpc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	posv1 "nhbchain/proto/pos"
	"nhbchain/storage"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"nhooyr.io/websocket"
)

// newStreamTestServer serves the two WebSocket stream handlers of an RPC server
// on a test HTTP server. Clients pick the address they appear to come from with
// an X-Forwarded-For header, which the server honours because the test's own
// loopback client is listed as a trusted proxy (a forwarded address is honoured
// only from a listed proxy).
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
	cfg.TrustedProxies = []string{"127.0.0.1", "::1"}
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

// ipv6InPrefix returns the n-th address of the /64 numbered prefix, so that
// tests can speak for several addresses of one IPv6 network.
func ipv6InPrefix(prefix, n int) string {
	return fmt.Sprintf("2001:db8:0:%x::%x", prefix, n)
}

func TestWebSocketStreamsFromOneIPv6PrefixShareOneCap(t *testing.T) {
	_, base := newStreamTestServer(t, ServerConfig{WebSocketMaxConnections: 100})

	// One IPv6 network can send from any address in its /64, so addresses of the
	// same /64 are one client as far as the per-address cap goes.
	accepted := 0
	for i := 1; i <= 3*defaultWebSocketMaxPerIP; i++ {
		conn, status := dialStream(t, base, "/ws/explorer", ipv6InPrefix(1, i), "")
		if conn == nil {
			if status != http.StatusTooManyRequests {
				t.Fatalf("address %d: expected 429 once the /64 is at its cap, got %d", i, status)
			}
			continue
		}
		accepted++
	}
	if accepted != defaultWebSocketMaxPerIP {
		t.Fatalf("expected one /64 to hold %d streams, it holds %d", defaultWebSocketMaxPerIP, accepted)
	}

	// Another /64 and an IPv4 client each have a cap of their own, on either stream.
	if conn, status := dialStream(t, base, "/ws/explorer", ipv6InPrefix(2, 1), ""); conn == nil {
		t.Fatalf("a different /64 must not be limited by the first one, got %d", status)
	}
	if conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.5", ""); conn == nil {
		t.Fatalf("an IPv4 client must not be limited by an IPv6 /64, got %d", status)
	}
}

func TestOneIPv6PrefixCannotTakeEveryStreamSlot(t *testing.T) {
	srv, base := newStreamTestServer(t, ServerConfig{WebSocketMaxConnections: 2 * defaultWebSocketMaxPerIP})

	for i := 1; i <= 4*defaultWebSocketMaxPerIP; i++ {
		dialStream(t, base, "/ws/pos/finality", ipv6InPrefix(1, i), "")
	}
	waitOpenStreams(t, srv, defaultWebSocketMaxPerIP)
	if conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.9", ""); conn == nil {
		t.Fatalf("a client outside the /64 was refused (%d) although the /64 is held to its own cap", status)
	}
}

func TestIPv6StreamSlotsAreReleasedUnderTheSameKey(t *testing.T) {
	srv, base := newStreamTestServer(t, ServerConfig{})

	var conns []*websocket.Conn
	for i := 1; i <= 3; i++ {
		conn, status := dialStream(t, base, "/ws/explorer", ipv6InPrefix(1, i), "")
		if conn == nil {
			t.Fatalf("stream %d refused with %d", i, status)
		}
		conns = append(conns, conn)
	}
	waitOpenStreams(t, srv, 3)
	for _, conn := range conns {
		conn.Close(websocket.StatusNormalClosure, "done")
	}
	waitOpenStreams(t, srv, 0)

	// A slot given back must leave no count behind for the /64.
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.wsMu.Lock()
		left := len(srv.wsOpenByIP)
		srv.wsMu.Unlock()
		if left == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected no per-address counts once every stream ended, have %d", left)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newServedStreamServer runs the real RPC server (Serve, which puts the gRPC
// dispatcher in front of the JSON-RPC mux) on a loopback listener and returns
// it with its address. Like newStreamTestServer it lets clients pick the
// address they appear to come from with a forwarded address, honoured because
// the test's own loopback client is a listed proxy.
func newServedStreamServer(t *testing.T, cfg ServerConfig) (*Server, string) {
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
	cfg.AllowInsecure = true
	cfg.TrustProxyHeaders = true
	cfg.TrustedProxies = []string{"127.0.0.1", "::1"}
	cfg.ProxyHeaders = ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle}
	srv := newTestServer(t, node, nil, cfg)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	t.Cleanup(func() {
		// A graceful stop of the gRPC server cannot drain streams served through
		// net/http (it panics if one is still registered), so stop it outright
		// first; the graceful stop in Shutdown then has nothing left to drain.
		srv.serverMu.Lock()
		grpcServer := srv.grpcServer
		srv.serverMu.Unlock()
		if grpcServer != nil {
			grpcServer.Stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-serveErr
	})

	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.serverMu.Lock()
		ready := srv.httpServer != nil
		srv.serverMu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start listening before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return srv, listener.Addr().String()
}

func newFinalityClient(t *testing.T, addr string) posv1.RealtimeClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return posv1.NewRealtimeClient(conn)
}

// openFinalityStream subscribes to the gRPC finality stream as a client at the
// given address. It reports whether the stream stayed open, the status the
// server refused it with when it did not, and a function that ends the stream.
func openFinalityStream(t *testing.T, client posv1.RealtimeClient, clientIP string) (bool, codes.Code, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "x-forwarded-for", clientIP)
	stream, err := client.SubscribeFinality(ctx, &posv1.SubscribeFinalityRequest{})
	if err != nil {
		return false, status.Code(err), cancel
	}
	// A refused stream ends at once with a status; an accepted one has nothing to
	// say until a finality update is published.
	ended := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		ended <- err
	}()
	select {
	case err := <-ended:
		return false, status.Code(err), cancel
	case <-time.After(300 * time.Millisecond):
		return true, codes.OK, cancel
	}
}

func TestGRPCFinalityStreamHonoursTheAllowlist(t *testing.T) {
	_, addr := newServedStreamServer(t, ServerConfig{AllowlistCIDRs: []string{"198.51.100.0/24"}})
	client := newFinalityClient(t, addr)

	open, code, _ := openFinalityStream(t, client, "203.0.113.5")
	if open {
		t.Fatalf("a client outside the allowlist holds an open gRPC finality stream")
	}
	if code != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for a client outside the allowlist, got %v", code)
	}
	if open, code, _ := openFinalityStream(t, client, "198.51.100.7"); !open {
		t.Fatalf("a client on the allowlist was refused: %v", code)
	}
}

func TestGRPCFinalityStreamsAreLimited(t *testing.T) {
	_, addr := newServedStreamServer(t, ServerConfig{WebSocketMaxConnections: 10, WebSocketMaxPerIP: 2})
	client := newFinalityClient(t, addr)

	opened := 0
	for i := 0; i < 5; i++ {
		open, code, _ := openFinalityStream(t, client, "203.0.113.5")
		if open {
			opened++
			continue
		}
		if code != codes.ResourceExhausted {
			t.Fatalf("stream %d: expected ResourceExhausted once the address is at its cap, got %v", i, code)
		}
	}
	if opened != 2 {
		t.Fatalf("expected one address to hold 2 gRPC finality streams, it holds %d", opened)
	}

	// The cap counts an IPv6 /64 as one address here as well.
	opened = 0
	for i := 1; i <= 5; i++ {
		if open, _, _ := openFinalityStream(t, client, ipv6InPrefix(1, i)); open {
			opened++
		}
	}
	if opened != 2 {
		t.Fatalf("expected one /64 to hold 2 gRPC finality streams, it holds %d", opened)
	}

	// A client elsewhere still gets one.
	if open, code, _ := openFinalityStream(t, client, "203.0.113.6"); !open {
		t.Fatalf("a different address was refused: %v", code)
	}
}

func TestGRPCFinalitySlotIsFreedWhenTheClientCancels(t *testing.T) {
	srv, addr := newServedStreamServer(t, ServerConfig{WebSocketMaxPerIP: 1})
	client := newFinalityClient(t, addr)

	for i := 0; i < 3; i++ {
		open, code, cancel := openFinalityStream(t, client, "203.0.113.5")
		if !open {
			t.Fatalf("round %d: stream refused with %v", i, code)
		}
		waitOpenStreams(t, srv, 1)
		if again, _, _ := openFinalityStream(t, client, "203.0.113.5"); again {
			t.Fatalf("round %d: a second stream from the same address was accepted", i)
		}
		cancel()
		waitOpenStreams(t, srv, 0)
	}
}

func TestStreamCapsAreSharedByWebSocketAndGRPC(t *testing.T) {
	srv, addr := newServedStreamServer(t, ServerConfig{WebSocketMaxConnections: 3, WebSocketMaxPerIP: 100})
	client := newFinalityClient(t, addr)
	base := "ws://" + addr

	open, code, cancelFirst := openFinalityStream(t, client, "203.0.113.1")
	if !open {
		t.Fatalf("first gRPC stream refused: %v", code)
	}
	if open, code, _ := openFinalityStream(t, client, "203.0.113.2"); !open {
		t.Fatalf("second gRPC stream refused: %v", code)
	}
	if conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.3", ""); conn == nil {
		t.Fatalf("WebSocket stream refused with %d", status)
	}
	waitOpenStreams(t, srv, 3)

	// The total is held across the two transports.
	if conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.4", ""); conn != nil || status != http.StatusTooManyRequests {
		t.Fatalf("expected a WebSocket stream beyond the total to be refused with 429, got conn=%v status=%d", conn != nil, status)
	}
	if open, code, _ := openFinalityStream(t, client, "203.0.113.4"); open || code != codes.ResourceExhausted {
		t.Fatalf("expected a gRPC stream beyond the total to be refused with ResourceExhausted, got open=%v code=%v", open, code)
	}

	cancelFirst()
	waitOpenStreams(t, srv, 2)
	if conn, status := dialStream(t, base, "/ws/pos/finality", "203.0.113.4", ""); conn == nil {
		t.Fatalf("expected the slot freed by the gRPC stream to be usable, got %d", status)
	}
}
