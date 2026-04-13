package payoutd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigResolvesWalletSignerFromEnv(t *testing.T) {
	const (
		signerEnv = "TEST_PAYOUTD_WALLET_KEY"
		signerKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	t.Setenv(signerEnv, signerKey)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	contents := "" +
		"listen: \":7082\"\n" +
		"policies: \"services/payoutd/policies.yaml\"\n" +
		"authority: \"nhb1authority\"\n" +
		"consensus:\n" +
		"  endpoint: \"127.0.0.1:9090\"\n" +
		"  chain_id: \"localnet\"\n" +
		"  signer_key: \"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\"\n" +
		"wallet:\n" +
		"  rpc_url: \"https://rpc.example\"\n" +
		"  chain_id: \"11155111\"\n" +
		"  signer_key_env: \"" + signerEnv + "\"\n" +
		"  from_address: \"0xFCAd0B19bB29D4674531d6f115237E16AfCE377c\"\n" +
		"  assets:\n" +
		"    - symbol: \"USDC\"\n" +
		"      token_address: \"0xA0b86991c6218b36c1d19d4a2e9eb0ce3606eb48\"\n" +
		"admin:\n" +
		"  bearer_token: \"secret\"\n" +
		"  tls:\n" +
		"    disable: true\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Wallet.SignerKey != signerKey {
		t.Fatalf("unexpected wallet signer key: %q", cfg.Wallet.SignerKey)
	}
	if got := cfg.Wallet.Assets[0].Symbol; got != "USDC" {
		t.Fatalf("unexpected asset symbol %q", got)
	}
}

func TestLoadConfigRejectsMissingWalletAssets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	contents := "" +
		"listen: \":7082\"\n" +
		"policies: \"services/payoutd/policies.yaml\"\n" +
		"authority: \"nhb1authority\"\n" +
		"consensus:\n" +
		"  endpoint: \"127.0.0.1:9090\"\n" +
		"  chain_id: \"localnet\"\n" +
		"  signer_key: \"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\"\n" +
		"wallet:\n" +
		"  rpc_url: \"https://rpc.example\"\n" +
		"  chain_id: \"11155111\"\n" +
		"  signer_key: \"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n" +
		"admin:\n" +
		"  bearer_token: \"secret\"\n" +
		"  tls:\n" +
		"    disable: true\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadConfig(configPath); err == nil {
		t.Fatalf("expected wallet validation error")
	}
}
