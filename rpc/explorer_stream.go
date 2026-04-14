package rpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

const explorerSnapshotPollInterval = 2 * time.Second

type explorerSnapshotEvent struct {
	Type      string                  `json:"type"`
	Height    uint64                  `json:"height"`
	UpdatedAt string                  `json:"updatedAt,omitempty"`
	Snapshot  *ExplorerSnapshotResult `json:"snapshot,omitempty"`
}

type ExplorerStream struct {
	mu          sync.RWMutex
	subscribers map[chan explorerSnapshotEvent]struct{}
}

func NewExplorerStream() *ExplorerStream {
	return &ExplorerStream{
		subscribers: make(map[chan explorerSnapshotEvent]struct{}),
	}
}

func (s *ExplorerStream) Subscribe() (<-chan explorerSnapshotEvent, func()) {
	ch := make(chan explorerSnapshotEvent, 4)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
	return ch, cancel
}

func (s *ExplorerStream) Publish(event explorerSnapshotEvent) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Server) startExplorerSnapshotLoop() {
	if s == nil || s.node == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(explorerSnapshotPollInterval)
		defer ticker.Stop()

		var lastHeight uint64
		for {
			currentHeight := s.node.Chain().GetHeight()
			s.explorerMu.RLock()
			cachedSnapshot := s.explorerSnapshot
			cachedHeight := s.explorerHeight
			window := s.explorerWindow
			s.explorerMu.RUnlock()

			needsRefresh := cachedSnapshot == nil || currentHeight != cachedHeight || currentHeight != lastHeight
			if needsRefresh {
				snapshot, err := s.buildExplorerSnapshot(window)
				if err != nil {
					slog.Warn("rpc: refresh explorer snapshot failed", slog.Any("error", err))
				} else {
					s.explorerMu.Lock()
					s.explorerSnapshot = snapshot
					s.explorerHeight = currentHeight
					s.explorerMu.Unlock()
					if s.explorerRealtime != nil && snapshot != nil {
						s.explorerRealtime.Publish(explorerSnapshotEvent{
							Type:      "explorer_snapshot",
							Height:    snapshot.LatestHeight,
							UpdatedAt: snapshot.UpdatedAt,
							Snapshot:  snapshot,
						})
					}
				}
			}

			lastHeight = currentHeight
			<-ticker.C
		}
	}()
}

func (s *Server) cachedExplorerSnapshot(recentBlocks int) *ExplorerSnapshotResult {
	if s == nil {
		return nil
	}
	s.explorerMu.RLock()
	defer s.explorerMu.RUnlock()
	if recentBlocks == s.explorerWindow && s.explorerSnapshot != nil {
		return s.explorerSnapshot
	}
	return nil
}

func (s *Server) handleExplorerWS(w http.ResponseWriter, r *http.Request) {
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
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "stream closed")

	if snapshot := s.cachedExplorerSnapshot(explorerDefaultRecentBlocks); snapshot != nil {
		if err := writeExplorerSnapshotEvent(r.Context(), conn, explorerSnapshotEvent{
			Type:      "explorer_snapshot",
			Height:    snapshot.LatestHeight,
			UpdatedAt: snapshot.UpdatedAt,
			Snapshot:  snapshot,
		}); err != nil {
			return
		}
	}

	if s.explorerRealtime == nil {
		<-r.Context().Done()
		return
	}
	updates, cancel := s.explorerRealtime.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-updates:
			if !ok {
				return
			}
			if err := writeExplorerSnapshotEvent(r.Context(), conn, event); err != nil {
				if status := websocket.CloseStatus(err); status == -1 {
					_ = conn.Close(websocket.StatusInternalError, "stream error")
				}
				return
			}
		}
	}
}

func writeExplorerSnapshotEvent(ctx context.Context, conn *websocket.Conn, event explorerSnapshotEvent) error {
	if event.Snapshot == nil && strings.TrimSpace(event.Type) == "" {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}
