package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// As root the producer vets --tool through readlink -f, so it has to run the resolved
// file it vetted and never the path it was given: a link in a directory the node's user
// can write (for example /opt/nhbchain/bin/nhb-snapshot) that leads to a root-only binary
// would otherwise pass the check and be swapped for something else while the run lasts
// minutes. A wrapper at the given path stands in for that link (there is no unprivileged
// symlink here) and a readlink stand-in resolves the given path to the root-only tool;
// the run must never execute the wrapper.
func TestMakeSnapshotAsRootRunsTheResolvedToolNotTheGivenPath(t *testing.T) {
	tool, repo := builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)
	base := t.TempDir()
	elsewhere := filepath.Join(base, "elsewhere")
	nhbdir := filepath.Join(base, "nhbdir")
	for _, d := range []string{elsewhere, nhbdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realTool := filepath.Join(elsewhere, "nhb-snapshot-real")
	copyTestFile(t, tool, realTool)
	logf := filepath.Join(base, "ran.log")
	planted := filepath.Join(nhbdir, "nhb-snapshot")
	wrapper := "#!/bin/sh\necho ran >> " + slash(logf) + "\nexec " + slash(realTool) + " \"$@\"\n"
	if err := os.WriteFile(planted, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "rootparent")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := `
current_uid() { echo 0; }
# the node user's directory is the only place that is not root's
owner_and_mode() {
  case "$1" in
    */nhbdir|*/nhbdir/*) echo "1000 755" ;;
    *) echo "0 755" ;;
  esac
}
owner_name() { echo nhb; }
# stands in for a symbolic link at ` + slash(planted) + ` that leads to the root-only tool
readlink() { if [[ "${!#}" == */nhbdir/nhb-snapshot ]]; then echo ` + slash(realTool) + `; else command readlink "$@"; fi; }
`
	res := runScriptSnippet(t, stub, nil,
		"--data-dir", slash(dir), "--out-dir", slash(filepath.Join(parent, "rootout")), "--work-dir", slash(filepath.Join(parent, "rootwork")),
		"--tool", slash(planted), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit)
	if ran, _ := os.ReadFile(logf); strings.Contains(string(ran), "ran") {
		t.Fatalf("root executed --tool through the path it was given (a directory the node user can write), not the file it vetted: err=%v\n%s", res.err, res.out)
	}
}
