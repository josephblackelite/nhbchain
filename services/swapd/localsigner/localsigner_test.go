package localsigner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/crypto"
	swap "nhbchain/native/swap"
)

const testPassphraseEnv = "NHB_LOCALSIGNER_TEST_PASSPHRASE"

func writeTestKeystore(t *testing.T, key *crypto.PrivateKey, passphrase string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signer.keystore.json")
	if err := crypto.SaveToKeystore(path, key, passphrase); err != nil {
		t.Fatalf("save keystore: %v", err)
	}
	return path
}

// TestClientSignProducesVerifiablePriceProof proves the signer's output is
// genuinely compatible with the real on-chain verification path, not merely
// the right length. It builds a swap.PriceProof, signs its hash with a
// Client loaded from a keystore created via crypto.SaveToKeystore, and then
// runs the exact code path native/swap.PriceProofEngine.Verify uses in
// production (ethcrypto.SigToPub against the signed hash) against a minimal
// in-memory PriceProofStore, confirming it recovers this key's own address.
func TestClientSignProducesVerifiablePriceProof(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	passphrase := "correct horse battery staple"
	path := writeTestKeystore(t, key, passphrase)

	t.Setenv(testPassphraseEnv, passphrase)
	client, err := NewClient(Config{KeystorePath: path, PassphraseEnv: testPassphraseEnv})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	expectedAddr := key.PubKey().Address().String()
	if client.Address() != expectedAddr {
		t.Fatalf("unexpected client address: got %s want %s", client.Address(), expectedAddr)
	}

	ts := time.Unix(1_700_500_000, 0).UTC()
	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, "nowpayments", "ZNHB/USD", "0.10", ts.Unix(), nil)
	if err != nil {
		t.Fatalf("build proof: %v", err)
	}
	hash, err := proof.Hash()
	if err != nil {
		t.Fatalf("hash proof: %v", err)
	}

	sig, signerRef, err := client.Sign(context.Background(), hash)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("expected 65-byte signature, got %d", len(sig))
	}
	if signerRef != expectedAddr {
		t.Fatalf("unexpected signer reference: got %s want %s", signerRef, expectedAddr)
	}
	proof.Signature = sig

	var signerAddr [20]byte
	copy(signerAddr[:], key.PubKey().Address().Bytes())
	store := &memPriceProofStore{signers: map[string][20]byte{"nowpayments": signerAddr}}
	engine := swap.NewPriceProofEngine(store, 5*time.Minute, 0)
	engine.SetClock(func() time.Time { return ts.Add(time.Second) })
	engine.RequireSignature(true)
	if err := engine.Verify(proof, "nowpayments", "ZNHB"); err != nil {
		t.Fatalf("expected proof signed by localsigner.Client to pass native/swap verification, got %v", err)
	}
}

// TestNewClientFailsLoudlyOnWrongPassphrase proves a wrong passphrase is a
// hard startup failure, not a silently-broken signer: NewClient must return
// an error (and no usable Client) rather than producing garbage key
// material that would later sign proofs no validator can ever verify.
func TestNewClientFailsLoudlyOnWrongPassphrase(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := writeTestKeystore(t, key, "the-real-passphrase")

	t.Setenv(testPassphraseEnv, "definitely-the-wrong-passphrase")
	client, err := NewClient(Config{KeystorePath: path, PassphraseEnv: testPassphraseEnv})
	if err == nil {
		t.Fatalf("expected error for wrong passphrase, got a usable client (address %s)", client.Address())
	}
	if client != nil {
		t.Fatalf("expected nil client on error, got %+v", client)
	}
}

func TestNewClientFailsWhenPassphraseEnvUnset(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := writeTestKeystore(t, key, "some-passphrase")

	if _, err := NewClient(Config{KeystorePath: path, PassphraseEnv: "NHB_LOCALSIGNER_TEST_DOES_NOT_EXIST"}); err == nil {
		t.Fatalf("expected error when passphrase environment variable is unset")
	}
}

func TestNewClientFailsWhenPassphraseEnvEmpty(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := writeTestKeystore(t, key, "some-passphrase")

	t.Setenv(testPassphraseEnv, "")
	if _, err := NewClient(Config{KeystorePath: path, PassphraseEnv: testPassphraseEnv}); err == nil {
		t.Fatalf("expected error when passphrase environment variable is set but empty")
	}
}

func TestNewClientFailsOnMissingKeystorePath(t *testing.T) {
	t.Setenv(testPassphraseEnv, "some-passphrase")
	if _, err := NewClient(Config{KeystorePath: "", PassphraseEnv: testPassphraseEnv}); err == nil {
		t.Fatalf("expected error for empty keystore path")
	}
}

func TestNewClientFailsOnNonexistentKeystoreFile(t *testing.T) {
	t.Setenv(testPassphraseEnv, "some-passphrase")
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	if _, err := NewClient(Config{KeystorePath: path, PassphraseEnv: testPassphraseEnv}); err == nil {
		t.Fatalf("expected error for nonexistent keystore file")
	}
}

// memPriceProofStore is a minimal in-memory implementation of
// native/swap.PriceProofStore for exercising the real Verify path in tests
// (mirrors the identical helper in services/swapd/priceproof/priceproof_test.go).
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
