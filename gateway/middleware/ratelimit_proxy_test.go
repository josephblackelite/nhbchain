package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func mustParseProxies(t *testing.T, entries ...string) []netip.Prefix {
	t.Helper()
	prefixes, err := ParseTrustedProxies(entries)
	if err != nil {
		t.Fatalf("parse trusted proxies %v: %v", entries, err)
	}
	return prefixes
}

func requestFrom(remote string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
	req.RemoteAddr = remote
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return req
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestClientIDOnlyBelievesListedProxies(t *testing.T) {
	trusted := mustParseProxies(t, "10.0.0.0/24", "192.0.2.77")
	limiter := NewRateLimiter(nil, nil, WithTrustedProxies(trusted))
	bare := NewRateLimiter(nil, nil)

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    string
	}{
		{"no headers", "203.0.113.9:5000", nil, "203.0.113.9"},
		{"headers from an unlisted peer are ignored", "203.0.113.9:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1", "X-Real-IP": "198.51.100.2", "X-API-Key": "k"}, "203.0.113.9"},
		{"real ip from a listed peer", "10.0.0.5:5000",
			map[string]string{"X-Real-IP": "198.51.100.2"}, "198.51.100.2"},
		{"forwarded for from a listed peer", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1"}, "198.51.100.1"},
		{"a single listed address", "192.0.2.77:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1"}, "198.51.100.1"},
		{"rightmost entry that is not a proxy", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "203.0.113.200, 198.51.100.1, 10.0.0.9"}, "198.51.100.1"},
		{"entries the caller prepended are not believed", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "1.1.1.1, 2.2.2.2, 198.51.100.1"}, "198.51.100.1"},
		{"forwarded for wins over real ip", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1", "X-Real-IP": "198.51.100.2"}, "198.51.100.1"},
		{"entry with a port", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1:4444"}, "198.51.100.1"},
		{"all entries are proxies", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "10.0.0.8, 10.0.0.9"}, "10.0.0.5"},
		{"unusable forwarded for falls back to the peer", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "not-an-address"}, "10.0.0.5"},
		{"unusable entry on the right falls back to the peer", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "198.51.100.1, garbage"}, "10.0.0.5"},
		{"unusable real ip falls back to the peer", "10.0.0.5:5000",
			map[string]string{"X-Real-IP": "somebody"}, "10.0.0.5"},
		{"real ip with several addresses falls back to the peer", "10.0.0.5:5000",
			map[string]string{"X-Real-IP": "198.51.100.1, 198.51.100.2"}, "10.0.0.5"},
		{"ipv6 client stands for its /64", "10.0.0.5:5000",
			map[string]string{"X-Forwarded-For": "2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "2001:db8:1:2::/64"},
		{"remote address that is not an ip", "@", nil, unknownPeerID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := limiter.clientID(requestFrom(tc.remote, tc.headers)); got != tc.want {
				t.Fatalf("client id %q, want %q", got, tc.want)
			}
		})
	}

	// With no proxy listed no header is ever believed.
	req := requestFrom("10.0.0.5:5000", map[string]string{"X-Forwarded-For": "198.51.100.1", "X-Real-IP": "198.51.100.2"})
	if got := bare.clientID(req); got != "10.0.0.5" {
		t.Fatalf("with no listed proxy the client id is %q, want the peer address", got)
	}
}

func TestClientIDMasksIPv6ToItsNetwork(t *testing.T) {
	limiter := NewRateLimiter(nil, nil)
	a := limiter.clientID(requestFrom("[2001:db8:1:2::1]:4000", nil))
	b := limiter.clientID(requestFrom("[2001:db8:1:2:ffff:ffff:ffff:ffff]:4000", nil))
	c := limiter.clientID(requestFrom("[2001:db8:1:3::1]:4000", nil))
	if a != b {
		t.Fatalf("two addresses of one /64 got %q and %q, want one identity", a, b)
	}
	if a == c {
		t.Fatalf("addresses of different /64 networks share the identity %q", a)
	}
	if mapped, plain := limiter.clientID(requestFrom("[::ffff:192.0.2.1]:4000", nil)), limiter.clientID(requestFrom("192.0.2.1:4000", nil)); mapped != plain {
		t.Fatalf("an IPv4-mapped address got %q, want the IPv4 identity %q", mapped, plain)
	}
	if zoned := limiter.clientID(requestFrom("[fe80::1%eth0]:4000", nil)); zoned != limiter.clientID(requestFrom("[fe80::2%eth1]:4000", nil)) {
		t.Fatalf("zone identifiers split one /64 into separate identities")
	}
}

func TestRateLimiterSharesBucketAcrossAnIPv6Network(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 0.001, Burst: 2},
	}, nil)
	limiter.clockNow = func() time.Time { return time.Unix(1700000000, 0) }
	handler := limiter.Middleware("lending")(okHandler())

	allowed := 0
	for i := 0; i < 40; i++ {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, requestFrom(fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1), nil))
		if res.Code == http.StatusOK {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("%d requests from one /64 were allowed, want the burst of 2", allowed)
	}
}

