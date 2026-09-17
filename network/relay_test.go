package network

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	networkv1 "nhbchain/proto/network/v1"
)

type failingGossipStream struct {
	networkv1.NetworkService_GossipServer

	ctx       context.Context
	cancel    context.CancelFunc
	sendError error
	once      sync.Once
}

func newFailingGossipStream(sendErr error) *failingGossipStream {
	ctx, cancel := context.WithCancel(context.Background())
	if sendErr == nil {
		sendErr = errors.New("send failure")
	}
	return &failingGossipStream{
		ctx:       ctx,
		cancel:    cancel,
		sendError: sendErr,
	}
}

func (f *failingGossipStream) Context() context.Context {
	return f.ctx
}

func (f *failingGossipStream) Send(*networkv1.GossipResponse) error {
	f.once.Do(func() {
		f.cancel()
	})
	return f.sendError
}

func (f *failingGossipStream) Recv() (*networkv1.GossipRequest, error) {
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}

func TestGossipStream_Shutdown_NoDeadlock(t *testing.T) {
	relay := NewRelay()
	stream := newFailingGossipStream(errors.New("send failed"))

	done := make(chan error, 1)
	go func() {
		done <- relay.GossipStream(stream)
	}()

	deadline := time.Now().Add(time.Second)
	attached := false
	for {
		relay.mu.RLock()
		active := relay.stream != nil
		relay.mu.RUnlock()
		if active || time.Now().After(deadline) {
			attached = active
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !attached {
		t.Fatal("relay stream was not attached before timeout")
	}

	envelope := &networkv1.GossipResponse{Envelope: &networkv1.NetworkEnvelope{}}
	if !relay.enqueue(envelope) {
		t.Fatal("expected enqueue to succeed")
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gossip stream did not shut down in time")
	}
}
