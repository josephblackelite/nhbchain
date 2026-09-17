package config

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	sampleSignerKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	signerKeyEnv    = "TEST_GOVERND_SIGNER_KEY"
	tlsKeyEnv       = "TEST_GOVERND_TLS_KEY"
	clientCertEnv   = "TEST_GOVERND_CLIENT_CERT"
	clientKeyEnv    = "TEST_GOVERND_CLIENT_KEY"
)

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "governd-config-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}
	return file.Name()
}

func writeTempSecret(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func baseConfig(prefix string) string {
	return "listen: \"" + prefix + "\"\n" +
		"consensus: \"localhost:9090\"\n" +
		"chain_id: \"localnet\"\n" +
		"signer_key_env: \"" + signerKeyEnv + "\"\n" +
		"nonce_start: 1\n" +
		"nonce_store_path: \"" + filepath.ToSlash(filepath.Join("var", "lib", "nhb", "governd-nonce")) + "\"\n" +
		"fee:\n" +
		"  amount: \"\"\n" +
		"  denom: \"\"\n" +
		"  payer: \"\"\n" +
		"tls:\n" +
		"  cert: \"" + filepath.ToSlash(filepath.Join("services", "governd", "config", "server.crt")) + "\"\n" +
		"  key_env: \"" + tlsKeyEnv + "\"\n" +
		"  client_ca: \"\"\n" +
		"auth:\n" +
		"  api_tokens: []\n" +
		"  mtls:\n" +
		"    allowed_common_names: []\n"
}

func configureRuntimeSecrets(t *testing.T) {
	t.Helper()
	t.Setenv(signerKeyEnv, sampleSignerKey)
	keyFile := writeTempSecret(t, "tls.key", "test")
	t.Setenv(tlsKeyEnv, keyFile)
}

func TestLoadRequiresConsensusClientSecurity(t *testing.T) {
	configureRuntimeSecrets(t)
	path := writeTempConfig(t, baseConfig(":50061"))
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail when consensus client security is missing")
	}
}

func TestLoadAllowsExplicitInsecureConsensus(t *testing.T) {
	configureRuntimeSecrets(t)
	cfg := baseConfig(":50062") +
		"consensus_client:\n" +
		"  allow_insecure: true\n" +
		"  tls:\n" +
		"    cert: \"\"\n" +
		"    key: \"\"\n" +
		"    ca: \"\"\n" +
		"  shared_secret:\n" +
		"    header: \"authorization\"\n" +
		"    token: \"\"\n"
	path := writeTempConfig(t, cfg)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !loaded.ConsensusClient.AllowInsecure {
		t.Fatalf("expected AllowInsecure to remain true")
	}
}

func TestLoadRejectsIncompleteClientTLS(t *testing.T) {
	configureRuntimeSecrets(t)
	certFile := writeTempSecret(t, "client.pem", "client")
	t.Setenv(clientCertEnv, certFile)
	t.Setenv(clientKeyEnv, "")
	cfg := baseConfig(":50063") +
		"consensus_client:\n" +
		"  allow_insecure: false\n" +
		"  tls:\n" +
		"    cert_env: \"" + clientCertEnv + "\"\n" +
		"    key_env: \"" + clientKeyEnv + "\"\n" +
		"    ca: \"\"\n" +
		"  shared_secret:\n" +
		"    header: \"authorization\"\n" +
		"    token: \"token\"\n"
	path := writeTempConfig(t, cfg)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to reject missing client key")
	}
}
