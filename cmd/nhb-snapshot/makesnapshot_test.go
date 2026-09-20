package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCommit is a full commit id, as git rev-parse HEAD prints it: what a
// producer that is not laid out like scripts/deployvalidator.sh has to pass.
const testCommit = "0123456789abcdef0123456789abcdef01234567"

// nodeDataDir builds the data directory of a stopped node: a chain database
// plus everything else a node keeps there, including a key someone left in it.
func nodeDataDir(t *testing.T) (dir string, secrets map[string][]byte) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "node", "data")
	buildChainDir(t, dir, 80, 6, testAddress(0x21))
	secrets = map[string][]byte{
		"p2p/node_key.json":        []byte(`{"privateKey":"5b0c1d2e3f405162738495a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f80"}`),
		"p2p/peerstore/000001.log": []byte("peer 203.0.113.7:6001"),
		"bft_sign_state.json":      []byte(`{"votes":[{"height":80}]}`),
		"polc_lock.json":           []byte(`{"height":80}`),
		"genesis.resolved.json":    []byte(`{"genesisTime":"x"}`),
		"validator.key":            bytes.Repeat([]byte{0xAB}, 32),
		"validator.keystore":       []byte(`{"crypto":{"cipher":"aes-128-ctr"}}`),
		".bft_sign_state-1.tmp":    []byte("partial"),
		"CURRENT.bak":              []byte("MANIFEST-000999\n"),
	}
	for name, content := range secrets {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, secrets
}

func TestMakeSnapshotNeverTakesWhatIdentifiesTheNode(t *testing.T) {
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	dir, secrets := nodeDataDir(t)
	out := filepath.Join(t.TempDir(), "out")
	res := runBashScript(t, bash, repo, "scripts/make-snapshot.sh",
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
		"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit)
	if res.err != nil {
		t.Fatalf("make-snapshot.sh: %v\n%s", res.err, res.out)
	}
	m, err := readManifestFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Archive.Files {
		if !allowedFileName(f.Name) {
			t.Fatalf("the manifest lists %q", f.Name)
		}
	}
	stream := archiveBytes(t, filepath.Join(out, m.Archive.Name))
	for name, content := range secrets {
		if bytes.Contains(stream, content) {
			t.Fatalf("the archive holds the content of %s", name)
		}
		// The peer store's own files are named like the chain database's.
		if base := filepath.Base(filepath.FromSlash(name)); !allowedFileName(base) && bytes.Contains(stream, []byte(base)) {
			t.Fatalf("the archive names %s", base)
		}
	}
	// The script says what it left out, including the files it never read.
	for _, want := range []string{"p2p/", "bft_sign_state.json", "polc_lock.json", "validator.key", "validator.keystore", "genesis.resolved.json"} {
		if !strings.Contains(res.out, want) {
			t.Fatalf("the output does not say that %s was left out:\n%s", want, res.out)
		}
	}
	// The node's directory is exactly as it was.
	for name, content := range secrets {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("%s in the data directory changed: %v", name, err)
		}
	}
	// Nothing was left behind next to the output but the three files.
	entries, _ := os.ReadDir(out)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 {
		t.Fatalf("the output directory holds %v", names)
	}
}

