package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"nhbchain/config"
)

// loadedShippedConfig returns the path of a copy of the shipped config.toml whose
// validator keystore already exists, so a tool that loads it needs no passphrase.
func parityShippedConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.toml"))
	if err != nil {
		t.Fatalf("read shipped config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("copy config: %v", err)
	}
	if _, err := config.Load(path, config.WithKeystorePassphrase("parity-test-pass")); err != nil {
		t.Fatalf("first load (creates the keystore): %v", err)
	}
	return path
}

func parityRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestConfigDigestCommandPrintsTheDigest(t *testing.T) {
	path := parityShippedConfig(t)
	code, out, errOut := parityRun(t, "config-digest", "--config", path)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	d, err := config.ParseConsensusDigest(out)
	if err != nil {
		t.Fatalf("output is not a digest: %v\n%s", err, out)
	}
	if d.Root == "" || len(d.Sections) == 0 {
		t.Fatalf("empty digest: %s", out)
	}
}

func TestCheckParityAgreesOnTwoCopiesOfTheSameConfig(t *testing.T) {
	path := parityShippedConfig(t)
	_, out, _ := parityRun(t, "config-digest", "--config", path)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, stdout, errOut := parityRun(t, "check-parity", "--a", a, "--b", b)
	if code != 0 || !strings.Contains(stdout, "ok") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, errOut)
	}
}

// Two validators whose settings differ are refused, and the sections that differ are
// named so the operator knows where to look.
func TestCheckParityNamesWhatDiffers(t *testing.T) {
	path := parityShippedConfig(t)
	cfgA, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfgB, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfgB.Global.Blocks.MaxTxs += 1
	cfgB.PotsoGovernanceWeightActivationHeight = 1_900_000
	cfgB.Consensus.CommitTimeout += 1
	da, _ := cfgA.ConsensusDigest(func(string) string { return "" })
	db, _ := cfgB.ConsensusDigest(func(string) string { return "" })
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte(da.Text()), 0o644)
	os.WriteFile(b, []byte(db.Text()), 0o644)

	code, _, errOut := parityRun(t, "check-parity", "--a", a, "--b", b)
	if code != 1 {
		t.Fatalf("differing validators passed, exit %d", code)
	}
	for _, want := range []string{"chain-rules", "global.blocks"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the section %q is not named in:\n%s", want, errOut)
		}
	}
}

// A difference in the round timers alone changes liveness, not state: it is reported
// and does not fail the check.
func TestCheckParityReportsTimingButPasses(t *testing.T) {
	path := parityShippedConfig(t)
	cfgA, _ := config.Load(path)
	cfgB, _ := config.Load(path)
	cfgB.Consensus.CommitTimeout += 1
	da, _ := cfgA.ConsensusDigest(func(string) string { return "" })
	db, _ := cfgB.ConsensusDigest(func(string) string { return "" })
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte(da.Text()), 0o644)
	os.WriteFile(b, []byte(db.Text()), 0o644)
	code, stdout, errOut := parityRun(t, "check-parity", "--a", a, "--b", b)
	if code != 0 {
		t.Fatalf("a timing-only difference failed the check: exit %d %s", code, errOut)
	}
	if !strings.Contains(stdout, "bft-timing") {
		t.Fatalf("the timing difference was not reported:\n%s", stdout)
	}
}

// A hand-edited file does not pass for a digest.
func TestCheckParityRefusesAForgedFile(t *testing.T) {
	path := parityShippedConfig(t)
	_, out, _ := parityRun(t, "config-digest", "--config", path)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte(out), 0o644)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "chain-rules ") {
			lines[i] = "chain-rules " + strings.Repeat("0", 64)
		}
	}
	os.WriteFile(b, []byte(strings.Join(lines, "\n")), 0o644)
	if code, _, errOut := parityRun(t, "check-parity", "--a", a, "--b", b); code == 0 {
		t.Fatalf("a doctored file passed: %s", errOut)
	}
}

func TestCheckParityNeedsBothFiles(t *testing.T) {
	if code, _, _ := parityRun(t, "check-parity", "--a", "x"); code != 2 {
		t.Fatalf("exit %d, want the usage code 2", code)
	}
	if code, _, _ := parityRun(t, "check-parity", "--a", filepath.Join(t.TempDir(), "no.txt"), "--b", filepath.Join(t.TempDir(), "no2.txt")); code != 1 {
		t.Fatalf("missing files: exit %d, want 1", code)
	}
}

// Every setting the snapshot tool pins off (liveChainRules) is a chain rule two
// validators must carry alike, so every one must move the digest. A rule pinned
// here and missing from the digest would be a rule nothing compares between
// validators; a rule added to the digest and not pinned would be one a follower
// is not held to.
func TestEveryPinnedChainRuleMovesTheDigest(t *testing.T) {
	base := parityShippedConfig(t)
	raw, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	baseCfg, err := config.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := baseCfg.ConsensusDigest(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range liveChainRules {
		pattern := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(rule.key) + `(\s*=\s*)\d+(\s*)$`)
		if n := len(pattern.FindAllIndex(raw, -1)); n != 1 {
			t.Fatalf("%s: expected one line in the shipped config.toml, found %d", rule.key, n)
		}
		changed := pattern.ReplaceAll(raw, []byte("${1}"+rule.key+"${2}1800000${3}"))
		path := filepath.Join(filepath.Dir(base), "changed.toml")
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("%s: %v", rule.key, err)
		}
		d, err := cfg.ConsensusDigest(func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if d.Root == baseDigest.Root {
			t.Errorf("%s is pinned off by the snapshot tool but does not move the consensus digest", rule.key)
		}
	}
}