func TestRateLimiterSeparatesClientsBehindAListedProxy(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 0.001, Burst: 1},
	}, nil, WithTrustedProxies(mustParseProxies(t, "10.0.0.0/24")))
	limiter.clockNow = func() time.Time { return time.Unix(1700000000, 0) }
	handler := limiter.Middleware("lending")(okHandler())

	send := func(forwarded string) int {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, requestFrom("10.0.0.5:4000", map[string]string{"X-Forwarded-For": forwarded}))
		return res.Code
	}
	if code := send("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first client: HTTP %d, want 200", code)
	}
	if code := send("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("first client again: HTTP %d, want 429", code)
	}
	if code := send("198.51.100.2"); code != http.StatusOK {
		t.Fatalf("second client behind the same proxy: HTTP %d, want its own bucket", code)
	}

	// A caller cannot get a fresh bucket by writing a different first entry,
	// because the proxy appended the address it actually saw after it.
	for i := 0; i < 20; i++ {
		if code := send(fmt.Sprintf("203.0.113.%d, 198.51.100.1", i+1)); code != http.StatusTooManyRequests {
			t.Fatalf("request %d with a made-up leading entry: HTTP %d, want 429", i, code)
		}
	}
}

func TestRateLimiterBoundsItsBuckets(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
	}, nil, WithMaxBuckets(100))
	handler := limiter.Middleware("lending")(okHandler())

	for i := 0; i < 5000; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), requestFrom(fmt.Sprintf("10.1.%d.%d:4000", i/250, i%250+1), nil))
		if got := len(limiter.visitors); got > 100 {
			t.Fatalf("%d buckets held after %d clients, want at most 100", got, i+1)
		}
	}
	if len(limiter.visitors) != 100 || limiter.order.Len() != 100 {
		t.Fatalf("table has %d entries and the order list %d, want both 100", len(limiter.visitors), limiter.order.Len())
	}
}

func TestRateLimiterDropsTheLeastRecentlyUsedBucket(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
	}, nil, WithMaxBuckets(2))
	handler := limiter.Middleware("lending")(okHandler())
	hit := func(addr string) {
		handler.ServeHTTP(httptest.NewRecorder(), requestFrom(addr+":4000", nil))
	}

	hit("192.0.2.1")
	hit("192.0.2.2")
	hit("192.0.2.1") // 192.0.2.2 is now the least recently used
	hit("192.0.2.3")

	if _, ok := limiter.visitors["lending|192.0.2.2"]; ok {
		t.Fatalf("the least recently used bucket was kept")
	}
	for _, id := range []string{"lending|192.0.2.1", "lending|192.0.2.3"} {
		if _, ok := limiter.visitors[id]; !ok {
			t.Fatalf("bucket %s was dropped", id)
		}
	}
}

func TestRateLimiterDropsIdleBucketsOnly(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 0.001, Burst: 1},
	}, nil, WithIdleTimeout(time.Minute))
	now := time.Unix(1700000000, 0)
	limiter.clockNow = func() time.Time { return now }
	handler := limiter.Middleware("lending")(okHandler())
	hit := func(addr string) int {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, requestFrom(addr+":4000", nil))
		return res.Code
	}

	hit("192.0.2.1")
	hit("192.0.2.2")
	now = now.Add(40 * time.Second)
	if code := hit("192.0.2.1"); code != http.StatusTooManyRequests {
		t.Fatalf("drained client: HTTP %d, want 429", code)
	}
	now = now.Add(50 * time.Second) // .2 has been idle for 90s, .1 for 50s
	hit("192.0.2.3")
	if got := len(limiter.visitors); got != 2 {
		t.Fatalf("%d buckets held, want the idle one dropped and 2 kept", got)
	}
	if code := hit("192.0.2.1"); code != http.StatusTooManyRequests {
		t.Fatalf("a bucket that was not idle lost its state: HTTP %d, want 429", code)
	}
	if code := hit("192.0.2.2"); code != http.StatusOK {
		t.Fatalf("a bucket idle past the timeout was kept: HTTP %d, want a fresh one", code)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := ParseTrustedProxies([]string{" 10.0.0.0/8 ", "192.0.2.7", "2001:db8::/32", "::ffff:198.51.100.4"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"10.0.0.0/8", "192.0.2.7/32", "2001:db8::/32", "198.51.100.4/32"}
	if len(prefixes) != len(want) {
		t.Fatalf("got %v, want %v", prefixes, want)
	}
	for i := range want {
		if prefixes[i].String() != want[i] {
			t.Fatalf("entry %d is %s, want %s", i, prefixes[i], want[i])
		}
	}
	if none, err := ParseTrustedProxies(nil); err != nil || len(none) != 0 {
		t.Fatalf("an empty list gave %v, %v, want no proxies and no error", none, err)
	}

	for _, bad := range []string{"", "  ", "proxy.internal", "10.0.0.0/33", "0.0.0.0/0", "::/0", "::ffff:10.0.0.0/104"} {
		if _, err := ParseTrustedProxies([]string{"10.0.0.1", bad}); err == nil {
			t.Fatalf("entry %q was accepted", bad)
		} else if !strings.Contains(err.Error(), "trustedProxies[1]") {
			t.Fatalf("entry %q: error %q does not name the entry", bad, err)
		}
	}
}
