package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/check_empty_commits.sh fails when a commit in the history changes nothing.
// A commit whose message describes work but whose diff is empty cannot be trusted as
// evidence that the work was done (PL-R8-EMPTYCOMMITS). Three such commits are already
// in the public history; they are listed in scripts/known-empty-commits.txt and do
// not fail the check.

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newScratchRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func runCheck(t *testing.T, bash, repo string, extraEnv ...string) (string, int) {
	t.Helper()
	script := bashArg(repoFile(t, "scripts/check_empty_commits.sh"))
	return runBash(t, bash, repo, extraEnv, script)
}

func TestEmptyCommitCheckPassesOnARealHistory(t *testing.T) {
	bash := bashPath(t)
	repo := newScratchRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-q", "-am", "second")
	if out, code := runCheck(t, bash, repo); code != 0 {
		t.Fatalf("a history of real changes was refused (exit %d):\n%s", code, out)
	}
}

func TestEmptyCommitCheckRefusesAnEmptyCommitAndNamesIt(t *testing.T) {
	bash := bashPath(t)
	repo := newScratchRepo(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "Remove the last service name from the examples")
	sha := git(t, repo, "rev-parse", "HEAD")
	out, code := runCheck(t, bash, repo)
	if code == 0 {
		t.Fatalf("an empty commit was accepted:\n%s", out)
	}
	if !strings.Contains(out, sha[:12]) || !strings.Contains(out, "Remove the last service name") {
		t.Fatalf("the output must name the commit and its message, got:\n%s", out)
	}
}

func TestEmptyCommitCheckSkipsMergesAndKnownCommits(t *testing.T) {
	bash := bashPath(t)
	repo := newScratchRepo(t)
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "known empty")
	sha := git(t, repo, "rev-parse", "HEAD")
	allow := filepath.Join(t.TempDir(), "known.txt")
	if err := os.WriteFile(allow, []byte("# known\n"+sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runCheck(t, bash, repo, "KNOWN_EMPTY_COMMITS="+bashArg(allow)); code != 0 {
		t.Fatalf("a listed commit was refused (exit %d):\n%s", code, out)
	}
	// An abbreviated or altered entry does not match.
	if err := os.WriteFile(allow, []byte(sha[:12]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runCheck(t, bash, repo, "KNOWN_EMPTY_COMMITS="+bashArg(allow)); code == 0 {
		t.Fatalf("an abbreviated entry must not exempt a commit: list full 40-character ids")
	}

	// A merge commit that changes nothing of its own is not an empty commit.
	repo2 := newScratchRepo(t)
	git(t, repo2, "checkout", "-q", "-b", "side")
	if err := os.WriteFile(filepath.Join(repo2, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo2, "add", "b.txt")
	git(t, repo2, "commit", "-q", "-m", "side change")
	git(t, repo2, "checkout", "-q", "-")
	if err := os.WriteFile(filepath.Join(repo2, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo2, "add", "c.txt")
	git(t, repo2, "commit", "-q", "-m", "main change")
	git(t, repo2, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	if out, code := runCheck(t, bash, repo2); code != 0 {
		t.Fatalf("a merge commit was treated as empty (exit %d):\n%s", code, out)
	}
}

// The three commits already in the public history are listed by full id, and they
// really are empty (so the list is not exempting anything that changes something).
func TestKnownEmptyCommitListIsExactlyTheEmptyCommitsInThisRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	data, err := os.ReadFile(repoFile(t, "scripts/known-empty-commits.txt"))
	if err != nil {
		t.Fatal(err)
	}
	root := repoFile(t, ".")
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) != 40 {
			t.Errorf("%q is not a full 40-character commit id", line)
			continue
		}
		cmd := exec.Command("git", "cat-file", "-t", line)
		cmd.Dir = root
		if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "commit" {
			// This checkout may be a shallow clone that does not hold it.
			continue
		}
		changed := git(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", line)
		if changed != "" {
			t.Errorf("%s is on the known-empty list but changes files: %s", line, changed)
		}
	}
}
