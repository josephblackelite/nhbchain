package main

import (
	"context"
	"sync"
	"time"

	"nhbchain/network"
	"nhbchain/p2p"
)

const (
	outboundQueueCapacity  = 4096
	outboundRetryBaseDelay = 100 * time.Millisecond
	outboundRetryMaxDelay  = 5 * time.Second
	notifyBuffer           = 1
	idleTickInterval       = time.Second
)

type resilientBroadcaster struct {
	mu      sync.Mutex
	queue   []*p2p.Message
	updates chan *network.Client
	notify  chan struct{}
}

func newResilientBroadcaster(ctx context.Context) *resilientBroadcaster {
	rb := &resilientBroadcaster{
		queue:   make([]*p2p.Message, 0, outboundQueueCapacity),
		updates: make(chan *network.Client, notifyBuffer),
		notify:  make(chan struct{}, notifyBuffer),
	}
	go rb.run(ctx)
	return rb
}

func (r *resilientBroadcaster) Broadcast(msg *p2p.Message) error {
	if msg == nil {
		return nil
	}

	copyMsg := &p2p.Message{Type: msg.Type, Payload: append([]byte(nil), msg.Payload...)}

	r.mu.Lock()
	if len(r.queue) >= outboundQueueCapacity {
		r.queue = r.queue[1:]
	}
	r.queue = append(r.queue, copyMsg)
	r.mu.Unlock()

	r.signal()
	return nil
}

func (r *resilientBroadcaster) SetClient(client *network.Client) {
	if r == nil {
		return
	}

	select {
	case r.updates <- client:
	default:
		select {
		case <-r.updates:
		default:
		}
		r.updates <- client
	}
	r.signal()
}

func (r *resilientBroadcaster) run(ctx context.Context) {
	if r == nil {
		return
	}

	var (
		client     *network.Client
		retryDelay = outboundRetryBaseDelay
	)

	for {
		if ctx.Err() != nil {
			return
		}

		r.mu.Lock()
		var next *p2p.Message
		if len(r.queue) > 0 {
			next = r.queue[0]
		}
		r.mu.Unlock()

		if client != nil && next != nil {
			if err := client.Broadcast(next); err != nil {
				retryDelay = nextRetryDelay(retryDelay)
				select {
				case <-ctx.Done():
					return
				case newClient := <-r.updates:
					client = newClient
					retryDelay = outboundRetryBaseDelay
				case <-time.After(retryDelay):
				case <-r.notify:
				}
				continue
			}

			r.mu.Lock()
			if len(r.queue) > 0 {
				r.queue = r.queue[1:]
			}
			r.mu.Unlock()
			retryDelay = outboundRetryBaseDelay
			continue
		}

		select {
		case <-ctx.Done():
			return
		case client = <-r.updates:
			retryDelay = outboundRetryBaseDelay
		case <-r.notify:
			// Wake loop to inspect queue or client updates.
		case <-time.After(idleTickInterval):
			// Periodic wake-up to avoid starving updates when idle.
		}
	}
}

func (r *resilientBroadcaster) signal() {
	if r == nil {
		return
	}
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func nextRetryDelay(current time.Duration) time.Duration {
	next := current * 2
	if next < outboundRetryBaseDelay {
		next = outboundRetryBaseDelay
	}
	if next > outboundRetryMaxDelay {
		return outboundRetryMaxDelay
	}
	return next
}
