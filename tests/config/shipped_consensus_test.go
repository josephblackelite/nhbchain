package config_test

// The consensus timing the shipped config.toml carries, and what a config like
// the live validators' (the four round timers written as "0s", a minimum block
// interval of 2s) makes of the node's own checks.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

// loadShippedConfigWith loads config.toml with each pattern replaced, keeping
// the validator key in the environment so the test touches no keystore.
func loadShippedConfigWith(t *testing.T, replacements map[string]string) *config.Config {
	t.Helper()
	content := string(readRepoFile(t, "config.toml"))
	if strings.Count(content, `ValidatorKMSEnv = ""`) != 1 {
		t.Fatalf("expected exactly one empty ValidatorKMSEnv in config.toml")
	}
	content = strings.Replace(content, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_TEST_VALIDATOR_KEY"`, 1)
	for pattern, replacement := range replacements {
		re := regexp.MustCompile(pattern)
		if !re.MatchString(content) {
			t.Fatalf("config.toml has nothing that matches %q", pattern)
		}
		content = re.ReplaceAllString(content, replacement)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// consensusd refuses a round timer that is not positive, so the shipped file must
// say what it means: the timers at the values the engine defaults to, and the
// minimum block interval the live validators run with.
func TestShippedConsensusTimingIsWrittenOutAndValid(t *testing.T) {
	cfg := loadShippedConfigWith(t, nil)
	if err := config.ValidateConsensus(cfg.Consensus); err != nil {
		t.Fatalf("config.toml [consensus] as written: %v", err)
	}
	want := config.Consensus{
		ProposalTimeout:  2 * time.Second,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: 2 * time.Second,
	}
	if cfg.Consensus != want {
		t.Fatalf("config.toml [consensus] is %+v, want %+v", cfg.Consensus, want)
	}
}
