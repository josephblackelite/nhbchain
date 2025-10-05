package main

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// WebhookEvent represents a queued webhook notification.
type WebhookEvent struct {
	Sequence   int64
	Type       string
	EscrowID   string
	TradeID    string
	Attributes map[string]string
	CreatedAt  time.Time
}

type WebhookTask struct {
	Event        WebhookEvent
	Subscription *WebhookSubscription
	Attempt      int
	NotBefore    time.Time
}

type queuedTask struct {
	task       WebhookTask
	enqueuedAt time.Time
}

type historyEntry struct {
	event      WebhookEvent
	enqueuedAt time.Time
}

// WebhookQueueOption adjusts the behaviour of the queue.
type WebhookQueueOption func(*webhookQueueConfig)

type webhookQueueConfig struct {
	taskCapacity    int
	historyCapacity int
	ttl             time.Duration
	now             func() time.Time
}

const (
	defaultTaskCapacity    = 1024
	defaultHistoryCapacity = 256
	defaultQueueTTL        = 15 * time.Minute
)

// WithWebhookTaskCapacity sets the maximum number of pending webhook tasks.
func WithWebhookTaskCapacity(capacity int) WebhookQueueOption {
	return func(cfg *webhookQueueConfig) {
		if capacity > 0 {
			cfg.taskCapacity = capacity
		}
	}
}

// WithWebhookHistoryCapacity sets the number of events retained for inspection.
func WithWebhookHistoryCapacity(capacity int) WebhookQueueOption {
	return func(cfg *webhookQueueConfig) {
		if capacity > 0 {
			cfg.historyCapacity = capacity
		}
	}
}

// WithWebhookTTL configures how long queued items remain eligible for delivery.
func WithWebhookTTL(ttl time.Duration) WebhookQueueOption {
	return func(cfg *webhookQueueConfig) {
		if ttl > 0 {
			cfg.ttl = ttl
		}
	}
}

// withWebhookClock overrides the clock used for TTL evaluation (test only).
func withWebhookClock(now func() time.Time) WebhookQueueOption {
	return func(cfg *webhookQueueConfig) {
		if now != nil {
			cfg.now = now
		}
	}
}

// WebhookQueue stores webhook tasks prior to delivery.
type WebhookQueue struct {
	mu      sync.Mutex
	tasks   queueRing[queuedTask]
	history queueRing[historyEntry]
	ttl     time.Duration
	now     func() time.Time
	metrics *webhookQueueMetrics
}

// NewWebhookQueue constructs a bounded queue with optional customisation.
func NewWebhookQueue(opts ...WebhookQueueOption) *WebhookQueue {
	cfg := webhookQueueConfig{
		taskCapacity:    defaultTaskCapacity,
		historyCapacity: defaultHistoryCapacity,
		ttl:             defaultQueueTTL,
		now:             time.Now,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	q := &WebhookQueue{
		tasks:   newQueueRing[queuedTask](cfg.taskCapacity),
		history: newQueueRing[historyEntry](cfg.historyCapacity),
		ttl:     cfg.ttl,
		now:     cfg.now,
		metrics: queueMetrics(),
	}
	return q
}

// Enqueue adds an event to the queue.
func (q *WebhookQueue) Enqueue(evt WebhookEvent) {
	q.enqueueTask(WebhookTask{Event: evt})
}

func (q *WebhookQueue) enqueueTask(task WebhookTask) {
	now := q.now()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.evictExpiredLocked(now)
	if task.Subscription == nil {
		q.recordHistoryLocked(historyEntry{event: task.Event, enqueuedAt: now})
	}
	q.recordTaskLocked(queuedTask{task: task, enqueuedAt: now})
}

// Events returns a snapshot copy of queued events. Primarily used in tests.
func (q *WebhookQueue) Events() []WebhookEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.evictExpiredLocked(q.now())
	snapshot := make([]WebhookEvent, 0, q.history.len())
	q.history.forEach(func(entry historyEntry) {
		snapshot = append(snapshot, entry.event)
	})
	return snapshot
}

// Dequeue waits for the next webhook task. Returns false if the context is cancelled.
func (q *WebhookQueue) Dequeue(ctx context.Context) (WebhookTask, bool) {
	for {
		q.mu.Lock()
		q.evictExpiredLocked(q.now())
		queued, ok := q.tasks.pop()
		q.mu.Unlock()
		if !ok {
			select {
			case <-ctx.Done():
				return WebhookTask{}, false
			case <-time.After(25 * time.Millisecond):
				continue
			}
		}

		if delay := time.Until(queued.task.NotBefore); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return WebhookTask{}, false
			case <-timer.C:
			}
		}

		if q.ttl > 0 {
			if age := q.now().Sub(queued.enqueuedAt); age > q.ttl {
				q.metrics.recordDropped("ttl", 1)
				continue
			}
		}

		return queued.task, true
	}
}

