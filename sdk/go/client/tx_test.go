package client

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

type rpcCall struct {
	Method string
	Body   string
	Header http.Header
}

func TestSendZNHBTransfer(t *testing.T) {
	privKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	calls := make([]rpcCall, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Helper()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		calls = append(calls, rpcCall{Method: req.Method, Body: string(body), Header: r.Header.Clone()})
		switch req.Method {
		case "nhb_getBalance":
			w.Header().Set("Content-Type", "application/json")
			io := `{"result":{"nonce":7}}`
			if _, err := w.Write([]byte(io)); err != nil {
				t.Fatalf("write response: %v", err)
			}
		case "nhb_sendTransaction":
			var tx types.Transaction
			if len(req.Params) == 0 {
				t.Fatalf("expected transaction parameter")
			}
			if err := json.Unmarshal(req.Params[0], &tx); err != nil {
				t.Fatalf("decode transaction: %v", err)
			}
			if got, want := tx.Type, types.TxTypeTransferZNHB; got != want {
				t.Fatalf("unexpected tx type: got %d want %d", got, want)
			}
			if got := tx.Nonce; got != 7 {
				t.Fatalf("unexpected nonce: got %d", got)
			}
			if tx.GasLimit == 0 {
				t.Fatalf("gas limit must be set")
			}
			if tx.GasPrice == nil || tx.GasPrice.Sign() != 1 {
				t.Fatalf("gas price must be positive")
			}
			if tx.R == nil || tx.S == nil || tx.V == nil {
				t.Fatalf("signature missing")
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer secret" {
				t.Fatalf("unexpected authorization header: %q", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(`{"result":"Transaction received by node."}`)); err != nil {
				t.Fatalf("write response: %v", err)
			}
		default:
			t.Fatalf("unexpected RPC method %q", req.Method)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, WithAuthToken("secret"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	recipient := recipientKey.PubKey().Address().String()
	amount := big.NewInt(1_000_000_000_000_000_000)

	tx, result, err := client.SendZNHBTransfer(context.Background(), privKey, recipient, amount)
	if err != nil {
		t.Fatalf("send znhb transfer: %v", err)
	}
	if result != "Transaction received by node." {
		t.Fatalf("unexpected result: %q", result)
	}
	if tx == nil {
		t.Fatalf("expected transaction")
	}
	if tx.Value == nil || tx.Value.Cmp(amount) != 0 {
		t.Fatalf("unexpected amount: %s", tx.Value)
	}
	if len(calls) != 2 {
		t.Fatalf("expected two RPC calls, got %d", len(calls))
	}
}

func TestSendZNHBTransferOptions(t *testing.T) {
	privKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	var submitted types.Transaction
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req.Method {
		case "nhb_getBalance":
			_, _ = w.Write([]byte(`{"result":{"nonce":11}}`))
		case "nhb_sendTransaction":
			if err := json.Unmarshal(req.Params[0], &submitted); err != nil {
				t.Fatalf("decode transaction: %v", err)
			}
			_, _ = w.Write([]byte(`{"result":"Transaction received by node."}`))
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, WithAuthToken("secret"), WithGasLimit(30_000))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	customPrice := big.NewInt(5)
	amount := big.NewInt(12345)
	_, _, err = client.SendZNHBTransfer(
		context.Background(),
		privKey,
		recipientKey.PubKey().Address().String(),
		amount,
		TxWithGasLimit(50_000),
		TxWithGasPrice(customPrice),
	)
	if err != nil {
		t.Fatalf("send transfer: %v", err)
	}
	if submitted.GasLimit != 50_000 {
		t.Fatalf("expected gas limit override, got %d", submitted.GasLimit)
	}
	if submitted.GasPrice == nil || submitted.GasPrice.Cmp(customPrice) != 0 {
		t.Fatalf("expected gas price override, got %s", submitted.GasPrice)
	}
}

func TestSendZNHBTransferValidation(t *testing.T) {
	client, err := New("http://example.com", WithAuthToken("token"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, _, err = client.SendZNHBTransfer(context.Background(), nil, "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqe0yn2u", big.NewInt(1))
	if err == nil {
		t.Fatalf("expected error for missing key")
	}
	key, _ := crypto.GeneratePrivateKey()
	_, _, err = client.SendZNHBTransfer(context.Background(), key, "invalid", big.NewInt(1))
	if err == nil {
		t.Fatalf("expected decode error")
	}
	_, _, err = client.SendZNHBTransfer(context.Background(), key, key.PubKey().Address().String(), big.NewInt(-1))
	if err == nil {
		t.Fatalf("expected amount validation error")
	}
}
