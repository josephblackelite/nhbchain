package hygiene

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func workflowFiles(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			out = append(out, ".github/workflows/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// A workflow must not install a tool at "@latest": the next release of that tool
// would run, unreviewed, with the workflow's token (PL-R5T-35).
func TestWorkflowsDoNotInstallAtLatest(t *testing.T) {
	for _, rel := range workflowFiles(t) {
		for i, line := range strings.Split(readRepoFile(t, rel), "\n") {
			if strings.Contains(line, "@latest") {
				t.Errorf("%s:%d installs a tool at @latest: %s", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	if strings.Contains(readRepoFile(t, "Makefile"), "buf/cmd/buf@latest") {
		t.Errorf("the Makefile installs buf at @latest")
	}
}

// The workflow-wide token is read-only. Write access to packages is given to the
// jobs that publish, by name, and to no other.
func TestWorkflowTokensAreReadOnlyExceptWherePublishing(t *testing.T) {
	publishing := map[string]bool{"build-images": true, "publish-charts": true}
	jobHeader := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	for _, rel := range workflowFiles(t) {
		lines := strings.Split(readRepoFile(t, rel), "\n")
		section, job := "", ""
		for i, line := range lines {
			if m := regexp.MustCompile(`^([a-z]+):`).FindStringSubmatch(line); m != nil {
				section, job = m[1], ""
			}
			if section == "jobs" {
				if m := jobHeader.FindStringSubmatch(line); m != nil {
					job = m[1]
				}
			}
			trimmed := strings.TrimSpace(line)
			if !strings.HasSuffix(trimmed, ": write") && trimmed != "permissions: write-all" {
				continue
			}
			if section == "permissions" {
				t.Errorf("%s:%d: the workflow-wide token has write access (%s)", rel, i+1, trimmed)
			} else if section == "jobs" && !(publishing[job] && trimmed == "packages: write") {
				t.Errorf("%s:%d: job %q has %s; only the publishing jobs may write packages", rel, i+1, job, trimmed)
			}
		}
	}
}

// Third-party actions should be pinned to a full commit SHA. The commit SHAs of
// the references below could not be looked up where this change was made (no
// network), so they are listed here by name: this is the debt, as a ratchet. A new
// unpinned reference fails the test, and so does a listed one that has been
// pinned (remove it from the list then).
var unpinnedActions = map[string]bool{
	"actions/checkout@v4":           true,
	"actions/setup-go@v5":           true,
	"actions/setup-node@v4":         true,
	"actions/upload-artifact@v4":    true,
	"docker/setup-qemu-action@v3":   true,
	"docker/setup-buildx-action@v3": true,
	"docker/metadata-action@v5":     true,
	"docker/login-action@v3":        true,
	"docker/build-push-action@v5":   true,
	"azure/setup-helm@v4":           true,
}

func TestActionsAreAPinnedCommitOrListedAsDebt(t *testing.T) {
	uses := regexp.MustCompile(`uses:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[A-Za-z0-9_.-]+)`)
	sha := regexp.MustCompile(`@[0-9a-f]{40}$`)
	seen := map[string]bool{}
	for _, rel := range workflowFiles(t) {
		for _, m := range uses.FindAllStringSubmatch(readRepoFile(t, rel), -1) {
			ref := m[1]
			if sha.MatchString(ref) {
				continue
			}
			seen[ref] = true
			if !unpinnedActions[ref] {
				t.Errorf("%s uses %s, a mutable tag that is not on the known-debt list: pin it to a commit SHA", rel, ref)
			}
		}
	}
	for ref := range unpinnedActions {
		if !seen[ref] {
			t.Errorf("%s is on the known-debt list but no workflow uses it unpinned any more: remove it from the list", ref)
		}
	}
}

// Dependabot keeps the pinned references current once they are pinned.
func TestDependabotWatchesTheActions(t *testing.T) {
	cfg := readRepoFile(t, ".github/dependabot.yml")
	if !strings.Contains(cfg, "package-ecosystem: \"github-actions\"") {
		t.Errorf(".github/dependabot.yml does not watch the github-actions ecosystem")
	}
}

// The old server setup script unpacked a Go tarball as root with no checksum,
// piped a remote script into a root shell and installed unpinned npm packages. It
// had no reference anywhere and has been removed; the validator installer pins and
// verifies its Go download.
func TestLegacyServerSetupScriptIsGone(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot(t), "server_setup.sh")); err == nil {
		t.Errorf("server_setup.sh is back: it pipes an unverified remote script into a root shell")
	}
}

// Every workflow states its token's permissions at the top, so a job that says
// nothing gets read access to the repository and no more.
func TestEveryWorkflowDeclaresAReadOnlyDefaultToken(t *testing.T) {
	for _, rel := range workflowFiles(t) {
		text := readRepoFile(t, rel)
		if !regexp.MustCompile(`(?m)^permissions:\s*\n\s+contents: read\s*$`).MatchString(text) {
			t.Errorf("%s has no top-level `permissions: contents: read`", rel)
		}
	}
}
