package scripts_test

// scripts/bugcheck.sh runs the pre-bounty checks and reports whether they
// passed. It used to record a check that failed as passed (it read $? after
// "if ! cmd", which is the negated status) and to stop on a top-level "local"
// before it wrote its verdict, so it could not give a real one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// fakeMake fails the target named in FAKE_MAKE_FAIL with exit code 3 and
	// succeeds for every other.
	fakeMake = `#!/usr/bin/env bash
if [ "$1" = "${FAKE_MAKE_FAIL:-}" ]; then
  echo "fake failure of $1"
  exit 3
fi
echo "fake $1 ok"
`
	fakeVerify = `#!/usr/bin/env bash
echo "fake verify_prod_config ok"
`
	pythonShim = `#!/usr/bin/env bash
exec python "$@"
`
)

// bugcheckRun runs a copy of the script in a scratch repository whose make and
// verify_prod_config.sh are fakes, and returns the output, the exit code and the
// repository directory.
func bugcheckRun(t *testing.T, failTarget string) (string, int, string) {
	t.Helper()
	bash := bashPath(t)
	repo := t.TempDir()
	scripts := filepath.Join(repo, "scripts")
	bin := filepath.Join(repo, "fakebin")
	for _, dir := range []string{scripts, bin, filepath.Join(repo, "config")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	script, err := os.ReadFile(repoFile(t, "scripts/bugcheck.sh"))
	if err != nil {
		t.Fatalf("read bugcheck.sh: %v", err)
	}
	files := map[string]string{
		filepath.Join(scripts, "bugcheck.sh"):           string(script),
		filepath.Join(scripts, "verify_prod_config.sh"): fakeVerify,
		filepath.Join(bin, "make"):                      fakeMake,
		filepath.Join(repo, "config", "prod.toml"):      "",
	}
	if _, code := runBash(t, bash, repo, nil, "-c", "command -v python3 >/dev/null 2>&1"); code != 0 {
		if _, code := runBash(t, bash, repo, nil, "-c", "command -v python >/dev/null 2>&1"); code != 0 {
			t.Skip("python is not available (the script escapes its JSON with it)")
		}
		files[filepath.Join(bin, "python3")] = pythonShim
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	env := []string{"PATH=" + bashArg(bin) + string(os.PathListSeparator) + os.Getenv("PATH"), "FAKE_MAKE_FAIL=" + failTarget}
	out, code := runBash(t, bash, repo, env, bashArg(filepath.Join(scripts, "bugcheck.sh")))
	return out, code, repo
}

func readBugcheckOutputs(t *testing.T, repo string) (markdown, summary string) {
	t.Helper()
	mds, _ := filepath.Glob(filepath.Join(repo, "audit", "bugcheck-*.md"))
	sums, _ := filepath.Glob(filepath.Join(repo, "artifacts", "bugcheck-*", "summary.json"))
	if len(mds) != 1 || len(sums) != 1 {
		t.Fatalf("expected one markdown report and one summary, found %v and %v", mds, sums)
	}
	md, err := os.ReadFile(mds[0])
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	sum, err := os.ReadFile(sums[0])
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	return string(md), string(sum)
}

func TestBugcheckReportsAFailedCheckAsFailedAndExitsNonZero(t *testing.T) {
	out, code, repo := bugcheckRun(t, "bugcheck-race")
	if code == 0 {
		t.Fatalf("the run exited 0 although the race check failed:\n%s", out)
	}
	md, summary := readBugcheckOutputs(t, repo)
	var row string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| race-tests |") {
			row = line
		}
	}
	if !strings.Contains(row, "| failed |") {
		t.Fatalf("the race check's row is %q, want it marked failed", row)
	}
	if !strings.Contains(summary, `"status":"failed"`) || !strings.Contains(summary, "Exit code 3") {
		t.Fatalf("the summary does not record the failure and its exit code:\n%s", summary)
	}
	if !strings.Contains(out, "failed") {
		t.Fatalf("no verdict was printed:\n%s", out)
	}
}

func TestBugcheckExitsZeroWhenEveryCheckPasses(t *testing.T) {
	out, code, repo := bugcheckRun(t, "")
	if code != 0 {
		t.Fatalf("the run exited %d although every check passed:\n%s", code, out)
	}
	md, summary := readBugcheckOutputs(t, repo)
	if strings.Contains(md, "| failed |") || strings.Contains(md, "| missing |") {
		t.Fatalf("a check is marked failed or missing:\n%s", md)
	}
	if !strings.Contains(summary, `"status":"passed"`) || strings.Count(summary, `"status":"passed"`) < 11 {
		t.Fatalf("the summary does not record 10 passed checks and a passed run:\n%s", summary)
	}
}
