package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestWebhookQueueDropOldest(t *testing.T) {
	clock := newFakeClock(time.Unix(1700000000, 0).UTC())
	queue := NewWebhookQueue(
		WithWebhookTaskCapacity(3),
		WithWebhookHistoryCapacity(2),
		WithWebhookTTL(time.Minute),
		withWebhookClock(clock.Now),
	)

	for i := 0; i < 5; i++ {
		queue.Enqueue(WebhookEvent{Sequence: int64(i), CreatedAt: clock.Now()})
	}

	events := queue.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events in history, got %d", len(events))
	}
	if events[0].Sequence != 3 || events[1].Sequence != 4 {
		t.Fatalf("unexpected history sequences: %+v", events)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var sequences []int64
	for len(sequences) < 3 {
		task, ok := queue.Dequeue(ctx)
		if !ok {
			t.Fatalf("expected task, queue closed early after %d items", len(sequences))
		}
		sequences = append(sequences, task.Event.Sequence)
	}

	expected := []int64{2, 3, 4}
	for i, seq := range expected {
		if sequences[i] != seq {
			t.Fatalf("expected sequence %d at position %d, got %d", seq, i, sequences[i])
		}
	}
}

func TestWebhookQueueEvictsExpired(t *testing.T) {
	clock := newFakeClock(time.Unix(1700000000, 0).UTC())
	queue := NewWebhookQueue(
		WithWebhookTaskCapacity(2),
		WithWebhookHistoryCapacity(2),
		WithWebhookTTL(10*time.Second),
		withWebhookClock(clock.Now),
	)

	queue.Enqueue(WebhookEvent{Sequence: 42, CreatedAt: clock.Now()})
	clock.Advance(11 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if task, ok := queue.Dequeue(ctx); ok {
		t.Fatalf("expected expired event to be dropped, dequeued sequence %d", task.Event.Sequence)
	}

	if remaining := queue.Events(); len(remaining) != 0 {
		t.Fatalf("expected no history events after TTL eviction, got %d", len(remaining))
	}
}
