package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"

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

// acquireStream reserves one of the WebSocket stream slots for the client
// address and returns the function that gives it back. It fails when either the
// total or the per-address cap is reached.
func (s *Server) acquireStream(clientIP string) (func(), bool) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsOpen >= s.wsMaxOpen || s.wsOpenByIP[clientIP] >= s.wsMaxPerIP {
		return nil, false
	}
	if s.wsOpenByIP == nil {
		s.wsOpenByIP = make(map[string]int)
	}
	s.wsOpen++
	s.wsOpenByIP[clientIP]++
	return func() {
		s.wsMu.Lock()
		defer s.wsMu.Unlock()
		s.wsOpen--
		if s.wsOpenByIP[clientIP] <= 1 {
			delete(s.wsOpenByIP, clientIP)
		} else {
			s.wsOpenByIP[clientIP]--
		}
	}, true
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
