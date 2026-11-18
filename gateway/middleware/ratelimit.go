package middleware

import (
	"container/list"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// defaultMaxBuckets bounds how many client buckets are held at once, across
	// all route keys. The least recently used bucket is dropped to make room.
	defaultMaxBuckets = 100000
	// defaultIdleTimeout is how long a bucket that has seen no request is kept.
	defaultIdleTimeout = 5 * time.Minute
	// maxForwardedHops bounds how many trusted entries are skipped while looking
	// for the client in a forwarded-for header.
	maxForwardedHops = 16
	// unknownPeerID is the client identity of a connection whose remote address
	// is not an IP address. All such connections share one bucket.
	unknownPeerID = "unknown-peer"
)

type RateLimit struct {
	RatePerSecond float64
	Burst         int
	Tokens        map[string]int
	DefaultTokens int
}

type rateEntry struct {
	id       string
	limiter  *rate.Limiter
	lastSeen time.Time
}

type RateLimiter struct {
	logger      *log.Logger
	limits      map[string]RateLimit
	trusted     []netip.Prefix
	maxBuckets  int
	idleTimeout time.Duration
	clockNow    func() time.Time

	mu       sync.Mutex
	visitors map[string]*list.Element
	// order holds the buckets from least to most recently used.
	order *list.List
}

// RateLimiterOption adjusts a RateLimiter at construction.
type RateLimiterOption func(*RateLimiter)

// WithTrustedProxies lists the peers whose X-Forwarded-For and X-Real-IP
// headers are believed. A request from any other peer is limited under the
// address it connected from, whatever headers it carries.
func WithTrustedProxies(proxies []netip.Prefix) RateLimiterOption {
	return func(r *RateLimiter) {
		r.trusted = append([]netip.Prefix(nil), proxies...)
	}
}

// WithMaxBuckets sets the most client buckets held at once. A value of zero or
// less keeps the default; the table is always bounded.
func WithMaxBuckets(n int) RateLimiterOption {
	return func(r *RateLimiter) {
		if n > 0 {
			r.maxBuckets = n
		}
	}
}

// WithIdleTimeout sets how long a bucket with no requests is kept. A value of
// zero or less keeps the default.
func WithIdleTimeout(d time.Duration) RateLimiterOption {
	return func(r *RateLimiter) {
		if d > 0 {
			r.idleTimeout = d
		}
	}
}

