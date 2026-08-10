package priceproof

import (
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"

	"nhbchain/crypto"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type stubQuoteSource struct {
	rate    *big.Rat
	feeders []string
	ts      time.Time
	err     error
	calls   int
}

func (s *stubQuoteSource) Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error) {
	s.calls++
	if s.err != nil {
		return nil, nil, time.Time{}, s.err
	}
	return s.rate, s.feeders, s.ts, nil
}

// keySigner signs digests with a real secp256k1 key via ethcrypto.Sign,
// producing genuine 65-byte recoverable signatures -- the same shape
// nhbchain/services/otc-gateway/hsm.Client produces in production, and the
// same shape native/swap.PriceProofEngine.Verify expects.
type keySigner struct {
	key   *crypto.PrivateKey
	calls int
	err   error
}

func (s *keySigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	s.calls++
	if s.err != nil {
		return nil, "", s.err
	}
	sig, err := ethcrypto.Sign(digest, s.key.PrivateKey)
	if err != nil {
		return nil, "", err
	}
	return sig, "CN=test-signer", nil
}

type wrongLengthSigner struct{}

func (wrongLengthSigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	return []byte{0x01, 0x02, 0x03}, "", nil
}

func newTestKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestServiceSignProducesVerifiablePriceProof(t *testing.T) {
	key := newTestKey(t)
	ts := time.Unix(1_700_500_000, 0).UTC()
	source := &stubQuoteSource{rate: big.NewRat(1, 10), feeders: []string{"nowpayments", "coingecko"}, ts: ts}
	signer := &keySigner{key: key}

	svc, err := New(source, signer, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	proof, err := svc.Sign(context.Background(), "ZNHB/USD")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("expected quote source called once, got %d", source.calls)
	}
	if signer.calls != 1 {
		t.Fatalf("expected signer called once, got %d", signer.calls)
	}
	if proof.Domain != swap.PriceProofDomainV1 {
		t.Fatalf("unexpected domain: %s", proof.Domain)
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

	// Now replay the EXACT verification path native/swap uses at consensus
	// time (PriceProofEngine.Verify) against a minimal in-memory store, to
	// prove this proof would actually be accepted by a validator -- not just
	// that it has the right shape.
	var signerAddr [20]byte
	copy(signerAddr[:], key.PubKey().Address().Bytes())
	store := &memPriceProofStore{signers: map[string][20]byte{"nowpayments": signerAddr}}
	engine := swap.NewPriceProofEngine(store, 5*time.Minute, 0)
	engine.SetClock(func() time.Time { return ts.Add(time.Second) })
	engine.RequireSignature(true)
	if err := engine.Verify(proof, "nowpayments", "ZNHB"); err != nil {
		t.Fatalf("expected proof to pass native/swap verification, got %v", err)
	}
}

func TestServiceSignRejectsNonPositiveRate(t *testing.T) {
	source := &stubQuoteSource{rate: big.NewRat(0, 1), ts: time.Now()}
	signer := &keySigner{key: newTestKey(t)}
	svc, err := New(source, signer, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Sign(context.Background(), "ZNHB/USD"); err == nil {
		t.Fatalf("expected error for non-positive rate")
	}
}

func TestServiceSignPropagatesQuoteSourceError(t *testing.T) {
	source := &stubQuoteSource{err: fmt.Errorf("boom")}
	signer := &keySigner{key: newTestKey(t)}
	svc, err := New(source, signer, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Sign(context.Background(), "ZNHB/USD"); err == nil {
		t.Fatalf("expected quote source error to propagate")
	}
}

func TestServiceSignPropagatesSignerError(t *testing.T) {
	source := &stubQuoteSource{rate: big.NewRat(1, 10), ts: time.Now()}
	signer := &keySigner{key: newTestKey(t), err: fmt.Errorf("hsm unavailable")}
	svc, err := New(source, signer, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Sign(context.Background(), "ZNHB/USD"); err == nil {
		t.Fatalf("expected signer error to propagate")
	}
}

func TestServiceSignRejectsWrongSignatureLength(t *testing.T) {
	source := &stubQuoteSource{rate: big.NewRat(1, 10), ts: time.Now()}
	svc, err := New(source, wrongLengthSigner{}, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Sign(context.Background(), "ZNHB/USD"); err == nil {
		t.Fatalf("expected error for wrong-length signature")
	}
}

func TestServiceSignRejectsInvalidPair(t *testing.T) {
	source := &stubQuoteSource{rate: big.NewRat(1, 10), ts: time.Now()}
	signer := &keySigner{key: newTestKey(t)}
	svc, err := New(source, signer, "nowpayments")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if _, err := svc.Sign(context.Background(), "ZNHBUSD"); err == nil {
		t.Fatalf("expected error for pair missing separator")
	}
	if source.calls != 0 {
		t.Fatalf("expected quote source not to be called for an invalid pair")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	source := &stubQuoteSource{}
	signer := &keySigner{key: newTestKey(t)}
	if _, err := New(nil, signer, "nowpayments"); err == nil {
		t.Fatalf("expected error for nil source")
	}
	if _, err := New(source, nil, "nowpayments"); err == nil {
		t.Fatalf("expected error for nil signer")
	}
	if _, err := New(source, signer, ""); err == nil {
		t.Fatalf("expected error for empty provider")
	}
}

// memPriceProofStore is a minimal in-memory implementation of
// native/swap.PriceProofStore for exercising the real Verify path in tests.
type memPriceProofStore struct {
	signers map[string][20]byte
}

func (m *memPriceProofStore) SwapPriceSigner(provider string) ([20]byte, bool, error) {
	addr, ok := m.signers[provider]
	return addr, ok, nil
}

func (m *memPriceProofStore) SwapLastPriceProof(base string) (*swap.PriceProofRecord, bool, error) {
	return nil, false, nil
}

func (m *memPriceProofStore) SwapPutPriceProof(base string, record *swap.PriceProofRecord) error {
	return nil
}
