package main

// Tests of what scripts/deployvalidator.sh builds, installs and runs as root (or
// as the operator who runs it), and where from.
//
// The install directory, the state directory and the config directory are handed to
// the service user: nhb.service runs as it, it is the network-facing part of the
// host, and it can rewrite anything in them. Whatever root or the operator runs,
// installs or builds with, and which comes from one of them, is therefore run as
// whatever that user chose. These tests plant what a compromised node could plant
// there (a tool, a unit, a Go build cache, a file that is read as data) and check
// that none of it is used. They go through the script's own functions, as the other
// deploy tests do, and none of them needs root or a link: what root is asked to do
// is recorded, not done.

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loggedRoot is a stand-in for as_root that only records what it was asked to do,
// and the directory it was asked in.
const loggedRoot = "as_root() { echo \"as_root cwd=${PWD} $*\" >> \"$FAKE_LOG\"; }\n"

// The unit is what root's systemd starts. It comes from the checkout the script runs
// from: by the time install_service runs (after the download, the verification and
// the key, which can take hours) the copy in the install directory, which the service
// user owns, could have been rewritten to start anything as root.
func TestDeployInstallsTheUnitFromTheCheckoutNotFromTheServiceUsersTree(t *testing.T) {
	h := newDeployHarness(t)
	hostile := "[Service]\nUser=root\nExecStartPre=/bin/sh -c 'touch /owned-by-the-node'\nExecStart=/opt/nhbchain/bin/nhb\n"
	if err := os.WriteFile(filepath.Join(h.install, "deploy", "systemd", "nhb.service"), []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}
	// install_service compares the unit with the one already installed: on a host that
	// has it, an identical one would skip the install this test looks at.
	snippet := `
cmp() { return 1; }
as_root() {
  echo "as_root $*" >> "$FAKE_LOG"
  if [[ "$1" == install && "$2" == -m ]]; then cat "$4" > "$FAKE_LOG.installed"; fi
}
start_or_leave_running() { echo "fingerprint-of $1" >> "$FAKE_LOG"; }
echo "checkout $REPO_ROOT" >> "$FAKE_LOG"
install_service
`
	out, code := h.run(snippet)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	calls := h.calls()
	checkout := regexp.MustCompile(`(?m)^checkout (\S+)$`).FindStringSubmatch(calls)
	install := regexp.MustCompile(`as_root install -m 0644 (\S+) /etc/systemd/system/nhb\.service`).FindStringSubmatch(calls)
	if checkout == nil || install == nil {
		t.Fatalf("no unit was installed, or the checkout was not recorded:\n%s", calls)
	}
	if want := checkout[1] + "/deploy/systemd/nhb.service"; install[1] != want {
		t.Fatalf("root installed the unit from %s, not from the checkout (%s): the copy in %s is the service user's to rewrite", install[1], want, slash(h.install))
	}
	installed := string(mustRead(t, h.log+".installed"))
	if strings.Contains(installed, "User=root") || strings.Contains(installed, "owned-by-the-node") || !strings.Contains(installed, "User=nhb") {
		t.Fatalf("root installed a unit the service user wrote:\n%s", installed)
	}
	// What is compared with the running service is the same file, or a change to the
	// copy the service user owns would be taken for a change of the unit.
	if !strings.Contains(calls, "fingerprint-of "+install[1]) {
		t.Fatalf("the fingerprint of the running service is not taken from the unit that was installed:\n%s", calls)
	}
}

