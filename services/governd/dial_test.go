package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/services/governd/config"
)

// writeSelfSignedTestCA generates a throwaway self-signed certificate and
// writes it (PEM-encoded) to a temp file, returning its path. The repo's
// .gitignore deliberately blocks every *.crt/*.key/*.pem from ever being
// committed as a hard backstop against leaking real TLS material, so this
// test can't rely on a checked-in fixture -- it has to make its own CA.
func writeSelfSignedTestCA(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "governd-dial-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}

	path := filepath.Join(t.TempDir(), "server.crt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test cert file: %v", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("write test cert file: %v", err)
	}
	return path
}

func TestConsensusDialOptionsRequireSecurity(t *testing.T) {
	if _, err := consensusDialOptions(config.ClientConfig{}); err == nil {
		t.Fatalf("expected consensus dial configuration to fail without security settings")
	}
}

func TestConsensusDialOptionsAllowInsecureOverride(t *testing.T) {
	opts, err := consensusDialOptions(config.ClientConfig{AllowInsecure: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected insecure dial option to be returned")
	}
}

func TestConsensusDialOptionsWithTLS(t *testing.T) {
	caPath := writeSelfSignedTestCA(t)
	opts, err := consensusDialOptions(config.ClientConfig{
		TLS: config.ClientTLSConfig{CAPath: caPath},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected tls dial option to be returned")
	}
}

func TestConsensusDialOptionsWithSharedSecret(t *testing.T) {
	opts, err := consensusDialOptions(config.ClientConfig{
		SharedSecret: config.SharedSecretConfig{Header: "x-test", Token: "value"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(opts) == 0 {
		t.Fatalf("expected dial options to include shared-secret credentials")
	}
}
