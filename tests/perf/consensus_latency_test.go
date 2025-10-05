package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"nhbchain/tests/support/cluster"
)

const (
	targetTransactionsPerMinute = 1000
	consensusJSONReport         = "consensus_latency_report.json"
	consensusTextReport         = "consensus_latency_report.txt"
)

type benchmarkProposalResponse struct {
	Proposal struct {
		ID       int    `json:"id"`
		Status   string `json:"status"`
		YesVotes int    `json:"yes_votes"`
	} `json:"proposal"`
}

type benchmarkApplyResponse struct {
	ProposalID int   `json:"proposal_id"`
	Height     int64 `json:"height"`
}

type benchmarkConsensusSnapshot struct {
	Height    int   `json:"height"`
	Proposals []int `json:"proposals"`
}

type consensusLatencyMetrics struct {
	Benchmark             string    `json:"benchmark"`
	Timestamp             time.Time `json:"timestamp"`
	Transactions          int       `json:"transactions"`
	TargetRatePerMinute   int       `json:"target_rate_per_minute"`
	AchievedRatePerMinute float64   `json:"achieved_rate_per_minute"`
	TotalDurationSeconds  float64   `json:"total_duration_seconds"`
	P50Millis             float64   `json:"p50_ms"`
	P95Millis             float64   `json:"p95_ms"`
	P99Millis             float64   `json:"p99_ms"`
	AvgMillis             float64   `json:"avg_ms"`
	MaxMillis             float64   `json:"max_ms"`
}

func BenchmarkConsensusFinalityLatency(b *testing.B) {
	if testing.Short() {
		b.Skip("consensus benchmark requires full environment")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cl, err := cluster.New(ctx)
	if err != nil {
		b.Fatalf("start cluster: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cl.Stop(shutdownCtx); err != nil {
			b.Fatalf("shutdown cluster: %v", err)
		}
	}()

	client := &http.Client{Timeout: 3 * time.Second}
	base := cl.GatewayURL()
	if base == "" {
		b.Fatal("gateway url missing")
	}

	if b.N == 0 {
		b.Fatalf("benchmark requires iterations")
	}

	interval := time.Minute / targetTransactionsPerMinute
	latencies := make([]time.Duration, 0, b.N)
	runID := time.Now().UnixNano()

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		targetStart := start.Add(time.Duration(i) * interval)
		if wait := time.Until(targetStart); wait > 0 {
			time.Sleep(wait)
		}

		txStart := time.Now()
		latency := executeConsensusTransaction(b, client, base, runID, i, txStart)
		latencies = append(latencies, latency)
	}
	b.StopTimer()

	totalDuration := time.Since(start)
	metrics := computeConsensusMetrics(latencies, totalDuration)
	metrics.Benchmark = "BenchmarkConsensusFinalityLatency"
	metrics.TargetRatePerMinute = targetTransactionsPerMinute

	if err := writeConsensusReports(metrics); err != nil {
		b.Fatalf("write consensus reports: %v", err)
	}

	b.Logf("consensus finality p95: %.2fms avg: %.2fms achieved_rate: %.1f tx/min (n=%d)", metrics.P95Millis, metrics.AvgMillis, metrics.AchievedRatePerMinute, metrics.Transactions)
}

func executeConsensusTransaction(tb testing.TB, client *http.Client, base string, runID int64, idx int, started time.Time) time.Duration {
	tb.Helper()

	requestPrefix := fmt.Sprintf("perf-%d-%d", runID, idx)
	proposalPayload := map[string]any{
		"title":       fmt.Sprintf("perf-proposal-%d", idx),
		"description": "benchmark governance proposal",
		"request_id":  requestPrefix + "-proposal",
	}
	var proposal benchmarkProposalResponse
	doPost(tb, client, fmt.Sprintf("%s/v1/gov/proposals", base), proposalPayload, &proposal)
	proposalID := proposal.Proposal.ID
	if proposalID == 0 {
		tb.Fatalf("proposal id missing for tx %d", idx)
	}

	votePayload := map[string]any{
		"voter":      "validator-1",
		"option":     "yes",
		"request_id": requestPrefix + "-vote",
	}
	doPost(tb, client, fmt.Sprintf("%s/v1/gov/proposals/%d/vote", base, proposalID), votePayload, &proposal)

	applyPayload := map[string]any{"request_id": requestPrefix + "-apply"}
	var applied benchmarkApplyResponse
	doPost(tb, client, fmt.Sprintf("%s/v1/gov/proposals/%d/apply", base, proposalID), applyPayload, &applied)

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := queryConsensus(tb, client, base)
		if containsProposal(snapshot.Proposals, proposalID) {
			break
		}
		if time.Now().After(deadline) {
			tb.Fatalf("proposal %d not finalized within deadline", proposalID)
		}
		time.Sleep(25 * time.Millisecond)
	}

	return time.Since(started)
}

