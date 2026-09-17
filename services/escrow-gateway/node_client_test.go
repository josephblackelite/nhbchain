package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
)

// fakeRPCServer captures the last nhb_sendTransaction request it received
// (decoded and re-verified) and answers nhb_getBalance with a fixed nonce,
// standing in for the real chain in these node_client tests.
type fakeRPCServer struct {
	*httptest.Server
	lastMethod string
	lastTx     *types.Transaction
	nonce      uint64
	txHash     string
	sendErr    *jsonRPCErrorObj
}

func newFakeRPCServer(t *testing.T) *fakeRPCServer {
	t.Helper()
	f := &fakeRPCServer{txHash: "0xdeadbeef"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decode Params as raw JSON, not interface{} -- a generic
		// interface{} decode turns large integers (ChainID, GasPrice) into
		// float64 and silently loses precision, exactly the class of bug
		// rpc/http.go's handleSendTransaction's own doc comment calls out
		// ("Javascript proxies... will lose precision if 256-bit integers
		// are parsed as unquoted JSON Numbers"). Decoding straight into
		// json.RawMessage avoids that round trip so this fake server
		// reconstructs *types.Transaction faithfully.
		var req struct {
			JSONRPC string            `json:"jsonrpc"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
			ID      int64             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc request: %v", err)
		}
		f.lastMethod = req.Method
		resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "nhb_getBalance":
			resp.Result, _ = json.Marshal(map[string]interface{}{"nonce": f.nonce})
		case "nhb_sendTransaction":
			if f.sendErr != nil {
				resp.Error = f.sendErr
				break
			}
			if len(req.Params) != 1 {
				t.Fatalf("unexpected nhb_sendTransaction params: %+v", req.Params)
			}
			var tx types.Transaction
			if err := json.Unmarshal(req.Params[0], &tx); err != nil {
				t.Fatalf("decode submitted tx: %v", err)
			}
			f.lastTx = &tx
			resp.Result, _ = json.Marshal(f.txHash)
		default:
			t.Fatalf("unexpected rpc method: %s", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return f
}

func mustGenerateEscrowGatewayKey(t *testing.T) *nhbcrypto.PrivateKey {
	t.Helper()
	key, err := nhbcrypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestInitRelayerFetchesNonceAndConfigures(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 7
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)

	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}
	if client.RelayerAddress() != key.PubKey().Address().String() {
		t.Fatalf("unexpected relayer address: %s", client.RelayerAddress())
	}
	if client.relayerNonce != 7 {
		t.Fatalf("expected cached nonce 7, got %d", client.relayerNonce)
	}
}

func TestMutatingCallsFailClosedWithoutRelayer(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")

	payload := []byte(`{"escrowId":"0x` + fixedHex64() + `","action":"release"}`)
	if _, err := client.EscrowCreate(context.Background(), []byte(`{"payer":"nhb1x","payee":"nhb1y","nonce":1}`), []byte("sig")); err == nil {
		t.Fatalf("expected create to fail without a relayer key")
	}
	if err := client.EscrowRelease(context.Background(), payload, []byte("sig")); err != ErrRelayerNotConfigured {
		t.Fatalf("expected ErrRelayerNotConfigured, got %v", err)
	}
}

func TestEscrowReleaseSubmitsDelegatedTransaction(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 3
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	escrowID := "0x" + fixedHex64()
	payload := []byte(`{"escrowId":"` + escrowID + `","action":"release"}`)
	signature := []byte("participant-signature-placeholder-000000000000000000000000000000")

	if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
		t.Fatalf("escrow release: %v", err)
	}
	if fake.lastMethod != "nhb_sendTransaction" {
		t.Fatalf("expected nhb_sendTransaction, last method was %s", fake.lastMethod)
	}
	if fake.lastTx == nil {
		t.Fatalf("expected a submitted transaction")
	}
	if fake.lastTx.Type != types.TxTypeDelegatedReleaseEscrow {
		t.Fatalf("unexpected tx type: %v", fake.lastTx.Type)
	}
	if fake.lastTx.Nonce != 3 {
		t.Fatalf("expected relayer nonce 3, got %d", fake.lastTx.Nonce)
	}
	var decoded delegatedEscrowActionPayload
	if err := rlp.DecodeBytes(fake.lastTx.Data, &decoded); err != nil {
		t.Fatalf("decode submitted tx data: %v", err)
	}
	if decoded.EscrowID != escrowID {
		t.Fatalf("unexpected escrowId in submitted tx: %s", decoded.EscrowID)
	}
	if string(decoded.Payload) != string(payload) {
		t.Fatalf("payload mismatch: got %s want %s", decoded.Payload, payload)
	}
	// A second call must consume the next nonce -- proving the local nonce
	// cache actually advances after a successful submission.
	if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
		t.Fatalf("second escrow release: %v", err)
	}
	if fake.lastTx.Nonce != 4 {
		t.Fatalf("expected relayer nonce to advance to 4, got %d", fake.lastTx.Nonce)
	}
}

func TestEscrowCreateComputesDeterministicEscrowID(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	payerKey := mustGenerateEscrowGatewayKey(t)
	payeeKey := mustGenerateEscrowGatewayKey(t)
	payer := payerKey.PubKey().Address()
	payee := payeeKey.PubKey().Address()

	envelope := map[string]interface{}{
		"action":   "create",
		"payer":    hex.EncodeToString(payer.Bytes()),
		"payee":    hex.EncodeToString(payee.Bytes()),
		"token":    "NHB",
		"amount":   "100",
		"feeBps":   0,
		"deadline": 9_999_999_999,
		"nonce":    42,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	resp, err := client.EscrowCreate(context.Background(), payload, []byte("sig-placeholder"))
	if err != nil {
		t.Fatalf("escrow create: %v", err)
	}
	if resp.ID == "" || len(resp.ID) != 66 {
		t.Fatalf("unexpected escrow id: %q", resp.ID)
	}
	if fake.lastTx == nil || fake.lastTx.Type != types.TxTypeDelegatedCreateEscrow {
		t.Fatalf("expected a submitted TxTypeDelegatedCreateEscrow transaction")
	}
}

func TestEscrowResolveSubmitsArbitrateReleaseTransaction(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 5
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	escrowID := "0x" + fixedHex64()
	decision := []byte(`{"escrowId":"` + escrowID + `","outcome":"release","policyNonce":1}`)
	signatures := [][]byte{[]byte("arbitrator-1-signature-placeholder-0000000000000000000000000000")}

	if err := client.EscrowResolve(context.Background(), escrowID, decision, signatures); err != nil {
		t.Fatalf("escrow resolve: %v", err)
	}
	if fake.lastTx == nil || fake.lastTx.Type != types.TxTypeArbitrateRelease {
		t.Fatalf("expected a submitted TxTypeArbitrateRelease transaction, got %+v", fake.lastTx)
	}
	if fake.lastTx.Nonce != 5 {
		t.Fatalf("expected relayer nonce 5, got %d", fake.lastTx.Nonce)
	}
	var decoded arbitrateEscrowPayload
	if err := rlp.DecodeBytes(fake.lastTx.Data, &decoded); err != nil {
		t.Fatalf("decode submitted tx data: %v", err)
	}
	if decoded.EscrowID != escrowID {
		t.Fatalf("unexpected escrowId in submitted tx: %s", decoded.EscrowID)
	}
	if string(decoded.Decision) != string(decision) {
		t.Fatalf("decision mismatch: got %s want %s", decoded.Decision, decision)
	}
	if len(decoded.Signatures) != 1 || decoded.Signatures[0] != "0x"+hex.EncodeToString(signatures[0]) {
		t.Fatalf("unexpected signatures in submitted tx: %+v", decoded.Signatures)
	}
}

func TestEscrowResolveSubmitsArbitrateRefundTransaction(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	escrowID := "0x" + fixedHex64()
	decision := []byte(`{"escrowId":"` + escrowID + `","outcome":"refund","policyNonce":1}`)
	signatures := [][]byte{[]byte("arbitrator-1-signature-placeholder-0000000000000000000000000000")}

	if err := client.EscrowResolve(context.Background(), escrowID, decision, signatures); err != nil {
		t.Fatalf("escrow resolve: %v", err)
	}
	if fake.lastTx == nil || fake.lastTx.Type != types.TxTypeArbitrateRefund {
		t.Fatalf("expected a submitted TxTypeArbitrateRefund transaction, got %+v", fake.lastTx)
	}
}

func TestEscrowResolveRejectsInvalidOutcome(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	escrowID := "0x" + fixedHex64()
	decision := []byte(`{"escrowId":"` + escrowID + `","outcome":"cancel","policyNonce":1}`)
	signatures := [][]byte{[]byte("arbitrator-1-signature-placeholder-0000000000000000000000000000")}

	if err := client.EscrowResolve(context.Background(), escrowID, decision, signatures); err == nil {
		t.Fatalf("expected an error for an unsupported decision outcome")
	}
	if fake.lastTx != nil {
		t.Fatalf("expected no transaction to be submitted for an invalid outcome")
	}
}

func TestEscrowResolveClientRequiresSignatures(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}

	escrowID := "0x" + fixedHex64()
	decision := []byte(`{"escrowId":"` + escrowID + `","outcome":"release","policyNonce":1}`)

	if err := client.EscrowResolve(context.Background(), escrowID, decision, nil); err == nil {
		t.Fatalf("expected an error when no signatures are supplied")
	}
	if fake.lastTx != nil {
		t.Fatalf("expected no transaction to be submitted with no signatures")
	}
}

func TestP2PCreateTradeAlwaysRetired(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := NewRPCNodeClient(fake.URL, "")
	if _, err := client.P2PCreateTrade(context.Background(), P2PAcceptRequest{}); err != ErrP2PTradeRetired {
		t.Fatalf("expected ErrP2PTradeRetired, got %v", err)
	}
}

func fixedHex64() string {
	return "00000000000000000000000000000000000000000000000000000000000dea"
}
