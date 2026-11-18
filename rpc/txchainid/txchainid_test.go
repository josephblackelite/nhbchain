package txchainid

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The network's own id on the live chain: larger than 2^53, so a reader that goes
// through a float64 (a JSON number decoded into interface{}) loses its low digits.
const liveNetworkID = "18346390202490284624"

func TestParseReadsTheDecimalStringTheNodeServes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"legacy constant", `{"txChainId":"5130306"}`, "5130306"},
		{"network id above 2^53 keeps every digit", `{"txChainId":"` + liveNetworkID + `"}`, liveNetworkID},
		{"surrounding spaces", `{"txChainId":"  42  "}`, "42"},
		{"other fields are ignored", `{"activeValidators":2,"txChainId":"7","tps":1.5}`, "7"},
		{"a JSON number is read exactly", `{"txChainId":` + liveNetworkID + `}`, liveNetworkID},
		{"the largest uint64", `{"txChainId":"18446744073709551615"}`, "18446744073709551615"},
	}
	for _, tc := range cases {
		got, err := Parse([]byte(tc.in))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.String() != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestParseRefusesAnythingThatIsNotAUsableID(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty body", ``},
		{"not json", `not json`},
		{"null result", `null`},
		{"a node that reports none (older release)", `{"activeValidators":2}`},
		{"empty string", `{"txChainId":""}`},
		{"blank string", `{"txChainId":"   "}`},
		{"zero", `{"txChainId":"0"}`},
		{"zero as a number", `{"txChainId":0}`},
		{"negative", `{"txChainId":"-5"}`},
		{"hex is not what the node serves", `{"txChainId":"0x4e4842"}`},
		{"text", `{"txChainId":"abc"}`},
		{"fraction", `{"txChainId":"12.5"}`},
		{"exponent", `{"txChainId":1e3}`},
		{"plus sign", `{"txChainId":"+7"}`},
		{"above uint64", `{"txChainId":"18446744073709551616"}`},
		{"null value", `{"txChainId":null}`},
		{"boolean", `{"txChainId":true}`},
		{"an object", `{"txChainId":{"id":1}}`},
		{"an array result", `[1,2]`},
	}
	for _, tc := range cases {
		got, err := Parse([]byte(tc.in))
		if err == nil {
			t.Fatalf("%s: accepted %q as %v", tc.name, tc.in, got)
		}
		if got != nil {
			t.Fatalf("%s: returned a value with an error", tc.name)
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: error %v does not wrap ErrUnavailable", tc.name, err)
		}
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestReader(fetch Fetcher, ttl time.Duration) (*Reader, *clock) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	r := NewReader(fetch, ttl)
	r.now = c.now
	return r, c
}

func fixed(id string, calls *int32) Fetcher {
	return func(ctx context.Context) (*big.Int, error) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		v, _ := new(big.Int).SetString(id, 10)
		return v, nil
	}
}

func TestReaderAsksOnceWithinTheExpiryAndAgainAfterIt(t *testing.T) {
	var calls int32
	r, c := newTestReader(fixed("5130306", &calls), 15*time.Second)
	for i := 0; i < 5; i++ {
		id, err := r.ChainID(context.Background())
		if err != nil || id.String() != "5130306" {
			t.Fatalf("call %d: %v %v", i, id, err)
		}
	}
	if calls != 1 {
		t.Fatalf("5 reads inside the expiry made %d node calls, want 1", calls)
	}
	c.t = c.t.Add(14 * time.Second)
	if _, err := r.ChainID(context.Background()); err != nil || calls != 1 {
		t.Fatalf("still inside the expiry: calls=%d err=%v", calls, err)
	}
	c.t = c.t.Add(time.Second) // exactly the expiry
	if _, err := r.ChainID(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("at the expiry the id must be asked again: calls=%d", calls)
	}
}

func TestReaderPicksUpTheActivationAfterItsExpiry(t *testing.T) {
	// The node serves the legacy constant until the activation height and the network id from it. A signer
	// that lives across the height must not keep signing with the old one.
	current := "5130306"
	fetch := func(ctx context.Context) (*big.Int, error) {
		v, _ := new(big.Int).SetString(current, 10)
		return v, nil
	}
	r, c := newTestReader(fetch, 15*time.Second)
	if id, _ := r.ChainID(context.Background()); id.String() != "5130306" {
		t.Fatalf("before the height: %v", id)
	}
	current = liveNetworkID
	if id, _ := r.ChainID(context.Background()); id.String() != "5130306" {
		t.Fatalf("inside the expiry the cached value is served: %v", id)
	}
	c.t = c.t.Add(16 * time.Second)
	if id, _ := r.ChainID(context.Background()); id.String() != liveNetworkID {
		t.Fatalf("after the expiry the new id must be read: %v", id)
	}
}

