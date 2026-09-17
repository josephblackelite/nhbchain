//go:build posreadiness

package posreadiness

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/tests/posreadiness/harness"
)

func TestPosQosSla(t *testing.T) {
	t.Helper()

	t.Setenv("NHB_RPC_TOKEN", mintMiniChainJWT(t))

	chain := newMiniChain(t)
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(0)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(10_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("finalize seed block: %v", err)
	}

	duration := 25 * time.Second
	loaderCtx, loaderCancel := context.WithTimeout(context.Background(), duration+45*time.Second)
	defer loaderCancel()

	rpcURL := fmt.Sprintf("http://%s", chain.RPCAddr())
	privHex := hex.EncodeToString(key.Bytes())
	rate := 3 // conservative to avoid RPC throttling

	cmd := exec.CommandContext(loaderCtx, "go", "run", "./bench/posloader",
		"--rpc", rpcURL,
		"--rate", fmt.Sprintf("%d", rate),
		"--duration", duration.String(),
		"--intent-prefix", "pos-qos",
	)
	cmd.Dir = filepath.Join("..", "..", "..")
	cmd.Env = append(os.Environ(),
		"POSLOADER_KEY="+privHex,
		"NHB_RPC_TOKEN="+os.Getenv("NHB_RPC_TOKEN"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := make(chan error, 1)
	go func() {
		runErr <- cmd.Run()
	}()

	drainCtx, drainCancel := context.WithCancel(context.Background())
	defer drainCancel()
	drainErrs := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-drainCtx.Done():
				return
			case <-ticker.C:
				if err := drainMempoolOnce(chain); err != nil {
					select {
					case drainErrs <- err:
					default:
					}
				}
			}
		}
	}()

	if err := <-runErr; err != nil {
		drainCancel()
		t.Fatalf("pos loader failed: %v\nstdout:%s\nstderr:%s", err, stdout.String(), stderr.String())
	}
	drainCancel()
	select {
	case err := <-drainErrs:
		t.Fatalf("mempool drain error: %v", err)
	default:
	}
	t.Logf("posloader stdout:\n%s", stdout.String())
	t.Logf("posloader stderr:\n%s", stderr.String())

	time.Sleep(500 * time.Millisecond)
	drainMempool(t, chain, 5*time.Second)

	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	var (
		laneFill float64
		enqueued float64
		finality *dto.Histogram
	)
	for _, family := range metrics {
		switch family.GetName() {
		case "nhb_mempool_pos_lane_fill":
			if len(family.Metric) > 0 && family.Metric[0].Gauge != nil {
				laneFill = family.Metric[0].Gauge.GetValue()
			}
		case "nhb_mempool_pos_tx_enqueued_total":
			if len(family.Metric) > 0 && family.Metric[0].Counter != nil {
				enqueued = family.Metric[0].Counter.GetValue()
			}
		case "nhb_mempool_pos_p95_finality_ms":
			if len(family.Metric) > 0 {
				finality = family.Metric[0].Histogram
			}
		}
	}

	if finality == nil {
		t.Fatalf("finality histogram not recorded")
	}
	t.Logf("lane fill %.3f, enqueued %.0f, finality samples %d", laneFill, enqueued, finality.GetSampleCount())
	if finality.GetSampleCount() == 0 {
		t.Fatalf("no finality samples captured")
	}

	p95 := histogramPercentile(finality, 0.95)
	if p95 > 5_000 {
		t.Fatalf("p95 finality exceeded SLA: %.2fms", p95)
	}

	if laneFill > 1.0 {
		t.Fatalf("pos lane saturated: %.2f", laneFill)
	}

	enqueuedCount := uint64(math.Round(enqueued))
	if enqueuedCount == 0 {
		t.Fatalf("no POS transactions enqueued")
	}
	if finality.GetSampleCount() < enqueuedCount {
		t.Fatalf("starvation detected: enqueued=%d finalized=%d", enqueuedCount, finality.GetSampleCount())
	}
}

func mintMiniChainJWT(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    "minichain",
		Audience:  jwt.ClaimStrings([]string{"pos-tests"}),
		ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Minute)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte("minichain-secret"))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

func histogramPercentile(hist *dto.Histogram, quantile float64) float64 {
	if hist == nil {
		return 0
	}
	total := hist.GetSampleCount()
	if total == 0 {
		return 0
	}
	rank := uint64(math.Ceil(float64(total) * quantile))
	if rank == 0 {
		rank = 1
	}
	var cumulative uint64
	for _, bucket := range hist.Bucket {
		cumulative = bucket.GetCumulativeCount()
		if cumulative >= rank {
			return bucket.GetUpperBound()
		}
	}
	return hist.GetSampleSum() / float64(total)
}

func newMiniChain(t *testing.T) *harness.MiniChain {
	t.Helper()
	chain, err := harness.NewMiniChain()
	if err != nil {
		t.Fatalf("new mini chain: %v", err)
	}
	t.Cleanup(func() {
		if err := chain.Close(); err != nil {
			t.Fatalf("close minichain: %v", err)
		}
	})
	return chain
}

func seedAccount(node *core.Node, key *crypto.PrivateKey, balance *big.Int) error {
	return node.WithState(func(m *nhbstate.Manager) error {
		account := &types.Account{BalanceNHB: new(big.Int).Set(balance), BalanceZNHB: big.NewInt(0)}
		return m.PutAccount(key.PubKey().Address().Bytes(), account)
	})
}

func drainMempool(t *testing.T, chain *harness.MiniChain, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		node := chain.Node()
		txs := node.GetMempool()
		if len(txs) == 0 {
			return
		}
		if _, err := chain.FinalizeTxs(txs...); err != nil {
			t.Fatalf("finalize mempool: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("mempool drain timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func drainMempoolOnce(chain *harness.MiniChain) error {
	node := chain.Node()
	txs := node.GetMempool()
	if len(txs) == 0 {
		return nil
	}
	_, err := chain.FinalizeTxs(txs...)
	return err
}
