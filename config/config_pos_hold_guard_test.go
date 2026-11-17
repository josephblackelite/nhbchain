package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The POS hold unstake guard is a chain rule, so it stays off unless a height is
// written into the file.
func TestLoadPOSHoldUnstakeGuardActivationHeight(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want uint64
	}{
		{"absent leaves the guard off", "", 0},
		{"explicit zero leaves the guard off", "POSHoldUnstakeGuardActivationHeight = 0\n", 0},
		{"a height turns it on from there", "POSHoldUnstakeGuardActivationHeight = 451949\n", 451949},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			keystorePath := filepath.Join(dir, "validator.keystore")
			contents := fmt.Sprintf("ListenAddress = \"0.0.0.0:6001\"\nValidatorKeystorePath = %s\n%s", tomlQuoted(keystorePath), tc.line)
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if cfg.POSHoldUnstakeGuardActivationHeight != tc.want {
				t.Fatalf("POSHoldUnstakeGuardActivationHeight = %d, want %d", cfg.POSHoldUnstakeGuardActivationHeight, tc.want)
			}
		})
	}
}
