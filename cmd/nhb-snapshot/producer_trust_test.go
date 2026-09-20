package main

// What scripts/make-snapshot.sh trusts. The node's own user controls the data
// directory, the script copies from it, and what it copies is published. These
// tests plant what that user can plant (files that only have the name of one of
// the database's, links, directories) and check that none of it is read, copied or
// published. Everything here goes through the script and the built tool, as an
// operator would run them.
//
// A test that needs a symbolic link makes one when the machine lets it (Windows
// needs a privilege for that) and skips, saying so, when it does not. The same
// refusals are also tested without any link: every question the script asks
// about what a name in the node's directory is goes through one function,
// file_kind, and the tests that replace it report a link for a file that is none.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// shadowLine is what the finding put in a file called like a journal.
const shadowLine = "root:$6$SECRETSHADOWLINE$abcdefghijklmnop:19000:0:99999:7:::\n"

// makeSnapshotOf runs the script on dir with a private output and work directory.
func makeSnapshotOf(t *testing.T, dir string, extra ...string) (res scriptRun, out, work string) {
	t.Helper()
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	out = filepath.Join(t.TempDir(), "out")
	work = filepath.Join(t.TempDir(), "work")
	args := append([]string{"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(work),
		"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit}, extra...)
	return runBashScript(t, bash, repo, "scripts/make-snapshot.sh", args...), out, work
}

// runScriptSnippet sources the script (its main only runs when it is executed),
// runs snippet, and then main with args.
func runScriptSnippet(t *testing.T, snippet string, env []string, args ...string) scriptRun {
	t.Helper()
	bash, repo := e2eBash(t), repoRoot(t)
	program := "set -euo pipefail\nsource \"$SCRIPT\"\n" + snippet + "\nmain \"$@\"\n"
	path := filepath.Join(t.TempDir(), "snippet.sh")
	if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, append([]string{slash(path)}, args...)...)
	cmd.Dir = repo
	cmd.Env = append(append(os.Environ(), "SCRIPT="+slash(filepath.Join(repo, "scripts", "make-snapshot.sh"))), env...)
	out, err := cmd.CombinedOutput()
	return scriptRun{out: string(out), err: err}
}

// canSymlink skips the test on a machine that cannot make a symbolic link.
func canSymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("this machine cannot make symbolic links (%v); the same refusals are tested without one through the file_kind stand-in", err)
	}
}

func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// nothingPublished fails the test when the output directory holds any file.
func nothingPublished(t *testing.T, out string) {
	t.Helper()
	if files := filesUnder(t, out); len(files) > 0 {
		t.Fatalf("a refused run left %v in the output directory", files)
	}
}

// A file that only has the name of one of the database's is not one the database
// refers to: no MANIFEST names it. The script used to copy every file with such a
// name and to publish it byte for byte.
func TestMakeSnapshotDoesNotPackAFileThatOnlyHasTheNameOfOneOfTheDatabases(t *testing.T) {
	dir, _ := nodeDataDir(t)
	planted := map[string]string{
		"999999.log":      shadowLine,
		"999998.ldb":      "SECRETSHADOWTABLE " + strings.Repeat("x", 200),
		"MANIFEST-999999": "SECRETOLDMANIFEST",
		"999997.sst":      "SECRETSSTNAME",
	}
	for name, content := range planted {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, out, _ := makeSnapshotOf(t, dir)
	if res.err != nil {
		t.Fatalf("make-snapshot.sh: %v\n%s", res.err, res.out)
	}
	m, err := readManifestFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Archive.Files {
		if _, junk := planted[f.Name]; junk {
			t.Fatalf("the manifest lists %s, which nothing in the database refers to", f.Name)
		}
	}
	stream := archiveBytes(t, filepath.Join(out, m.Archive.Name))
	if bytes.Contains(stream, []byte("SECRET")) {
		t.Fatalf("the archive holds what was in a file that only has a database file's name")
	}
	for name := range planted {
		if !strings.Contains(res.out, name+" (the MANIFEST does not refer to it: not copied)") {
			t.Fatalf("the output does not say that %s was left out:\n%s", name, res.out)
		}
	}
	// The node's directory is exactly as it was.
	for name, content := range planted {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != content {
			t.Fatalf("%s in the data directory changed: %v", name, err)
		}
	}
}

// A directory, a pipe or a link under the name of a database file is not
// something the node wrote. The run is refused whole, before anything is read.
func TestMakeSnapshotRefusesANameOfTheDatabaseThatIsNotARegularFile(t *testing.T) {
	dir, _ := nodeDataDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "999999.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "999999.log", "inner"), []byte(shadowLine), 0o600); err != nil {
		t.Fatal(err)
	}
	res, out, work := makeSnapshotOf(t, dir)
	if res.err == nil || !strings.Contains(res.out, "999999.log") || !strings.Contains(res.out, "is a directory, not a regular file") {
		t.Fatalf("expected the run to be refused, naming the directory: err=%v\n%s", res.err, res.out)
	}
	nothingPublished(t, out)
	if _, err := os.Stat(filepath.Join(work, "stage")); err == nil {
		t.Fatalf("something was staged before the refusal")
	}
}

