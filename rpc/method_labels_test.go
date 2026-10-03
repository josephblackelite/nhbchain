package rpc

// The method name of a JSON-RPC request is read before anything is checked
// about it, so it must not decide how many metric series or rate limiter
// entries the node keeps: those are bounded by the methods the server serves
// (and the route limits an operator configures), not by what callers send.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/observability"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// dispatchSwitchMethods returns the string case labels of the switch on
// req.Method in Server.handle, read from the source so the check cannot be
// satisfied by copying the list.
func dispatchSwitchMethods(t *testing.T) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "http.go", nil, 0)
	if err != nil {
		t.Fatalf("parse http.go: %v", err)
	}
	methods := make(map[string]struct{})
	switches := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name.Name != "handle" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			sw, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			sel, ok := sw.Tag.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Method" {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "req" {
				return true
			}
			switches++
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: a dispatch case that is not a string literal cannot be checked against knownRPCMethods", fset.Position(expr.Pos()))
					}
					name, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
					}
					methods[name] = struct{}{}
				}
			}
			return true
		})
	}
	if switches != 1 {
		t.Fatalf("expected exactly one switch on req.Method in Server.handle, found %d; update this test with the new dispatch", switches)
	}
	return methods
}

// TestKnownRPCMethodsMatchDispatchSwitch keeps knownRPCMethods equal to what
// Server.handle dispatches. A method missing from the set would be reported as
// "unknown" and share the unknown rate limit bucket; a name left in the set
// after its case is removed would keep a metric label and a bucket of its own
// for a name that is no longer served.
func TestKnownRPCMethodsMatchDispatchSwitch(t *testing.T) {
	dispatched := dispatchSwitchMethods(t)
	if len(dispatched) < 100 {
		t.Fatalf("found only %d dispatch cases; the switch was probably restructured and this test needs updating", len(dispatched))
	}
	var missing, stale []string
	for name := range dispatched {
		if _, ok := knownRPCMethods[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range knownRPCMethods {
		if _, ok := dispatched[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("dispatched but missing from knownRPCMethods: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("in knownRPCMethods but not dispatched: %v", stale)
	}
}

func TestMetricModuleAndMethod(t *testing.T) {
	cases := []struct {
		method, module, action string
	}{
		{"nhb_getBalance", "nhb", "getBalance"},
		{"  nhb_getBalance\t", "nhb", "getBalance"},
		{"swap_voucher_list", "swap", "voucher_list"},
		{"nhb_notAMethod", unknownRPCLabel, unknownRPCLabel},
		{"nhb_getBalance_extra", unknownRPCLabel, unknownRPCLabel},
		{"NHB_GETBALANCE", unknownRPCLabel, unknownRPCLabel},
		{"nhb_", unknownRPCLabel, unknownRPCLabel},
		{"nobody", unknownRPCLabel, unknownRPCLabel},
		{"", "", ""},
		{"   ", "", ""},
	}
	for _, tc := range cases {
		module, action := metricModuleAndMethod(tc.method)
		if module != tc.module || action != tc.action {
			t.Errorf("metricModuleAndMethod(%q) = (%q, %q), want (%q, %q)", tc.method, module, action, tc.module, tc.action)
		}
	}
}

// probeMethod sends a JSON-RPC request for method as a plain client at remote
// and returns the HTTP status.
func probeMethod(srv *Server, remote, method string) int {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

// metricSeries returns the label sets of every series of the named metric on
// the default registry.
func metricSeries(t *testing.T, name string) []map[string]string {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	var series []map[string]string
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			series = append(series, labels)
		}
	}
	return series
}

func hasSeries(series []map[string]string, want map[string]string) bool {
	for _, labels := range series {
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestUnknownMethodNamesDoNotGrowMetricSeries sends requests whose method names
// are all different and not served, and checks that the metric series the
// server keeps do not grow with them.
func TestUnknownMethodNamesDoNotGrowMetricSeries(t *testing.T) {
	// Register the collectors so the families exist before they are counted.
	observability.ModuleMetrics()
	observability.RPC()

	families := []string{
		"nhb_module_requests_total",
		"nhb_module_errors_total",
		"nhb_module_request_duration_seconds",
		"nhb_module_throttles_total",
		"nhb_rpc_limiter_hits_total",
	}
	count := func(name string) int {
		n, err := testutil.GatherAndCount(prometheus.DefaultGatherer, name)
		if err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		return n
	}
	before := make(map[string]int, len(families))
	for _, name := range families {
		before[name] = count(name)
	}

	srv := newTestServer(t, nil, nil, ServerConfig{MaxTxPerWindow: 3, MaxTxPerIP: 3})
	const names = 300
	for i := 0; i < names; i++ {
		method := fmt.Sprintf("labelprobe%d_op%d_%s", i, i, strings.Repeat("x", 200))
		switch code := probeMethod(srv, "198.51.100.7:4000", method); code {
		case http.StatusNotFound, http.StatusTooManyRequests:
		default:
			t.Fatalf("request %d for an unserved method: HTTP %d", i, code)
		}
	}

	// The unknown label itself may add a handful of series (one per outcome,
	// status or scope); what must not happen is one set per name.
	const allowedGrowth = 6
	for _, name := range families {
		if growth := count(name) - before[name]; growth > allowedGrowth {
			t.Errorf("%s grew by %d series for %d distinct unserved method names", name, growth, names)
		}
	}
	for _, name := range families[:4] {
		for _, labels := range metricSeries(t, name) {
			if strings.HasPrefix(labels["module"], "labelprobe") || strings.HasPrefix(labels["method"], "labelprobe") {
				t.Fatalf("%s has a series labelled with a caller-chosen method name: %v", name, labels)
			}
		}
	}
}

// TestServedMethodKeepsItsMetricLabels checks the labels of a served method
// are what they were: module and action of its own name.
func TestServedMethodKeepsItsMetricLabels(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{})
	if code := probeMethod(srv, "198.51.100.8:4000", "nhb_getNetworkStats"); code != http.StatusOK {
		t.Fatalf("nhb_getNetworkStats: HTTP %d", code)
	}
	if !hasSeries(metricSeries(t, "nhb_module_requests_total"), map[string]string{"module": "nhb", "method": "getNetworkStats", "outcome": "success"}) {
		t.Fatalf("expected a nhb/getNetworkStats success series after a served call")
	}
}

// TestRotatingUnknownMethodNamesShareOneBucket checks that changing the method
// name does not give a caller a new rate limit bucket, and that the shared
// bucket of unserved names does not hold back a served method.
func TestRotatingUnknownMethodNamesShareOneBucket(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{MaxTxPerWindow: 2, MaxTxPerIP: 2})
	remote := "198.51.100.9:4000"

	// The limit is per source and method, so a served method is limited at 2.
	for i := 0; i < 2; i++ {
		if code := probeMethod(srv, remote, "nhb_getNetworkStats"); code != http.StatusOK {
			t.Fatalf("served method request %d: HTTP %d", i, code)
		}
	}
	if code := probeMethod(srv, remote, "nhb_getNetworkStats"); code != http.StatusTooManyRequests {
		t.Fatalf("served method over its limit: HTTP %d, want 429", code)
	}

	// Changing the name does not start a new bucket.
	limited := 0
	for i := 0; i < 20; i++ {
		switch code := probeMethod(srv, remote, fmt.Sprintf("rotating%d_call", i)); code {
		case http.StatusNotFound:
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unserved name %d: HTTP %d", i, code)
		}
	}
	if limited != 18 {
		t.Fatalf("expected 18 of 20 rotating unserved names to be limited together, got %d", limited)
	}

	// Another source has its own shared bucket.
	if code := probeMethod(srv, "198.51.100.10:4000", "rotating0_call"); code != http.StatusNotFound {
		t.Fatalf("another source's first unserved name: HTTP %d, want 404", code)
	}
}

// TestUnknownMethodFloodKeepsLimiterEntriesBounded checks that a flood of
// distinct unserved names neither fills the limiter table nor pushes a served
// method's exhausted bucket out of it, which would hand the caller a fresh
// burst.
func TestUnknownMethodFloodKeepsLimiterEntriesBounded(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{MaxTxPerWindow: 2, MaxTxPerIP: 2})
	source := "203.0.113.9"
	served := "nhb_getBalance"
	start := time.Now()

	for i := 0; i < 2; i++ {
		if !srv.allowSource(source, "", "", served, start) {
			t.Fatalf("request %d for a served method should be allowed", i)
		}
	}
	if srv.allowSource(source, "", "", served, start) {
		t.Fatalf("the served method should be limited after its burst")
	}

	for i := 0; i < rateLimiterMaxEntries+8; i++ {
		at := start.Add(time.Duration(i+1) * time.Millisecond)
		srv.allowSource(source, "", "", fmt.Sprintf("flood%d_call", i), at)
	}

	if srv.allowSource(source, "", "", served, start.Add(time.Second)) {
		t.Errorf("the flood gave the caller a fresh burst on a served method")
	}
	srv.mu.Lock()
	entries := len(srv.rateLimiters)
	srv.mu.Unlock()
	if entries > 2 {
		t.Errorf("a flood of distinct unserved names left %d limiter entries, want at most 2", entries)
	}
}

// TestConfiguredRouteLimitStillAppliesToItsName checks that a name an operator
// gave a route limit keeps its own bucket and limit even when the server does
// not serve it, and that it does not use up the shared bucket.
func TestConfiguredRouteLimitStillAppliesToItsName(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{
		MaxTxPerWindow: 5,
		MaxTxPerIP:     5,
		RouteRateLimits: map[string]RouteRateLimitConfig{
			"custom_route": {MaxTxPerWindow: 1, MaxTxPerIP: 1},
		},
	})
	now := time.Now()
	source := "203.0.113.20"

	if !srv.allowSource(source, "", "", "custom_route", now) {
		t.Fatalf("first request on the configured route should be allowed")
	}
	if srv.allowSource(source, "", "", "custom_route", now) {
		t.Fatalf("second request on the configured route should be limited at its own limit of 1")
	}
	for i := 0; i < 5; i++ {
		if !srv.allowSource(source, "", "", fmt.Sprintf("other%d_call", i), now) {
			t.Fatalf("unserved name %d should still have the default limit of 5 to itself", i)
		}
	}
	if srv.allowSource(source, "", "", "other5_call", now) {
		t.Fatalf("unserved names should share one bucket with the default limit of 5")
	}
}
