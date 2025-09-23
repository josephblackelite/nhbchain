package main

import (
	"sync"
	"time"
)

// WebhookEvent represents a queued webhook notification.
type WebhookEvent struct {
	Type      string
	EscrowID  string
	CreatedAt time.Time
}

// WebhookQueue is a simple in-memory queue that stores events for later processing.
type WebhookQueue struct {
	mu     sync.Mutex
	events []WebhookEvent
}

func NewWebhookQueue() *WebhookQueue {
	return &WebhookQueue{}
}

// Enqueue adds a webhook event to the in-memory buffer.
func (q *WebhookQueue) Enqueue(evt WebhookEvent) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.events = append(q.events, evt)
}

// Events returns a snapshot copy of queued events. Primarily used in tests.
func (q *WebhookQueue) Events() []WebhookEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	snapshot := make([]WebhookEvent, len(q.events))
	copy(snapshot, q.events)
	return snapshot
}
