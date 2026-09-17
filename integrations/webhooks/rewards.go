package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventType represents the logical webhook topic.
type EventType string

const (
	// EventRewardsReady is emitted when a new reward epoch ledger is available.
	EventRewardsReady EventType = "potso.rewards.ready"
	// EventRewardsPaid is emitted when an epoch's payouts have been settled.
	EventRewardsPaid EventType = "potso.rewards.paid"

	defaultMaxAttempts = 5
	defaultMinBackoff  = 2 * time.Second
	defaultMaxBackoff  = 30 * time.Second
)

// RewardsReadyPayload describes the webhook body for ready events.
type RewardsReadyPayload struct {
	Type        EventType `json:"type"`
	Epoch       uint64    `json:"epoch"`
	Count       int       `json:"count"`
	ExportURLs  []string  `json:"exportUrls"`
	Checksum    string    `json:"checksum"`
	GeneratedAt time.Time `json:"generatedAt"`
	DeliveryID  string    `json:"deliveryId"`
}

// RewardsPaidPayload describes the webhook body for paid events.
type RewardsPaidPayload struct {
	Type       EventType `json:"type"`
	Epoch      uint64    `json:"epoch"`
	Count      int       `json:"count"`
	TxRef      string    `json:"txRef"`
	PaidAt     time.Time `json:"paidAt"`
	DeliveryID string    `json:"deliveryId"`
}

// Dispatcher orchestrates webhook deliveries with retry and exponential backoff.
type Dispatcher struct {
	endpoint    string
	secret      []byte
	client      *http.Client
	maxAttempts int
	minBackoff  time.Duration
	maxBackoff  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan delivery
	wg     sync.WaitGroup
}

type delivery struct {
	eventType EventType
	body      []byte
}

// Option mutates dispatcher configuration.
type Option func(*Dispatcher)

// WithHTTPClient overrides the HTTP client used for deliveries.
func WithHTTPClient(client *http.Client) Option {
	return func(d *Dispatcher) {
		if client != nil {
			d.client = client
		}
	}
}

// WithRetryPolicy overrides the retry configuration.
func WithRetryPolicy(maxAttempts int, minBackoff, maxBackoff time.Duration) Option {
	return func(d *Dispatcher) {
		if maxAttempts > 0 {
			d.maxAttempts = maxAttempts
		}
		if minBackoff > 0 {
			d.minBackoff = minBackoff
		}
		if maxBackoff >= minBackoff && maxBackoff > 0 {
			d.maxBackoff = maxBackoff
		}
	}
}

// NewDispatcher constructs a dispatcher and spawns the worker goroutine.
func NewDispatcher(endpoint string, secret []byte, opts ...Option) (*Dispatcher, error) {
	endpoint = string(bytes.TrimSpace([]byte(endpoint)))
	if endpoint == "" {
		return nil, errors.New("webhook: endpoint required")
	}
	if len(secret) == 0 {
		return nil, errors.New("webhook: secret required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &Dispatcher{
		endpoint:    endpoint,
		secret:      append([]byte(nil), secret...),
		client:      &http.Client{Timeout: 15 * time.Second},
		maxAttempts: defaultMaxAttempts,
		minBackoff:  defaultMinBackoff,
		maxBackoff:  defaultMaxBackoff,
		ctx:         ctx,
		cancel:      cancel,
		queue:       make(chan delivery, 32),
	}
	for _, opt := range opts {
		opt(dispatcher)
	}
	dispatcher.wg.Add(1)
	go dispatcher.worker()
	return dispatcher, nil
}

// Close stops the dispatcher and waits for inflight deliveries to complete.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.cancel()
	d.wg.Wait()
}

// EnqueueReady sends a ready event asynchronously.
func (d *Dispatcher) EnqueueReady(payload RewardsReadyPayload) error {
	payload.Type = EventRewardsReady
	if payload.GeneratedAt.IsZero() {
		payload.GeneratedAt = time.Now().UTC()
	}
	if payload.DeliveryID == "" {
		payload.DeliveryID = fmt.Sprintf("ready-%d-%d", payload.Epoch, time.Now().UnixNano())
	}
	return d.enqueue(payload.Type, payload)
}

// EnqueuePaid sends a paid event asynchronously.
func (d *Dispatcher) EnqueuePaid(payload RewardsPaidPayload) error {
	payload.Type = EventRewardsPaid
	if payload.PaidAt.IsZero() {
		payload.PaidAt = time.Now().UTC()
	}
	if payload.DeliveryID == "" {
		payload.DeliveryID = fmt.Sprintf("paid-%d-%d", payload.Epoch, time.Now().UnixNano())
	}
	return d.enqueue(payload.Type, payload)
}

func (d *Dispatcher) enqueue(eventType EventType, body interface{}) error {
	if d == nil {
		return errors.New("webhook: dispatcher not initialised")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	select {
	case d.queue <- delivery{eventType: eventType, body: data}:
		return nil
	case <-d.ctx.Done():
		return errors.New("webhook: dispatcher closed")
	}
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case job := <-d.queue:
			d.process(job)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *Dispatcher) process(job delivery) {
	attempt := 0
	backoff := d.minBackoff
	for {
		attempt++
		ctx, cancel := context.WithTimeout(d.ctx, d.client.Timeout)
		err := d.send(ctx, job)
		cancel()
		if err == nil {
			return
		}
		if attempt >= d.maxAttempts {
			return
		}
		select {
		case <-time.After(backoff):
		case <-d.ctx.Done():
			return
		}
		backoff = nextBackoff(backoff, d.maxBackoff)
	}
}

func (d *Dispatcher) send(ctx context.Context, job delivery) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, bytes.NewReader(job.body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NHB-Event", string(job.eventType))
	req.Header.Set("X-NHB-Signature", d.sign(job.body))
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("webhook: delivery failed with status %d", resp.StatusCode)
}

func (d *Dispatcher) sign(body []byte) string {
	mac := hmac.New(sha256.New, d.secret)
	_, _ = mac.Write(body)
	sum := mac.Sum(nil)
	return "sha256=" + hex.EncodeToString(sum)
}

func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	if next < current {
		return max
	}
	return next
}
