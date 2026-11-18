package main

import (
	"os"
	"path/filepath"
	"testing"
)

func specs(names ...string) []checkSpec {
	out := make([]checkSpec, 0, len(names))
	for _, n := range names {
		out = append(out, checkSpec{Name: n, Severity: "high"})
	}
	return out
}

// A report built from checks nobody ran must not look like a passing audit: the
// tool used to mark every check "pending" forever and said nothing about the
// result (PL-DC-27).
func TestChecksWithoutResultsAreNotRunAndGiveNoVerdict(t *testing.T) {
	checks := buildChecks(specs("a", "b"), nil)
	for _, c := range checks {
		if c.Status != statusNotRun {
			t.Fatalf("check %s status = %q, want %q", c.Name, c.Status, statusNotRun)
		}
	}
	if got := verdictOf(checks); got != verdictNone {
		t.Fatalf("verdict = %q, want %q", got, verdictNone)
	}
}

func TestVerdictFollowsTheRecordedResults(t *testing.T) {
	cases := []struct {
		name    string
		results map[string]string
		want    string
	}{
		{"all passed", map[string]string{"a": "passed", "b": "passed"}, verdictPass},
		{"one failed", map[string]string{"a": "passed", "b": "failed"}, verdictFail},
		{"a failure beats a missing result", map[string]string{"a": "failed"}, verdictFail},
		{"one not recorded", map[string]string{"a": "passed"}, verdictPartial},
		{"skipped is not a pass", map[string]string{"a": "passed", "b": "skipped"}, verdictPartial},
	}
	for _, tc := range cases {
		checks := buildChecks(specs("a", "b"), tc.results)
		if got := verdictOf(checks); got != tc.want {
			t.Errorf("%s: verdict = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnknownStatusWordsAndUnknownChecksAreRefused(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "r.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := loadResults(write(`{"a":"ok"}`), specs("a")); err == nil {
		t.Fatalf(`status "ok" must be refused: only passed, failed and skipped are valid`)
	}
	if _, err := loadResults(write(`{"zzz":"passed"}`), specs("a")); err == nil {
		t.Fatalf("a result for a check the config does not list must be refused")
	}
	got, err := loadResults(write(`{"a":"passed"}`), specs("a"))
	if err != nil || got["a"] != "passed" {
		t.Fatalf("valid results rejected: %v %v", got, err)
	}
}

func TestStrictModeRequiresAPassVerdict(t *testing.T) {
	if exitCodeFor(verdictPass, true) != 0 {
		t.Fatalf("a pass verdict must exit 0 in strict mode")
	}
	for _, v := range []string{verdictNone, verdictPartial, verdictFail} {
		if exitCodeFor(v, true) == 0 {
			t.Fatalf("verdict %q must not exit 0 in strict mode", v)
		}
		if exitCodeFor(v, false) != 0 {
			t.Fatalf("without -strict the tool only reports, verdict %q", v)
		}
	}
}