func (q *WebhookQueue) recordTaskLocked(task queuedTask) {
	if q.tasks.capacity() == 0 {
		q.metrics.recordDropped("overflow", 1)
		return
	}
	if _, ok := q.tasks.push(task); ok {
		q.metrics.recordDropped("overflow", 1)
	}
}

func (q *WebhookQueue) recordHistoryLocked(entry historyEntry) {
	if q.history.capacity() == 0 {
		q.metrics.recordDropped("history_overflow", 1)
		return
	}
	if _, ok := q.history.push(entry); ok {
		q.metrics.recordDropped("history_overflow", 1)
	}
}

func (q *WebhookQueue) evictExpiredLocked(now time.Time) {
	if q.ttl <= 0 {
		return
	}
	expired := 0
	for {
		queued, ok := q.tasks.peek()
		if !ok {
			break
		}
		if now.Sub(queued.enqueuedAt) <= q.ttl {
			break
		}
		q.tasks.pop()
		expired++
	}
	if expired > 0 {
		q.metrics.recordDropped("ttl", expired)
	}

	historyExpired := 0
	for {
		entry, ok := q.history.peek()
		if !ok {
			break
		}
		if now.Sub(entry.enqueuedAt) <= q.ttl {
			break
		}
		q.history.pop()
		historyExpired++
	}
	if historyExpired > 0 {
		q.metrics.recordDropped("history_ttl", historyExpired)
	}
}

// queueRing is a fixed-size ring buffer that overwrites the oldest element on overflow.
type queueRing[T any] struct {
	buf  []T
	head int
	size int
}

func newQueueRing[T any](capacity int) queueRing[T] {
	if capacity <= 0 {
		return queueRing[T]{}
	}
	return queueRing[T]{
		buf: make([]T, capacity),
	}
}

func (r *queueRing[T]) push(v T) (T, bool) {
	if len(r.buf) == 0 {
		var zero T
		return zero, true
	}
	if r.size == len(r.buf) {
		dropped := r.buf[r.head]
		r.buf[r.head] = v
		r.head = (r.head + 1) % len(r.buf)
		return dropped, true
	}
	idx := (r.head + r.size) % len(r.buf)
	r.buf[idx] = v
	r.size++
	var zero T
	return zero, false
}

func (r *queueRing[T]) pop() (T, bool) {
	if r.size == 0 || len(r.buf) == 0 {
		var zero T
		return zero, false
	}
	v := r.buf[r.head]
	var zero T
	r.buf[r.head] = zero
	r.head = (r.head + 1) % len(r.buf)
	r.size--
	return v, true
}

func (r *queueRing[T]) peek() (T, bool) {
	if r.size == 0 || len(r.buf) == 0 {
		var zero T
		return zero, false
	}
	return r.buf[r.head], true
}

func (r *queueRing[T]) len() int {
	return r.size
}

func (r *queueRing[T]) capacity() int {
	return len(r.buf)
}

func (r *queueRing[T]) forEach(fn func(T)) {
	if r.size == 0 || len(r.buf) == 0 {
		return
	}
	for i := 0; i < r.size; i++ {
		idx := (r.head + i) % len(r.buf)
		fn(r.buf[idx])
	}
}

var (
	metricsOnce        sync.Once
	sharedQueueMetrics *webhookQueueMetrics
)

type webhookQueueMetrics struct {
	dropped metric.Int64Counter
}

func queueMetrics() *webhookQueueMetrics {
	metricsOnce.Do(func() {
		meter := otel.GetMeterProvider().Meter("nhbchain/escrow-gateway")
		counter, err := meter.Int64Counter("nhb.escrow.webhooks.dropped")
		if err != nil {
			fallback := noop.NewMeterProvider().Meter("nhbchain/escrow-gateway")
			counter, _ = fallback.Int64Counter("nhb.escrow.webhooks.dropped")
		}
		sharedQueueMetrics = &webhookQueueMetrics{dropped: counter}
	})
	return sharedQueueMetrics
}

func (m *webhookQueueMetrics) recordDropped(reason string, count int) {
	if m == nil || m.dropped == nil || count <= 0 {
		return
	}
	m.dropped.Add(context.Background(), int64(count), metric.WithAttributes(attribute.String("reason", reason)))
}
