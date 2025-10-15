package swap_test

import (
	"crypto/ecdsa"
	"errors"
	"strings"
	"testing"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	swap "nhbchain/native/swap"
)

type stubPriceStore struct {
	signer map[string][20]byte
	proofs map[string]*swap.PriceProofRecord
}

func newStubPriceStore() *stubPriceStore {
	return &stubPriceStore{
		signer: make(map[string][20]byte),
		proofs: make(map[string]*swap.PriceProofRecord),
	}
}

func (s *stubPriceStore) SwapPriceSigner(provider string) ([20]byte, bool, error) {
	addr, ok := s.signer[strings.ToLower(strings.TrimSpace(provider))]
	return addr, ok, nil
}

func (s *stubPriceStore) SwapLastPriceProof(base string) (*swap.PriceProofRecord, bool, error) {
	if record, ok := s.proofs[base]; ok {
		return record.Clone(), true, nil
	}
	return nil, false, nil
}

func (s *stubPriceStore) SwapPutPriceProof(base string, record *swap.PriceProofRecord) error {
	s.proofs[base] = record.Clone()
	return nil
}

func (s *stubPriceStore) setSigner(provider string, addr [20]byte) {
	s.signer[strings.ToLower(strings.TrimSpace(provider))] = addr
}

func addrToArray(addr ethcommon.Address) [20]byte {
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}

func signProof(t *testing.T, proof *swap.PriceProof, priv *ecdsa.PrivateKey) {
	t.Helper()
	hash, err := proof.Hash()
	if err != nil {
		t.Fatalf("hash proof: %v", err)
	}
	sig, err := ethcrypto.Sign(hash, priv)
	if err != nil {
		t.Fatalf("sign proof: %v", err)
	}
	proof.Signature = sig
}

func TestPriceProofEngineVerification(t *testing.T) {
	store := newStubPriceStore()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	provider := "prime-gateway"
	store.setSigner(provider, addrToArray(ethcrypto.PubkeyToAddress(priv.PublicKey)))

	now := time.Unix(1700000000, 0).UTC()
	engine := swap.NewPriceProofEngine(store, time.Minute, 200)
	engine.SetClock(func() time.Time { return now })

	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", "0.10", now.Unix(), nil)
	if err != nil {
		t.Fatalf("new price proof: %v", err)
	}
	signProof(t, proof, priv)

	if err := engine.Verify(proof, provider, "ZNHB"); err != nil {
		t.Fatalf("verify valid proof: %v", err)
	}
	if err := engine.Record(proof); err != nil {
		t.Fatalf("record proof: %v", err)
	}

	// Large deviation should be rejected.
	proofDeviate, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", "0.50", now.Add(30*time.Second).Unix(), nil)
	if err != nil {
		t.Fatalf("new proof (deviation): %v", err)
	}
	signProof(t, proofDeviate, priv)
	if err := engine.Verify(proofDeviate, provider, "ZNHB"); !errors.Is(err, swap.ErrPriceProofDeviation) {
		t.Fatalf("expected deviation error, got %v", err)
	}

	// Stale proof rejected.
	engine.SetClock(func() time.Time { return now.Add(2 * time.Minute) })
	if err := engine.Verify(proof, provider, "ZNHB"); !errors.Is(err, swap.ErrPriceProofStale) {
		t.Fatalf("expected stale error, got %v", err)
	}

	// Unknown signer rejected.
	otherProof, err := swap.NewPriceProof(swap.PriceProofDomainV1, "unknown", "ZNHB/USD", "0.10", now.Unix(), nil)
	if err != nil {
		t.Fatalf("new proof (unknown signer): %v", err)
	}
	signProof(t, otherProof, priv)
	engine.SetClock(func() time.Time { return now })
	if err := engine.Verify(otherProof, "unknown", "ZNHB"); !errors.Is(err, swap.ErrPriceProofSignerUnknown) {
		t.Fatalf("expected signer unknown, got %v", err)
	}

	// Provider mismatch should fail.
	if err := engine.Verify(proof, "other-provider", "ZNHB"); !errors.Is(err, swap.ErrPriceProofProviderMismatch) {
		t.Fatalf("expected provider mismatch, got %v", err)
	}
}

func TestPriceProofEngineRequiresSignature(t *testing.T) {
	store := newStubPriceStore()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	provider := "prime-gateway"
	store.setSigner(provider, addrToArray(ethcrypto.PubkeyToAddress(priv.PublicKey)))

	now := time.Unix(1700000000, 0).UTC()
	engine := swap.NewPriceProofEngine(store, time.Minute, 200)
	engine.SetClock(func() time.Time { return now })
	engine.RequireSignature(true)

	if _, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", "0.10", now.Unix(), nil, swap.WithSignatureRequired(true)); !errors.Is(err, swap.ErrPriceProofSignatureMissing) {
		t.Fatalf("expected signature missing error, got %v", err)
	}

	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, provider, "ZNHB/USD", "0.10", now.Unix(), nil)
	if err != nil {
		t.Fatalf("new price proof: %v", err)
	}
	signProof(t, proof, priv)

	if err := engine.Verify(proof, provider, "ZNHB"); err != nil {
		t.Fatalf("verify signed proof: %v", err)
	}

	proof.Signature = nil
	if err := engine.Verify(proof, provider, "ZNHB"); !errors.Is(err, swap.ErrPriceProofSignatureMissing) {
		t.Fatalf("expected signature missing error, got %v", err)
	}
}
