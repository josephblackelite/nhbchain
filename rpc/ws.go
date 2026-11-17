package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nhbchain/core"

	"google.golang.org/grpc/codes"
	"nhooyr.io/websocket"
)

const (
	wsWriteTimeout = 10 * time.Second

	// The WebSocket streams are public and read-only, so anybody may open one,
	// but only this many at once: each holds a connection, a goroutine and, for
	// the explorer stream, a per-subscriber snapshot marshal on every block. The
	// per-address cap keeps one client from using them all up.
	defaultWebSocketMaxConnections = 256
	defaultWebSocketMaxPerIP       = 8
)

func trimmedNonEmpty(values []string) []string {
	var out []string
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// acquireStream reserves one of the stream slots (shared by the WebSocket
// streams and the gRPC finality stream) for the client address and returns the
// function that gives it back. It fails when either the total or the
// per-address cap is reached.
func (s *Server) acquireStream(clientIP string) (func(), bool) {
	// The per-address count is kept under limiterAddressKey, as the query
	// limits are: whoever holds an IPv6 network can use any address in its /64,
	// so the whole /64 shares one count.
	key := limiterAddressKey(clientIP)
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsOpen >= s.wsMaxOpen || s.wsOpenByIP[key] >= s.wsMaxPerIP {
		return nil, false
	}
	if s.wsOpenByIP == nil {
		s.wsOpenByIP = make(map[string]int)
	}
	s.wsOpen++
	s.wsOpenByIP[key]++
	return func() {
		s.wsMu.Lock()
		defer s.wsMu.Unlock()
		s.wsOpen--
		if s.wsOpenByIP[key] <= 1 {
			delete(s.wsOpenByIP, key)
		} else {
			s.wsOpenByIP[key]--
		}
	}, true
}

// admitGRPCStream applies to the gRPC finality stream what the WebSocket
// handlers apply to theirs: the client address must resolve, be on the
// allowlist and fit under the stream caps. A refusal is answered as a gRPC
// "trailers only" response (HTTP 200 carrying a gRPC status), so that gRPC
// clients see a status instead of a malformed reply. On success the returned
// function gives the slot back and must be called when the stream ends.
func (s *Server) admitGRPCStream(w http.ResponseWriter, r *http.Request) (func(), bool) {
	refuse := func(code codes.Code, message string) (func(), bool) {
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", strconv.Itoa(int(code)))
		w.Header().Set("Grpc-Message", message)
		w.WriteHeader(http.StatusOK)
		return nil, false
	}
	clientIP, err := s.resolveClientIP(r)
	if err != nil {
		return refuse(codes.PermissionDenied, "invalid client address")
	}
	if !s.isClientAllowed(clientIP) {
		return refuse(codes.PermissionDenied, "client address not allowed")
	}
	release, ok := s.acquireStream(clientIP)
	if !ok {
		return refuse(codes.ResourceExhausted, "too many open streams")
	}
	return release, true
}

// streamAcceptOptions returns the origin policy of the WebSocket streams: the
// configured origins, or any origin when none are configured.
func (s *Server) streamAcceptOptions() *websocket.AcceptOptions {
	if len(s.wsOrigins) == 0 {
		return &websocket.AcceptOptions{OriginPatterns: []string{"*"}}
	}
	return &websocket.AcceptOptions{OriginPatterns: append([]string(nil), s.wsOrigins...)}
}

func (s *Server) handlePOSFinalityWS(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.node == nil {
		http.Error(w, "node unavailable", http.StatusServiceUnavailable)
		return
	}
	clientIP, err := s.resolveClientIP(r)
	if err != nil {
		http.Error(w, "invalid client address", http.StatusForbidden)
		return
	}
	if !s.isClientAllowed(clientIP) {
		http.Error(w, "client address not allowed", http.StatusForbidden)
		return
	}
	release, ok := s.acquireStream(clientIP)
	if !ok {
		http.Error(w, "too many open streams", http.StatusTooManyRequests)
		return
	}
	defer release()
	ctx := context.WithValue(r.Context(), clientIPContextKey, clientIP)
	r = r.WithContext(ctx)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	conn, err := websocket.Accept(w, r, s.streamAcceptOptions())
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "stream closed")
	// The stream only writes. Reading in the background notices a client that
	// went away, so that its slot is freed at once instead of at the next update.
	if err := s.streamPOSFinality(conn.CloseRead(r.Context()), conn, cursor); err != nil {
		if status := websocket.CloseStatus(err); status == -1 {
			_ = conn.Close(websocket.StatusInternalError, "stream error")
		}
	}
}

func (s *Server) streamPOSFinality(ctx context.Context, conn *websocket.Conn, cursor string) error {
	updates, cancel, backlog, err := s.node.POSFinalitySubscribe(ctx, cursor)
	if err != nil {
		return err
	}
	defer cancel()

	for _, update := range backlog {
		if err := writeFinalityUpdate(ctx, conn, update); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if err := writeFinalityUpdate(ctx, conn, update); err != nil {
				return err
			}
		}
	}
}

func writeFinalityUpdate(ctx context.Context, conn *websocket.Conn, update core.POSFinalityUpdate) error {
	payload := finalityUpdatePayloadFrom(update)
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
