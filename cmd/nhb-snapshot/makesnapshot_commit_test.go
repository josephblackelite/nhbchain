package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest that does not name the commit its node was built from is one no new
// node can accept: scripts/deployvalidator.sh compares the commit (or the binary)
// with its own build, and has nothing to tell the operator to check out. The
// producer therefore refuses to publish one, whatever the reason it cannot tell
// the commit: a binary outside a checkout, a checkout git will not read, or a
// commit given in a form the consumer would never match. Only an explicit
// --allow-unknown-binary publishes it.
func TestMakeSnapshotRefusesToPublishAManifestNoNewNodeCanAccept(t *testing.T) {
	bash, tool, repo := e2eBash(t), builtTool(t), repoRoot(t)
	dir, _ := nodeDataDir(t)

	run := func(t *testing.T, extra ...string) (scriptRun, string) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "out")
		args := append([]string{"--data-dir", slash(dir), "--out-dir", slash(out),
			"--work-dir", slash(filepath.Join(t.TempDir(), "work")), "--tool", slash(tool)}, extra...)
		return runBashScript(t, bash, repo, "scripts/make-snapshot.sh", args...), out
	}
	// What a host that scripts/deployvalidator.sh did not install looks like: a node
	// binary whose install root is no checkout, so that there is no commit to read.
	outsideACheckout := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		if o, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").CombinedOutput(); err == nil {
			t.Skipf("the temporary directory is inside a git checkout (%s)", strings.TrimSpace(string(o)))
		}
		bin := filepath.Join(root, "bin", "nhb")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("a node binary"), 0o755); err != nil {
			t.Fatal(err)
		}
		return slash(bin)
	}
	refused := func(t *testing.T, res scriptRun, out string, want string) {
		t.Helper()
		if res.err == nil || !strings.Contains(res.out, want) || !strings.Contains(res.out, "dead end") {
			t.Fatalf("expected a refusal saying %q, got err=%v\n%s", want, res.err, res.out)
		}
		if entries, _ := os.ReadDir(out); len(entries) > 0 {
			t.Fatalf("a refused run left %d files in the output directory", len(entries))
		}
		for _, hint := range []string{"--binary-commit", "--node-binary", "--allow-unknown-binary"} {
			if !strings.Contains(res.out, hint) {
				t.Fatalf("the refusal does not tell the operator about %s:\n%s", hint, res.out)
			}
		}
	}

	t.Run("no binary and no checkout", func(t *testing.T) {
		res, out := run(t, "--node-binary", slash(filepath.Join(t.TempDir(), "bin", "nhb")))
		refused(t, res, out, "the source commit of the node binary is not known")
	})
	t.Run("a known binary sha256 is not enough: the commit is what a new node checks out", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t))
		refused(t, res, out, "git could not read")
		if strings.Contains(res.out, "sha256=unknown") {
			t.Fatalf("the binary's sha256 was not read, so this does not test what it says:\n%s", res.out)
		}
	})
	t.Run("a commit given as unknown", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t), "--binary-commit", "unknown")
		refused(t, res, out, "is not a full commit id")
	})
	t.Run("an abbreviated commit, which a new node would never match", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t), "--binary-commit", "0123abc")
		refused(t, res, out, "is not a full commit id")
	})
	t.Run("a tag instead of a commit", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t), "--binary-commit", "v1.2.3")
		refused(t, res, out, "is not a full commit id")
	})

	t.Run("the full commit id is published, and a new node built from it accepts the snapshot", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t), "--binary-commit", testCommit)
		if res.err != nil {
			t.Fatalf("make-snapshot.sh: %v\n%s", res.err, res.out)
		}
		m, err := readManifestFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if m.Producer.BinaryCommit != testCommit || m.Producer.BinarySha256 == "" {
			t.Fatalf("the manifest names commit %q and binary %q", m.Producer.BinaryCommit, m.Producer.BinarySha256)
		}
		h := newDeployHarness(t)
		o, code := h.run("check_binary_identity '' " + m.Producer.BinaryCommit + " " + strings.Repeat("e", 64) + " " + testCommit)
		if code != 0 || !strings.Contains(o, "the commit the snapshot was taken with") {
			t.Fatalf("a new node built from the commit the manifest names refuses it: exit %d\n%s", code, o)
		}
	})
	t.Run("--allow-unknown-binary publishes it, and says what that costs", func(t *testing.T) {
		res, out := run(t, "--node-binary", outsideACheckout(t), "--allow-unknown-binary")
		if res.err != nil || !strings.Contains(res.out, "--allow-binary-mismatch") {
			t.Fatalf("err=%v\n%s", res.err, res.out)
		}
		m, err := readManifestFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if m.Producer.BinaryCommit != "unknown" {
			t.Fatalf("the manifest names commit %q", m.Producer.BinaryCommit)
		}
		// A new node refuses it, as the warning said, and is not sent to check out
		// a commit called "unknown".
		h := newDeployHarness(t)
		o, code := h.run("check_binary_identity '" + m.Producer.BinarySha256 + "' " + m.Producer.BinaryCommit + " " + strings.Repeat("e", 64) + " " + testCommit)
		if code == 0 || strings.Contains(o, "git checkout unknown") {
			t.Fatalf("a new node accepted the snapshot, or sent the operator to check out %q: exit %d\n%s", m.Producer.BinaryCommit, code, o)
		}
	})

	// Where the layout is the one the installer makes, nothing changes: the commit
	// is read from the checkout above the node binary.
	t.Run("the commit is read from the checkout the binary sits in", func(t *testing.T) {
		want, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Skipf("the repository is not a git checkout here: %v", err)
		}
		res, out := run(t, "--node-binary", slash(filepath.Join(repo, "bin", "nhb")))
		if res.err != nil {
			t.Fatalf("make-snapshot.sh: %v\n%s", res.err, res.out)
		}
		m, err := readManifestFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(want)); m.Producer.BinaryCommit != got {
			t.Fatalf("the manifest names commit %q, the checkout is at %q", m.Producer.BinaryCommit, got)
		}
	})
}

