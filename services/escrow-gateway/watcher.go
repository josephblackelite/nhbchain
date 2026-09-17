package main

import (
	"context"
	"strings"
	"time"
)

// EventWatcher periodically pulls events from the node and persists them locally
// while enqueueing webhook notifications.
type EventWatcher struct {
	node         NodeClient
	store        *SQLiteStore
	queue        *WebhookQueue
	pollInterval time.Duration
	batchSize    int
	nowFn        func() time.Time
}

// NewEventWatcher constructs a watcher with sane defaults.
func NewEventWatcher(node NodeClient, store *SQLiteStore, queue *WebhookQueue) *EventWatcher {
	if queue == nil {
		queue = NewWebhookQueue()
	}
	return &EventWatcher{
		node:         node,
		store:        store,
		queue:        queue,
		pollInterval: 5 * time.Second,
		batchSize:    100,
		nowFn:        time.Now,
	}
}

// Run starts the polling loop until the context is cancelled.
func (w *EventWatcher) Run(ctx context.Context) {
	if w.node == nil || w.store == nil || w.queue == nil {
		return
	}
	interval := w.pollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	after, _ := w.store.LastEventSequence(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			after = w.poll(ctx, after)
		}
	}
}

func (w *EventWatcher) poll(ctx context.Context, after int64) int64 {
	batch := w.batchSize
	if batch <= 0 {
		batch = 100
	}
	events, err := w.node.FetchEvents(ctx, after, batch)
	if err != nil {
		return after
	}
	if len(events) == 0 {
		return after
	}
	lastSeq := after
	for _, evt := range events {
		if evt.Sequence <= lastSeq {
			continue
		}
		w.handleEvent(ctx, evt)
		lastSeq = evt.Sequence
	}
	_ = w.store.UpdateEventSequence(ctx, lastSeq)
	return lastSeq
}

func (w *EventWatcher) handleEvent(ctx context.Context, evt NodeEvent) {
	createdAt := time.Unix(evt.Timestamp, 0)
	if evt.Timestamp == 0 {
		createdAt = w.nowFn().UTC()
	}
	payload := make(map[string]string, len(evt.Attributes))
	for k, v := range evt.Attributes {
		payload[k] = v
	}
	stored := StoredEvent{
		Sequence:  evt.Sequence,
		Type:      evt.Type,
		Height:    evt.Height,
		TxHash:    evt.TxHash,
		Payload:   payload,
		CreatedAt: createdAt,
	}
	_ = w.store.InsertEvent(ctx, stored)

	webhook := WebhookEvent{
		Sequence:   evt.Sequence,
		Type:       evt.Type,
		Attributes: payload,
		CreatedAt:  createdAt,
	}
	if id := strings.TrimSpace(payload["id"]); id != "" {
		webhook.EscrowID = normalizeHex(id)
	}
	if trade := strings.TrimSpace(payload["tradeId"]); trade != "" {
		webhook.TradeID = normalizeHex(trade)
		if status := tradeStatusFromEvent(evt.Type); status != "" {
			_ = w.store.UpdateTradeStatus(ctx, webhook.TradeID, status, createdAt)
		}
	} else if webhook.EscrowID != "" {
		if tradeID, err := w.store.TradeIDByEscrow(ctx, webhook.EscrowID); err == nil {
			webhook.TradeID = tradeID
		}
	}
	w.queue.Enqueue(webhook)
}

func tradeStatusFromEvent(eventType string) string {
	switch strings.ToLower(eventType) {
	case "escrow.trade.created":
		return "created"
	case "escrow.trade.partial_funded":
		return "partial_funded"
	case "escrow.trade.funded":
		return "funded"
	case "escrow.trade.disputed":
		return "disputed"
	case "escrow.trade.resolved":
		return "resolved"
	case "escrow.trade.settled":
		return "settled"
	case "escrow.trade.expired":
		return "expired"
	default:
		return ""
	}
}

func normalizeHex(hexStr string) string {
	cleaned := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(hexStr), "0x"), "0X")
	if cleaned == "" {
		return ""
	}
	return "0x" + strings.ToLower(cleaned)
}
