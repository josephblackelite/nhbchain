package hygiene

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The repository checks in this package must look at what the repository CONTAINS,
// not at whatever happens to lie in the folder: a work tree also holds untracked
// files (a package's report and logs in an outbox-* folder, scratch output, editor
// files), and a check that read them would fail on a clean commit's own CI only
// by luck of where it is run. repoFileList therefore asks git for the tracked
// files when the root is the top of a git work tree and falls back to a plain walk
// (which skips the folders that are never content) when it is not, for example in
// an unpacked archive.

// skippedWalkDir names folders a plain walk never enters.
func skippedWalkDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".claude", ".next", "vendor":
		return true
	}
	return strings.HasPrefix(name, "outbox-")
}

// repoFileList returns the absolute paths of the files under dir (relative to root;
// "" is the whole tree) that belong to the repository, sorted.
func repoFileList(t *testing.T, root, dir string) []string {
	t.Helper()
	if files, ok := gitTrackedFiles(root, dir); ok {
		return files
	}
	return walkedFiles(t, root, dir)
}

// gitEnv is the environment for git: the caller's, minus the variables that point
// git at some other repository.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "GIT_DIR="), strings.HasPrefix(kv, "GIT_WORK_TREE="), strings.HasPrefix(kv, "GIT_INDEX_FILE="):
			continue
		}
		env = append(env, kv)
	}
	return env
}

func gitTrackedFiles(root, dir string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = gitEnv()
		return cmd.Output()
	}
	// The prefix is empty only when root is the top of a work tree: an unpacked
	// archive inside some other repository must not be judged by that repository.
	prefix, err := run("rev-parse", "--show-prefix")
	if err != nil || strings.TrimSpace(string(prefix)) != "" {
		return nil, false
	}
	args := []string{"ls-files", "-z"}
	if dir != "" {
		args = append(args, "--", filepath.ToSlash(dir))
	}
	out, err := run(args...)
	if err != nil {
		return nil, false
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		// a tracked file deleted from the folder, or a link, is not content to read
		if st, serr := os.Stat(path); serr == nil && st.Mode().IsRegular() {
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files, true
}

func walkedFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != filepath.Join(root, dir) && skippedWalkDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(files)
	return files
}

// ---- tests of the helper and of the checks that use it ----

func scratchGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(gitEnv(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeScratch(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func relList(t *testing.T, root string, paths []string) []string {
	t.Helper()
	var out []string
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func newScratchWorkTree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	root := t.TempDir()
	scratchGit(t, root, "init", "-q")
	return root
}

func TestRepoFileListIsTheTrackedFilesOnly(t *testing.T) {
	root := newScratchWorkTree(t)
	writeScratch(t, root, "a.txt", "tracked\n")
	writeScratch(t, root, "sub/b.txt", "tracked\n")
	writeScratch(t, root, "gone.txt", "tracked, then deleted from the folder\n")
	scratchGit(t, root, "add", "a.txt", "sub/b.txt", "gone.txt")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, root, "untracked.txt", "not in the repository\n")
	writeScratch(t, root, "outbox-W9/report.txt", "a package's report\n")
	writeScratch(t, root, "sub/untracked.txt", "not in the repository\n")

	got := relList(t, root, repoFileList(t, root, ""))
	want := []string{"a.txt", "sub/b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tracked files = %v, want %v", got, want)
	}
	got = relList(t, root, repoFileList(t, root, "sub"))
	if strings.Join(got, ",") != "sub/b.txt" {
		t.Fatalf("tracked files under sub = %v, want [sub/b.txt]", got)
	}
}

func TestRepoFileListFallsBackToAWalkOutsideGit(t *testing.T) {
	root := t.TempDir() // not a git work tree
	writeScratch(t, root, "a.txt", "x\n")
	writeScratch(t, root, "sub/z.txt", "x\n")
	writeScratch(t, root, "node_modules/m/index.js", "x\n")
	writeScratch(t, root, "outbox-7/report.txt", "x\n")
	writeScratch(t, root, ".git/config", "x\n")
	got := relList(t, root, repoFileList(t, root, ""))
	if strings.Join(got, ",") != "a.txt,sub/z.txt" {
		t.Fatalf("walked files = %v, want [a.txt sub/z.txt]: node_modules, .git and outbox-* folders are never content", got)
	}
}

// An unpacked archive lying inside some other repository is judged by a walk, not by
// the outer repository's file list.
func TestRepoFileListDoesNotUseAnOuterRepository(t *testing.T) {
	outer := newScratchWorkTree(t)
	writeScratch(t, outer, "pack/a.txt", "x\n")
	writeScratch(t, outer, "pack/b.txt", "x\n")
	scratchGit(t, outer, "add", "pack/a.txt") // only a.txt is tracked by the outer repository
	inner := filepath.Join(outer, "pack")
	got := relList(t, inner, repoFileList(t, inner, ""))
	if strings.Join(got, ",") != "a.txt,b.txt" {
		t.Fatalf("files = %v, want the walk's [a.txt b.txt]", got)
	}
}

// A file in the folder that names an off-chain service is not a finding unless the
// repository tracks it; a tracked one is. The text with a name in it is taken from
// a file the repository already tracks (the first file of the reviewed baseline), so
// this test does not spell a name.
func TestNameScanIgnoresUntrackedFilesAndCountsTrackedOnes(t *testing.T) {
	var sample string
	var keys []string
	for k := range bannedNameBaseline {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "path-sha256:") || !strings.HasSuffix(k, ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(k)))
		if err == nil && countBannedNames(data) > 0 && len(data) < 1<<20 {
			sample = string(data)
			break
		}
	}
	if sample == "" {
		t.Fatalf("no baseline document with a name in it was found to use as a sample")
	}
	root := newScratchWorkTree(t)
	writeScratch(t, root, "tracked-doc.md", sample)
	scratchGit(t, root, "add", "tracked-doc.md")
	writeScratch(t, root, "untracked-doc.md", sample)
	writeScratch(t, root, "outbox-W9/report.txt", sample)

	got := scanBannedNamesIn(t, root)
	if got["tracked-doc.md"] == 0 {
		t.Fatalf("a tracked file with a name in it was not counted: %v", got)
	}
	for k := range got {
		if k != "tracked-doc.md" {
			t.Errorf("an untracked file was counted: %s", k)
		}
	}
}