// ParseTrustedProxies reads a list of IP addresses and CIDR ranges. An entry
// that covers every address is refused, since it would trust every caller.
func ParseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(entries))
	for i, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return nil, fmt.Errorf("trustedProxies[%d] is empty", i)
		}
		if strings.Contains(trimmed, "/") {
			prefix, err := netip.ParsePrefix(trimmed)
			if err != nil {
				return nil, fmt.Errorf("trustedProxies[%d] %q: %w", i, trimmed, err)
			}
			if prefix.Addr().Is4In6() {
				return nil, fmt.Errorf("trustedProxies[%d] %q: write an IPv4 range in its IPv4 form", i, trimmed)
			}
			if prefix.Bits() == 0 {
				return nil, fmt.Errorf("trustedProxies[%d] %q covers every address", i, trimmed)
			}
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(trimmed)
		if err != nil {
			return nil, fmt.Errorf("trustedProxies[%d] %q: %w", i, trimmed, err)
		}
		addr = addr.WithZone("").Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

func NewRateLimiter(limits map[string]RateLimit, logger *log.Logger, opts ...RateLimiterOption) *RateLimiter {
	if logger == nil {
		logger = log.Default()
	}
	limiter := &RateLimiter{
		logger:      logger,
		limits:      limits,
		maxBuckets:  defaultMaxBuckets,
		idleTimeout: defaultIdleTimeout,
		clockNow:    time.Now,
		visitors:    make(map[string]*list.Element),
		order:       list.New(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(limiter)
		}
	}
	return limiter
}

func (r *RateLimiter) Middleware(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			limit, ok := r.limits[key]
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			identifier := r.clientID(req)
			bucketKey := key + "|" + identifier
			limiter := r.obtainLimiter(bucketKey, limit)
			tokens := r.tokensFor(limit, req)
			if !limiter.AllowN(r.clockNow(), tokens) {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecondsFor(limit.RatePerSecond, tokens)))
				http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

func (r *RateLimiter) obtainLimiter(id string, cfg RateLimit) *rate.Limiter {
	now := r.clockNow()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictIdleLocked(now)
	if element, ok := r.visitors[id]; ok {
		entry := element.Value.(*rateEntry)
		entry.lastSeen = now
		r.order.MoveToBack(element)
		return entry.limiter
	}
	perSecond := cfg.RatePerSecond
	if perSecond <= 0 {
		perSecond = 1
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = 1
	}
	for len(r.visitors) >= r.maxBuckets {
		front := r.order.Front()
		if front == nil {
			break
		}
		r.removeLocked(front)
	}
	limiter := rate.NewLimiter(rate.Limit(perSecond), burst)
	r.visitors[id] = r.order.PushBack(&rateEntry{id: id, limiter: limiter, lastSeen: now})
	return limiter
}

// evictIdleLocked drops the buckets that have seen no request for the idle
// timeout. The list is in order of last use, so it stops at the first one that
// is still fresh.
func (r *RateLimiter) evictIdleLocked(now time.Time) {
	cutoff := now.Add(-r.idleTimeout)
	for {
		front := r.order.Front()
		if front == nil {
			return
		}
		if !front.Value.(*rateEntry).lastSeen.Before(cutoff) {
			return
		}
		r.removeLocked(front)
	}
}

func (r *RateLimiter) removeLocked(element *list.Element) {
	entry := r.order.Remove(element).(*rateEntry)
	delete(r.visitors, entry.id)
}

func (r *RateLimiter) tokensFor(limit RateLimit, req *http.Request) int {
	if len(limit.Tokens) == 0 {
		if limit.DefaultTokens > 0 {
			return limit.DefaultTokens
		}
		return 1
	}
	lookup := strings.ToUpper(req.Method) + " " + req.URL.Path
	if tokens, ok := limit.Tokens[lookup]; ok && tokens > 0 {
		return tokens
	}
	if limit.DefaultTokens > 0 {
		return limit.DefaultTokens
	}
	return 1
}

// clientID names the caller a request is limited as. The limiter runs before
// authentication, so nothing the caller writes may choose its own identity: it
// is the address the connection came from (an IPv6 address stands for its /64),
// or, when that peer is a listed proxy, the client address the proxy relayed.
func (r *RateLimiter) clientID(req *http.Request) string {
	peer, ok := parseClientAddr(req.RemoteAddr)
	if !ok {
		return unknownPeerID
	}
	client := peer
	if r.isTrustedProxy(peer) {
		if forwarded, ok := r.forwardedClient(req); ok {
			client = forwarded
		}
	}
	return addrKey(client)
}

func (r *RateLimiter) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// forwardedClient reads the client address a trusted proxy relayed. With an
// X-Forwarded-For header it is the rightmost entry that is not itself a listed
// proxy, since each proxy appends the address it received the request from and
// everything to the left of that is the caller's own claim. Without one it is
// X-Real-IP. A header that does not hold usable addresses is ignored.
func (r *RateLimiter) forwardedClient(req *http.Request) (netip.Addr, bool) {
	if values := req.Header.Values("X-Forwarded-For"); len(values) > 0 {
		return r.rightmostUntrusted(values)
	}
	realIP := strings.TrimSpace(req.Header.Get("X-Real-IP"))
	if realIP == "" || strings.Contains(realIP, ",") {
		return netip.Addr{}, false
	}
	return parseClientAddr(realIP)
}

func (r *RateLimiter) rightmostUntrusted(values []string) (netip.Addr, bool) {
	hops := 0
	for i := len(values) - 1; i >= 0; i-- {
		rest := values[i]
		for done := false; !done; {
			token := rest
			if cut := strings.LastIndexByte(rest, ','); cut >= 0 {
				token, rest = rest[cut+1:], rest[:cut]
			} else {
				done = true
			}
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			addr, ok := parseClientAddr(token)
			if !ok {
				return netip.Addr{}, false
			}
			if !r.isTrustedProxy(addr) {
				return addr, true
			}
			hops++
			if hops >= maxForwardedHops {
				return netip.Addr{}, false
			}
		}
	}
	return netip.Addr{}, false
}

// parseClientAddr reads an IP address, with or without a port, as it appears
// in a remote address or a forwarded header. Text that is not an IP address is
// refused, so that it can never become a key of its own.
func parseClientAddr(value string) (netip.Addr, bool) {
	trimmed := strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	trimmed = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.WithZone("").Unmap(), true
}

// addrKey is the key a client address is limited under. Whoever holds an IPv6
// network holds at least a /64 of addresses and can send from a different one
// each time, so an IPv6 address stands for its /64.
func addrKey(addr netip.Addr) string {
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// retryAfterSecondsFor is a stateless worst-case Retry-After hint for a
// refused request needing tokens tokens from a bucket refilling at
// perSecond tokens/second: the time to accumulate them from empty, rounded
// up. It does not read the limiter's actual remaining tokens (AllowN does
// not expose them), so it is the same kind of advisory upper bound the
// portal's own retryAfterSecondsFor (src/lib/server/rateLimit.ts) gives for
// its token-bucket limits, not an exact wait. (ledger PL-DC-53)
func retryAfterSecondsFor(perSecond float64, tokens int) int {
	if perSecond <= 0 {
		perSecond = 1
	}
	if tokens <= 0 {
		tokens = 1
	}
	secs := int(math.Ceil(float64(tokens) / perSecond))
	if secs < 1 {
		secs = 1
	}
	return secs
}