func TestReaderFailsClosedAndNeverServesAnExpiredValue(t *testing.T) {
	failing := false
	fetch := func(ctx context.Context) (*big.Int, error) {
		if failing {
			return nil, errors.New("node unreachable")
		}
		return big.NewInt(99), nil
	}
	r, c := newTestReader(fetch, 15*time.Second)
	if _, err := r.ChainID(context.Background()); err != nil {
		t.Fatal(err)
	}
	failing = true
	c.t = c.t.Add(time.Minute)
	id, err := r.ChainID(context.Background())
	if err == nil || id != nil {
		t.Fatalf("an expired value must not be served when the node cannot be asked: %v %v", id, err)
	}
	if !strings.Contains(err.Error(), "node unreachable") {
		t.Fatalf("the cause is lost: %v", err)
	}
	// An error is not cached: the next call asks again and recovers.
	failing = false
	if id, err := r.ChainID(context.Background()); err != nil || id.Int64() != 99 {
		t.Fatalf("no recovery after the node came back: %v %v", id, err)
	}
}

func TestReaderRefusesWhatTheFetcherReturnsWhenItIsNotAUsableID(t *testing.T) {
	cases := map[string]*big.Int{
		"nil":      nil,
		"zero":     big.NewInt(0),
		"negative": big.NewInt(-1),
		"too big":  new(big.Int).Lsh(big.NewInt(1), 64),
	}
	for name, v := range cases {
		v := v
		r, _ := newTestReader(func(ctx context.Context) (*big.Int, error) { return v, nil }, time.Minute)
		id, err := r.ChainID(context.Background())
		if err == nil || id != nil {
			t.Fatalf("%s: accepted %v", name, id)
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// And a refused answer is not cached as if it were a good one.
	var calls int32
	r, _ := newTestReader(func(ctx context.Context) (*big.Int, error) {
		atomic.AddInt32(&calls, 1)
		return big.NewInt(0), nil
	}, time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := r.ChainID(context.Background()); err == nil {
			t.Fatal("zero accepted")
		}
	}
	if calls != 3 {
		t.Fatalf("a refused answer was cached: %d calls for 3 reads", calls)
	}
}

func TestReaderHandsOutCopies(t *testing.T) {
	r, _ := newTestReader(fixed("1234", nil), time.Minute)
	first, _ := r.ChainID(context.Background())  // read from the node
	first.SetInt64(1)                            // a caller that mutates what it got
	second, _ := r.ChainID(context.Background()) // served from the kept answer
	if second.String() != "1234" {
		t.Fatalf("the cached value was changed through a returned copy: %s", second)
	}
	second.SetInt64(2) // and one that mutates a kept answer it was handed
	third, _ := r.ChainID(context.Background())
	if third.String() != "1234" {
		t.Fatalf("the cache hands out its own value, not a copy: %s", third)
	}
	// And the fetcher's own value is not kept by reference either.
	shared := big.NewInt(77)
	r2, _ := newTestReader(func(ctx context.Context) (*big.Int, error) { return shared, nil }, time.Minute)
	if _, err := r2.ChainID(context.Background()); err != nil {
		t.Fatal(err)
	}
	shared.SetInt64(5)
	got, _ := r2.ChainID(context.Background())
	if got.Int64() != 77 {
		t.Fatalf("the cache aliases the fetcher's value: %v", got)
	}
}

func TestReaderInvalidateForcesANewRead(t *testing.T) {
	var calls int32
	r, _ := newTestReader(fixed("5", &calls), time.Hour)
	_, _ = r.ChainID(context.Background())
	_, _ = r.ChainID(context.Background())
	r.Invalidate()
	_, _ = r.ChainID(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
}

func TestReaderIsSafeUnderConcurrentUseAndAsksOnce(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func(ctx context.Context) (*big.Int, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return big.NewInt(31), nil
	}
	r, _ := newTestReader(fetch, time.Minute)
	var wg sync.WaitGroup
	var bad int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := r.ChainID(context.Background())
			if err != nil || id.Int64() != 31 {
				atomic.AddInt32(&bad, 1)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d goroutines got a wrong answer", bad)
	}
	if calls != 1 {
		t.Fatalf("40 concurrent readers made %d node calls, want 1", calls)
	}
}

func TestReaderHonoursACancelledContext(t *testing.T) {
	r, _ := newTestReader(func(ctx context.Context) (*big.Int, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if id, err := r.ChainID(ctx); err == nil || id != nil {
		t.Fatalf("a cancelled read returned %v %v", id, err)
	}
	// A source that ignores the context must not be asked at all once the caller has given up.
	var asked int32
	r2, _ := newTestReader(func(ctx context.Context) (*big.Int, error) {
		atomic.AddInt32(&asked, 1)
		return big.NewInt(5), nil
	}, time.Minute)
	if id, err := r2.ChainID(ctx); err == nil || id != nil || asked != 0 {
		t.Fatalf("a cancelled caller was served: %v %v asked=%d", id, err, asked)
	}
}

func TestNewReaderWithoutAFetcherOrExpiryIsSafe(t *testing.T) {
	r := NewReader(nil, 0)
	if id, err := r.ChainID(context.Background()); err == nil || id != nil {
		t.Fatalf("a reader with no source must refuse: %v %v", id, err)
	}
	var nilReader *Reader
	if id, err := nilReader.ChainID(context.Background()); err == nil || id != nil {
		t.Fatalf("a nil reader must refuse: %v %v", id, err)
	}
	nilReader.Invalidate() // must not panic
	if DefaultTTL <= 0 || DefaultTTL > time.Minute {
		t.Fatalf("the default expiry must be short: %v", DefaultTTL)
	}
	r2 := NewReader(fixed("1", nil), 0)
	if r2.ttl != DefaultTTL {
		t.Fatalf("a zero expiry must become the default, got %v", r2.ttl)
	}
}

// --- the HTTP fetcher -------------------------------------------------------------------

type rpcServer struct {
	*httptest.Server
	calls   int32
	handler func(w http.ResponseWriter, r *http.Request, method string)
	auth    string
	body    string
}

func newRPCServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, method string)) *rpcServer {
	t.Helper()
	s := &rpcServer{handler: handler}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.calls, 1)
		s.auth = r.Header.Get("Authorization")
		var req struct {
			JSONRPC string            `json:"jsonrpc"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
			ID      json.RawMessage   `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.body = req.Method
		s.handler(w, r, req.Method)
	}))
	t.Cleanup(s.Close)
	return s
}

