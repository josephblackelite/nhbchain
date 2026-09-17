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
)

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
	ctx := context.WithValue(r.Context(), clientIPContextKey, clientIP)
	r = r.WithContext(ctx)
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "stream closed")
	if err := s.streamPOSFinality(r.Context(), conn, cursor); err != nil {
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
