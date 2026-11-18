package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
)

// A node as the SDK sees it: it answers the three calls a transfer makes, serves a configurable
// nhb_getNetworkStats answer, and records what it was asked.
type chainIDNode struct {
	*httptest.Server
	mu        sync.Mutex
	stats     func(w http.ResponseWriter) // nil: serve txChainId = id
	id        string
	methods   []string
	submitted []types.Transaction
}

func newChainIDNode(t *testing.T, id string) *chainIDNode {
	t.Helper()
	n := &chainIDNode{id: id}
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		n.methods = append(n.methods, req.Method)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "nhb_getNetworkStats":
			if n.stats != nil {
				n.stats(w)
				return
			}
			_, _ = w.Write([]byte(`{"result":{"activeValidators":2,"txChainId":"` + n.id + `"}}`))
		case "nhb_getBalance":
			_, _ = w.Write([]byte(`{"result":{"nonce":3}}`))
		case "nhb_sendTransaction":
			var tx types.Transaction
			if err := json.Unmarshal(req.Params[0], &tx); err != nil {
				t.Errorf("decode transaction: %v", err)
			}
			n.submitted = append(n.submitted, tx)
			_, _ = w.Write([]byte(`{"result":"Transaction received by node."}`))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	t.Cleanup(n.Close)
	return n
}

func (n *chainIDNode) count(method string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, m := range n.methods {
		if m == method {
			c++
		}
	}
	return c
}

func (n *chainIDNode) setID(id string) {
	n.mu.Lock()
	n.id = id
	n.mu.Unlock()
}

func newKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	k, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTransfersCarryTheChainIDTheNodeReports(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"the network's own id (after the activation height)", "18346390202490284624"},
		{"the shared constant (before it)", "5130306"},
	} {
		node := newChainIDNode(t, tc.id)
		key, to := newKey(t), newKey(t)
		c, err := New(node.URL, WithAuthToken("secret"))
		if err != nil {
			t.Fatal(err)
		}
		for name, send := range map[string]func() (*types.Transaction, string, error){
			"NHB": func() (*types.Transaction, string, error) {
				return c.SendNHBTransfer(context.Background(), key, to.PubKey().Address().String(), big.NewInt(5))
			},
			"ZNHB": func() (*types.Transaction, string, error) {
				return c.SendZNHBTransfer(context.Background(), key, to.PubKey().Address().String(), big.NewInt(5))
			},
		} {
			tx, _, err := send()
			if err != nil {
				t.Fatalf("%s/%s: %v", tc.name, name, err)
			}
			if tx.ChainID.String() != tc.id {
				t.Fatalf("%s/%s: signed with %s, the node reported %s", tc.name, name, tx.ChainID, tc.id)
			}
			// What the node received is the same, and its signature verifies against that id: the id is inside the signed hash.
			got := node.submitted[len(node.submitted)-1]
			if got.ChainID.String() != tc.id {
				t.Fatalf("%s/%s: the node received %s", tc.name, name, got.ChainID)
			}
			tampered := got // a copy that has not recovered its sender yet (From keeps its answer)
			tampered.ChainID = big.NewInt(1)
			from, err := got.From()
			if err != nil || string(from) != string(key.PubKey().Address().Bytes()) {
				t.Fatalf("%s/%s: the signature does not recover the sender: %v", tc.name, name, err)
			}
			if other, err := tampered.From(); err == nil && string(other) == string(key.PubKey().Address().Bytes()) {
				t.Fatalf("%s/%s: the signature still verifies for another chain id", tc.name, name)
			}
		}
	}
}