// --help is the header of the script. It has to say what a host that the installer
// did not lay out must pass, and the two operator pages have to say it where the
// snapshot is made.
func TestMakeSnapshotSaysWhatAHostNotInstalledByTheScriptMustPass(t *testing.T) {
	bash, repo := e2eBash(t), repoRoot(t)
	res := runBashScript(t, bash, repo, "scripts/make-snapshot.sh", "--help")
	if res.err != nil {
		t.Fatalf("--help: %v\n%s", res.err, res.out)
	}
	help := strings.Join(strings.Fields(res.out), " ")
	for _, want := range []string{"--node-binary", "--binary-commit", "--allow-unknown-binary", "not installed by scripts/deployvalidator.sh", "cmd/nhb-snapshot"} {
		if !strings.Contains(help, want) {
			t.Errorf("--help does not mention %q:\n%s", want, res.out)
		}
	}
	section := func(doc, heading string) string {
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(doc)))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		i := strings.Index(text, "\n## "+heading+"\n")
		if i < 0 {
			t.Fatalf("%s has no section %q", doc, heading)
		}
		rest := text[i+1:]
		if j := strings.Index(rest[3:], "\n## "); j >= 0 {
			rest = rest[:j+3]
		}
		return strings.Join(strings.Fields(rest), " ")
	}
	for doc, heading := range map[string]string{
		"docs/validators/snapshot-onboarding.md": "Making snapshots",
		"docs/ops/snapshots.md":                  "Producing a snapshot",
	} {
		body := section(doc, heading)
		for _, want := range []string{"--node-binary", "--binary-commit", "--allow-unknown-binary", "not installed by"} {
			if !strings.Contains(body, want) {
				t.Errorf("the %q section of %s does not mention %q", heading, doc, want)
			}
		}
	}
}
