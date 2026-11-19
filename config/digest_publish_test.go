package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func capturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// At start-up a node logs the root and the hash of every section, and leaves the same
// text in its data directory, so an operator can put the two validators' lines side
// by side (nhb-snapshot check-parity) before the first block that would tell them.
func TestPublishConsensusDigestLogsAndWritesTheFile(t *testing.T) {
	cfg := loadShipped(t)
	dir := t.TempDir()
	logger, logs := capturedLogger()

	d, err := cfg.PublishConsensusDigest(logger, noEnv, dir)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	want := mustDigest(t, cfg, noEnv)
	if d.Root != want.Root {
		t.Fatalf("published root %s, want %s", d.Root, want.Root)
	}
	written, err := os.ReadFile(filepath.Join(dir, ConsensusDigestFileName))
	if err != nil {
		t.Fatalf("the digest file was not written: %v", err)
	}
	parsed, err := ParseConsensusDigest(string(written))
	if err != nil || parsed.Root != want.Root {
		t.Fatalf("the file does not hold the digest: %v %q", err, written)
	}
	out := logs.String()
	if !strings.Contains(out, "Consensus settings digest") || !strings.Contains(out, want.Root) {
		t.Fatalf("no start-up line with the root:\n%s", out)
	}
	for _, s := range want.Sections {
		if !strings.Contains(out, s.Name) || !strings.Contains(out, s.Digest) {
			t.Errorf("section %s is not in the start-up line", s.Name)
		}
	}
	if strings.Contains(out, "level=ERROR") || strings.Contains(out, "level=WARN") {
		t.Fatalf("a clean config logged a warning:\n%s", out)
	}
}

// NHB_MASTER_TREASURY replaces the genesis admin wallet silently on the node that has
// it set (PL-DC-04). The node still starts, but says so in the log at error level.
func TestPublishConsensusDigestShoutsWhenTheTreasuryOverrideIsSet(t *testing.T) {
	cfg := loadShipped(t)
	logger, logs := capturedLogger()
	env := func(k string) string {
		if k == "NHB_MASTER_TREASURY" {
			return "nhb1someaddressthatmustnotbelogged"
		}
		return ""
	}
	if _, err := cfg.PublishConsensusDigest(logger, env, t.TempDir()); err != nil {
		t.Fatalf("publish refused to start the node: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "NHB_MASTER_TREASURY") {
		t.Fatalf("an override in force was not reported at error level:\n%s", out)
	}
	if strings.Contains(out, "nhb1someaddressthatmustnotbelogged") {
		t.Fatalf("the override's value was logged:\n%s", out)
	}
}

// A data directory that cannot be written is a warning, never a reason not to start.
func TestPublishConsensusDigestSurvivesAnUnwritableDirectory(t *testing.T) {
	cfg := loadShipped(t)
	logger, logs := capturedLogger()
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	d, err := cfg.PublishConsensusDigest(logger, noEnv, missing)
	if err != nil {
		t.Fatalf("an unwritable directory stopped the node: %v", err)
	}
	if d.Root == "" {
		t.Fatalf("no digest returned")
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("no warning for the file that could not be written:\n%s", logs.String())
	}
}