func TestATransferIsNeverSignedWhenTheNodeCannotGiveAnID(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter){
		"an HTTP error": func(w http.ResponseWriter) { http.Error(w, "down", http.StatusBadGateway) },
		"a JSON-RPC error": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"error":{"code":-32601,"message":"method not found"}}`))
		},
		"a node with no id":  func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"activeValidators":2}}`)) },
		"an empty result":    func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":null}`)) },
		"zero":               func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"txChainId":"0"}}`)) },
		"not a number":       func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"txChainId":"NHB"}}`)) },
		"a hex constant":     func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"txChainId":"0x4e4842"}}`)) },
		"a negative number":  func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"txChainId":"-1"}}`)) },
		"more than a uint64": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"result":{"txChainId":"18446744073709551616"}}`)) },
	}
	for name, stats := range cases {
		node := newChainIDNode(t, "1")
		node.stats = stats
		c, err := New(node.URL, WithAuthToken("secret"))
		if err != nil {
			t.Fatal(err)
		}
		to := newKey(t).PubKey().Address().String()
		for _, send := range []func() error{
			func() error {
				_, _, err := c.SendNHBTransfer(context.Background(), newKey(t), to, big.NewInt(1))
				return err
			},
			func() error {
				_, _, err := c.SendZNHBTransfer(context.Background(), newKey(t), to, big.NewInt(1))
				return err
			},
		} {
			if err := send(); err == nil {
				t.Fatalf("%s: a transfer was signed and sent", name)
			}
		}
		if n := node.count("nhb_sendTransaction"); n != 0 {
			t.Fatalf("%s: %d transactions reached the node", name, n)
		}
		if _, err := c.TxChainID(context.Background()); err == nil {
			t.Fatalf("%s: TxChainID returned an id", name)
		}
	}
}

func TestAnUnreachableNodeGivesNoSignedTransfer(t *testing.T) {
	node := newChainIDNode(t, "1")
	url := node.URL
	node.Close()
	c, err := New(url)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.SendNHBTransfer(context.Background(), newKey(t), newKey(t).PubKey().Address().String(), big.NewInt(1)); err == nil {
		t.Fatal("a transfer was built without a node to ask")
	}
}

func TestNewClientCarriesNoChainIDConstant(t *testing.T) {
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if c.chainID != nil {
		t.Fatalf("a new client holds the id %v before anyone chose or read one", c.chainID)
	}
	// A nil override is not a way to get the constant back.
	c2, _ := New("http://127.0.0.1:1", WithChainID(nil))
	if c2.chainID != nil {
		t.Fatalf("WithChainID(nil) installed %v", c2.chainID)
	}
	src, err := os.ReadFile("tx.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "NHBChainID()") {
		t.Fatal("tx.go still refers to the shared constant: a signer must read the id from the node (PL-R5T-29)")
	}
}

func TestAnExplicitChainIDIsUsedAndTheNodeIsNotAsked(t *testing.T) {
	node := newChainIDNode(t, "18346390202490284624")
	c, err := New(node.URL, WithAuthToken("secret"), WithChainID(big.NewInt(777)))
	if err != nil {
		t.Fatal(err)
	}
	tx, _, err := c.SendNHBTransfer(context.Background(), newKey(t), newKey(t).PubKey().Address().String(), big.NewInt(1))
	if err != nil {
		t.Fatal(err)
	}
	if tx.ChainID.Int64() != 777 {
		t.Fatalf("an explicit id was ignored: %s", tx.ChainID)
	}
	if node.count("nhb_getNetworkStats") != 0 {
		t.Fatal("the node was asked although the caller chose the id")
	}
	// The option copies its argument.
	shared := big.NewInt(5)
	c2, _ := New(node.URL, WithChainID(shared))
	shared.SetInt64(6)
	if got, _ := c2.TxChainID(context.Background()); got.Int64() != 5 {
		t.Fatalf("the option kept the caller's value by reference: %v", got)
	}
}

func TestTheNodeIsAskedOnceForManyTransfersAndAgainAfterTheExpiry(t *testing.T) {
	node := newChainIDNode(t, "5130306")
	c, err := New(node.URL, WithAuthToken("secret"), WithChainIDExpiry(150*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	key, to := newKey(t), newKey(t).PubKey().Address().String()
	for i := 0; i < 4; i++ {
		if _, _, err := c.SendNHBTransfer(context.Background(), key, to, big.NewInt(1)); err != nil {
			t.Fatal(err)
		}
	}
	if n := node.count("nhb_getNetworkStats"); n != 1 {
		t.Fatalf("4 transfers made %d stats calls, want 1", n)
	}
	// The activation height passes: the node now reports the network's own id.
	node.setID("18346390202490284624")
	deadline := time.Now().Add(5 * time.Second)
	for {
		tx, _, err := c.SendNHBTransfer(context.Background(), key, to, big.NewInt(1))
		if err != nil {
			t.Fatal(err)
		}
		if tx.ChainID.String() == "18346390202490284624" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a long-lived client kept signing with the old id after the node changed it")
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestTxChainIDIsExportedAndUsesTheDefaultExpiry(t *testing.T) {
	node := newChainIDNode(t, "42")
	c, err := New(node.URL)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.TxChainID(context.Background())
	if err != nil || id.Int64() != 42 {
		t.Fatalf("%v %v", id, err)
	}
	id.SetInt64(1)
	again, _ := c.TxChainID(context.Background())
	if again.Int64() != 42 {
		t.Fatalf("a returned copy changed the kept value: %v", again)
	}
	if node.count("nhb_getNetworkStats") != 1 {
		t.Fatalf("stats calls = %d", node.count("nhb_getNetworkStats"))
	}
	if _, err := (&Client{}).TxChainID(context.Background()); !errors.Is(err, txchainid.ErrUnavailable) {
		t.Fatalf("a client with no reader must refuse: %v", err)
	}
}

func TestTheStatsCallIsPublicAndSendsNoToken(t *testing.T) {
	var auth string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"result":{"txChainId":"9"}}`))
	}))
	defer node.Close()
	c, err := New(node.URL, WithAuthToken("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.TxChainID(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Fatalf("the public stats call carried the bearer token %q", auth)
	}
}

func TestANilClientRefusesInsteadOfPanicking(t *testing.T) {
	var c *Client
	if id, err := c.TxChainID(context.Background()); err == nil || id != nil || !errors.Is(err, txchainid.ErrUnavailable) {
		t.Fatalf("a nil client gave %v %v", id, err)
	}
	fresh, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.chainIDs.Expiry() != txchainid.DefaultTTL {
		t.Fatalf("a client built with no option must use the default expiry, got %v", fresh.chainIDs.Expiry())
	}
}