// The same with real links, on a machine that can make them: a link named like a
// journal, a table or a MANIFEST, or the MANIFEST CURRENT names, or CURRENT
// itself, aimed at a file only another user can read. The script used to test
// with -f and copy with cp, which follow links, and published the target.
func TestMakeSnapshotNeverFollowsALinkInTheDataDirectory(t *testing.T) {
	canSymlink(t)
	secret := filepath.Join(t.TempDir(), "root-only-file")
	if err := os.WriteFile(secret, []byte(shadowLine), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		plant func(t *testing.T, dir string)
	}{
		{"a link named like a journal", func(t *testing.T, dir string) { mustSymlink(t, secret, filepath.Join(dir, "999999.log")) }},
		{"a link named like a table", func(t *testing.T, dir string) { mustSymlink(t, secret, filepath.Join(dir, "999998.ldb")) }},
		{"a link named like a MANIFEST", func(t *testing.T, dir string) { mustSymlink(t, secret, filepath.Join(dir, "MANIFEST-999999")) }},
		{"the MANIFEST CURRENT names is a link", func(t *testing.T, dir string) {
			name := currentManifestName(t, dir)
			aside := filepath.Join(t.TempDir(), name)
			if err := os.Rename(filepath.Join(dir, name), aside); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, aside, filepath.Join(dir, name))
		}},
		{"CURRENT is a link", func(t *testing.T, dir string) {
			aside := filepath.Join(t.TempDir(), "CURRENT")
			if err := os.Rename(filepath.Join(dir, "CURRENT"), aside); err != nil {
				t.Fatal(err)
			}
			mustSymlink(t, aside, filepath.Join(dir, "CURRENT"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := nodeDataDir(t)
			tc.plant(t, dir)
			res, out, _ := makeSnapshotOf(t, dir)
			if res.err == nil {
				t.Fatalf("a data directory with a link in it was published:\n%s", res.out)
			}
			nothingPublished(t, out)
			for _, f := range filesUnder(t, filepath.Dir(out)) {
				if data, err := os.ReadFile(f); err == nil && bytes.Contains(data, []byte("SECRETSHADOWLINE")) && f != secret {
					t.Fatalf("what a link led to was copied to %s", f)
				}
			}
		})
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// currentManifestName returns the MANIFEST file CURRENT names.
func currentManifestName(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// stubLink makes the script's file_kind report a link for the paths that end
// with $STUB_LINK, and answer as it always does for every other path: a link
// without needing the privilege to make one.
const stubLinkSnippet = `
eval "$(declare -f file_kind | sed '1s/^file_kind/real_file_kind/')"
file_kind() {
  case "$1" in
    *"${STUB_LINK}") KIND=symlink ;;
    *) real_file_kind "$1" ;;
  esac
}
`

func TestMakeSnapshotRefusesWhatFileKindReportsAsALink(t *testing.T) {
	tool, repo := builtTool(t), repoRoot(t)
	build := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "node", "data")
		b := newChainBuilder(t, dir, true, testAddress(0x31))
		for i := 0; i < 150; i++ {
			b.addBlock(12)
		}
		b.close()
		if err := os.WriteFile(filepath.Join(dir, "999999.log"), []byte(shadowLine), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	firstTable := func(t *testing.T, dir string) string {
		for _, name := range dbFiles(t, dir) {
			if strings.HasSuffix(name, ".ldb") {
				return name
			}
		}
		t.Fatal("the fixture has no table file")
		return ""
	}
	cases := []struct {
		name string
		stub func(t *testing.T, dir string) string
	}{
		{"a journal the MANIFEST does not name, which is never read", func(t *testing.T, dir string) string { return "/999999.log" }},
		{"the MANIFEST CURRENT names", func(t *testing.T, dir string) string { return "/" + currentManifestName(t, dir) }},
		{"CURRENT", func(t *testing.T, dir string) string { return "/CURRENT" }},
		{"a table", func(t *testing.T, dir string) string { return "/" + firstTable(t, dir) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := build(t)
			out := filepath.Join(t.TempDir(), "out")
			res := runScriptSnippet(t, stubLinkSnippet, []string{"STUB_LINK=" + tc.stub(t, dir)},
				"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
				"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit)
			if res.err == nil || !strings.Contains(res.out, "is a symbolic link, not a regular file") {
				t.Fatalf("a name that is a link was accepted (err=%v):\n%s", res.err, res.out)
			}
			nothingPublished(t, out)
		})
	}
}

// Every copy is made without following a link: cp -P and ln -P, whatever the
// file is when the copy is made. A link that appears after the check is copied
// as a link (never read through) and the pass finds it.
func TestMakeSnapshotCopiesAreMadeWithoutFollowingLinks(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "make-snapshot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var code []string
	for _, line := range strings.Split(string(raw), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") {
			code = append(code, line)
		}
	}
	text := strings.Join(code, "\n")
	for _, bad := range []string{"cp \"${", "ln \"${", " cp -p ", " cp -a "} {
		if strings.Contains(text, bad) {
			t.Errorf("the script copies with %q, which follows a link", bad)
		}
	}
	for _, want := range []string{"cp -P -t", "ln -P -t"} {
		if !strings.Contains(text, want) {
			t.Errorf("the script does not use %q", want)
		}
	}
}

const rootStubSnippet = `
current_uid() { echo "${STUB_UID}"; }
owner_and_mode() {
  case "$1" in
    */node/data) echo "${STUB_DATA_OWNER:-1000} 755" ;;
    */elsewhere/nhb-snapshot*) echo "${STUB_TOOL_SPEC:-0 755}" ;;
    */rootparent) echo "${STUB_PARENT_SPEC:-0 755}" ;;
    */elsewhere) echo "${STUB_TOOL_DIR_SPEC:-0 755}" ;;
    */rootwork*) echo "${STUB_WORK_SPEC:-0 700}" ;;
    */rootout*) echo "${STUB_OUT_SPEC:-0 755}" ;;
    *) echo "0 755" ;;
  esac
}
owner_name() { echo nhb; }
`

// As root the script reads a directory the node's user controls, executes a
// binary that user may have replaced, and writes into a staging area that user
// may have made a link of. It refuses all three, and does not take the tool or
// the work directory from a default.
func TestMakeSnapshotAsRoot(t *testing.T) {
	tool, repo := builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)
	toolDir := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootTool := filepath.Join(toolDir, filepath.Base(tool))
	copyTestFile(t, tool, rootTool)
	parent := filepath.Join(t.TempDir(), "rootparent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(parent, "rootwork")
	out := filepath.Join(parent, "rootout")
	run := func(env []string, args ...string) scriptRun {
		base := []string{"--data-dir", slash(dir), "--out-dir", slash(out), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit}
		return runScriptSnippet(t, rootStubSnippet, append([]string{"STUB_UID=0"}, env...), append(base, args...)...)
	}
	refused := func(name string, res scriptRun, want string) {
		t.Helper()
		if res.err == nil || !strings.Contains(res.out, want) {
			t.Fatalf("%s: expected a refusal saying %q, got err=%v\n%s", name, want, res.err, res.out)
		}
		nothingPublished(t, out)
	}

	refused("a directory that belongs to the node's user", run(nil, "--tool", slash(rootTool), "--work-dir", slash(work)), "refusing to run as root")
	res := run(nil, "--tool", slash(rootTool), "--work-dir", slash(work))
	if !strings.Contains(res.out, "sudo -u nhb bash") {
		t.Fatalf("the refusal does not say how to run it:\n%s", res.out)
	}
	refused("no tool", run([]string{"STUB_DATA_OWNER=0"}, "--work-dir", slash(work)), "--tool is required")
	refused("no work directory", run([]string{"STUB_DATA_OWNER=0"}, "--tool", slash(rootTool)), "--work-dir is required")
	refused("a tool that another user can write", run([]string{"STUB_DATA_OWNER=0", "STUB_TOOL_SPEC=1000 755"}, "--tool", slash(rootTool), "--work-dir", slash(work)), "can be changed by someone other than root")
	refused("a tool in a directory that another user can write", run([]string{"STUB_DATA_OWNER=0", "STUB_TOOL_DIR_SPEC=0 775"}, "--tool", slash(rootTool), "--work-dir", slash(work)), "can be changed by someone other than root")
	refused("a work directory that another user can write", run([]string{"STUB_DATA_OWNER=0", "STUB_WORK_SPEC=0 777"}, "--tool", slash(rootTool), "--work-dir", slash(work)), "can be changed by someone other than root")
	refused("an output directory that another user can write", run([]string{"STUB_DATA_OWNER=0", "STUB_OUT_SPEC=1000 755"}, "--tool", slash(rootTool), "--work-dir", slash(work)), "can be changed by someone other than root")

	// Root makes no directory in a place that is not its own: where the nearest
	// directory that exists can be changed by someone else, it says so and makes none.
	other := filepath.Join(t.TempDir(), "rootparent")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	notMade := filepath.Join(other, "not-made")
	refused("an output directory that would be made in a place others can write", runScriptSnippet(t, rootStubSnippet, []string{"STUB_UID=0", "STUB_DATA_OWNER=0", "STUB_PARENT_SPEC=1000 777"},
		"--data-dir", slash(dir), "--out-dir", slash(notMade), "--tool", slash(rootTool), "--work-dir", slash(work),
		"--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit), "where it would be made, can be changed by someone other than root")
	refused("a work directory that would be made in a place others can write", runScriptSnippet(t, rootStubSnippet, []string{"STUB_UID=0", "STUB_DATA_OWNER=0", "STUB_PARENT_SPEC=1000 777"},
		"--data-dir", slash(dir), "--out-dir", slash(out), "--tool", slash(rootTool), "--work-dir", slash(notMade),
		"--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit), "where it would be made, can be changed by someone other than root")
	if _, err := os.Stat(notMade); err == nil {
		t.Fatalf("root made a directory in a place others can write")
	}

	// Where root's own directory, tool, work directory and output are all root's,
	// it runs.
	res = run([]string{"STUB_DATA_OWNER=0"}, "--tool", slash(rootTool), "--work-dir", slash(work))
	if res.err != nil {
		t.Fatalf("root with every place root's own was refused: %v\n%s", res.err, res.out)
	}
	if _, err := readManifestFile(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

// root_only_path is what tells root's places from the rest.
func TestRootOnlyPath(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "tnA", "tnB", "tnFile")
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	bash, repo := e2eBash(t), repoRoot(t)
	check := func(specs map[string]string) bool {
		var stub strings.Builder
		stub.WriteString("owner_and_mode() {\n  case \"$1\" in\n")
		for suffix, spec := range specs {
			stub.WriteString("    *" + suffix + ") echo '" + spec + "' ;;\n")
		}
		stub.WriteString("    *) echo '0 755' ;;\n  esac\n}\nif root_only_path \"$LEAF\"; then echo YES; else echo NO; fi\n")
		path := filepath.Join(t.TempDir(), "check.sh")
		program := "set -euo pipefail\nsource \"$SCRIPT\"\n" + stub.String()
		if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, slash(path))
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "SCRIPT="+slash(filepath.Join(repo, "scripts", "make-snapshot.sh")), "LEAF="+slash(leaf))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return strings.Contains(string(out), "YES")
	}
	for _, tc := range []struct {
		name  string
		specs map[string]string
		want  bool
	}{
		{"all of it root's", nil, true},
		{"a directory above that its group can write", map[string]string{"/tnA": "0 775"}, false},
		{"a directory above that anyone can write and that is not sticky", map[string]string{"/tnA/tnB": "0 777"}, false},
		{"a directory above that anyone can write but only the owner of an entry can remove", map[string]string{"/tnA": "0 1777"}, true},
		{"a directory above that belongs to another user", map[string]string{"/tnA": "1000 755"}, false},
		{"a file that belongs to another user", map[string]string{"/tnFile": "1000 644"}, false},
		{"a file its group can write", map[string]string{"/tnFile": "0 664"}, false},
		{"a file anyone can write, in a sticky directory", map[string]string{"/tnFile": "0 666", "/tnA/tnB": "0 1777"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := check(tc.specs); got != tc.want {
				t.Fatalf("root_only_path said %v, want %v", got, tc.want)
			}
		})
	}
}

// The default tool is used only when the user running the script, or root, owns
// it and nobody else can write it.
func TestMakeSnapshotDefaultToolIsNotTakenFromAnotherUser(t *testing.T) {
	bash, repo := e2eBash(t), repoRoot(t)
	check := func(uid, spec string) bool {
		snippet := "current_uid() { echo " + uid + "; }\nowner_and_mode() { echo '" + spec + "'; }\nif trusted_tool \"$1\"; then echo YES; else echo NO; fi\n"
		path := filepath.Join(t.TempDir(), "check.sh")
		program := "set -euo pipefail\nsource \"$SCRIPT\"\n" + snippet
		if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bash, slash(path), "/somewhere/nhb-snapshot")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "SCRIPT="+slash(filepath.Join(repo, "scripts", "make-snapshot.sh")))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return strings.Contains(string(out), "YES")
	}
	for _, tc := range []struct {
		name      string
		uid, spec string
		want      bool
	}{
		{"owned by the user who runs the script", "1000", "1000 755", true},
		{"owned by root", "1000", "0 755", true},
		{"owned by another user", "1000", "1001 755", false},
		{"its group can write it", "1000", "1000 775", false},
		{"anyone can write it", "1000", "0 777", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := check(tc.uid, tc.spec); got != tc.want {
				t.Fatalf("trusted_tool said %v, want %v", got, tc.want)
			}
		})
	}
}

// A work directory that is a link, or whose staging directory is one, is not
// used: the script removes and writes what it staged, and a link would send that
// to whatever it points at.
func TestMakeSnapshotRefusesAPlantedWorkDirectory(t *testing.T) {
	canSymlink(t)
	dir, _ := nodeDataDir(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(victim, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	intact := func() {
		t.Helper()
		if data, err := os.ReadFile(precious); err != nil || string(data) != "keep" {
			t.Fatalf("what a planted link pointed at was touched: %v", err)
		}
		if entries, _ := os.ReadDir(victim); len(entries) != 1 {
			t.Fatalf("something was written where a planted link pointed: %v", entries)
		}
	}
	t.Run("a stage that is a link", func(t *testing.T) {
		work := filepath.Join(t.TempDir(), "work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, ".nhb-snapshot-work"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, victim, filepath.Join(work, "stage"))
		res, out, _ := runWithWork(t, dir, work)
		if res.err != nil {
			t.Fatalf("the run failed: %v\n%s", res.err, res.out)
		}
		intact()
		if _, err := readManifestFile(filepath.Join(out, "manifest.json")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a work directory that is a link", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "work")
		mustSymlink(t, victim, link)
		res, out, _ := runWithWork(t, dir, link)
		if res.err == nil || !strings.Contains(res.out, "is a symbolic link") {
			t.Fatalf("a work directory that is a link was used: err=%v\n%s", res.err, res.out)
		}
		nothingPublished(t, out)
		intact()
	})
}

func runWithWork(t *testing.T, dir, work string) (scriptRun, string, string) {
	t.Helper()
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	out := filepath.Join(t.TempDir(), "out")
	return runBashScript(t, bash, repo, "scripts/make-snapshot.sh",
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(work),
		"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit), out, work
}

func TestMakeSnapshotKeepPrunesOnlyOldSnapshotsOfTheSameChain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node", "data")
	validator := testAddress(0x44)
	out := filepath.Join(t.TempDir(), "out")
	work := filepath.Join(t.TempDir(), "work")
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	run := func(extra ...string) scriptRun {
		args := append([]string{"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(work),
			"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit}, extra...)
		return runBashScript(t, bash, repo, "scripts/make-snapshot.sh", args...)
	}
	names := func() []string {
		entries, _ := os.ReadDir(out)
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		return got
	}
	var archives []string
	height := 0
	for i := 0; i < 4; i++ {
		b := newChainBuilder(t, dir, false, validator)
		for j := 0; j < 20; j++ {
			b.addBlock(4)
		}
		height = int(b.bc.Height())
		b.close()
		var res scriptRun
		if i < 3 {
			res = run()
		} else {
			// Snapshots of another chain, and files that are none of the script's:
			// left alone.
			for name, content := range map[string]string{
				"nhb-snapshot-deadbeef-h0000000001.tar.gz":        "another chain",
				"nhb-snapshot-deadbeef-h0000000001.manifest.json": "{}",
				"notes.txt":                       "keep",
				"nhb-snapshot-deadbeef-h1.tar.gz": "not named as this script names them",
			} {
				if err := os.WriteFile(filepath.Join(out, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res = run("--keep", "2")
		}
		if res.err != nil {
			t.Fatalf("run %d: %v\n%s", i+1, res.err, res.out)
		}
		m, err := readManifestFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if int(m.Height) != height {
			t.Fatalf("run %d: the snapshot is at %d, the chain at %d", i+1, m.Height, height)
		}
		archives = append(archives, m.Archive.Name)
		if i == 2 {
			// Without --keep every snapshot is kept.
			for _, a := range archives {
				for _, f := range []string{a, strings.TrimSuffix(a, ".tar.gz") + ".manifest.json"} {
					if _, err := os.Stat(filepath.Join(out, f)); err != nil {
						t.Fatalf("a snapshot was removed without --keep: %v", err)
					}
				}
			}
		}
	}
	// With --keep 2 the two newest of the chain are left, with their manifests.
	for i, a := range archives {
		for _, f := range []string{a, strings.TrimSuffix(a, ".tar.gz") + ".manifest.json"} {
			_, err := os.Stat(filepath.Join(out, f))
			if kept := i >= 2; kept != (err == nil) {
				t.Fatalf("%s: kept=%v, want %v (the output holds %v)", f, err == nil, kept, names())
			}
		}
	}
	for _, f := range []string{"manifest.json", "notes.txt", "nhb-snapshot-deadbeef-h0000000001.tar.gz", "nhb-snapshot-deadbeef-h0000000001.manifest.json", "nhb-snapshot-deadbeef-h1.tar.gz"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatalf("%s was removed: %v (the output holds %v)", f, err, names())
		}
	}
	if !strings.Contains(strings.Join(names(), " "), archives[3]) {
		t.Fatalf("the snapshot just made is not there")
	}
	// --keep is a whole number of at least one.
	for _, bad := range []string{"0", "-1", "two", "", "1.5"} {
		if res := run("--keep", bad); res.err == nil || !strings.Contains(res.out, "--keep must be a whole number") {
			t.Fatalf("--keep %q was accepted: err=%v\n%s", bad, res.err, res.out)
		}
	}
}

// structureOf is what a copy of the node's directory depends on: the MANIFEST
// CURRENT names, its size, and the journals.
func structureOf(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "CURRENT"))
	name := strings.TrimSpace(string(raw))
	size := int64(-1)
	if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
		size = info.Size()
	}
	var journals []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			journals = append(journals, e.Name())
		}
	}
	return name + " " + strconv.FormatInt(size, 10) + " " + strings.Join(journals, ",")
}

// A pass that is overtaken by a flush or a compaction is refused and made again.
// This makes the database change in the middle of the first pass, every time: the
// script is sourced and its cp is replaced by one that, the first time it is asked
// to copy CURRENT, has the writer add blocks until the MANIFEST or the journals are
// not what they were, and waits for it. The snapshot that comes out must open and
// must be a block the writer wrote.
func TestMakeSnapshotRetriesWhenTheDatabaseChangesDuringAPass(t *testing.T) {
	tool, repo := builtTool(t), repoRoot(t)
	dir := filepath.Join(t.TempDir(), "node", "data")
	b := newChainBuilder(t, dir, true, testAddress(0x71))
	var (
		mu     sync.Mutex
		hashes = map[uint64]string{}
	)
	add := func() error {
		var err error
		quietly(func() { err = b.tryAddBlock(12) })
		if err == nil {
			mu.Lock()
			hashes[b.bc.Height()] = hex0x(b.bc.Tip())
			mu.Unlock()
		}
		return err
	}
	for i := 0; i < 100; i++ {
		if err := add(); err != nil {
			t.Fatal(err)
		}
	}
	before := b.bc.Height()

	control := t.TempDir()
	trigger, ack := filepath.Join(control, "trigger"), filepath.Join(control, "ack")
	var (
		wg      sync.WaitGroup
		werr    error
		written uint64
		done    = make(chan struct{})
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, err := os.Stat(trigger); err != nil {
				continue
			}
			was := structureOf(dir)
			for n := 0; n < 4000 && structureOf(dir) == was; n++ {
				if err := add(); err != nil {
					werr = err
					break
				}
				written++
			}
			if structureOf(dir) == was && werr == nil {
				werr = os.ErrInvalid
			}
			_ = os.WriteFile(ack, nil, 0o644)
			return
		}
	}()
	stopWriter := func() {
		select {
		case <-done:
		default:
			close(done)
		}
		wg.Wait()
	}
	defer func() {
		stopWriter()
		b.close()
	}()

	overtaken := `
cp() {
  if [[ "$*" == *CURRENT* && ! -e "${TRIGGERED}" ]]; then
    : > "${TRIGGERED}"
    : > "${TRIGGER}"
    for _ in $(seq 1 600); do [[ -e "${ACK}" ]] && break; sleep 0.05; done
  fi
  command cp "$@"
}
`
	out := filepath.Join(t.TempDir(), "out")
	res := runScriptSnippet(t, overtaken,
		[]string{"TRIGGER=" + slash(trigger), "ACK=" + slash(ack), "TRIGGERED=" + slash(filepath.Join(control, "triggered"))},
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
		"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit, "--max-passes", "40")
	stopWriter()
	if werr != nil {
		t.Fatalf("the writer: %v (it wrote %d blocks without the database changing)", werr, written)
	}
	if res.err != nil {
		t.Fatalf("make-snapshot.sh: %v\n%s", res.err, res.out)
	}
	if !strings.Contains(res.out, "changed during the pass") && !strings.Contains(res.out, "the copied MANIFEST has") {
		t.Fatalf("the first pass was not overtaken (the writer added %d blocks):\n%s", written, res.out)
	}
	m, err := readManifestFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	want, seen := hashes[m.Height]
	mu.Unlock()
	if !seen || m.TipHash != want {
		t.Fatalf("the snapshot is at height %d with tip %s, which is not a block the writer wrote (%v %s)", m.Height, m.TipHash, seen, want)
	}
	if m.Height < before {
		t.Fatalf("the snapshot is at height %d, older than the %d the database was at when the copy began", m.Height, before)
	}
	// Unpacked the way a new node does it.
	target := filepath.Join(t.TempDir(), "restored")
	if o, code := runToolCode(tool, "extract", "--manifest", filepath.Join(out, "manifest.json"),
		"--archive", filepath.Join(out, m.Archive.Name), "--target", target,
		"--chain-id", m.ChainID, "--genesis-hash", m.GenesisHash); code != 0 {
		t.Fatalf("the snapshot does not unpack: %s", o)
	}
}

// A running node removes files all the time (a journal that was rotated, a table
// that was compacted away), so a name in the listing can be gone by the time it
// is looked at. That is not a link or a directory and must not end the run: it was
// taken for one, and a snapshot of a node under churn was refused with "000026.log
// is missing, not a regular file". file_kind reports the file as gone here, for a
// name that is on the disk, as it would have been had the node removed it a moment
// before.
func TestMakeSnapshotToleratesAFileThatVanishesBetweenTheListingAndTheCheck(t *testing.T) {
	tool, repo := builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)
	if err := os.WriteFile(filepath.Join(dir, "999999.log"), []byte(shadowLine), 0o600); err != nil {
		t.Fatal(err)
	}
	vanished := `
eval "$(declare -f file_kind | sed '1s/^file_kind/real_file_kind/')"
file_kind() {
  case "$1" in
    *"${STUB_GONE}") KIND=missing ;;
    *) real_file_kind "$1" ;;
  esac
}
`
	out := filepath.Join(t.TempDir(), "out")
	res := runScriptSnippet(t, vanished, []string{"STUB_GONE=/999999.log"},
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
		"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit)
	if res.err != nil {
		t.Fatalf("a file that vanished ended the run: %v\n%s", res.err, res.out)
	}
	if _, err := readManifestFile(filepath.Join(out, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}
