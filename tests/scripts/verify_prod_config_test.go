package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyConfig runs scripts/verify_prod_config.sh on a config file.
func verifyConfig(t *testing.T, bash, configPath string) (string, int) {
	t.Helper()
	root := repoFile(t, ".")
	script := bashArg(repoFile(t, "scripts/verify_prod_config.sh"))
	return runBash(t, bash, root, nil, script, "-c", bashArg(configPath))
}

func requireTOMLPython(t *testing.T, bash string) {
	t.Helper()
	_, code := runBash(t, bash, ".", nil, "-c", "python3 -c 'import tomllib' || python3 -c 'import tomli'")
	if code != 0 {
		t.Skip("python3 with a TOML reader is not available")
	}
}

// mutate replaces exactly one occurrence of old, so a test cannot silently
// stop exercising the line it means to change.
func mutate(t *testing.T, content, old, replacement string) string {
	t.Helper()
	if strings.Count(content, old) != 1 {
		t.Fatalf("expected exactly one occurrence of %q in the template", old)
	}
	return strings.Replace(content, old, replacement, 1)
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// The production check must accept every configuration the repository ships:
// config.toml (plaintext RPC on loopback behind a TLS-terminating proxy) and
// config/prod.toml (TLS terminated by the node).
func TestVerifyProdConfigAcceptsTheShippedConfigs(t *testing.T) {
	bash := bashPath(t)
	requireTOMLPython(t, bash)
	for _, rel := range []string{"config.toml", "config/prod.toml"} {
		out, code := verifyConfig(t, bash, repoFile(t, rel))
		if code != 0 {
			t.Fatalf("verify_prod_config.sh rejected %s (exit %d):\n%s", rel, code, out)
		}
	}
}

func TestVerifyProdConfigRejectsUnsafeVariants(t *testing.T) {
	bash := bashPath(t)
	requireTOMLPython(t, bash)
	raw, err := os.ReadFile(repoFile(t, "config/prod.toml"))
	if err != nil {
		t.Fatalf("read prod.toml: %v", err)
	}
	prod := string(raw)

	insecure := mutate(t, prod, "RPCAllowInsecure = false", "RPCAllowInsecure = true")
	tlsKeys := `RPCAllowInsecure = false
RPCTLSCertFile = "/etc/nhb/rpc/rpc.crt"
RPCTLSKeyFile = "/etc/nhb/rpc/rpc.key"
RPCTLSClientCAFile = "/etc/nhb/rpc/clients-ca.pem"
`
	movedBelowTable := mutate(t, mutate(t, prod, tlsKeys, ""), "MaxTransactions = 5000\n", "MaxTransactions = 5000\n"+tlsKeys)

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "plaintext RPC on a wildcard address",
			content: mutate(t, insecure, `RPCAddress = "127.0.0.1:8080"`, `RPCAddress = "0.0.0.0:8080"`),
			want:    "RPCAllowInsecure may only be true when RPCAddress is a loopback address",
		},
		{
			name:    "plaintext RPC on a routable address",
			content: mutate(t, insecure, `RPCAddress = "127.0.0.1:8080"`, `RPCAddress = "10.1.2.3:8080"`),
			want:    "RPCAllowInsecure may only be true when RPCAddress is a loopback address",
		},
		{
			name:    "plaintext RPC with the unspecified-address override",
			content: mutate(t, insecure, "RPCAllowInsecure = true", "RPCAllowInsecure = true\nRPCAllowInsecureUnspecified = true"),
			want:    "RPCAllowInsecureUnspecified must be false",
		},
		{
			name:    "RPC TLS keys below a table header, where the node does not read them",
			content: movedBelowTable,
			want:    "RPC TLS certificate path must be set",
		},
		{
			name:    "fee wallet spelled owner_wallet, which the loader ignores",
			content: strings.ReplaceAll(prod, "OwnerWallet =", "owner_wallet ="),
			want:    "global.Fees.OwnerWallet must be set",
		},
		{
			name:    "pro-rate enforcement off",
			content: mutate(t, prod, "EnforceProRate = true", "EnforceProRate = false"),
			want:    "global.Loyalty.Dynamic.EnforceProRate must be true",
		},
		{
			name:    "wildcard P2P listen address",
			content: mutate(t, prod, `ListenAddress = "192.0.2.10:6001"`, `ListenAddress = "0.0.0.0:6001"`),
			want:    "ListenAddress must not bind to an unspecified or wildcard address",
		},
		{
			name:    "a paused module",
			content: mutate(t, prod, "\nSwap = false\n", "\nSwap = true\n"),
			want:    "global.Pauses disables critical modules: Swap",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := verifyConfig(t, bash, writeConfig(t, tc.content))
			if code == 0 {
				t.Fatalf("expected the check to fail:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("expected %q in the output, got:\n%s", tc.want, out)
			}
		})
	}

	t.Run("plaintext RPC on loopback is accepted", func(t *testing.T) {
		if out, code := verifyConfig(t, bash, writeConfig(t, insecure)); code != 0 {
			t.Fatalf("expected plaintext RPC on a loopback address to pass (exit %d):\n%s", code, out)
		}
	})
}