func writeResult(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`))
}

func TestHTTPFetcherAsksTheNetworkStatsCallAndReadsTxChainID(t *testing.T) {
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		if method != "nhb_getNetworkStats" {
			t.Errorf("asked %q, want nhb_getNetworkStats (net_info is operator-only)", method)
		}
		if r.Method != http.MethodPost {
			t.Errorf("verb %s", r.Method)
		}
		writeResult(w, `{"activeValidators":2,"txChainId":"`+liveNetworkID+`","tps":0}`)
	})
	id, err := HTTPFetcher(srv.Client(), srv.URL, "")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != liveNetworkID {
		t.Fatalf("got %s", id)
	}
	if srv.auth != "" {
		t.Fatalf("no token was configured but Authorization was sent: %q", srv.auth)
	}
	if Method != "nhb_getNetworkStats" {
		t.Fatalf("Method = %q", Method)
	}
}

func TestHTTPFetcherSendsTheBearerTokenOnlyWhenOneIsGiven(t *testing.T) {
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) { writeResult(w, `{"txChainId":"9"}`) })
	if _, err := HTTPFetcher(srv.Client(), srv.URL, "  secret-token  ")(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.auth != "Bearer secret-token" {
		t.Fatalf("authorization header %q", srv.auth)
	}
}

func TestHTTPFetcherRefusesEveryBadAnswer(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request, method string)
	}{
		{"http 500", func(w http.ResponseWriter, r *http.Request, m string) { http.Error(w, "boom", 500) }},
		{"http 404", func(w http.ResponseWriter, r *http.Request, m string) { http.NotFound(w, r) }},
		{"http 503 that still carries a usable body", func(w http.ResponseWriter, r *http.Request, m string) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"txChainId":"5"}}`))
		}},
		{"a json-rpc error", func(w http.ResponseWriter, r *http.Request, m string) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
		}},
		{"an error next to a usable result", func(w http.ResponseWriter, r *http.Request, m string) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"txChainId":"5"},"error":{"code":-1,"message":"x"}}`))
		}},
		{"not json", func(w http.ResponseWriter, r *http.Request, m string) { _, _ = w.Write([]byte("<html>")) }},
		{"no result", func(w http.ResponseWriter, r *http.Request, m string) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1}`))
		}},
		{"a node with no txChainId", func(w http.ResponseWriter, r *http.Request, m string) { writeResult(w, `{"activeValidators":2}`) }},
		{"zero", func(w http.ResponseWriter, r *http.Request, m string) { writeResult(w, `{"txChainId":"0"}`) }},
		{"garbage", func(w http.ResponseWriter, r *http.Request, m string) { writeResult(w, `{"txChainId":"x"}`) }},
	}
	for _, tc := range cases {
		srv := newRPCServer(t, tc.handler)
		id, err := HTTPFetcher(srv.Client(), srv.URL, "")(context.Background())
		if err == nil || id != nil {
			t.Fatalf("%s: accepted %v", tc.name, id)
		}
	}
	// An unreachable node.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	if id, err := HTTPFetcher(http.DefaultClient, url, "")(context.Background()); err == nil || id != nil {
		t.Fatalf("an unreachable node gave %v", id)
	}
	// No endpoint at all.
	if id, err := HTTPFetcher(nil, "  ", "")(context.Background()); err == nil || id != nil {
		t.Fatalf("an empty endpoint gave %v", id)
	}
}

