package ledger

import (
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

type supplyFixture struct {
	Supply struct {
		Minted      string `yaml:"minted"`
		Burned      string `yaml:"burned"`
		Circulating string `yaml:"circulating"`
	} `yaml:"supply"`
}

func TestSupplyFixtureBalances(t *testing.T) {
	_, filename, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(filename), "..", "..", "ops", "audit", "supply.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read supply fixture: %v", err)
	}
	var fixture supplyFixture
	if err := yaml.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode supply fixture: %v", err)
	}
	minted, ok := new(big.Int).SetString(fixture.Supply.Minted, 10)
	if !ok {
		t.Fatalf("invalid minted amount %s", fixture.Supply.Minted)
	}
	burned, ok := new(big.Int).SetString(fixture.Supply.Burned, 10)
	if !ok {
		t.Fatalf("invalid burned amount %s", fixture.Supply.Burned)
	}
	circulating, ok := new(big.Int).SetString(fixture.Supply.Circulating, 10)
	if !ok {
		t.Fatalf("invalid circulating amount %s", fixture.Supply.Circulating)
	}
	diff := new(big.Int).Sub(minted, burned)
	if diff.Cmp(circulating) != 0 {
		t.Fatalf("circulating mismatch: minted-burned=%s circulating=%s", diff.String(), circulating.String())
	}
}
