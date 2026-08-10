package server

import (
	"bytes"
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/services/otc-gateway/priceproofclient"
	"nhbchain/services/swapd/priceproof"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type integrationKeySigner struct {
	key *crypto.PrivateKey
}

func (s *integrationKeySigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	sig, err := ethcrypto.Sign(digest, s.key.PrivateKey)
	if err != nil {
		return nil, "", err
	}
	return sig, "CN=integration-test-signer", nil
}

type integrationQuoteSource struct {
	rate    *big.Rat
	feeders []string
	ts      time.Time
}

func (s *integrationQuoteSource) Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error) {
	return s.rate, s.feeders, s.ts, nil
}

// TestOTCGatewayClientAgainstRealSwapdPriceProofHandler drives the exact
// otc-gateway <-> swapd wiring gap flagged in the fiat-onramp investigation
// ("these two services do not talk to each other today"): a real swapd
// server.Server, with a real priceproof.Service and real partner-auth
// middleware, serving POST /v1/price-proof; and the REAL
// nhbchain/services/otc-gateway/priceproofclient.Client (the same client
// otc-gateway's production code uses) fetching from it over HTTP. The
// returned swap.PriceProof is exactly what services/otc-gateway/server's
// SignAndSubmit embeds in its swap_submitVoucher call.
func TestOTCGatewayClientAgainstRealSwapdPriceProofHandler(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ts := time.Unix(1_700_900_000, 0).UTC()
	source := &integrationQuoteSource{rate: big.NewRat(1, 10), feeders: []string{"nowpayments"}, ts: ts}
	service, err := priceproof.New(source, &integrationKeySigner{key: key}, "nowpayments")
	if err != nil {
		t.Fatalf("new price proof service: %v", err)
	}

	creds := partnerCreds{id: "otc-gateway", apiKey: "gateway-key", secret: "gateway-secret"}
	srv := newPriceProofTestServer(t, service, []Partner{{ID: creds.id, APIKey: creds.apiKey, Secret: creds.secret}})

	mux := http.NewServeMux()
	srv.registerPriceProofHandlers(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	client, err := priceproofclient.NewClient(priceproofclient.Config{
		URL:       httpServer.URL + "/v1/price-proof",
		APIKey:    creds.apiKey,
		APISecret: creds.secret,
	})
	if err != nil {
		t.Fatalf("new price proof client: %v", err)
	}

	proof, err := client.PriceProof(context.Background(), "ZNHB/USD")
	if err != nil {
		t.Fatalf("fetch price proof: %v", err)
	}
	if proof.Provider != "nowpayments" {
		t.Fatalf("unexpected provider: %s", proof.Provider)
	}
	if proof.Base != "ZNHB" || proof.Quote != "USD" {
		t.Fatalf("unexpected pair: %s/%s", proof.Base, proof.Quote)
	}
	if proof.Timestamp.Unix() != ts.Unix() {
		t.Fatalf("unexpected timestamp: %v", proof.Timestamp)
	}
	if len(proof.Signature) != 65 {
		t.Fatalf("expected 65-byte signature, got %d", len(proof.Signature))
	}

	// Recover the signer from the fetched proof and confirm it matches the
	// key swapd actually signed with -- the full round trip, not just a
	// shape check.
	hash, err := proof.Hash()
	if err != nil {
		t.Fatalf("hash proof: %v", err)
	}
	pubKey, err := ethcrypto.SigToPub(hash, proof.Signature)
	if err != nil {
		t.Fatalf("recover signer: %v", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	var expected [20]byte
	copy(expected[:], key.PubKey().Address().Bytes())
	if !bytes.Equal(recovered.Bytes(), expected[:]) {
		t.Fatalf("recovered signer does not match expected key: got %x want %x", recovered.Bytes(), expected[:])
	}
}
