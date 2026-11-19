package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"nhbchain/rpc/txchainid"
)

// The relayer signs every delegated escrow transaction. The chain id each one carries is the one the node reports in
// nhb_getNetworkStats (field txChainId), never the shared constant, and nothing is signed or sent when the node
// cannot say (ledger PL-R5T-29).

func releasePayload() []byte {
	return []byte(`{"escrowId":"0x` + fixedHex64() + `","action":"release"}`)
}

func newReadyClient(t *testing.T, fake *fakeRPCServer) *RPCNodeClient {
	t.Helper()
	client := NewRPCNodeClient(fake.URL, "")
	if err := client.InitRelayer(context.Background(), mustGenerateEscrowGatewayKey(t)); err != nil {
		t.Fatalf("init relayer: %v", err)
	}
	return client
}

func TestRelayerTransactionsCarryTheNodesChainIDAndItIsAskedOnce(t *testing.T) {
	for _, id := range []string{fakeNetworkChainID, "5130306"} {
		fake := newFakeRPCServer(t)
		fake.chainID = id
		fake.nonce = 5
		client := newReadyClient(t, fake)
		for i := 0; i < 3; i++ {
			if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err != nil {
				t.Fatalf("id %s: %v", id, err)
			}
			if fake.lastTx.ChainID.String() != id {
				t.Fatalf("id %s: transaction %d carries %s", id, i, fake.lastTx.ChainID)
			}
		}
		if fake.statsCalls != 1 {
			t.Fatalf("id %s: the node was asked %d times for 3 transactions, want 1", id, fake.statsCalls)
		}
		if from, err := fake.lastTx.From(); err != nil || strings.ToLower(string(from)) != strings.ToLower(string(mustAddressBytes(t, client))) {
			t.Fatalf("id %s: the signature does not recover the relayer: %v", id, err)
		}
		fake.Close()
	}
}

func mustAddressBytes(t *testing.T, c *RPCNodeClient) []byte {
	t.Helper()
	c.relayerMu.Lock()
	defer c.relayerMu.Unlock()
	return c.relayerKey.PubKey().Address().Bytes()
}

func TestRelayerSignsNothingWhenTheNodeGivesNoChainID(t *testing.T) {
	for name, stats := range map[string]string{
		"no id":        `{"activeValidators":2}`,
		"zero":         `{"txChainId":"0"}`,
		"not a number": `{"txChainId":"NHB"}`,
		"hex":          `{"txChainId":"0x4e4842"}`,
		"above uint64": `{"txChainId":"18446744073709551616"}`,
		"null":         `null`,
	} {
		fake := newFakeRPCServer(t)
		fake.stats = stats
		fake.nonce = 9
		client := newReadyClient(t, fake)
		err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig"))
		if err == nil {
			t.Fatalf("%s: a transaction was signed and sent", name)
		}
		if fake.lastTx != nil {
			t.Fatalf("%s: a transaction reached the node", name)
		}
		if client.relayerNonce != 9 {
			t.Fatalf("%s: the nonce moved to %d without a transaction", name, client.relayerNonce)
		}
		fake.Close()
	}
	// A node that errors on the call.
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := newReadyClient(t, fake)
	fake.Close() // unreachable from here on
	if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err == nil {
		t.Fatal("a transaction was built with no node to ask")
	}
}

func TestRelayerRecoversWhenTheNodeAnswersAgain(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.nonce = 2
	client := newReadyClient(t, fake)
	fake.stats = `{"activeValidators":2}`
	if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err == nil {
		t.Fatal("signed without an id")
	}
	fake.stats = ""
	if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err != nil {
		t.Fatalf("a refusal must not stick: %v", err)
	}
	if fake.lastTx.Nonce != 2 {
		t.Fatalf("nonce %d: a refused attempt must not burn a nonce", fake.lastTx.Nonce)
	}
}

func TestRelayerFollowsTheChainIDChangeAtTheActivationHeight(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	fake.chainID = "5130306"
	client := newReadyClient(t, fake)
	client.chainIDs = txchainid.NewReader(client.fetchTxChainID, 50*time.Millisecond)
	if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err != nil {
		t.Fatal(err)
	}
	fake.chainID = fakeNetworkChainID
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig")); err != nil {
			t.Fatal(err)
		}
		if fake.lastTx.ChainID.String() == fakeNetworkChainID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the relayer kept signing with the old chain id after the node changed it")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAClientWithoutAChainIDSourceSignsNothing(t *testing.T) {
	fake := newFakeRPCServer(t)
	defer fake.Close()
	client := newReadyClient(t, fake)
	client.chainIDs = nil
	err := client.EscrowRelease(context.Background(), releasePayload(), []byte("sig"))
	if err == nil || !errors.Is(err, txchainid.ErrUnavailable) {
		t.Fatalf("expected a refusal that wraps ErrUnavailable, got %v", err)
	}
	if fake.lastTx != nil {
		t.Fatal("a transaction reached the node")
	}
}

func TestTheNodeClientDoesNotRefertoTheSharedConstant(t *testing.T) {
	src, err := os.ReadFile("node_client.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "NHBChainID()") {
		t.Fatal("node_client.go signs with the shared constant instead of the id the node reports (PL-R5T-29)")
	}
}

func TestTheNodeClientKeepsTheChainIDOnlyBrieflyAcrossTheActivationHeight(t *testing.T) {
	c := NewRPCNodeClient("http://127.0.0.1:1", "")
	if c.chainIDs.Expiry() != txchainid.DefaultTTL {
		t.Fatalf("the client keeps a chain id for %v", c.chainIDs.Expiry())
	}
}
