package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

type e2eFixture struct {
	Checks []struct {
		Name string `yaml:"name"`
	} `yaml:"checks"`
}

func TestAuditSmokePlan(t *testing.T) {
	_, filename, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(filename), "..", "..", "ops", "audit", "e2e.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read e2e plan: %v", err)
	}
	var fixture e2eFixture
	if err := yaml.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode e2e plan: %v", err)
	}
	if len(fixture.Checks) == 0 {
		t.Fatal("expected at least one e2e check defined")
	}
}
