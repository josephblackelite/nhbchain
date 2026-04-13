package p2p

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type nonceGuard struct {
	ttl        time.Duration
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List
	maxEntries int
	now        func() time.Time

	janitorStop chan struct{}
	stopOnce    sync.Once
	janitorWG   sync.WaitGroup

	metrics *nonceGuardMetrics
}

type nonceRecord struct {
	key    string
	seen   time.Time
	expiry time.Time
}

func newNonceGuard(window time.Duration) *nonceGuard {
	ttl := window
	if ttl <= 0 {
		ttl = defaultNonceGuardTTL
	}
	guard := &nonceGuard{
		ttl:         ttl,
		entries:     make(map[string]*list.Element),
		order:       list.New(),
		maxEntries:  defaultNonceGuardMaxEntries,
		now:         time.Now,
		janitorStop: make(chan struct{}),
		metrics:     getNonceGuardMetrics(),
	}
	guard.metrics.observeSize(0)
	guard.janitorWG.Add(1)
	go guard.runJanitor()
	runtime.SetFinalizer(guard, func(g *nonceGuard) {
		g.stopJanitor()
	})
	return guard
}

func (g *nonceGuard) Remember(nodeID, nonce string, observedAt time.Time) bool {
	if nonce == "" {
		return false
	}
	if observedAt.IsZero() {
		observedAt = g.now()
	}

	fingerprint := g.fingerprint(nodeID, nonce)
	if fingerprint == "" {
		return false
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if elem := g.entries[fingerprint]; elem != nil {
		if record, _ := elem.Value.(*nonceRecord); record != nil {
			record.seen = observedAt
		}
		return false
	}

	record := &nonceRecord{
		key:    fingerprint,
		seen:   observedAt,
		expiry: observedAt.Add(g.ttl),
	}
	elem := g.order.PushFront(record)
	g.entries[fingerprint] = elem
	g.metrics.observeSize(len(g.entries))
	g.evictOverflowLocked()
	return true
}

func (g *nonceGuard) evictOverflowLocked() {
	if g.maxEntries <= 0 {
		return
	}
	for len(g.entries) > g.maxEntries {
		elem := g.order.Back()
		if elem == nil {
			break
		}
		g.removeElementLocked(elem, true)
	}
}

func (g *nonceGuard) removeExpiredLocked(now time.Time) {
	for {
		elem := g.order.Back()
		if elem == nil {
			break
		}
		record, _ := elem.Value.(*nonceRecord)
		if record == nil {
			g.removeElementLocked(elem, false)
			continue
		}
		if now.Before(record.expiry) {
			break
		}
		g.removeElementLocked(elem, true)
	}
}

func (g *nonceGuard) removeElementLocked(elem *list.Element, count bool) {
	if elem == nil {
		return
	}
	record, _ := elem.Value.(*nonceRecord)
	g.order.Remove(elem)
	if record != nil {
		delete(g.entries, record.key)
		if count {
			g.metrics.observeEvicted(1)
		}
	}
	g.metrics.observeSize(len(g.entries))
}

func (g *nonceGuard) runJanitor() {
	defer g.janitorWG.Done()
	ticker := time.NewTicker(nonceGuardJanitorInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			g.sweep()
		case <-g.janitorStop:
			return
		}
	}
}

func (g *nonceGuard) sweep() {
	now := g.now()
	g.mu.Lock()
	g.removeExpiredLocked(now)
	g.evictOverflowLocked()
	g.mu.Unlock()
}

func (g *nonceGuard) stopJanitor() {
	g.stopOnce.Do(func() {
		close(g.janitorStop)
		g.janitorWG.Wait()
	})
}

func (g *nonceGuard) Close() {
	if g == nil {
		return
	}
	g.stopJanitor()
}

func (g *nonceGuard) fingerprint(nodeID, nonce string) string {
	canonicalNonce, ok := canonicalizeNonce(nonce)
	if !ok {
		return ""
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(nodeID))
	}
	if normalized == "" {
		return ""
	}
	payload := normalized + ":" + canonicalNonce
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func (g *nonceGuard) Size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

func (g *nonceGuard) SetMaxEntries(max int) {
	if max <= 0 {
		return
	}
	g.mu.Lock()
	g.maxEntries = max
	g.evictOverflowLocked()
	g.mu.Unlock()
}

func (g *nonceGuard) RunJanitorSweep(now time.Time) {
	g.mu.Lock()
	g.removeExpiredLocked(now)
	g.evictOverflowLocked()
	g.mu.Unlock()
}

const (
	defaultNonceGuardMaxEntries = 100_000
	defaultNonceGuardTTL        = 15 * time.Minute
	nonceGuardJanitorInterval   = time.Minute
)

type nonceGuardMetrics struct {
	size    prometheus.Gauge
	evicted prometheus.Counter
}

var (
	nonceGuardMetricsOnce sync.Once
	nonceGuardMetricsInst *nonceGuardMetrics
)

func getNonceGuardMetrics() *nonceGuardMetrics {
	nonceGuardMetricsOnce.Do(func() {
		nonceGuardMetricsInst = &nonceGuardMetrics{
			size: prometheus.NewGauge(prometheus.GaugeOpts{
				Name: "nhb_p2p_nonce_guard_size",
				Help: "Number of entries tracked by the handshake nonce guard.",
			}),
			evicted: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "nhb_p2p_nonce_guard_evicted_total",
				Help: "Number of nonce guard entries evicted due to TTL or capacity.",
			}),
		}
		prometheus.MustRegister(nonceGuardMetricsInst.size, nonceGuardMetricsInst.evicted)
	})
	return nonceGuardMetricsInst
}

func (m *nonceGuardMetrics) observeSize(size int) {
	if m == nil {
		return
	}
	m.size.Set(float64(size))
}

func (m *nonceGuardMetrics) observeEvicted(delta int) {
	if m == nil || delta <= 0 {
		return
	}
	m.evicted.Add(float64(delta))
}
