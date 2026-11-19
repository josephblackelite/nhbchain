package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"nhbchain/consensus/codec"
	"nhbchain/crypto"
)

// The sample must produce an envelope that the node's own envelope decoder
// accepts. It used to send a lending message, which the decoder rejects with
// "unsupported module payload type" (PL-DC-41).
func TestSampleEnvelopeIsAcceptedByTheNodeDecoder(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	signed, sender, err := buildSignedEnvelope(key, "430060579445266314", uint64(time.Now().Add(time.Hour).Unix()))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	tx, err := codec.TransactionFromEnvelope(signed)
	if err != nil {
		t.Fatalf("the node decoder rejects the sample's envelope: %v", err)
	}
	if tx.ChainID == nil || tx.ChainID.String() != "430060579445266314" {
		t.Fatalf("decoded chain id = %v", tx.ChainID)
	}
	if sender == "" {
		t.Fatalf("no sender")
	}
}

func TestSampleRefusesToGuessTheNetwork(t *testing.T) {
	key, _ := crypto.GeneratePrivateKey()
	if _, _, err := buildSignedEnvelope(key, "  ", 0); err == nil {
		t.Fatalf("an empty chain id must be refused, not defaulted to a made-up network name")
	}
}

// The chain id comes from the node (nhb_getNetworkStats, txChainId), not from a number in the sample (PL-R5T-29).
func TestSampleReadsTheChainIDFromTheNode(t *testing.T) {
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"result":{"txChainId":"18346390202490284624"}}`))
	}))
	defer srv.Close()
	got, err := resolveChainID(context.Background(), "", srv.URL)
	if err != nil || got != "18346390202490284624" {
		t.Fatalf("got %q %v", got, err)
	}
	// An explicit NHB_CHAIN_ID is the caller's choice and skips the question.
	asked = 0
	if got, err := resolveChainID(context.Background(), " 7 ", srv.URL); err != nil || got != "7" || asked != 0 {
		t.Fatalf("explicit: %q %v asked=%d", got, err, asked)
	}
}

func TestSampleSignsNothingWhenTheNodeCannotGiveAChainID(t *testing.T) {
	for name, body := range map[string]string{
		"no id":    `{"result":{"activeValidators":2}}`,
		"zero":     `{"result":{"txChainId":"0"}}`,
		"an error": `{"error":{"code":-1,"message":"x"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		got, err := resolveChainID(context.Background(), "", srv.URL)
		srv.Close()
		if err == nil || got != "" {
			t.Fatalf("%s: got %q %v", name, got, err)
		}
	}
	if got, err := resolveChainID(context.Background(), "", "http://127.0.0.1:1"); err == nil || got != "" {
		t.Fatalf("an unreachable node gave %q", got)
	}
}

func TestSampleDoesNotTellTheReaderToUseTheOperatorOnlyCall(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "net_info") {
		t.Fatal("net_info is operator-only: the sample must point at nhb_getNetworkStats")
	}
}
