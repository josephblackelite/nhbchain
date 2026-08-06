package main

import (
	"strings"
	"testing"
)

func TestResolveGenesisPathPrecedence(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key != genesisPathEnv {
			t.Fatalf("unexpected lookup key: %s", key)
		}
		return "env-path", true
	}

	t.Run("cli flag takes precedence", func(t *testing.T) {
		path, err := resolveGenesisPath("cli-path", "cfg-path", true, lookup)
		if err != nil {
			t.Fatalf("resolveGenesisPath returned error: %v", err)
		}
		if path != "cli-path" {
			t.Fatalf("unexpected path: got %q want %q", path, "cli-path")
		}
	})

	t.Run("environment overrides config", func(t *testing.T) {
		path, err := resolveGenesisPath("", "cfg-path", true, lookup)
		if err != nil {
			t.Fatalf("resolveGenesisPath returned error: %v", err)
		}
		if path != "env-path" {
			t.Fatalf("unexpected path: got %q want %q", path, "env-path")
		}
	})

	t.Run("config used when no other sources", func(t *testing.T) {
		emptyLookup := func(string) (string, bool) { return "", false }
		path, err := resolveGenesisPath("", "cfg-path", true, emptyLookup)
		if err != nil {
			t.Fatalf("resolveGenesisPath returned error: %v", err)
		}
		if path != "cfg-path" {
			t.Fatalf("unexpected path: got %q want %q", path, "cfg-path")
		}
	})
}

func TestResolveGenesisPathErrorWhenRequired(t *testing.T) {
	emptyLookup := func(string) (string, bool) { return "", false }
	if _, err := resolveGenesisPath("", "", false, emptyLookup); err == nil {
		t.Fatalf("expected error when no genesis sources available and autogenesis disabled")
	} else if !strings.Contains(err.Error(), "no genesis file provided") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveGenesisPathTrimsValues(t *testing.T) {
	emptyLookup := func(string) (string, bool) { return "  \t ", true }
	path, err := resolveGenesisPath("  cli  ", " cfg ", true, emptyLookup)
	if err != nil {
		t.Fatalf("resolveGenesisPath returned error: %v", err)
	}
	if path != "cli" {
		t.Fatalf("expected trimmed CLI path, got %q", path)
	}

	path, err = resolveGenesisPath("", " cfg ", true, emptyLookup)
	if err != nil {
		t.Fatalf("resolveGenesisPath returned error: %v", err)
	}
	if path != "cfg" {
		t.Fatalf("expected trimmed config path, got %q", path)
	}
}

func TestResolveAllowAutogenesisPrecedence(t *testing.T) {
	lookup := func(key string) (string, bool) {
		if key != allowAutogenesisEnv {
			t.Fatalf("unexpected lookup key: %s", key)
		}
		return "true", true
	}

	t.Run("cli override takes priority", func(t *testing.T) {
		allow, err := resolveAllowAutogenesis(false, true, false, lookup)
		if err != nil {
			t.Fatalf("resolveAllowAutogenesis returned error: %v", err)
		}
		if allow {
			t.Fatalf("expected cli override to force disable autogenesis")
		}
	})

	t.Run("env overrides config when cli absent", func(t *testing.T) {
		allow, err := resolveAllowAutogenesis(false, false, false, lookup)
		if err != nil {
			t.Fatalf("resolveAllowAutogenesis returned error: %v", err)
		}
		if !allow {
			t.Fatalf("expected env override to enable autogenesis")
		}
	})

	t.Run("config used when nothing else set", func(t *testing.T) {
		emptyLookup := func(string) (string, bool) { return "", false }
		allow, err := resolveAllowAutogenesis(true, false, false, emptyLookup)
		if err != nil {
			t.Fatalf("resolveAllowAutogenesis returned error: %v", err)
		}
		if !allow {
			t.Fatalf("expected config value to be respected")
		}
	})
}

func TestResolveAllowAutogenesisRejectsInvalidEnv(t *testing.T) {
	lookup := func(string) (string, bool) { return "definitely-not-bool", true }
	if _, err := resolveAllowAutogenesis(false, false, false, lookup); err == nil {
		t.Fatalf("expected error for invalid %s value", allowAutogenesisEnv)
	}
}
