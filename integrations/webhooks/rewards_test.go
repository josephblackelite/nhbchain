package webhooks

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcherSignsPayload(t *testing.T) {
	var receivedSignature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if string(body) == "" {
			t.Fatalf("expected body")
		}
		receivedSignature = r.Header.Get("X-NHB-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dispatcher, err := NewDispatcher(server.URL, []byte("secret"))
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.EnqueueReady(RewardsReadyPayload{Epoch: 1, Count: 1}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitFor(func() bool { return receivedSignature != "" }, time.Second)
	if receivedSignature == "" {
		t.Fatalf("expected signature header")
	}
	if receivedSignature[:7] != "sha256=" {
		t.Fatalf("unexpected signature prefix %s", receivedSignature)
	}
}

func TestDispatcherRetries(t *testing.T) {
	attempts := int32(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	dispatcher, err := NewDispatcher(server.URL, []byte("secret"), WithRetryPolicy(5, time.Millisecond*10, time.Millisecond*20))
	if err != nil {
		t.Fatalf("dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.EnqueuePaid(RewardsPaidPayload{Epoch: 2, Count: 1, TxRef: "tx"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitFor(func() bool { return atomic.LoadInt32(&attempts) >= 3 }, time.Second)
	if atomic.LoadInt32(&attempts) < 3 {
		t.Fatalf("expected retries, got %d", attempts)
	}
}

func waitFor(cond func() bool, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
}
