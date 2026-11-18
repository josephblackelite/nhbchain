package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

// nhb_getNetworkStats also reports the running chain's own, genesis-derived id as networkChainId (decimal string): the value
// net_info calls chainId. It does not change at TxNetworkChainIDActivationHeight (txChainId does), and it is public like the rest
// of the call: net_info is operator-only, so a signer that has to name the running chain (the mint voucher signers, ledger
// PL-R5T-58) has no other public place to read it. Additive: nothing the call returned before changes.

type statsFields struct {
	NetworkChainID json.RawMessage `json:"networkChainId"`
	TxChainID      string          `json:"txChainId"`
	Active         *int            `json:"activeValidators"`
	CurrentEpoch   *uint64         `json:"currentEpoch"`
	Mempool        *int            `json:"mempoolSize"`
	TPS            *float64        `json:"tps"`
	CurrentTime    *int64          `json:"currentTime"`
}

// readStatsOverHTTP calls the method through the real dispatcher with NO credentials, as a public caller would.
func readStatsOverHTTP(t *testing.T, env *testEnv) (statsFields, string) {
	t.Helper()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"nhb_getNetworkStats","params":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nhb_getNetworkStats without credentials answered HTTP %d: %s", rec.Code, rec.Body.String())
	}
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("nhb_getNetworkStats without credentials: %+v", rpcErr)
	}
	var out statsFields
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("decode nhb_getNetworkStats: %v", err)
	}
	return out, string(result)
}

func TestNetworkStatsNameTheRunningChainID(t *testing.T) {
	env := newTestEnv(t)
	want := new(big.Int).SetUint64(env.node.ChainID()).String()
	if want == "0" {
		t.Fatal("the test node has no chain id: the test would prove nothing")
	}
	out, raw := readStatsOverHTTP(t, env)

	// A decimal STRING, like txChainId: a JSON number above 2^53 is rounded by JavaScript callers.
	var asString string
	if err := json.Unmarshal(out.NetworkChainID, &asString); err != nil {
		t.Fatalf("networkChainId is not a string: %s", raw)
	}
	if asString != want {
		t.Fatalf("networkChainId %q, want the node's genesis-derived id %q", asString, want)
	}
}

func TestNetworkChainIDDoesNotMoveWithTheTransactionActivation(t *testing.T) {
	env := newTestEnv(t)
	want := new(big.Int).SetUint64(env.node.ChainID()).String()

	read := func() (network, tx string) {
		out, _ := readStatsOverHTTP(t, env)
		if err := json.Unmarshal(out.NetworkChainID, &network); err != nil {
			t.Fatalf("networkChainId: %v", err)
		}
		return network, out.TxChainID
	}

	network, tx := read()
	if network != want || tx == want {
		t.Fatalf("setting off: networkChainId %q txChainId %q: the first must be the network's own id (%s) and the second the legacy constant", network, tx, want)
	}
	env.node.SetTxNetworkChainIDActivationHeight(1)
	network, tx = read()
	if network != want {
		t.Fatalf("setting on: networkChainId %q changed; it must stay the network's own id %s", network, want)
	}
	if tx != want {
		t.Fatalf("setting on: txChainId %q, want the network id %s", tx, want)
	}
}

func TestNetworkStatsKeepEveryFieldTheyAlwaysHad(t *testing.T) {
	env := newTestEnv(t)
	out, raw := readStatsOverHTTP(t, env)
	if out.Active == nil || out.CurrentEpoch == nil || out.Mempool == nil || out.TPS == nil || out.CurrentTime == nil || out.TxChainID == "" {
		t.Fatalf("a field nhb_getNetworkStats returned before is missing: %s", raw)
	}
}

// A server with no node behind it (some conformance tests run that way) answers the public call like every other field: no panic,
// and a networkChainId of 0, which no signer accepts.
func TestNetworkStatsWithoutANodeReportNoNetworkChainID(t *testing.T) {
	srv := newTestServer(t, nil, nil, ServerConfig{MaxTxPerWindow: 1000, MaxTxPerIP: 1000})
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"nhb_getNetworkStats","params":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	result, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("%+v", rpcErr)
	}
	var out struct {
		NetworkChainID string `json:"networkChainId"`
		TxChainID      string `json:"txChainId"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if out.NetworkChainID != "0" || out.TxChainID != "5130306" {
		t.Fatalf("networkChainId %q txChainId %q", out.NetworkChainID, out.TxChainID)
	}
}
