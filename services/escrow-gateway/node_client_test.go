package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// stats is what nhb_getNetworkStats answers (the raw JSON result); empty serves the network's own chain id.
	// A transaction the client signs must carry the id reported here (ledger PL-R5T-29).
	stats      string
	statsCalls int
	chainID    string
	// The fake behaves like the node's admission rule (core/node.go, addTransaction: a native transaction is admitted
	// only when its nonce EQUALS the account's current on-chain nonce) and, by default, like a block that executes
	// every admitted transaction at once, so the account nonce then moves up by one. dropNext is the number of the
	// next admitted transactions that are NOT executed (a competing refund landed first, a module pause): they are
	// accepted into the mempool and the account nonce stays where it was (ledger PL-EXT-70).
	dropNext int
	// balanceErr makes nhb_getBalance fail, so the relayer's nonce cannot be read.
	balanceErr *jsonRPCErrorObj
	// sendCalls counts nhb_sendTransaction requests that reached the fake, accepted or not.
	sendCalls int
}

// fakeNetworkChainID is the id the fake node reports by default: the network's own, not the shared constant.
const fakeNetworkChainID = "18346390202490284624"

func newFakeRPCServer(t *testing.T) *fakeRPCServer {
	t.Helper()
	f := &fakeRPCServer{txHash: "0xdeadbeef", chainID: fakeNetworkChainID}
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
		case "nhb_getNetworkStats":
			f.statsCalls++
			if f.stats != "" {
				resp.Result = json.RawMessage(f.stats)
				break
			}
			resp.Result, _ = json.Marshal(map[string]interface{}{"activeValidators": 2, "txChainId": f.chainID})
		case "nhb_getBalance":
			if f.balanceErr != nil {
				resp.Error = f.balanceErr
				break
			}
			resp.Result, _ = json.Marshal(map[string]interface{}{"nonce": f.nonce})
		case "nhb_sendTransaction":
			f.sendCalls++
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
			if tx.Nonce != f.nonce {
				resp.Error = &jsonRPCErrorObj{Code: -32000, Message: fmt.Sprintf("mempool: invalid transaction: strict nonce sequence breached: expected %d, got %d", f.nonce, tx.Nonce)}
				break
			}
			f.lastTx = &tx
			if f.dropNext > 0 {
				f.dropNext--
			} else {
				f.nonce++
			}
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
	if fake.lastTx.ChainID == nil || fake.lastTx.ChainID.String() != fakeNetworkChainID {
		t.Fatalf("the transaction must carry the id the node reports, got %v", fake.lastTx.ChainID)
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

// TestAnAdmittedButDroppedTransactionDoesNotWedgeTheRelayer is the report of ledger PL-EXT-70: the node admits a
// transaction only at the account's current on-chain nonce, so when one admitted transaction never executes (a competing
// refund or dispute landed first, a module pause) a counter kept in the gateway is one ahead of the chain for good and
// every later mutating call is refused until the process restarts. The relayer must follow the chain, not its own count.
func TestAnAdmittedButDroppedTransactionDoesNotWedgeTheRelayer(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 7
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}
	payload := []byte(`{"escrowId":"0x` + fixedHex64() + `","action":"release"}`)
	signature := []byte("participant-signature-placeholder-000000000000000000000000000000")

	// The first call is admitted and then never executes: the chain's nonce stays at 7.
	fake.dropNext = 1
	if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if fake.lastTx == nil || fake.lastTx.Nonce != 7 || fake.nonce != 7 {
		t.Fatalf("setup: the dropped transaction must carry nonce 7 and leave the chain at 7 (tx %v, chain %d)", fake.lastTx, fake.nonce)
	}

	// Before the fix the cache stood at 8 here: the next call was signed with 8, the node refused it ("expected 7, got 8")
	// and so did every call after it.
	for i := 2; i <= 4; i++ {
		if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
			t.Fatalf("release %d after the dropped transaction: %v (the relayer is wedged)", i, err)
		}
	}
	if fake.lastTx.Nonce != 9 || fake.nonce != 10 {
		t.Fatalf("the later calls must follow the chain (7, 8, 9): last nonce %d, chain %d", fake.lastTx.Nonce, fake.nonce)
	}
}

// TestRelayerRefusesToSignWhenItsChainNonceCannotBeRead pins that a failed read of the account nonce signs and sends
// nothing, and that the relayer works again as soon as the read does.
func TestRelayerRefusesToSignWhenItsChainNonceCannotBeRead(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 3
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}
	payload := []byte(`{"escrowId":"0x` + fixedHex64() + `","action":"release"}`)
	signature := []byte("participant-signature-placeholder-000000000000000000000000000000")

	fake.balanceErr = &jsonRPCErrorObj{Code: -32000, Message: "node busy"}
	err := client.EscrowRelease(context.Background(), payload, signature)
	if err == nil || !strings.Contains(err.Error(), "read relayer nonce") {
		t.Fatalf("a failed nonce read must refuse to sign, got %v", err)
	}
	if fake.sendCalls != 0 {
		t.Fatalf("a transaction was sent although the relayer's nonce could not be read (%d sends)", fake.sendCalls)
	}
	fake.balanceErr = nil
	if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
		t.Fatalf("release once the nonce can be read again: %v", err)
	}
	if fake.lastTx == nil || fake.lastTx.Nonce != 3 {
		t.Fatalf("expected the chain's nonce 3, got %v", fake.lastTx)
	}
}

// TestRelayerFollowsTheChainWhenItsNonceMovedWhileTheGatewayRan covers the other direction of the same desync: the
// account's nonce moved without this process knowing (another instance, a restart in between), so the cache is BEHIND.
func TestRelayerFollowsTheChainWhenItsNonceMovedWhileTheGatewayRan(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 3
	client := NewRPCNodeClient(fake.URL, "")
	key := mustGenerateEscrowGatewayKey(t)
	if err := client.InitRelayer(context.Background(), key); err != nil {
		t.Fatalf("init relayer: %v", err)
	}
	fake.nonce = 10
	payload := []byte(`{"escrowId":"0x` + fixedHex64() + `","action":"release"}`)
	signature := []byte("participant-signature-placeholder-000000000000000000000000000000")
	if err := client.EscrowRelease(context.Background(), payload, signature); err != nil {
		t.Fatalf("release with the chain ahead of the start value: %v", err)
	}
	if fake.lastTx == nil || fake.lastTx.Nonce != 10 || fake.nonce != 11 {
		t.Fatalf("expected a transaction at the chain's nonce 10, got %v (chain %d)", fake.lastTx, fake.nonce)
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