func TestMakeSnapshotRefusesBadArgumentsAndUnsafePlaces(t *testing.T) {
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)
	run := func(args ...string) scriptRun {
		return runBashScript(t, bash, repo, "scripts/make-snapshot.sh", args...)
	}
	base := func(extra ...string) []string {
		return append([]string{"--data-dir", slash(dir), "--out-dir", slash(filepath.Join(t.TempDir(), "out")),
			"--work-dir", slash(filepath.Join(t.TempDir(), "work")), "--tool", slash(tool)}, extra...)
	}
	expectFail := func(name string, res scriptRun, want string) {
		t.Helper()
		if res.err == nil || !strings.Contains(res.out, want) {
			t.Fatalf("%s: expected failure containing %q, got err=%v\n%s", name, want, res.err, res.out)
		}
	}

	expectFail("no arguments", run(), "--data-dir is required")
	expectFail("no output", run("--data-dir", slash(dir)), "--out-dir is required")
	expectFail("an unknown flag", run(append(base(), "--frobnicate")...), "unknown argument")
	expectFail("too few passes", run(append(base(), "--max-passes", "1")...), "at least 2")
	expectFail("a missing tool", run("--data-dir", slash(dir), "--out-dir", slash(filepath.Join(t.TempDir(), "o")), "--tool", slash(filepath.Join(t.TempDir(), "nope"))), "nhb-snapshot binary was not found")

	notNode := t.TempDir()
	expectFail("not a data directory", run("--data-dir", slash(notNode), "--out-dir", slash(filepath.Join(t.TempDir(), "o")), "--tool", slash(tool)), "no CURRENT file")
	expectFail("output inside the data directory", run("--data-dir", slash(dir), "--out-dir", slash(filepath.Join(dir, "out")), "--tool", slash(tool)), "must not be inside the data directory")
	expectFail("staging inside the data directory", run("--data-dir", slash(dir), "--out-dir", slash(filepath.Join(t.TempDir(), "o")), "--work-dir", slash(filepath.Join(dir, "work")), "--tool", slash(tool)), "must not be inside the data directory")

	// A work directory that is not empty and was not made by the script is
	// never touched: the script removes what it staged there.
	foreign := filepath.Join(t.TempDir(), "important")
	if err := os.MkdirAll(filepath.Join(foreign, "stage"), 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(foreign, "stage", "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	expectFail("a foreign work directory", run("--data-dir", slash(dir), "--out-dir", slash(filepath.Join(t.TempDir(), "o")), "--work-dir", slash(foreign), "--tool", slash(tool)), "was not made by this script")
	if data, err := os.ReadFile(precious); err != nil || string(data) != "keep" {
		t.Fatalf("a foreign work directory was touched: %v", err)
	}
}

// A copy that does not open is never published, and nothing is left where the
// snapshots go.
func TestMakeSnapshotPublishesNothingFromABrokenDatabase(t *testing.T) {
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)

	// The MANIFEST the database points at is gone.
	var manifestName string
	for _, name := range dbFiles(t, dir) {
		if strings.HasPrefix(name, "MANIFEST-") {
			manifestName = name
		}
	}
	if manifestName == "" {
		t.Fatal("no MANIFEST in the fixture")
	}
	if err := os.Remove(filepath.Join(dir, manifestName)); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	res := runBashScript(t, bash, repo, "scripts/make-snapshot.sh",
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
		"--tool", slash(tool), "--binary-commit", testCommit, "--max-passes", "3")
	if res.err == nil || strings.Contains(res.out, "dead end") {
		t.Fatalf("a snapshot of a broken database was made, or it was refused for another reason than the database (err=%v):\n%s", res.err, res.out)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) > 0 {
		t.Fatalf("a failed run left %d files in the output directory", len(entries))
	}
}

// A database with a table file the MANIFEST refers to missing is refused, and
// nothing is published. The copy asks the MANIFEST which tables to take, so a
// table that is listed and not there fails the pass itself, and the message that
// ends the run says which one.
func TestMakeSnapshotRefusesACopyThatLacksATable(t *testing.T) {
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	dir := filepath.Join(t.TempDir(), "node", "data")
	b := newChainBuilder(t, dir, true, testAddress(0x31))
	for i := 0; i < 150; i++ {
		b.addBlock(12)
	}
	b.close()
	// A table the MANIFEST lists: a closed database can hold tables it does not.
	refs, err := readDBRefs(dir)
	if err != nil {
		t.Fatal(err)
	}
	listed := refs.tableNames()
	if len(listed) == 0 {
		t.Fatal("the fixture has no table file")
	}
	table := listed[0]
	if err := os.Remove(filepath.Join(dir, table)); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	res := runBashScript(t, bash, repo, "scripts/make-snapshot.sh",
		"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(filepath.Join(t.TempDir(), "work")),
		"--tool", slash(tool), "--binary-commit", testCommit, "--max-passes", "3")
	if res.err == nil || !strings.Contains(res.out, "table "+table+", which the MANIFEST lists, could not be copied") || !strings.Contains(res.out, "the last trouble: table "+table) {
		t.Fatalf("expected the copy to be refused, naming the table that is missing, got err=%v\n%s", res.err, res.out)
	}
	if entries, _ := os.ReadDir(out); len(entries) > 0 {
		t.Fatalf("a refused copy left %d files in the output directory", len(entries))
	}
}
