package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"nhbchain/services/webhook"
)

const maxWebhookAttempts = 5

// WebhookWorker delivers queued events to external subscribers.
type WebhookWorker struct {
	store  *SQLiteStore
	queue  *WebhookQueue
	client *http.Client
	nowFn  func() time.Time

	limiter *webhook.RateLimiter
}

func NewWebhookWorker(store *SQLiteStore, queue *WebhookQueue) *WebhookWorker {
	return &WebhookWorker{
		store:   store,
		queue:   queue,
		client:  &http.Client{Timeout: 10 * time.Second},
		nowFn:   time.Now,
		limiter: webhook.NewRateLimiter(),
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
	if !w.limiter.Allow(sub.ID, sub.RateLimit, now) {
		task.NotBefore = w.limiter.ResetAt(sub.ID, now)
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

func signPayload(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func bytesClone(b []byte) *bytes.Reader {
	clone := append([]byte(nil), b...)
	return bytes.NewReader(clone)
}
