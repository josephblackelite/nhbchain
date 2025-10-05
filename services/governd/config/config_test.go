package config

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	sampleSignerKey = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
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

func baseConfig(prefix string) string {
        return "listen: \"" + prefix + "\"\n" +
                "consensus: \"localhost:9090\"\n" +
                "chain_id: \"localnet\"\n" +
                "signer_key: \"" + sampleSignerKey + "\"\n" +
                "nonce_start: 1\n" +
                "nonce_store_path: \"" + filepath.ToSlash(filepath.Join("var", "lib", "nhb", "governd-nonce")) + "\"\n" +
                "fee:\n" +
                "  amount: \"\"\n" +
                "  denom: \"\"\n" +
		"  payer: \"\"\n" +
		"tls:\n" +
		"  cert: \"" + filepath.ToSlash(filepath.Join("services", "governd", "config", "server.crt")) + "\"\n" +
		"  key: \"" + filepath.ToSlash(filepath.Join("services", "governd", "config", "server.key")) + "\"\n" +
		"  client_ca: \"\"\n" +
		"auth:\n" +
		"  api_tokens: []\n" +
		"  mtls:\n" +
		"    allowed_common_names: []\n"
}

func TestLoadRequiresConsensusClientSecurity(t *testing.T) {
	path := writeTempConfig(t, baseConfig(":50061"))
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail when consensus client security is missing")
	}
}

func TestLoadAllowsExplicitInsecureConsensus(t *testing.T) {
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
	cfg := baseConfig(":50063") +
		"consensus_client:\n" +
		"  allow_insecure: false\n" +
		"  tls:\n" +
		"    cert: \"/tmp/test-cert.pem\"\n" +
		"    key: \"\"\n" +
		"    ca: \"\"\n" +
		"  shared_secret:\n" +
		"    header: \"authorization\"\n" +
		"    token: \"token\"\n"
	path := writeTempConfig(t, cfg)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to reject missing client key")
	}
}
