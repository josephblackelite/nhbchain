package main

// Tests of what scripts/deployvalidator.sh trusts, and of the guards it puts
// between a snapshot host, the service user and root. Like the other deploy tests
// they source the script and call its functions against fake sudo, systemctl and
// install, and a real nhb-snapshot.
//
// Where a test needs a symbolic link it makes one when the machine lets it and
// otherwise stands in for the question "is this a link?" (in this script it is
// always asked as "test -L"), so that the refusal is tested on every machine.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// reportsALink is a stand-in for as_service (or as_root) that answers "yes" to
// "test -L <path>" for the paths that end with $LINK_SUFFIX and passes everything
// else on.
func reportsALink(function string) string {
	if function == "as_service" {
		return `as_service() { if [[ "$1" == test && "$2" == -L && "$3" == *"${LINK_SUFFIX}" ]]; then return 0; fi; sudo -u "${SERVICE_USER}" "$@"; }` + "\n"
	}
	return `as_root() { if [[ "$1" == test && "$2" == -L && "$3" == *"${LINK_SUFFIX}" ]]; then return 0; fi; sudo "$@"; }` + "\n"
}

// --reset-state carries the node's identity and what it has voted into the new
// data directory. Both directories belong to the service user, who can plant
// anything in them: root used to copy with cp, which follows a link, and so
// handed the content of any file to the node in its own data directory.
func TestDeployRestoreIdentityRefusesALink(t *testing.T) {
	setup := func(t *testing.T) (h *deployHarness, from, to string) {
		h = newDeployHarness(t)
		from = filepath.Join(h.state, "nhb-data")
		to = filepath.Join(h.state, "nhb-data.new")
		for name, content := range map[string]string{"p2p/node_key.json": `{"privateKey":"aa"}`, "bft_sign_state.json": `{"votes":1}`, "polc_lock.json": `{"h":1}`} {
			path := filepath.Join(from, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(to, 0o755); err != nil {
			t.Fatal(err)
		}
		return h, from, to
	}
	call := func(h *deployHarness, from, to, link string) (string, int) {
		snippet := reportsALink("as_service") + "restore_identity " + slash(from) + " " + slash(to) + "\n"
		return h.run(snippet, "LINK_SUFFIX="+link)
	}
	t.Run("nothing is a link: everything is copied, by the service user", func(t *testing.T) {
		h, from, to := setup(t)
		out, code := call(h, from, to, "/no-such-suffix")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		for _, name := range []string{"p2p/node_key.json", "bft_sign_state.json", "polc_lock.json"} {
			if string(mustRead(t, filepath.Join(to, filepath.FromSlash(name)))) != string(mustRead(t, filepath.Join(from, filepath.FromSlash(name)))) {
				t.Fatalf("%s was not copied", name)
			}
		}
		calls := h.calls()
		if strings.Contains(calls, "sudo cp") || strings.Contains(calls, "sudo mkdir") || strings.Contains(calls, "sudo chown") || !strings.Contains(calls, "sudo -u nhb cp -p -P") {
			t.Fatalf("the copy was not made by the service user, without following links:\n%s", calls)
		}
	})
	for _, link := range []string{"/bft_sign_state.json", "/polc_lock.json", "/p2p/node_key.json"} {
		t.Run("a source file that is a link: "+link, func(t *testing.T) {
			h, from, to := setup(t)
			out, code := call(h, from, to, link)
			if code == 0 || !strings.Contains(out, "is a symbolic link") {
				t.Fatalf("a link was copied (exit %d):\n%s", code, out)
			}
			// The files before it may have been copied; this one, and any after it, were not.
			if _, err := os.Stat(filepath.Join(to, filepath.FromSlash(strings.TrimPrefix(link, "/")))); err == nil {
				t.Fatalf("what the link led to was copied to %s", link)
			}
		})
	}
	t.Run("a p2p directory that is a link, in either place", func(t *testing.T) {
		for _, place := range []string{"from", "to"} {
			h, from, to := setup(t)
			dir := from
			if place == "to" {
				dir = to
			}
			// The stand-in answers for the directory's own name.
			out, code := call(h, from, to, "/"+filepath.Base(dir)+"/p2p")
			if code == 0 || !strings.Contains(out, "symbolic link") {
				t.Fatalf("%s: a p2p directory that is a link was copied through (exit %d):\n%s", place, code, out)
			}
			if _, err := os.Stat(filepath.Join(to, "p2p", "node_key.json")); err == nil {
				t.Fatalf("%s: the node key was copied through a link", place)
			}
		}
	})
	t.Run("a real link", func(t *testing.T) {
		canSymlink(t)
		h, from, to := setup(t)
		secret := filepath.Join(t.TempDir(), "only-root-can-read")
		if err := os.WriteFile(secret, []byte("SECRETSHADOWLINE"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(from, "bft_sign_state.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, secret, path)
		out, code := h.run("restore_identity " + slash(from) + " " + slash(to) + "\n")
		if code == 0 || !strings.Contains(out, "is a symbolic link") {
			t.Fatalf("a link was copied (exit %d):\n%s", code, out)
		}
		if data, err := os.ReadFile(filepath.Join(to, "bft_sign_state.json")); err == nil {
			t.Fatalf("the link's target was copied: %s", data)
		}
	})
}

// The key file is in a directory the service user owns. Root used to chown and
// chmod it by name, which follows a link: a link to /etc/shadow made the next run
// hand that file to the service user; and it read the key with od, so a link made
// the node's own environment file hold whatever it pointed at.
func TestDeployKeyIsNeverHandledThroughALink(t *testing.T) {
	t.Run("a link is refused", func(t *testing.T) {
		h := newDeployHarness(t)
		h.stubNodeBinaries()
		out, code := h.run(reportsALink("as_root")+"ensure_key\n", "LINK_SUFFIX=/validator.key")
		if code == 0 || !strings.Contains(out, "is a symbolic link, not a key file") {
			t.Fatalf("a key file that is a link was accepted (exit %d):\n%s", code, out)
		}
		if calls := h.calls(); strings.Contains(calls, "chown") || strings.Contains(calls, "chmod") {
			t.Fatalf("something was chowned or chmodded before the refusal:\n%s", calls)
		}
	})
	t.Run("an environment file that is a link is not read for the secret", func(t *testing.T) {
		h := newDeployHarness(t)
		h.stubNodeBinaries()
		out, code := h.run("ensure_key\nJWT_SECRET=s\n"+reportsALink("as_root")+"write_env\n", "LINK_SUFFIX=/node.env")
		if code == 0 || !strings.Contains(out, "node.env is a symbolic link") {
			t.Fatalf("a node.env that is a link was read (exit %d):\n%s", code, out)
		}
		if calls := h.calls(); strings.Contains(calls, "sudo grep") {
			t.Fatalf("root read the file that is a link:\n%s", calls)
		}
	})
	t.Run("what root does to the key, and what the service user does", func(t *testing.T) {
		h := newDeployHarness(t)
		h.stubNodeBinaries()
		out, code := h.run("ensure_key\nJWT_SECRET=s\nwrite_env\n")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		key := slash(filepath.Join(h.config, "validator.key"))
		calls := h.calls()
		for _, want := range []string{
			"sudo chown -h nhb:nhb " + key,   // the name, never what a link leads to
			"sudo -u nhb chmod 600 " + key,   // the mode, by the user who owns it
			"sudo -u nhb od -An -tx1 " + key, // the key is read by the user who owns it
			"sudo install -m 0600 -o root -g root /dev/null " + slash(filepath.Join(h.config, ".validator.key.created-here")),
		} {
			if !strings.Contains(calls, want) {
				t.Fatalf("expected %q in the calls:\n%s", want, calls)
			}
		}
		for _, bad := range []string{"sudo chown nhb:nhb", "sudo chmod", "sudo od ", "sudo touch"} {
			if strings.Contains(calls, bad) {
				t.Fatalf("root ran %q, which follows a link:\n%s", bad, calls)
			}
		}
	})
}

// The directory the snapshot is unpacked into, the fingerprint of what was last
// started, and the download directory are all in the service user's own state
// directory. Root does not chmod, tee or install into them: it would follow a
// link put there.
func TestDeployRootTouchesNothingTheServiceUserCanReplace(t *testing.T) {
	h := newDeployHarness(t)
	sha := h.stubNodeBinaries()
	p := newLivePublisher(t)
	p.publish(sha, rerunCommit)
	out, code := h.run(mainSnippet(p.server.URL))
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	dataDir := slash(filepath.Join(h.state, "nhb-data"))
	calls := h.calls()
	for _, want := range []string{
		"sudo -u nhb chmod 0700 -- " + dataDir,
		"sudo -u nhb mkdir -p -- " + slash(filepath.Join(h.state, ".snapshot-download")),
		"sudo -u nhb chmod 0700 -- " + slash(filepath.Join(h.state, ".snapshot-download")),
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("expected %q in the calls:\n%s", want, calls)
		}
	}
	for _, bad := range []string{"sudo chmod 0700 " + dataDir, "sudo install -d -m 0700 -o nhb -g nhb " + slash(filepath.Join(h.state, ".snapshot-download"))} {
		if strings.Contains(calls, bad) {
			t.Fatalf("root ran %q on a path the service user owns:\n%s", bad, calls)
		}
	}

	// The fingerprint of the running service.
	if err := os.WriteFile(filepath.Join(h.config, "config.toml"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.config, "node.env"), []byte("e"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(h.log)
	out, code = h.run("start_or_leave_running \"${INSTALL_ROOT}/deploy/systemd/nhb.service\"\n")
	if code != 0 {
		t.Fatalf("start_or_leave_running exit %d:\n%s", code, out)
	}
	calls = h.calls()
	if !strings.Contains(calls, "sudo -u nhb tee "+slash(filepath.Join(h.state, ".deploy-fingerprint"))) || strings.Contains(calls, "sudo tee "+slash(filepath.Join(h.state, ".deploy-fingerprint"))) {
		t.Fatalf("the fingerprint was not written by the service user:\n%s", calls)
	}
}

// A host that sends a few bytes a minute holds a download for as long as curl
// lets it: --max-time is two hours and there are three retries. It is given up on
// as soon as it is too slow.
func TestDeployFetchGivesUpOnAHostThatTrickles(t *testing.T) {
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Length", "100000")
		for i := 0; i < 100000; i++ {
			if _, err := w.Write([]byte{'x'}); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-stop:
				return
			case <-r.Context().Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}))
	defer server.Close()
	defer close(stop)

	h := newDeployHarness(t)
	h.timeout = 90 * time.Second
	dest := filepath.Join(h.state, "trickled")
	started := time.Now()
	out, code := h.run("CURL_PROTO='=https,http,file'\nFETCH_SPEED_TIME=2\nfetch_file "+server.URL+"/manifest.json "+slash(dest)+" 4194304\n", "CLI_RETRY_DELAY=0")
	took := time.Since(started)
	if code == 0 || !strings.Contains(out, "could not download") || !strings.Contains(out, "at least 10240 bytes a second") {
		t.Fatalf("a host that trickles was not given up on (exit %d after %s):\n%s", code, took.Round(time.Second), out)
	}
	if took > 60*time.Second {
		t.Fatalf("giving up took %s", took)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatalf("what a host trickled was kept")
	}
	if !strings.Contains(h.calls(), "--speed-limit 10240 --speed-time") {
		// The fake sudo logs the command line of the service user's curl.
		t.Fatalf("curl was not given a low-speed limit:\n%s", h.calls())
	}
}

// The lock that keeps two runs apart is in a state directory of the user who
// runs the script. It used to be a fixed name in /tmp: any user could make it
// first, which stopped every run, and a process of another user cannot be
// signalled, so a lock that was still held looked stale.
func TestDeployLock(t *testing.T) {
	t.Run("it is in the state directory of the user, not in the temporary directory", func(t *testing.T) {
		h := newDeployHarness(t)
		out, code := h.run("take_lock\nls -d \"${LOCK_DIR}\"\necho \"${LOCK_DIR}\"\n")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if !strings.Contains(out, slash(filepath.Join(h.root, "xdgstate", "nhbchain", "deploy.lock.d"))) || strings.Contains(out, "/tmp/nhb-deploy") {
			t.Fatalf("the lock is somewhere else:\n%s", out)
		}
		if _, err := os.Stat(filepath.Join(h.root, "xdgstate", "nhbchain", "deploy.lock.d")); err == nil {
			t.Fatalf("the lock was not released when the run ended")
		}
	})
	t.Run("a lock in the temporary directory, that another user made, is nothing to this run", func(t *testing.T) {
		h := newDeployHarness(t)
		// What any user could make first, held by a process that is alive.
		out, code := h.run("mkdir -p \"${TMPDIR}/nhb-deploy.lock.d\"\n" +
			"sleep 60 >/dev/null 2>&1 &\nholder=$!\n" +
			"echo \"${holder}\" > \"${TMPDIR}/nhb-deploy.lock.d/pid\"\n" +
			"take_lock\necho got-the-lock\nkill \"${holder}\" || true\n")
		if code != 0 || !strings.Contains(out, "got-the-lock") {
			t.Fatalf("a lock made in the temporary directory stopped the run (exit %d):\n%s", code, out)
		}
	})
	t.Run("two runs are kept apart", func(t *testing.T) {
		h := newDeployHarness(t)
		var wg sync.WaitGroup
		firstOut := make(chan string, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _ := h.run("take_lock\necho holding\nsleep 6\n")
			firstOut <- out
		}()
		lock := filepath.Join(h.root, "xdgstate", "nhbchain", "deploy.lock.d", "pid")
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(lock); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		out, code := h.run("take_lock\necho second-run-got-it\n")
		if code == 0 || !strings.Contains(out, "another run of this script (pid") || strings.Contains(out, "second-run-got-it") {
			t.Fatalf("a second run was not stopped (exit %d):\n%s", code, out)
		}
		wg.Wait()
		if first := <-firstOut; !strings.Contains(first, "holding") {
			t.Fatalf("the first run did not get the lock:\n%s", first)
		}
	})
	t.Run("a lock left by a process that is gone is taken over", func(t *testing.T) {
		h := newDeployHarness(t)
		dir := filepath.Join(h.root, "xdgstate", "nhbchain", "deploy.lock.d")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pid"), []byte("2147483646"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := h.run("take_lock\necho got-the-lock\n")
		if code != 0 || !strings.Contains(out, "removing a stale lock") || !strings.Contains(out, "got-the-lock") {
			t.Fatalf("a stale lock stopped the run (exit %d):\n%s", code, out)
		}
	})
	t.Run("a state directory that is not the user's own is not used", func(t *testing.T) {
		h := newDeployHarness(t)
		if err := os.MkdirAll(filepath.Join(h.root, "xdgstate"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A file where the directory goes.
		if err := os.WriteFile(filepath.Join(h.root, "xdgstate", "nhbchain"), []byte("in the way"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := h.run("take_lock\necho got-the-lock\n")
		if code == 0 || strings.Contains(out, "got-the-lock") {
			t.Fatalf("a lock was taken in a place that is not a directory (exit %d):\n%s", code, out)
		}
	})
}

// The Go toolchain is unpacked as root, so it is checked against the sha256 that
// go.dev publishes for the exact file before it is unpacked.
func TestDeployGoToolchainIsCheckedBeforeItIsUnpacked(t *testing.T) {
	h := newDeployHarness(t)
	content := "not the toolchain"
	sum := sha256.Sum256([]byte(content))
	h.writeFake("curl", "#!/usr/bin/env bash\necho \"curl $*\" >> \"$FAKE_LOG\"\nwhile [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then printf '%s' \"$FAKE_CURL_CONTENT\" > \"$2\"; fi; shift; done\n")
	asRoot := "as_root() { echo \"as_root $*\" >> \"$FAKE_LOG\"; }\n"

	t.Run("the pinned checksum is the one for the version the script installs", func(t *testing.T) {
		out, code := h.run("echo \"${GO_VERSION} ${GO_TARBALL_SHA256}\"\n")
		if code != 0 || !strings.Contains(out, "1.24.3 3333f6ea53afa971e9078895eaa4ac7204a8c6b5c68c10e6bc9a33e8e391bdd8") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a download that does not match is not unpacked", func(t *testing.T) {
		_ = os.Remove(h.log)
		out, code := h.run(asRoot+"install_go_toolchain\n", "FAKE_CURL_CONTENT="+content)
		if code == 0 || !strings.Contains(out, "it is not being unpacked") {
			t.Fatalf("a tarball with the wrong checksum was accepted (exit %d):\n%s", code, out)
		}
		if calls := h.calls(); strings.Contains(calls, "as_root") {
			t.Fatalf("root did something with a tarball that does not match:\n%s", calls)
		}
	})
	t.Run("one that matches is unpacked as root, after the check", func(t *testing.T) {
		_ = os.Remove(h.log)
		out, code := h.run(asRoot+"GO_TARBALL_SHA256="+hex.EncodeToString(sum[:])+"\ninstall_go_toolchain\n", "FAKE_CURL_CONTENT="+content)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		mustContainInOrder(t, h.calls(), "as_root rm -rf /usr/local/go", "as_root tar -C /usr/local -xzf")
		if !strings.Contains(h.calls(), "--proto =https") || !strings.Contains(h.calls(), "--speed-limit") {
			t.Fatalf("the download was not made over https with a low-speed limit:\n%s", h.calls())
		}
	})
}

// The commit of this checkout is read with the checkout named as safe for this
// one command, and when git still cannot read it the operator is told what git
// said instead of being sent to check out the commit they are already on.
func TestDeployTheLocalCommitIsReadOrExplained(t *testing.T) {
	h := newDeployHarness(t)
	h.writeFake("git", `#!/usr/bin/env bash
echo "git $*" >> "$FAKE_LOG"
if [ -n "${FAKE_GIT_FAIL:-}" ] || [[ "$*" != *"safe.directory="* ]]; then
  echo "fatal: detected dubious ownership in repository at '/x'" >&2
  exit 128
fi
case "$*" in
  *rev-parse*) echo 1111111111111111111111111111111111111111 ;;
esac
`)
	t.Run("the checkout is named as safe for the command", func(t *testing.T) {
		out, code := h.run("detect_local_commit\necho \"commit=${LOCAL_COMMIT} why=${LOCAL_COMMIT_WHY}\"\n")
		if code != 0 || !strings.Contains(out, "commit=1111111111111111111111111111111111111111 why=") {
			t.Fatalf("the commit was not read (exit %d):\n%s", code, out)
		}
		// git -c safe.directory=<checkout> -C <the same checkout> rev-parse HEAD
		calls := h.calls()
		at := strings.Index(calls, "git -c safe.directory=")
		if at < 0 {
			t.Fatalf("git was not told which checkout it may read:\n%s", calls)
		}
		rest := strings.TrimPrefix(calls[at:], "git -c safe.directory=")
		checkout := strings.Fields(rest)[0]
		if !strings.HasPrefix(rest, checkout+" -C "+checkout+" rev-parse HEAD") {
			t.Fatalf("git was not told to read the checkout it is run in:\n%s", calls)
		}
	})
	t.Run("what git said is kept and shown", func(t *testing.T) {
		out, code := h.run("detect_local_commit\necho \"commit=${LOCAL_COMMIT}\"\necho \"why=${LOCAL_COMMIT_WHY}\"\n"+
			"check_binary_identity '' "+strings.Repeat("a", 40)+" "+strings.Repeat("b", 64)+" \"${LOCAL_COMMIT}\" || true\n", "FAKE_GIT_FAIL=1")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		for _, want := range []string{"commit=unknown", "why=git said: fatal: detected dubious ownership", "This checkout's commit could not be read: git said: fatal: detected dubious ownership", "safe.directory", "cannot tell whether it is " + strings.Repeat("a", 40)} {
			if !strings.Contains(out, want) {
				t.Fatalf("the output does not say %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "Check out the commit above") {
			t.Fatalf("the operator was sent to check out a commit that may be the one they are on:\n%s", out)
		}
	})
}

// The manifest is not signed, and the commit it names is the one the operator is
// told to build. With a minimum release given, a commit that is not that release
// or built on it is refused; without one, the operator is told to check that it is
// a release they recognise.
func TestDeployMinimumReleaseCommit(t *testing.T) {
	git := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("git is not usable here: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := t.TempDir()
	git(t, repo, "init")
	commit := func(msg string) string {
		git(t, repo, "commit", "--allow-empty", "-m", msg)
		return git(t, repo, "rev-parse", "HEAD")
	}
	a := commit("a")
	b := commit("b")
	c := commit("c")
	git(t, repo, "checkout", "--orphan", "other")
	x := commit("x")
	git(t, repo, "checkout", "main")
	missing := strings.Repeat("9", 40)

	h := newDeployHarness(t)
	sha := h.stubNodeBinaries()
	script := filepath.Join(repo, "scripts", "deployvalidator.sh")
	copyTestFile(t, filepath.Join(h.repo, "scripts", "deployvalidator.sh"), script)
	h.script = script
	p := newLivePublisher(t)

	run := func(t *testing.T, manifestCommit string, args ...string) (string, int) {
		t.Helper()
		p.publish(sha, manifestCommit)
		if err := os.RemoveAll(filepath.Join(h.state, "nhb-data")); err != nil {
			t.Fatal(err)
		}
		return h.run(mainSnippet(p.server.URL, args...))
	}
	refused := func(t *testing.T, out string, code int, want string) {
		t.Helper()
		if code == 0 || !strings.Contains(out, want) {
			t.Fatalf("expected a refusal saying %q (exit %d):\n%s", want, code, lastLines(out))
		}
		if strings.Contains(out, "generating a fresh validator key") {
			t.Fatalf("the refusal came after the host was changed:\n%s", lastLines(out))
		}
	}

	t.Run("the release itself, and one built on it", func(t *testing.T) {
		for _, m := range []string{a, b, c} {
			out, code := run(t, m, "--min-release-commit", a)
			if code != 0 || !strings.Contains(out, "is the release you pinned or built on it") {
				t.Fatalf("the manifest names %s and was refused (exit %d):\n%s", m, code, lastLines(out))
			}
		}
	})
	t.Run("a commit older than the release", func(t *testing.T) {
		out, code := run(t, a, "--min-release-commit", b)
		refused(t, out, code, "neither the release you pinned")
	})
	t.Run("a commit on another branch", func(t *testing.T) {
		out, code := run(t, x, "--min-release-commit", a)
		refused(t, out, code, "neither the release you pinned")
	})
	t.Run("a commit that is not in this checkout", func(t *testing.T) {
		out, code := run(t, missing, "--min-release-commit", a)
		refused(t, out, code, "is not in this checkout")
	})
	t.Run("a release that is not in this checkout", func(t *testing.T) {
		out, code := run(t, b, "--min-release-commit", missing)
		refused(t, out, code, "the release you pinned ("+missing+") is not in this checkout")
	})
	t.Run("a manifest that names no commit", func(t *testing.T) {
		out, code := run(t, "unknown", "--min-release-commit", a)
		refused(t, out, code, "names no commit")
	})
	t.Run("the pin comes from the environment, too", func(t *testing.T) {
		p.publish(sha, x)
		_ = os.RemoveAll(filepath.Join(h.state, "nhb-data"))
		out, code := h.run(mainSnippet(p.server.URL), "NHB_MIN_RELEASE_COMMIT="+a)
		refused(t, out, code, "neither the release you pinned")
	})
	t.Run("a pin that is not a commit id is refused before anything is done", func(t *testing.T) {
		out, code := h.run("parse_args --beneficiary " + goodBeneficiary + " --snapshot-url https://x.example/y --bootnode a.example:6001 --min-release-commit v1.2.3\nvalidate_inputs\n")
		if code == 0 || !strings.Contains(out, "--min-release-commit must be a full commit id") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("without a pin the operator is told to check the commit", func(t *testing.T) {
		out, code := h.run("check_binary_identity '' " + x + " " + strings.Repeat("e", 64) + " " + a + "\n")
		if code == 0 || !strings.Contains(out, "is a release you recognise") || !strings.Contains(out, "--min-release-commit") || !strings.Contains(out, "git checkout "+x) {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
}

// A run that installed a snapshot and was interrupted before it saw the node get
// past the snapshot's height has to require that of the next run as well: that
// run finds data in place, fetches no manifest, and used to have no height to
// give the sync check, so a snapshot that is not part of the network's chain was
// judged by a peer and a clock.
func TestDeployRemembersTheSnapshotHeightUntilTheNodeHasBeenSeenPastIt(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	p.publish(h.stubNodeBinaries(), rerunCommit)
	marker := filepath.Join(h.state, ".snapshot-height")
	height := strconv.FormatUint(p.m.Height, 10)
	failingWait := strings.Replace(mainSnippet(p.server.URL), `wait_until_synced(){ echo "wait_until_synced: snapshot height [${SNAPSHOT_HEIGHT:-}]"; forget_snapshot_height; }`,
		`wait_until_synced(){ echo "wait_until_synced: snapshot height [${SNAPSHOT_HEIGHT:-}]"; echo interrupted; exit 1; }`, 1)
	if failingWait == mainSnippet(p.server.URL) {
		t.Fatal("the stand-in for the wait was not replaced: this test does not test what it says")
	}

	out, code := h.run(failingWait)
	if code != 1 || !strings.Contains(out, "interrupted") {
		t.Fatalf("the interrupted run (exit %d):\n%s", code, lastLines(out))
	}
	if got := strings.TrimSpace(string(mustRead(t, marker))); got != height {
		t.Fatalf("the run recorded %q, the snapshot's height is %s", got, height)
	}

	// The next run finds the data, fetches no manifest, and still requires the node
	// to get past that height.
	_ = os.Remove(h.log)
	out, code = h.run(mainSnippet(p.server.URL), "FAKE_ACTIVE=0")
	if code != 0 || !strings.Contains(out, "snapshot height ["+height+"]") || !strings.Contains(out, "did not finish waiting for the node") {
		t.Fatalf("the run after an interrupted one did not require the snapshot's height (exit %d):\n%s", code, lastLines(out))
	}
	if strings.Contains(h.calls(), "curl") {
		t.Fatalf("a run that installs no snapshot went to the snapshot host:\n%s", h.calls())
	}
	// The node has been seen past it: a later run has nothing to require.
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the record was kept after the node had been seen at the tip")
	}
	out, code = h.run(mainSnippet(p.server.URL), "FAKE_ACTIVE=0")
	if code != 0 || !strings.Contains(out, "snapshot height []") {
		t.Fatalf("a later run still required a height (exit %d):\n%s", code, lastLines(out))
	}

	// A record that does not hold a height is not guessed at.
	for _, bad := range []string{"abc", "12x", "", "-4", "99999999999999999999999"} {
		if err := os.WriteFile(marker, []byte(bad+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code = h.run(mainSnippet(p.server.URL), "FAKE_ACTIVE=0")
		if code == 0 || !strings.Contains(out, "does not hold a block height") {
			t.Fatalf("a record %q was accepted (exit %d):\n%s", bad, code, lastLines(out))
		}
	}
}
