package rpc

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkServerRememberTx(b *testing.B) {
	server := NewServer(nil, nil, ServerConfig{})
	start := time.Now()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := fmt.Sprintf("tx-%d", i)
		// Advance the logical clock enough to keep the sliding window bounded.
		now := start.Add(time.Duration(i) * (txSeenTTL + time.Second))
		if !server.rememberTx(hash, now) {
			b.Fatalf("unexpected duplicate rejection for %s", hash)
		}
	}
}

func BenchmarkServerRememberTxParallel(b *testing.B) {
	server := NewServer(nil, nil, ServerConfig{})
	var counter uint64
	start := time.Now()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := atomic.AddUint64(&counter, 1)
			hash := fmt.Sprintf("tx-%d", id)
			now := start.Add(time.Duration(id) * (txSeenTTL + time.Second))
			if !server.rememberTx(hash, now) {
				b.Fatalf("unexpected duplicate rejection for %s", hash)
			}
		}
	})
}
