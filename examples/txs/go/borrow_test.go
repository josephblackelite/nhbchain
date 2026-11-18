package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The envelope's chain id comes from the node (nhb_getNetworkStats, txChainId), not from a made-up network name
// (ledger PL-R5T-29).
func TestEnvelopeChainIDComesFromTheNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"txChainId":"18346390202490284624"}}`))
	}))
	defer srv.Close()
	if got, err := envelopeChainID(context.Background(), "", srv.URL); err != nil || got != "18346390202490284624" {
		t.Fatalf("got %q %v", got, err)
	}
	if got, err := envelopeChainID(context.Background(), " 9 ", "http://127.0.0.1:1"); err != nil || got != "9" {
		t.Fatalf("an explicit id must be used without asking: %q %v", got, err)
	}
}

func TestNothingIsSignedWhenTheNodeCannotGiveAChainID(t *testing.T) {
	for name, body := range map[string]string{
		"no id": `{"result":{"activeValidators":2}}`,
		"zero":  `{"result":{"txChainId":"0"}}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		got, err := envelopeChainID(context.Background(), "", srv.URL)
		srv.Close()
		if err == nil || got != "" {
			t.Fatalf("%s: got %q %v", name, got, err)
		}
	}
}

func TestTheExampleDoesNotGuessANetworkName(t *testing.T) {
	src, err := os.ReadFile("borrow.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"localnet"`) {
		t.Fatal("borrow.go still hard-codes a chain id")
	}
}
