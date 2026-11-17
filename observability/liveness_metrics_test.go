package observability

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func metricFamily(t *testing.T, name string) (found bool, samples int) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return true, len(family.GetMetric())
		}
	}
	return false, 0
}

func labelValues(t *testing.T, name string) map[string]bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	values := map[string]bool{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				values[pair.GetValue()] = true
			}
		}
	}
	return values
}

// The containment metrics carry the names the alert rules and runbook refer
// to. Renaming one silently breaks an alert, so the names are pinned.
func TestContainmentMetricNames(t *testing.T) {
	consensus := Consensus()
	mempool := Mempool()

	// Give every metric a sample so vectors have children to gather.
	consensus.SetConsecutiveBuildFailures(2)
	consensus.RecordBuildFailure("lifecycle")
	consensus.RecordEmptyBlockFallback()
	consensus.ObserveBuild(120*time.Millisecond, 2)
	consensus.SetSecondsSinceLastCommit(12)
	consensus.SetLastCommitHeight(34)
	consensus.SetLivenessStalled(true)
	consensus.RecordTxPanicRecovered()
	consensus.RecordLocalValidationFailure()
	mempool.RecordTxFailure("quarantine")
	mempool.RecordEviction("strikes")
	mempool.SetStrikeRecords(7)
	mempool.RecordStrikeOverflowEvictions(3)
	mempool.RecordInflightLeaseExpired()

	for _, name := range []string{
		"nhb_consensus_build_failures_consecutive",
		"nhb_consensus_build_failures_total",
		"nhb_consensus_empty_block_fallbacks_total",
		"nhb_consensus_build_duration_seconds",
		"nhb_consensus_build_waves",
		"nhb_consensus_seconds_since_last_commit",
		"nhb_consensus_last_commit_height",
		"nhb_consensus_liveness_stalled",
		"nhb_consensus_tx_panics_recovered_total",
		"nhb_consensus_local_validation_failures_total",
		"nhb_mempool_tx_failures_total",
		"nhb_mempool_evictions_total",
		"nhb_mempool_strike_records",
		"nhb_mempool_strike_overflow_evictions_total",
		"nhb_mempool_inflight_leases_expired_total",
	} {
		if found, _ := metricFamily(t, name); !found {
			t.Errorf("metric %s is not registered", name)
		}
	}
}

// Label values are a closed set: attacker-influenced text can never become a
// label, so metric cardinality cannot be inflated.
func TestContainmentMetricLabelsAreBounded(t *testing.T) {
	consensus := Consensus()
	mempool := Mempool()
	for _, attacker := range []string{"tx-0xdeadbeef", "some\nerror text", strings.Repeat("A", 4000), ""} {
		consensus.RecordBuildFailure(attacker)
		mempool.RecordTxFailure(attacker)
		mempool.RecordEviction(attacker)
	}
	allowed := map[string]bool{
		"lifecycle": true, "evidence": true, "state_copy": true, "budget": true, "waves": true, "infra": true, "panic": true, "other": true,
		"prune": true, "skip": true, "quarantine": true, "nondeterministic": true, "abort": true,
		"classified_prune": true, "strikes": true, "ttl": true, "isolation": true,
	}
	for _, name := range []string{"nhb_consensus_build_failures_total", "nhb_mempool_tx_failures_total", "nhb_mempool_evictions_total"} {
		for value := range labelValues(t, name) {
			if !allowed[value] {
				t.Errorf("%s has an unbounded label value %q", name, value)
			}
		}
	}
	// A nil registry (metrics disabled) must be safe to call.
	var nilConsensus *consensusMetrics
	nilConsensus.RecordBuildFailure("panic")
	nilConsensus.SetLivenessStalled(true)
	var nilMempool *MempoolMetrics
	nilMempool.RecordEviction("ttl")
}

// freeLoopbackAddr returns a loopback address that was free a moment ago.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func TestMetricsServer(t *testing.T) {
	// Disabled by default: an empty address serves nothing.
	if server, err := StartMetricsServer("", nil); server != nil || err != nil {
		t.Fatalf("an empty address serves nothing, got %v %v", server, err)
	}
	if StartMetricsServerFromEnv(nil) != nil {
		t.Fatalf("with NHB_METRICS_ADDR unset nothing is started")
	}
	// A bad address is reported to the caller, not swallowed in a goroutine.
	if _, err := StartMetricsServer("not-an-address", nil); err == nil {
		t.Fatalf("a bad address must be reported")
	}
	// ... and through the environment form it is logged and never fatal.
	t.Setenv(MetricsAddrEnv, "not-an-address")
	if StartMetricsServerFromEnv(nil) != nil {
		t.Fatalf("a bad NHB_METRICS_ADDR must not start a server")
	}

	Consensus().SetConsecutiveBuildFailures(3)
	addr := freeLoopbackAddr(t)
	server, err := StartMetricsServer(addr, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Close()

	var body string
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(data)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics endpoint did not come up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "nhb_consensus_build_failures_consecutive 3") {
		t.Fatalf("the endpoint must serve the liveness gauges; got: %.400s", body)
	}
	// Only /metrics is served.
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("get /: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /, got %d", resp.StatusCode)
	}
}