// closedAddress is a loopback address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// recordingTool writes into dir a stand-in for the tool called name: it notes which
// directory it was run from and with which command (in $TOOL_LOG), does what
// behaviour says (bash, run first; it may end the run), and otherwise runs the tool
// it stands for.
func recordingTool(t *testing.T, dir, name, label, target, behaviour string) {
	t.Helper()
	body := "#!/usr/bin/env bash\necho \"" + label + " $1\" >> \"$TOOL_LOG\"\n" + behaviour + "exec \"" + slash(target) + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// What root or the operator runs itself is the tool root built, never the copy in
// the install directory that the service user owns and can replace. Each of the
// three steps below is one whose answer matters, and a replaced tool would give the
// one it likes: check-config says a config is right, generate-key writes the key the
// validator then has, wait-synced says the node is at the tip and lets the
// registration go ahead.
func TestDeployRunsTheToolsRootBuiltAndNeverTheServiceUsersCopies(t *testing.T) {
	newHarness := func(t *testing.T, serviceCLI, serviceTool string) (*deployHarness, string) {
		h := newDeployHarness(t)
		log := filepath.Join(h.root, "tools.log")
		fresh := filepath.Join(h.root, "fake-nhb-cli.sh")
		if err := os.WriteFile(fresh, []byte(strings.Replace(fakeCLIScript, "%s", goodBeneficiary, 1)), 0o755); err != nil {
			t.Fatal(err)
		}
		tool := builtTool(t)
		// Root's own copies do what they should. The service user's are the ones a
		// compromised node would have written: they do what they like, and keep the
		// commands the service user itself is given (address, and so on) working.
		recordingTool(t, h.tools, "nhb-cli", "tools", fresh, "")
		recordingTool(t, h.tools, "nhb-snapshot", "tools", tool, "")
		recordingTool(t, filepath.Join(h.install, "bin"), "nhb-cli", "service-tree", fresh, serviceCLI)
		recordingTool(t, filepath.Join(h.install, "bin"), "nhb-snapshot", "service-tree", tool, serviceTool)
		return h, log
	}
	ran := func(t *testing.T, log string) string {
		data, _ := os.ReadFile(log)
		return string(data)
	}

	t.Run("check-config decides whether a config is installed", func(t *testing.T) {
		h, log := newHarness(t, "", "case \"$1\" in check-config) echo ok; exit 0 ;; esac\n")
		// A template whose values are not the network's: the tool that is run says so.
		drifted := strings.Replace(string(mustRead(t, filepath.Join(repoRoot(t), "config.toml"))), "QuorumCertActivationHeight = 0", "QuorumCertActivationHeight = 451949", 1)
		if err := os.WriteFile(filepath.Join(h.install, "config.toml"), []byte(drifted), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := h.run("BOOTNODE=peer.example.invalid:6001; install_config", "TOOL_LOG="+slash(log))
		if code == 0 || !strings.Contains(out, "QuorumCertActivationHeight") {
			t.Fatalf("a config that does not carry the network's values was accepted because the service user's copy of the tool said yes (exit %d):\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(h.config, "config.toml")); err == nil {
			t.Fatalf("a config that failed the check was installed")
		}
		if got := ran(t, log); !strings.Contains(got, "tools check-config") || strings.Contains(got, "service-tree check-config") {
			t.Fatalf("check-config was not run from root's directory of tools:\n%s", got)
		}
	})

	t.Run("generate-key makes the key the validator has", func(t *testing.T) {
		h, log := newHarness(t, "case \"$1\" in generate-key) echo KEY-THE-NODE-KEEPS > wallet.key; exit 0 ;; esac\n", "")
		out, code := h.run("ensure_key\n", "TOOL_LOG="+slash(log))
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		key := string(mustRead(t, filepath.Join(h.config, "validator.key")))
		if strings.Contains(key, "KEY-THE-NODE-KEEPS") {
			t.Fatalf("the validator's key is one the service user made: the script ran its copy of nhb-cli to generate it")
		}
		got := ran(t, log)
		if !strings.Contains(got, "tools generate-key") || strings.Contains(got, "service-tree generate-key") {
			t.Fatalf("generate-key was not run from root's directory of tools:\n%s", got)
		}
		// What is asked of the service user's own key file is run as that user, from its
		// own tree: that is where its binaries are.
		if !strings.Contains(got, "service-tree address") {
			t.Fatalf("the address was not read by the service user's own nhb-cli:\n%s", got)
		}
	})

	t.Run("wait-synced lets the registration go ahead", func(t *testing.T) {
		h, log := newHarness(t, "", "case \"$1\" in wait-synced) echo '{\"height\": 1}'; exit 0 ;; esac\n")
		// No node answers: the real tool gives up on it, the service user's copy says it
		// is at the tip.
		out, code := h.run("RPC_ADDR="+closedAddress(t)+"; SYNC_INTERVAL=100ms; SYNC_TIMEOUT_SECS=40; RPC_UP_TIMEOUT_SECS=1\nwait_until_synced\necho reached",
			"NHB_SYNC_INTERVAL=100ms", "TOOL_LOG="+slash(log))
		if code == 0 || strings.Contains(out, "reached") {
			t.Fatalf("the node was reported at the tip although no node answered, because the service user's copy of the tool said so (exit %d):\n%s", code, out)
		}
		if got := ran(t, log); !strings.Contains(got, "tools wait-synced") || strings.Contains(got, "service-tree wait-synced") {
			t.Fatalf("wait-synced was not run from root's directory of tools:\n%s", got)
		}
	})
}

// Root builds with everything it reads and writes in a directory only root can write,
// from the checkout it runs from. The Go build cache used to be in the install
// directory, which the service user owns: entries it planted there are compiled
// into the binaries root builds next, and those are then run as root.
func TestDeployBuildsAsRootInADirectoryOnlyRootCanWrite(t *testing.T) {
	h := newDeployHarness(t)
	out, code := h.run(loggedRoot + "echo \"checkout $REPO_ROOT\" >> \"$FAKE_LOG\"\ninstall_tree_and_build\n")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	calls := h.calls()
	checkout := regexp.MustCompile(`(?m)^checkout (\S+)$`).FindStringSubmatch(calls)
	if checkout == nil {
		t.Fatalf("the checkout was not recorded:\n%s", calls)
	}
	build, tools, install := slash(h.build), slash(h.tools), slash(h.install)
	var builds []string
	for _, line := range strings.Split(calls, "\n") {
		if strings.Contains(line, "go build") {
			builds = append(builds, line)
		}
	}
	if len(builds) != 3 {
		t.Fatalf("expected the three tools to be built, saw %d builds:\n%s", len(builds), calls)
	}
	for i, pkg := range []string{"nhb", "nhb-cli", "nhb-snapshot"} {
		line := builds[i]
		for _, want := range []string{" GOCACHE=" + build + "/go-cache ", " GOPATH=" + build + "/go-path ", " GOTMPDIR=" + build + "/go-tmp ", " TMPDIR=" + build + "/go-tmp ", " -o " + tools + "/" + pkg + " "} {
			if !strings.Contains(line+" ", want) {
				t.Errorf("the build of %s does not carry %q:\n%s", pkg, want, line)
			}
		}
		// Nothing the build reads or writes is in a place the service user owns.
		for _, place := range []string{install, slash(h.state), slash(h.config)} {
			if strings.Contains(strings.Replace(line, "cwd="+checkout[1], "", 1), place) {
				t.Errorf("the build of %s uses %s, which belongs to the service user:\n%s", pkg, place, line)
			}
		}
		// It builds the checkout, not the copy the service user could rewrite while a
		// build that takes minutes reads it.
		if !strings.Contains(line, "cwd="+checkout[1]+" ") {
			t.Errorf("the build of %s does not run in the checkout %s:\n%s", pkg, checkout[1], line)
		}
	}
	// The directories are made root's and closed before anything is built into them,
	// and nothing in them is ever handed to the service user.
	mustContainInOrder(t, calls,
		"install -d -m 0755 -o root -g root "+build+" "+tools,
		"install -d -m 0700 -o root -g root "+build+"/go-cache "+build+"/go-path "+build+"/go-tmp",
		"go build")
	for _, line := range strings.Split(calls, "\n") {
		if strings.Contains(line, "chown") && strings.Contains(line, build) {
			t.Errorf("something in root's build directory was chowned: %s", line)
		}
		if strings.Contains(line, "chown -R") && !strings.HasSuffix(line, "chown -R nhb:nhb "+install) {
			t.Errorf("a recursive chown that is not of the install directory alone: %s", line)
		}
	}
	// The service user gets copies of the tools, made after they were built, and
	// only then is the tree given to it.
	mustContainInOrder(t, calls,
		"go build", "go build", "go build",
		"install -m 0755 "+tools+"/nhb "+install+"/bin/nhb",
		"install -m 0755 "+tools+"/nhb-cli "+install+"/bin/nhb-cli",
		"install -m 0755 "+tools+"/nhb-snapshot "+install+"/bin/nhb-snapshot",
		"chown -R nhb:nhb "+install)
	// The copy of the tree carries no cache of its own to keep: what an earlier
	// version left under the install directory is removed by it.
	if strings.Contains(calls, "--exclude") || strings.Contains(calls, ".gocache") || strings.Contains(calls, ".gopath") || strings.Contains(calls, ".gotmp") {
		t.Errorf("the tree is still copied around a build cache in the install directory:\n%s", calls)
	}
}

// Read as text, the script does not run, install or build with anything in the
// install directory as root or as the operator. (The behaviour is tested above; this
// is what notices a new line that does.) A command that starts with as_service or
// sudo -u runs as the service user, whose own tree it is; a hash, a copy's
// destination and a printed command are not run.
func TestDeployScriptRunsNothingOfTheInstallDirectoryAsRootOrTheOperator(t *testing.T) {
	raw := string(mustRead(t, filepath.Join(repoRoot(t), "scripts", "deployvalidator.sh")))
	var logical []string
	pending := ""
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		logical = append(logical, pending+line)
		pending = ""
	}
	for _, line := range logical {
		switch {
		case strings.Contains(line, "as_service") || strings.Contains(line, "sudo -u"):
			continue // the service user runs its own tree
		case strings.Contains(line, "sha256sum") || strings.Contains(line, "TOKEN_RECIPE") || strings.Contains(line, "echo "):
			continue // a hash, a printed command
		case strings.Contains(line, "install -m 0755 \"${TOOL_DIR}/"):
			continue // the destination of a copy
		}
		if strings.Contains(line, "${INSTALL_ROOT}/bin/") {
			t.Errorf("runs a tool of the install directory as root or the operator: %s", strings.TrimSpace(line))
		}
		if strings.Contains(line, "${INSTALL_ROOT}/.go") || strings.Contains(line, "${INSTALL_ROOT}/deploy/") {
			t.Errorf("reads a build cache or the unit from the install directory: %s", strings.TrimSpace(line))
		}
	}
}

// The script is run from a checkout of the operator's, never from inside a directory
// the service user owns: as root it would build, install a unit from and run files
// that user can rewrite, itself included. (A second run from /opt/nhbchain, where the
// first run left a copy of the checkout, is the way to do it by mistake.)
func TestDeployRefusesToRunFromInsideTheServiceUsersDirectories(t *testing.T) {
	h := newDeployHarness(t)
	elsewhere := filepath.Join(h.root, "home", "operator", "nhbchain")
	inside := map[string]string{
		"the install directory itself": h.install,
		"a copy under the install dir": filepath.Join(h.install, "scripts", "nested"),
		"the state directory":          filepath.Join(h.state, "src"),
		"the config directory":         filepath.Join(h.config, "src"),
	}
	for _, dir := range append([]string{elsewhere}, filepath.Join(h.install, "scripts", "nested"), filepath.Join(h.state, "src"), filepath.Join(h.config, "src")) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, dir := range inside {
		t.Run(name, func(t *testing.T) {
			out, code := h.run("REPO_ROOT=\"" + slash(dir) + "\"\nrefuse_service_owned_checkout\n")
			if code == 0 || !strings.Contains(out, "is inside") || !strings.Contains(out, "belongs to the service user") {
				t.Fatalf("a checkout inside a directory of the service user was accepted (exit %d):\n%s", code, out)
			}
		})
	}
	t.Run("a checkout of the operator's own", func(t *testing.T) {
		out, code := h.run("REPO_ROOT=\"" + slash(elsewhere) + "\"\nrefuse_service_owned_checkout\necho accepted\n")
		if code != 0 || !strings.Contains(out, "accepted") {
			t.Fatalf("a checkout outside the service user's directories was refused (exit %d):\n%s", code, out)
		}
	})
	t.Run("a directory that only starts with the same letters is not inside", func(t *testing.T) {
		near := h.install + "-src"
		if err := os.MkdirAll(near, 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := h.run("REPO_ROOT=\"" + slash(near) + "\"\nrefuse_service_owned_checkout\necho accepted\n")
		if code != 0 || !strings.Contains(out, "accepted") {
			t.Fatalf("%s was taken for a directory inside %s (exit %d):\n%s", near, h.install, code, out)
		}
	})
	// A checkout reached through a link (~/nhbchain pointing at /opt/nhbchain) is the
	// one it really is: the guard resolves the path (physical_dir) before it compares.
	// The first test answers that one question for a link it does not have to make;
	// the second makes the link when the machine lets it, and skips, saying so, when it
	// does not.
	t.Run("a checkout reached through a link, without making one", func(t *testing.T) {
		via := slash(filepath.Join(h.root, "home", "operator", "link-to-the-install-directory"))
		stub := "physical_dir() { if [[ \"$1\" == \"" + via + "\" ]]; then ( cd \"${NHB_INSTALL_ROOT}\" && pwd -P ); else ( cd \"$1\" 2>/dev/null && pwd -P ); fi; }\n"
		out, code := h.run(stub + "REPO_ROOT=\"" + via + "\"\nrefuse_service_owned_checkout\n")
		if code == 0 || !strings.Contains(out, "is inside") {
			t.Fatalf("a checkout that is the install directory, reached through a link, was accepted (exit %d):\n%s", code, out)
		}
		// The same stand-in leaves a checkout that is not reached through it alone.
		out, code = h.run(stub + "REPO_ROOT=\"" + slash(elsewhere) + "\"\nrefuse_service_owned_checkout\necho accepted\n")
		if code != 0 || !strings.Contains(out, "accepted") {
			t.Fatalf("a checkout of the operator's own was refused (exit %d):\n%s", code, out)
		}
	})
	t.Run("a checkout reached through a link", func(t *testing.T) {
		link := filepath.Join(h.root, "home", "operator", "link-to-the-install-directory")
		if err := os.Symlink(h.install, link); err != nil {
			t.Skipf("this machine cannot make symbolic links (%v); the same refusal is tested above, with a stand-in for physical_dir, which is where the script resolves a link", err)
		}
		out, code := h.run("REPO_ROOT=\"" + slash(link) + "\"\nrefuse_service_owned_checkout\n")
		if code == 0 || !strings.Contains(out, "is inside") {
			t.Fatalf("a checkout that is the install directory, reached through a link, was accepted (exit %d):\n%s", code, out)
		}
	})
	t.Run("the run stops before it touches anything", func(t *testing.T) {
		_ = os.Remove(h.log)
		out, code := h.run("REPO_ROOT=\"" + slash(h.install) + "\"\nmain --beneficiary " + goodBeneficiary + " --snapshot-url https://snapshots.example.invalid/nhb --bootnode peer.example.invalid:6001\n")
		if code == 0 || !strings.Contains(out, "belongs to the service user") {
			t.Fatalf("main went on from the service user's directory (exit %d):\n%s", code, out)
		}
		if calls := h.calls(); calls != "" {
			t.Fatalf("something was run before the refusal:\n%s", calls)
		}
	})
}

// The command the script prints for the operator to get a token reads the node's RPC
// secret as data. node.env is in /etc/nhbchain, a directory the service user owns:
// it can replace the file, and a root shell that sourced the file ran what it found.
func TestDeployTokenRecipeReadsTheNodeSecretAsDataNotAsCode(t *testing.T) {
	h := newDeployHarness(t)
	marker := filepath.Join(h.root, "sourced-as-root")
	env := "NHB_ENV=prod\nNHB_RPC_JWT_SECRET=the-node-secret\ntouch " + slash(marker) + "\nNHB_VALIDATOR_RAW_KEY=00\n"
	if err := os.WriteFile(filepath.Join(h.config, "node.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	// A CLI that mints "a token" from the secret it is given on standard input.
	cli := "#!/usr/bin/env bash\ncase \"$1\" in rpc-token) echo \"token-for-$(cat)\" ;; esac\n"
	if err := os.WriteFile(filepath.Join(h.install, "bin", "nhb-cli"), []byte(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	out, code := h.run("eval \"${TOKEN_RECIPE}\"\necho \"TOKEN=${TOKEN}\"\n")
	if code != 0 || !strings.Contains(out, "TOKEN=token-for-the-node-secret") {
		t.Fatalf("the recipe did not give a token for the node's secret (exit %d):\n%s", code, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the recipe ran what was in node.env: a command in a file the service user owns ran as root")
	}
}
