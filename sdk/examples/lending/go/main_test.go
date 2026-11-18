package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	goclient "nhbchain/sdk/go/client"
	"nhbchain/sdk/lending"
)

// The example signs lending transactions locally. The id each one carries must be the one the node reports
// (nhb_getNetworkStats), not the shared constant (ledger PL-R5T-29).

func fakeNode(t *testing.T, stats string) (*goclient.Client, *[]string) {
	t.Helper()
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &req)
		methods = append(methods, req.Method)
		switch req.Method {
		case "nhb_getNetworkStats":
			_, _ = w.Write([]byte(stats))
		case "nhb_getBalance":
			_, _ = w.Write([]byte(`{"result":{"nonce":4}}`))
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	t.Cleanup(srv.Close)
	node, err := goclient.New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return node, &methods
}

func TestEveryLendingTransactionCarriesTheNodesChainID(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	account, err := lending.SenderAddress(key)
	if err != nil {
		t.Fatal(err)
	}
	builders := map[string]txBuilder{"supply": lending.NewSupplyTx, "borrow": lending.NewBorrowTx, "repay": lending.NewRepayTx}
	for name, build := range builders {
		node, _ := fakeNode(t, `{"result":{"txChainId":"18346390202490284624"}}`)
		signed, err := signedLendingTx(context.Background(), node, key, account, "default", "100", time.Second, build)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var tx types.Transaction
		if err := json.Unmarshal([]byte(signed), &tx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tx.ChainID.String() != "18346390202490284624" {
			t.Fatalf("%s: signed with %s, the node reported 18346390202490284624", name, tx.ChainID)
		}
		if tx.Nonce != 4 {
			t.Fatalf("%s: nonce %d", name, tx.Nonce)
		}
		if from, err := tx.From(); err != nil || string(from) != string(key.PubKey().Address().Bytes()) {
			t.Fatalf("%s: the signature does not recover the sender: %v", name, err)
		}
	}
}

func TestNothingIsSignedWhenTheNodeGivesNoChainID(t *testing.T) {
	key, _ := crypto.GeneratePrivateKey()
	account, _ := lending.SenderAddress(key)
	for name, stats := range map[string]string{
		"no id":    `{"result":{"activeValidators":2}}`,
		"zero":     `{"result":{"txChainId":"0"}}`,
		"an error": `{"error":{"code":-32601,"message":"method not found"}}`,
	} {
		node, _ := fakeNode(t, stats)
		signed, err := signedLendingTx(context.Background(), node, key, account, "default", "100", time.Second, lending.NewSupplyTx)
		if err == nil || signed != "" {
			t.Fatalf("%s: signed %q", name, signed)
		}
	}
}

func TestTheExampleDoesNotRefertoTheSharedConstant(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "NHBChainID()") {
		t.Fatal("main.go signs with the shared constant instead of the id the node reports (PL-R5T-29)")
	}
}
