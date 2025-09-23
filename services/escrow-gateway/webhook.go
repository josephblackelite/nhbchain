package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
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

// WebhookQueue stores webhook tasks prior to delivery.
type WebhookQueue struct {
	mu      sync.Mutex
	tasks   []WebhookTask
	history []WebhookEvent
}

func NewWebhookQueue() *WebhookQueue {
	return &WebhookQueue{}
}

// Enqueue adds an event to the queue.
func (q *WebhookQueue) Enqueue(evt WebhookEvent) {
	q.enqueueTask(WebhookTask{Event: evt})
}

func (q *WebhookQueue) enqueueTask(task WebhookTask) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if task.Subscription == nil {
		q.history = append(q.history, task.Event)
	}
	q.tasks = append(q.tasks, task)
}

// Events returns a snapshot copy of queued events. Primarily used in tests.
func (q *WebhookQueue) Events() []WebhookEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	snapshot := make([]WebhookEvent, len(q.history))
	copy(snapshot, q.history)
	return snapshot
}

// Dequeue waits for the next webhook task. Returns false if the context is cancelled.
func (q *WebhookQueue) Dequeue(ctx context.Context) (WebhookTask, bool) {
	for {
		q.mu.Lock()
		if len(q.tasks) > 0 {
			task := q.tasks[0]
			copy(q.tasks, q.tasks[1:])
			q.tasks = q.tasks[:len(q.tasks)-1]
			q.mu.Unlock()
			if !task.NotBefore.IsZero() {
				delay := time.Until(task.NotBefore)
				if delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return WebhookTask{}, false
					case <-timer.C:
					}
				}
			}
			return task, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return WebhookTask{}, false
		case <-time.After(25 * time.Millisecond):
		}
	}
}

const maxWebhookAttempts = 5

// WebhookWorker delivers queued events to external subscribers.
type WebhookWorker struct {
	store  *SQLiteStore
	queue  *WebhookQueue
	client *http.Client
	nowFn  func() time.Time

	rateMu sync.Mutex
	rate   map[int64]rateWindow
}

type rateWindow struct {
	windowStart time.Time
	count       int
}

func NewWebhookWorker(store *SQLiteStore, queue *WebhookQueue) *WebhookWorker {
	return &WebhookWorker{
		store:  store,
		queue:  queue,
		client: &http.Client{Timeout: 10 * time.Second},
		nowFn:  time.Now,
		rate:   make(map[int64]rateWindow),
	}
}

// Run processes webhook tasks until the context is cancelled.
func (w *WebhookWorker) Run(ctx context.Context) {
	for {
		task, ok := w.queue.Dequeue(ctx)
		if !ok {
			return
		}
		if task.Subscription == nil {
			w.expandTask(ctx, task)
			continue
		}
		w.handleDelivery(ctx, task)
	}
}

func (w *WebhookWorker) expandTask(ctx context.Context, task WebhookTask) {
	subs, err := w.store.ListWebhooksForEvent(ctx, task.Event.Type)
	if err != nil {
		return
	}
	for i := range subs {
		sub := subs[i]
		if !sub.Active {
			continue
		}
		clone := WebhookTask{
			Event:        task.Event,
			Subscription: &sub,
			Attempt:      0,
		}
		w.queue.enqueueTask(clone)
	}
}

func (w *WebhookWorker) handleDelivery(ctx context.Context, task WebhookTask) {
	sub := task.Subscription
	if sub == nil || !sub.Active {
		return
	}
	now := w.nowFn()
	if !w.allow(sub.ID, sub.RateLimit, now) {
		task.NotBefore = w.rateReset(sub.ID)
		w.queue.enqueueTask(task)
		return
	}
	body := map[string]interface{}{
		"type":       task.Event.Type,
		"sequence":   task.Event.Sequence,
		"escrowId":   task.Event.EscrowID,
		"tradeId":    task.Event.TradeID,
		"attributes": task.Event.Attributes,
		"timestamp":  task.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(body)
	if err != nil {
		w.recordAttempt(ctx, task, "error", err.Error(), now, time.Time{})
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytesClone(payload))
	if err != nil {
		w.recordAttempt(ctx, task, "error", err.Error(), now, time.Time{})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signPayload(sub.Secret, payload))

	resp, err := w.client.Do(req)
	if err != nil {
		w.retryLater(ctx, task, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.retryLater(ctx, task, resp.Status)
		return
	}
	w.recordAttempt(ctx, task, "success", "", now, time.Time{})
}

func (w *WebhookWorker) retryLater(ctx context.Context, task WebhookTask, errMsg string) {
	now := w.nowFn()
	attemptNum := task.Attempt + 1
	w.recordAttempt(ctx, task, "failed", errMsg, now, now.Add(w.backoffDuration(attemptNum)))
	if attemptNum >= maxWebhookAttempts {
		return
	}
	task.Attempt++
	task.NotBefore = now.Add(w.backoffDuration(attemptNum))
	w.queue.enqueueTask(task)
}

func (w *WebhookWorker) backoffDuration(attempt int) time.Duration {
	base := time.Second
	if attempt <= 0 {
		attempt = 1
	}
	d := base * time.Duration(1<<uint(attempt-1))
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}

func (w *WebhookWorker) recordAttempt(ctx context.Context, task WebhookTask, status, errMsg string, now time.Time, next time.Time) {
	attempt := WebhookAttempt{
		WebhookID:     task.Subscription.ID,
		EventSequence: task.Event.Sequence,
		Attempt:       task.Attempt + 1,
		Status:        status,
		Error:         errMsg,
		NextAttempt:   next,
		CreatedAt:     now,
	}
	_ = w.store.InsertWebhookAttempt(ctx, attempt)
}

func (w *WebhookWorker) allow(id int64, limit int, now time.Time) bool {
	if limit <= 0 {
		limit = 60
	}
	w.rateMu.Lock()
	defer w.rateMu.Unlock()
	state := w.rate[id]
	if now.Sub(state.windowStart) >= time.Minute {
		state.windowStart = now
		state.count = 0
	}
	if state.count >= limit {
		w.rate[id] = state
		return false
	}
	state.count++
	w.rate[id] = state
	return true
}

func (w *WebhookWorker) rateReset(id int64) time.Time {
	w.rateMu.Lock()
	defer w.rateMu.Unlock()
	state := w.rate[id]
	if state.windowStart.IsZero() {
		state.windowStart = w.nowFn()
	}
	reset := state.windowStart.Add(time.Minute)
	w.rate[id] = state
	return reset
}

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func bytesClone(b []byte) *bytes.Reader {
	clone := append([]byte(nil), b...)
	return bytes.NewReader(clone)
}
