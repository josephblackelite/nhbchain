package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
	"nhbchain/rpc/txchainid"
)

// The load generator signs every transaction itself. The chain id each one carries must be the one the node reports in
// nhb_getNetworkStats, not the shared constant (ledger PL-R5T-29), and nothing is signed when the node cannot say.

type fakeNode struct {
	*httptest.Server
	mu        sync.Mutex
	stats     string
	submitted []types.Transaction
	statsHits int
}

func newFakeNode(t *testing.T, stats string) *fakeNode {
	t.Helper()
	n := &fakeNode{stats: stats}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r) // a node serves JSON-RPC on its root only
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &req)
		n.mu.Lock()
		defer n.mu.Unlock()
		switch req.Method {
		case "nhb_getNetworkStats":
			n.statsHits++
			_, _ = w.Write([]byte(n.stats))
		case "nhb_sendTransaction":
			var tx types.Transaction
			if err := json.Unmarshal(req.Params[0], &tx); err != nil {
				t.Errorf("decode tx: %v", err)
			}
			n.submitted = append(n.submitted, tx)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	t.Cleanup(n.Close)
	return n
}

func submitOne(t *testing.T, n *fakeNode, reader *txchainid.Reader, nonce uint64) (string, error) {
	t.Helper()
	key, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(n.URL)
	return submitPOSTransaction(context.Background(), n.Client(), u, "token", key, reader, "load", nonce)
}

func readerFor(n *fakeNode, ttl time.Duration) *txchainid.Reader {
	return txchainid.NewReader(txchainid.HTTPFetcher(n.Client(), n.URL, ""), ttl)
}

func TestLoadTransactionsCarryTheNodesChainID(t *testing.T) {
	n := newFakeNode(t, `{"result":{"txChainId":"18346390202490284624"}}`)
	reader := readerFor(n, time.Minute)
	for nonce := uint64(0); nonce < 3; nonce++ {
		if _, err := submitOne(t, n, reader, nonce); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.submitted) != 3 {
		t.Fatalf("submitted %d", len(n.submitted))
	}
	for i, tx := range n.submitted {
		if tx.ChainID.String() != "18346390202490284624" {
			t.Fatalf("transaction %d carries %s, the node reported 18346390202490284624", i, tx.ChainID)
		}
	}
	if n.statsHits != 1 {
		t.Fatalf("the node was asked %d times for 3 transactions, want 1", n.statsHits)
	}
}

func TestNothingIsSignedWhenTheNodeGivesNoChainID(t *testing.T) {
	for name, stats := range map[string]string{
		"no id":    `{"result":{"activeValidators":2}}`,
		"zero":     `{"result":{"txChainId":"0"}}`,
		"an error": `{"error":{"code":-1,"message":"down"}}`,
		"junk":     `{"result":{"txChainId":"x"}}`,
	} {
		n := newFakeNode(t, stats)
		hash, err := submitOne(t, n, readerFor(n, time.Minute), 0)
		if err == nil || hash != "" {
			t.Fatalf("%s: signed and sent %q", name, hash)
		}
		if len(n.submitted) != 0 {
			t.Fatalf("%s: %d transactions reached the node", name, len(n.submitted))
		}
	}
	if _, err := submitPOSTransaction(context.Background(), http.DefaultClient, &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, "t", nil, nil, "p", 0); err == nil {
		t.Fatal("a nil reader must refuse")
	}
}

func TestALongRunPicksUpTheChainIDChangeAtTheActivationHeight(t *testing.T) {
	n := newFakeNode(t, `{"result":{"txChainId":"5130306"}}`)
	reader := readerFor(n, 100*time.Millisecond)
	if _, err := submitOne(t, n, reader, 0); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.stats = `{"result":{"txChainId":"18346390202490284624"}}`
	n.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for nonce := uint64(1); ; nonce++ {
		if _, err := submitOne(t, n, reader, nonce); err != nil {
			t.Fatal(err)
		}
		if n.submitted[len(n.submitted)-1].ChainID.String() == "18346390202490284624" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the loader kept the old chain id after the node changed it")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func TestTheLoaderDoesNotRefertoTheSharedConstant(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "NHBChainID()") {
		t.Fatal("main.go signs with the shared constant instead of the id the node reports (PL-R5T-29)")
	}
}

func TestTheLoaderDoesNotStartWithoutAChainID(t *testing.T) {
	good := newFakeNode(t, `{"result":{"txChainId":"18346390202490284624"}}`)
	u, _ := url.Parse(good.URL)
	reader, err := startChainIDReader(context.Background(), good.Client(), u)
	if err != nil || reader == nil {
		t.Fatalf("%v", err)
	}
	if id, _ := reader.ChainID(context.Background()); id.String() != "18346390202490284624" {
		t.Fatalf("the reader returned %v", id)
	}
	if reader.Expiry() != txchainid.DefaultTTL {
		t.Fatalf("a long run must follow the activation height: the reader keeps an answer for %v", reader.Expiry())
	}
	for name, stats := range map[string]string{"no id": `{"result":{}}`, "zero": `{"result":{"txChainId":"0"}}`, "an error": `{"error":{"code":-1,"message":"x"}}`} {
		bad := newFakeNode(t, stats)
		bu, _ := url.Parse(bad.URL)
		if r, err := startChainIDReader(context.Background(), bad.Client(), bu); err == nil || r != nil {
			t.Fatalf("%s: the loader started", name)
		}
	}
}