func TestHTTPFetcherReadsAtMostAMegabyte(t *testing.T) {
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		// A valid answer that is padded past the limit must not be parsed to the end.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"pad":"` + strings.Repeat("a", 2<<20) + `","txChainId":"5"}}`))
	})
	if id, err := HTTPFetcher(srv.Client(), srv.URL, "")(context.Background()); err == nil || id != nil {
		t.Fatalf("an oversized answer was accepted: %v", id)
	}
}

func TestHTTPFetcherStopsReadingAnEndlessAnswer(t *testing.T) {
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		chunk := []byte(strings.Repeat("a", 64<<10))
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := HTTPFetcher(srv.Client(), srv.URL, "")(ctx)
	if err == nil || id != nil {
		t.Fatalf("an endless answer gave %v", id)
	}
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("the read must stop at the limit, not at the deadline: %v", err)
	}
}

func TestHTTPFetcherBoundsTheWaitWithTheContext(t *testing.T) {
	release := make(chan struct{})
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if id, err := HTTPFetcher(srv.Client(), srv.URL, "")(ctx); err == nil || id != nil {
		t.Fatalf("a hung node gave %v", id)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the read did not honour the context")
	}
}

func TestEndToEndReaderOverHTTPAsksTheNodeOncePerExpiry(t *testing.T) {
	current := "5130306"
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		writeResult(w, `{"txChainId":"`+current+`"}`)
	})
	r, c := newTestReader(HTTPFetcher(srv.Client(), srv.URL, ""), 15*time.Second)
	for i := 0; i < 4; i++ {
		if id, err := r.ChainID(context.Background()); err != nil || id.String() != "5130306" {
			t.Fatalf("%v %v", id, err)
		}
	}
	if srv.calls != 1 {
		t.Fatalf("node calls = %d, want 1", srv.calls)
	}
	current = liveNetworkID
	c.t = c.t.Add(20 * time.Second)
	if id, err := r.ChainID(context.Background()); err != nil || id.String() != liveNetworkID {
		t.Fatalf("%v %v", id, err)
	}
	if srv.calls != 2 {
		t.Fatalf("node calls = %d, want 2", srv.calls)
	}
}

func TestExpiryReportsTheReuseTime(t *testing.T) {
	if got := NewReader(fixed("1", nil), 3*time.Second).Expiry(); got != 3*time.Second {
		t.Fatalf("Expiry = %v", got)
	}
	if got := NewReader(fixed("1", nil), 0).Expiry(); got != DefaultTTL {
		t.Fatalf("a zero expiry must be the default, got %v", got)
	}
	var nilReader *Reader
	if nilReader.Expiry() != 0 {
		t.Fatal("a nil reader has no expiry")
	}
}

