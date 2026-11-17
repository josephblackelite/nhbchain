package middleware

import (
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type RateLimit struct {
	RatePerSecond float64
	Burst         int
	Tokens        map[string]int
	DefaultTokens int
}

type rateEntry struct {
	limiter *rate.Limiter
}

type RateLimiter struct {
	logger   *log.Logger
	limits   map[string]RateLimit
	mu       sync.RWMutex
	visitors map[string]*rateEntry
	clockNow func() time.Time
}

func NewRateLimiter(limits map[string]RateLimit, logger *log.Logger) *RateLimiter {
	if logger == nil {
		logger = log.Default()
	}
	return &RateLimiter{
		logger:   logger,
		limits:   limits,
		visitors: make(map[string]*rateEntry),
		clockNow: time.Now,
	}
}

func (r *RateLimiter) Middleware(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			limit, ok := r.limits[key]
			if !ok {
				next.ServeHTTP(w, req)
				return
			}
			identifier := clientID(req)
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
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.visitors[id]
	if ok {
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
	limiter := rate.NewLimiter(rate.Limit(perSecond), burst)
	r.visitors[id] = &rateEntry{limiter: limiter}
	go r.cleanup(id)
	return limiter
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

// retryAfterSecondsFor is a stateless worst-case Retry-After hint for a
// refused request needing tokens tokens from a bucket refilling at
// perSecond tokens/second: the time to accumulate them from empty, rounded
// up. It does not read the limiter's actual remaining tokens (AllowN does
// not expose them), so it is the same kind of advisory upper bound the
// portal's own retryAfterSecondsFor (src/lib/server/rateLimit.ts) gives for
// its token-bucket limits, not an exact wait.
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

func (r *RateLimiter) cleanup(id string) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		delete(r.visitors, id)
		r.mu.Unlock()
		return
	}
}

func clientID(r *http.Request) string {
	if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
		return "api-key:" + apiKey
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		parts := net.ParseIP(ip)
		if parts != nil {
			return parts.String()
		}
		if comma := stringIndex(ip, ','); comma > 0 {
			trimmed := strings.TrimSpace(ip[:comma])
			if parsed := net.ParseIP(trimmed); parsed != nil {
				return parsed.String()
			}
		}
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func stringIndex(s string, ch byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ch {
			return i
		}
	}
	return -1
}
