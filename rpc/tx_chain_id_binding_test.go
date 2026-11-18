package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-29 on the RPC surface: nhb_getNetworkStats names the chain id a transaction must carry
// now, and the submit pre-check applies the same rule as the node.

func signedTransferWithChainID(t *testing.T, chainID *big.Int) json.RawMessage {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  chainID,
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       make([]byte, 20),
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(10),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	param, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return param
}

func submitErrorMessage(t *testing.T, env *testEnv, chainID *big.Int) string {
	t.Helper()
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{signedTransferWithChainID(t, chainID)}}
	rec := httptest.NewRecorder()
	env.server.handleSendTransaction(rec, httptest.NewRequest(http.MethodPost, "/", nil), req)
	var resp RPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil {
		return ""
	}
	return resp.Error.Message
}

func TestHandleSendTransactionChainIDFollowsTheNetworkSetting(t *testing.T) {
	env := newTestEnv(t)
	network := env.node.ChainID()
	if network == 0 {
		t.Skip("the test node has no chain id")
	}
	const mismatch = "transaction chainId does not match"
	legacy := types.NHBChainID()
	networkID := new(big.Int).SetUint64(network)

	// Off: the legacy constant passes the pre-check, the network id does not.
	if msg := submitErrorMessage(t, env, legacy); strings.Contains(msg, mismatch) {
		t.Fatalf("setting off: the legacy constant was refused by the pre-check: %q", msg)
	}
	if msg := submitErrorMessage(t, env, networkID); !strings.Contains(msg, mismatch) {
		t.Fatalf("setting off: the network id must be refused by the pre-check, got %q", msg)
	}

	// On from the next block: the other way round.
	env.node.SetTxNetworkChainIDActivationHeight(1)
	if msg := submitErrorMessage(t, env, legacy); !strings.Contains(msg, mismatch) {
		t.Fatalf("setting on: the legacy constant must be refused by the pre-check, got %q", msg)
	}
	if msg := submitErrorMessage(t, env, networkID); strings.Contains(msg, mismatch) {
		t.Fatalf("setting on: the network id was refused by the pre-check: %q", msg)
	}
}

func TestNetworkStatsNameTheChainIDATransactionMustCarry(t *testing.T) {
	env := newTestEnv(t)
	network := env.node.ChainID()

	read := func() string {
		rec := httptest.NewRecorder()
		env.server.handleGetNetworkStats(rec, env.newRequest(), &RPCRequest{ID: 1})
		result, rpcErr := decodeRPCResponse(t, rec)
		if rpcErr != nil {
			t.Fatalf("nhb_getNetworkStats: %+v", rpcErr)
		}
		var out struct {
			TxChainID string `json:"txChainId"`
			Active    *int   `json:"activeValidators"`
		}
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatalf("decode nhb_getNetworkStats: %v", err)
		}
		if out.Active == nil {
			t.Fatalf("the fields nhb_getNetworkStats always had are gone: %s", result)
		}
		return out.TxChainID
	}

	if got := read(); got != types.NHBChainID().String() {
		t.Fatalf("setting off: txChainId %q, want the legacy constant %q", got, types.NHBChainID().String())
	}
	env.node.SetTxNetworkChainIDActivationHeight(1)
	if got := read(); got != new(big.Int).SetUint64(network).String() {
		t.Fatalf("setting on: txChainId %q, want the network id %d", got, network)
	}
}
