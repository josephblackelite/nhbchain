package config

// RPCSwapAuth.PartnerProviders says which providers' vouchers each partner key
// may read back (PL-EXT-32). A line that names a key the node does not know, or
// lists no provider, is a mistake that would silently leave a partner reading
// nothing or, worse, being thought scoped when it is not, so it stops the node
// at start-up.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWithSwapAuth(t *testing.T, swapAuthTOML string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	keystorePath := filepath.Join(dir, "validator.keystore")
	contents := fmt.Sprintf("ListenAddress = \"0.0.0.0:6001\"\nValidatorKeystorePath = %s\n\n[RPCSwapAuth]\n%s", tomlQuoted(keystorePath), swapAuthTOML)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Load(path, WithKeystorePassphrase(testKeystorePassphrase))
}

func TestPartnerProvidersAreReadFromTheFile(t *testing.T) {
	cfg, err := loadWithSwapAuth(t, `Secrets = { alpha = "s1", beta = "s2" }
PartnerProviders = { alpha = ["alpha-pay"], beta = ["beta-pay", "beta-cards"] }
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := cfg.RPCSwapAuth.PartnerProviders
	if len(got["alpha"]) != 1 || got["alpha"][0] != "alpha-pay" || len(got["beta"]) != 2 {
		t.Fatalf("PartnerProviders = %v", got)
	}
}

func TestPartnerProvidersAreOptional(t *testing.T) {
	cfg, err := loadWithSwapAuth(t, `Secrets = { alpha = "s1" }
`)
	if err != nil {
		t.Fatalf("a config with partner keys and no PartnerProviders must still load (the key then reads no voucher): %v", err)
	}
	if len(cfg.RPCSwapAuth.PartnerProviders) != 0 {
		t.Fatalf("PartnerProviders = %v, want none", cfg.RPCSwapAuth.PartnerProviders)
	}
}

func TestPartnerProvidersMayNameTheWildcard(t *testing.T) {
	if _, err := loadWithSwapAuth(t, `Secrets = { ledger = "s1" }
PartnerProviders = { ledger = ["*"] }
`); err != nil {
		t.Fatalf("a key mapped to the wildcard must load: %v", err)
	}
}

func TestPartnerProvidersRefuseAKeyWithNoSecret(t *testing.T) {
	_, err := loadWithSwapAuth(t, `Secrets = { alpha = "s1" }
PartnerProviders = { alhpa = ["alpha-pay"] }
`)
	if err == nil {
		t.Fatalf("a PartnerProviders key that is not in Secrets (a typo) must stop the node")
	}
	if !strings.Contains(err.Error(), "alhpa") || !strings.Contains(err.Error(), "Secrets") {
		t.Fatalf("the error should name the key and say it is not in Secrets: %v", err)
	}
}

func TestPartnerProvidersRefuseAnEmptyList(t *testing.T) {
	for name, line := range map[string]string{
		"no providers":        `PartnerProviders = { alpha = [] }`,
		"only blank names":    `PartnerProviders = { alpha = ["  ", ""] }`,
		"a blank next to one": `PartnerProviders = { alpha = ["alpha-pay", " "] }`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadWithSwapAuth(t, "Secrets = { alpha = \"s1\" }\n"+line+"\n")
			if err == nil {
				t.Fatalf("%s: a provider list with an empty name must stop the node (it would read as 'no scope')", name)
			}
			if !strings.Contains(err.Error(), "alpha") {
				t.Fatalf("the error should name the key: %v", err)
			}
		})
	}
}

func TestPartnerProvidersErrorNeverPrintsASecret(t *testing.T) {
	_, err := loadWithSwapAuth(t, `Secrets = { alpha = "do-not-print-this-secret" }
PartnerProviders = { nokey = ["x"] }
`)
	if err == nil {
		t.Fatalf("expected an error")
	}
	if strings.Contains(err.Error(), "do-not-print-this-secret") {
		t.Fatalf("the error prints a secret: %v", err)
	}
}
