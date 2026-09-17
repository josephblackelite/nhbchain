package middleware

import (
	"log"
	"net"
	"net/http"
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
