package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"

	"nhooyr.io/websocket"
)

const (
	defaultDuration = 2 * time.Minute
	defaultRate     = 600 // transactions per minute
)

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	ID      int             `json:"id"`
}

type finalityPayload struct {
	Type      string `json:"type"`
	Cursor    string `json:"cursor"`
	IntentRef string `json:"intentRef"`
	TxHash    string `json:"txHash"`
	Status    string `json:"status"`
	Timestamp int64  `json:"ts"`
}

type latencyTracker struct {
	mu        sync.Mutex
	pending   map[string]time.Time
	latencies []time.Duration
}

func newLatencyTracker() *latencyTracker {
	return &latencyTracker{pending: make(map[string]time.Time)}
}

func (lt *latencyTracker) track(hash string, at time.Time) {
	lt.mu.Lock()
	lt.pending[strings.ToLower(hash)] = at
	lt.mu.Unlock()
}

func (lt *latencyTracker) finalize(hash string, at time.Time) {
	key := strings.ToLower(hash)
	lt.mu.Lock()
	start, ok := lt.pending[key]
	if ok {
		lt.latencies = append(lt.latencies, at.Sub(start))
		delete(lt.pending, key)
	}
	lt.mu.Unlock()
}

func (lt *latencyTracker) snapshot() (latencies []time.Duration, pending int) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	latencies = append([]time.Duration(nil), lt.latencies...)
	pending = len(lt.pending)
	return latencies, pending
}

func (lt *latencyTracker) waitForEmpty(ctx context.Context) bool {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		lt.mu.Lock()
		remaining := len(lt.pending)
		lt.mu.Unlock()
		if remaining == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func main() {
	var (
		rpcURL       string
		privateHex   string
		txRate       int
		durationFlag time.Duration
		intentPrefix string
	)
	flag.StringVar(&rpcURL, "rpc", "http://127.0.0.1:8545", "RPC endpoint for submitting transactions")
	flag.StringVar(&privateHex, "key", "", "hex-encoded secp256k1 private key for funding account (overrides POSLOADER_KEY)")
	flag.IntVar(&txRate, "rate", defaultRate, "target rate of POS-tagged transactions per minute")
	flag.DurationVar(&durationFlag, "duration", defaultDuration, "load duration")
	flag.StringVar(&intentPrefix, "intent-prefix", "pos-load", "prefix for generated intent references")
	flag.Parse()

	if privateHex == "" {
		privateHex = os.Getenv("POSLOADER_KEY")
	}
	privateHex = strings.TrimSpace(privateHex)
	if privateHex == "" {
		log.Fatal("missing private key: provide --key or POSLOADER_KEY")
	}

	keyBytes, err := hex.DecodeString(strings.TrimPrefix(privateHex, "0x"))
	if err != nil {
		log.Fatalf("decode private key: %v", err)
	}
	signer, err := nhbcrypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("load private key: %v", err)
	}

	token := strings.TrimSpace(os.Getenv("NHB_RPC_TOKEN"))
	if token == "" {
		log.Fatal("missing NHB_RPC_TOKEN for RPC authentication")
	}
	parsed, err := url.Parse(rpcURL)
	if err != nil {
		log.Fatalf("parse rpc url: %v", err)
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "http"
	}

	if txRate <= 0 {
		log.Fatalf("rate must be positive, got %d", txRate)
	}
	if durationFlag <= 0 {
		durationFlag = defaultDuration
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	httpClient := &http.Client{Timeout: 10 * time.Second}
	tracker := newLatencyTracker()

	wsURL := *parsed
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		wsURL.Scheme = "wss"
	default:
		wsURL.Scheme = "ws"
	}
	wsURL.Path = "/ws/pos/finality"
	wsURL.RawQuery = ""

	wsCtx, wsCancel := context.WithTimeout(ctx, 5*time.Second)
	conn, _, err := websocket.Dial(wsCtx, wsURL.String(), nil)
	wsCancel()
	if err != nil {
		log.Fatalf("connect finality stream: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "load complete")

	finalityCtx, finalityCancel := context.WithCancel(ctx)
	defer finalityCancel()
	go consumeFinality(finalityCtx, conn, tracker)

	interval := time.Minute / time.Duration(txRate)
	if interval <= 0 {
		interval = time.Millisecond
	}
	deadline := time.Now().Add(durationFlag)
	var nonce uint64
	var submitted int
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			log.Printf("context cancelled: %v", ctx.Err())
			return
		default:
		}
		hash, err := submitPOSTransaction(ctx, httpClient, parsed, token, signer, intentPrefix, nonce)
		if err != nil {
			log.Printf("submit tx %d failed: %v", nonce, err)
		} else {
			tracker.track(hash, time.Now())
			submitted++
		}
		nonce++
		time.Sleep(interval)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer waitCancel()
	if !tracker.waitForEmpty(waitCtx) {
		log.Printf("pending finality for %d transactions", trackerPending(tracker))
	}

	finalityCancel()

	latencies, pending := tracker.snapshot()
	reportLoadSummary(latencies, pending, submitted)
}

func submitPOSTransaction(ctx context.Context, client *http.Client, rpcURL *url.URL, token string, signer *nhbcrypto.PrivateKey, prefix string, nonce uint64) (string, error) {
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeTransfer,
		Nonce:           nonce,
		GasLimit:        53_000,
		GasPrice:        big.NewInt(1),
		IntentRef:       []byte(fmt.Sprintf("%s-%d", prefix, nonce)),
		IntentExpiry:    uint64(time.Now().Add(5 * time.Minute).Unix()),
		MerchantAddress: "pos-qos",
		DeviceID:        "loader",
		Value:           big.NewInt(0),
	}
	if err := tx.Sign(signer.PrivateKey); err != nil {
		return "", fmt.Errorf("sign tx: %w", err)
	}
	hash, err := tx.Hash()
	if err != nil {
		return "", fmt.Errorf("hash tx: %w", err)
	}

	payload := rpcRequest{
		JSONRPC: "2.0",
		Method:  "nhb_sendTransaction",
		Params:  []interface{}{tx},
		ID:      1,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL.String(), bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("rpc call: %w", err)
	}
	defer resp.Body.Close()

	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("rpc error %d: %s", decoded.Error.Code, decoded.Error.Message)
	}
	return "0x" + hex.EncodeToString(hash), nil
}

func consumeFinality(ctx context.Context, conn *websocket.Conn, tracker *latencyTracker) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var payload finalityPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			log.Printf("decode finality payload: %v", err)
			continue
		}
		if strings.EqualFold(payload.Status, "finalized") {
			tracker.finalize(payload.TxHash, time.Now())
		}
	}
}

func trackerPending(t *latencyTracker) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

func reportLoadSummary(latencies []time.Duration, pending int, submitted int) {
	var max time.Duration
	var total time.Duration
	for _, latency := range latencies {
		if latency > max {
			max = latency
		}
		total += latency
	}
	avg := time.Duration(0)
	if len(latencies) > 0 {
		avg = time.Duration(int64(total) / int64(len(latencies)))
	}
	log.Printf("POS loader submitted %d transactions", submitted)
	log.Printf("Finalized %d transactions (pending: %d)", len(latencies), pending)
	log.Printf("Latency avg=%s max=%s", avg, max)
}