// --- the running chain's own id (networkChainId), for the signers that name the chain rather than sign a transaction ----------

func TestParseNetworkReadsNetworkChainIDAndNothingElse(t *testing.T) {
	got, err := ParseNetwork([]byte(`{"txChainId":"5130306","networkChainId":"` + liveNetworkID + `"}`))
	if err != nil || got.String() != liveNetworkID {
		t.Fatalf("%v %v", got, err)
	}
	// The transaction id is a different value with a different rule: a result that only has it is not a network id.
	for name, in := range map[string]string{
		"only txChainId":       `{"txChainId":"` + liveNetworkID + `"}`,
		"empty":                `{"networkChainId":""}`,
		"zero":                 `{"networkChainId":"0"}`,
		"negative":             `{"networkChainId":"-1"}`,
		"hex":                  `{"networkChainId":"0x4e4842"}`,
		"above uint64":         `{"networkChainId":"18446744073709551616"}`,
		"null":                 `{"networkChainId":null}`,
		"an old node":          `{"activeValidators":2}`,
		"not json":             `nope`,
		"a boolean":            `{"networkChainId":true}`,
		"a number rounded off": `{"networkChainId":18346390202490284624e0}`,
	} {
		if v, err := ParseNetwork([]byte(in)); err == nil || v != nil || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s: accepted %v %v", name, v, err)
		}
	}
	// And Parse still reads only txChainId.
	if v, err := Parse([]byte(`{"networkChainId":"` + liveNetworkID + `"}`)); err == nil || v != nil {
		t.Fatalf("Parse accepted a result that only names the network id: %v", v)
	}
}

func TestNetworkHTTPFetcherAsksTheSameCallAndReadsTheNetworkID(t *testing.T) {
	srv := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) {
		if method != "nhb_getNetworkStats" {
			t.Errorf("asked %q, want nhb_getNetworkStats", method)
		}
		writeResult(w, `{"txChainId":"5130306","networkChainId":"`+liveNetworkID+`"}`)
	})
	id, err := NetworkHTTPFetcher(srv.Client(), srv.URL, "")(context.Background())
	if err != nil || id.String() != liveNetworkID {
		t.Fatalf("%v %v", id, err)
	}
	// A node older than the field answers without it: refused, never the transaction id.
	old := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) { writeResult(w, `{"txChainId":"5130306"}`) })
	if id, err := NetworkHTTPFetcher(old.Client(), old.URL, "")(context.Background()); err == nil || id != nil {
		t.Fatalf("a node with no networkChainId gave %v", id)
	}
	for name, h := range map[string]func(w http.ResponseWriter, r *http.Request, m string){
		"http 500": func(w http.ResponseWriter, r *http.Request, m string) { http.Error(w, "x", 500) },
		"rpc error": func(w http.ResponseWriter, r *http.Request, m string) {
			_, _ = w.Write([]byte(`{"error":{"code":-1,"message":"x"},"result":{"networkChainId":"5"}}`))
		},
	} {
		bad := newRPCServer(t, h)
		if id, err := NetworkHTTPFetcher(bad.Client(), bad.URL, "")(context.Background()); err == nil || id != nil {
			t.Fatalf("%s: gave %v", name, id)
		}
	}
	srv2 := newRPCServer(t, func(w http.ResponseWriter, r *http.Request, method string) { writeResult(w, `{"networkChainId":"9"}`) })
	if _, err := NetworkHTTPFetcher(srv2.Client(), srv2.URL, " tok ")(context.Background()); err != nil || srv2.auth != "Bearer tok" {
		t.Fatalf("token handling: %v %q", err, srv2.auth)
	}
}

func TestANetworkReaderCachesLikeTheTransactionReader(t *testing.T) {
	var calls int32
	r, c := newTestReader(func(ctx context.Context) (*big.Int, error) {
		atomic.AddInt32(&calls, 1)
		v, _ := new(big.Int).SetString(liveNetworkID, 10)
		return v, nil
	}, 15*time.Second)
	for i := 0; i < 3; i++ {
		if id, err := r.ChainID(context.Background()); err != nil || id.String() != liveNetworkID {
			t.Fatalf("%v %v", id, err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	c.t = c.t.Add(15 * time.Second)
	_, _ = r.ChainID(context.Background())
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}