func doPost(tb testing.TB, client *http.Client, url string, payload map[string]any, out any) {
	tb.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		tb.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		tb.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		tb.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		tb.Fatalf("post %s status %d: %s", url, resp.StatusCode, string(data))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			tb.Fatalf("decode response from %s: %v", url, err)
		}
	}
}

func queryConsensus(tb testing.TB, client *http.Client, base string) benchmarkConsensusSnapshot {
	tb.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/consensus/applied", base), nil)
	if err != nil {
		tb.Fatalf("consensus request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		tb.Fatalf("get consensus snapshot: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		tb.Fatalf("consensus status %d: %s", resp.StatusCode, string(data))
	}
	var snapshot benchmarkConsensusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		tb.Fatalf("decode consensus snapshot: %v", err)
	}
	return snapshot
}

func containsProposal(proposals []int, id int) bool {
	for _, proposalID := range proposals {
		if proposalID == id {
			return true
		}
	}
	return false
}

func computeConsensusMetrics(latencies []time.Duration, total time.Duration) consensusLatencyMetrics {
	metrics := consensusLatencyMetrics{
		Timestamp:            time.Now().UTC(),
		Transactions:         len(latencies),
		TotalDurationSeconds: total.Seconds(),
	}
	if len(latencies) == 0 {
		return metrics
	}

	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	metrics.P50Millis = durationToMillis(percentile(sorted, 0.50))
	metrics.P95Millis = durationToMillis(percentile(sorted, 0.95))
	metrics.P99Millis = durationToMillis(percentile(sorted, 0.99))

	var totalLatency time.Duration
	var maxLatency time.Duration
	for _, latency := range latencies {
		totalLatency += latency
		if latency > maxLatency {
			maxLatency = latency
		}
	}
	metrics.AvgMillis = durationToMillis(time.Duration(int64(totalLatency) / int64(len(latencies))))
	metrics.MaxMillis = durationToMillis(maxLatency)

	if total.Minutes() > 0 {
		metrics.AchievedRatePerMinute = float64(len(latencies)) / total.Minutes()
	}

	return metrics
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := p * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	lowerVal := float64(sorted[lower])
	upperVal := float64(sorted[upper])
	return time.Duration(lowerVal + frac*(upperVal-lowerVal))
}

func durationToMillis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

func writeConsensusReports(metrics consensusLatencyMetrics) error {
	jsonData, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(consensusJSONReport, jsonData, 0o644); err != nil {
		return err
	}

	summary := fmt.Sprintf(
		"Benchmark: %s\nTimestamp: %s\nTransactions: %d\nTarget rate: %d tx/min\nAchieved rate: %.1f tx/min\nTotal duration: %.2fs\nP50 finality latency: %.2f ms\nP95 finality latency: %.2f ms\nP99 finality latency: %.2f ms\nAverage finality latency: %.2f ms\nMax finality latency: %.2f ms\n",
		metrics.Benchmark,
		metrics.Timestamp.Format(time.RFC3339),
		metrics.Transactions,
		metrics.TargetRatePerMinute,
		metrics.AchievedRatePerMinute,
		metrics.TotalDurationSeconds,
		metrics.P50Millis,
		metrics.P95Millis,
		metrics.P99Millis,
		metrics.AvgMillis,
		metrics.MaxMillis,
	)
	if err := os.WriteFile(consensusTextReport, []byte(summary), 0o644); err != nil {
		return err
	}

	return nil
}
