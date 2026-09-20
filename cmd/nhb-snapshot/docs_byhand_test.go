package main

// "Doing it by hand" (docs/validators/snapshot-onboarding.md) says that each block does
// what a function of scripts/deployvalidator.sh does, and that whatever touches the
// service user's directories is done as that user. The service user is the
// network-facing part of the host and owns /etc/nhbchain, /var/lib/nhbchain and
// /opt/nhbchain, so after the node has run it can leave a link under any name in them,
// and a command that root runs on such a name follows the link: root writes where the
// link leads, or reads what it leads to and hands it on. These tests read the block for
// that, and run the step that writes the node's environment file the way the script's
// write_env does.
//
// Where a test needs a real symbolic link it makes one when the machine lets it and
// skips, saying so, when it does not (Windows needs a privilege for that). The same
// refusals are also tested without any link: the fake sudo of the step-five test answers
// "test -L" for the names it is told are links.

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// byHandBlock returns the code block of "Doing it by hand".
func byHandBlock(t *testing.T) string {
	t.Helper()
	text := docSection(t, "docs/validators/snapshot-onboarding.md", "## Doing it by hand")
	const open = "```bash\n"
	start := strings.Index(text, open)
	if start < 0 {
		t.Fatal("Doing it by hand has no code block")
	}
	rest := text[start+len(open):]
	end := strings.Index(rest, "\n```")
	if end < 0 {
		t.Fatal("the code block of Doing it by hand is not closed")
	}
	return rest[:end+1]
}

// byHandStep returns step n of that block: from its "# n. " line to the line before
// the next step's.
func byHandStep(t *testing.T, n int) string {
	t.Helper()
	lines := strings.Split(byHandBlock(t), "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if strings.HasPrefix(line, "# "+strconv.Itoa(n)+". ") {
			start = i
		}
		if start >= 0 && i > start && strings.HasPrefix(line, "# "+strconv.Itoa(n+1)+". ") {
			end = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("Doing it by hand has no step %d", n)
	}
	return strings.Join(lines[start:end], "\n") + "\n"
}

// commandLines returns the commands of a piece of a code block, one per logical line:
// a line that ends with a backslash is joined to the next, and comments and blank
// lines are dropped.
func commandLines(text string) []string {
	var out []string
	pending := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if pending == "" && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			pending += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		out = append(out, strings.Join(strings.Fields(pending+line), " "))
		pending = ""
	}
	return out
}

// rootCall is one sudo of a line that runs as root: the command it runs, that
// command's arguments, and the options given to sudo.
type rootCall struct {
	verb string
	args []string
	opts []string
}

var sudoCall = regexp.MustCompile(`\bsudo\b([^|;&)]*)`)

// rootCalls returns the commands of a line that root runs: every sudo that does not
// say -u followed by another user.
func rootCalls(line string) []rootCall {
	var calls []rootCall
	for _, m := range sudoCall.FindAllStringSubmatch(line, -1) {
		words := strings.Fields(m[1])
		var opts []string
		user := ""
		i := 0
		for i < len(words) && strings.HasPrefix(words[i], "-") {
			if words[i] == "-u" && i+1 < len(words) {
				user = words[i+1]
				i += 2
				continue
			}
			opts = append(opts, words[i])
			i++
		}
		if user != "" && user != "root" {
			continue
		}
		if i < len(words) && words[i] == "env" {
			for i++; i < len(words) && strings.Contains(words[i], "="); i++ {
			}
		}
		if i >= len(words) {
			continue
		}
		calls = append(calls, rootCall{verb: words[i], args: words[i+1:], opts: opts})
	}
	return calls
}

// serviceUserPath is a name in one of the directories the service user owns.
var serviceUserPath = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(/etc|/var/lib|/opt)/nhbchain(/|$|["'])`)

func mentionsServiceUserPath(s string) bool { return serviceUserPath.MatchString(s) }

func hasWord(words []string, want string) bool {
	for _, w := range words {
		if w == want {
			return true
		}
	}
	return false
}

// isRootShell is a command that gives root a shell, which can redirect into any name.
func isRootShell(call rootCall) bool {
	switch call.verb {
	case "sh", "bash", "dash", "zsh", "su":
		return true
	}
	for _, opt := range call.opts {
		if opt == "-s" || opt == "-i" || opt == "--shell" || opt == "--login" {
			return true
		}
	}
	return false
}

// The block never has root open a name in the service user's directories by itself.
// The only things root does to those names are the ones that create, replace or test
// them and read nothing of them: install (which replaces what is at the name, a link
// included, and does not write through one) to that name as the last argument, mkdir,
// rsync into a directory, chown -R (which does not follow links), test -L, sed -n as
// data, and the tool that checks a config. A root shell, tee, cp, cat, od, chmod, a
// redirect made by a root shell, or a service-user name that root reads as the source
// of a copy, is what follows a link.
func TestDoingItByHandRootOnlyCreatesInTheServiceUsersDirectories(t *testing.T) {
	block := byHandBlock(t)
	checked := 0
	for _, line := range commandLines(block) {
		for _, call := range rootCalls(line) {
			checked++
			if isRootShell(call) {
				t.Errorf("Doing it by hand gives root a shell (%s), which can write through any name: %s", call.verb, line)
				continue
			}
			var named []string
			for _, arg := range call.args {
				if mentionsServiceUserPath(arg) {
					named = append(named, arg)
				}
			}
			if len(named) == 0 {
				continue
			}
			last := call.args[len(call.args)-1]
			ok := false
			switch {
			case call.verb == "useradd" || call.verb == "mkdir":
				ok = true
			case call.verb == "test":
				ok = call.args[0] == "-L"
			case call.verb == "chown":
				ok = call.args[0] == "-R"
			case call.verb == "install" && hasWord(call.args, "-d"):
				ok = true
			case call.verb == "install" || call.verb == "rsync":
				ok = len(named) == 1 && named[0] == last
			case call.verb == "sed":
				ok = call.args[0] == "-n" && !hasWord(call.args, "-i") && !hasWord(call.args, "--in-place")
			case strings.HasSuffix(call.verb, "/nhb-snapshot"):
				ok = call.args[0] == "check-config"
			}
			if !ok {
				t.Errorf("Doing it by hand has root run %q on %v, names in a directory the service user owns: that user can leave a link under any of them and root follows it (root writes where it leads, or reads what it leads to). Run it as the service user (sudo -u nhb), or put the file there with install from a file of your own: %s", call.verb, named, line)
			}
		}
	}
	if checked < 15 {
		t.Fatalf("the test found %d commands that root runs; it no longer reads the block", checked)
	}

	// The config is copied from the checkout: /opt/nhbchain belongs to the service
	// user, and install reads its source through a link.
	step4 := byHandStep(t, 4)
	if !strings.Contains(step4, "sudo install -m 0600 -o nhb -g nhb $SRC/config.toml /etc/nhbchain/config.toml") {
		t.Errorf("step 4 does not install the config from the checkout ($SRC/config.toml):\n%s", step4)
	}
	if strings.Contains(strings.Join(commandLines(step4), "\n"), "install -m 0600 -o nhb -g nhb /opt/nhbchain/config.toml") {
		t.Errorf("step 4 has root read /opt/nhbchain/config.toml, a name in the service user's tree:\n%s", step4)
	}
}

// fencedCommands returns the commands of every code block of a page.
func fencedCommands(page string) []string {
	var out []string
	var block []string
	fenced := false
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if fenced {
				out = append(out, commandLines(strings.Join(block, "\n"))...)
				block = nil
			}
			fenced = !fenced
			continue
		}
		if fenced {
			block = append(block, line)
		}
	}
	return out
}

// No page shows root writing into the service user's directories through a shell or
// tee: "sudo sh -c '... > /etc/nhbchain/node.env'" truncates whatever a link at
// node.env leads to, /etc/passwd included.
func TestNoPageHasRootWriteIntoTheServiceUsersDirectoriesThroughAShell(t *testing.T) {
	seen := 0
	for _, page := range []string{"README.md", "docs/validators/onboarding.md", "docs/validators/snapshot-onboarding.md", "docs/ops/snapshots.md"} {
		for _, line := range fencedCommands(readRepoFile(t, page)) {
			for _, call := range rootCalls(line) {
				seen++
				if isRootShell(call) && mentionsServiceUserPath(line) {
					t.Errorf("%s has a root shell in a command that names the service user's directories: %s", page, line)
				}
				if call.verb == "tee" && mentionsServiceUserPath(strings.Join(call.args, " ")) {
					t.Errorf("%s has root write with tee through a name in the service user's directories: %s", page, line)
				}
			}
			if strings.Contains(line, "sudo sh -c") && strings.Contains(line, "> /etc/nhbchain/") {
				t.Errorf("%s has root redirect into /etc/nhbchain: %s", page, line)
			}
		}
	}
	if seen < 10 {
		t.Fatalf("the test found %d commands that root runs; it no longer reads the pages", seen)
	}
}

// A stand-in for sudo: it records who each command ran as, answers "test -L" as a link
// would for the names in $FAKE_LINKS (and as the machine does for any other), and does
// what GNU install does with a name that is already there: removes it and makes a new
// file, never writing through a link.
const stepFiveSudo = `#!/usr/bin/env bash
who=root
while [ $# -gt 0 ]; do
  case "$1" in
    -u) who=$2; shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
echo "$who $*" >> "$FAKE_LOG"
case "$1" in
  test)
    shift
    if [ "$1" = -L ]; then
      case " $FAKE_LINKS " in *" $2 "*) exit 0 ;; esac
    fi
    [ "$@" ]
    exit $?
    ;;
  install)
    shift
    mode=''; src=''; dest=''
    while [ $# -gt 0 ]; do
      case "$1" in
        -m) mode=$2; shift 2 ;;
        -o|-g) shift 2 ;;
        *) if [ -z "$src" ]; then src=$1; else dest=$1; fi; shift ;;
      esac
    done
    rm -f "$dest" && cp "$src" "$dest" || exit 1
    if [ -n "$mode" ]; then chmod "$mode" "$dest"; fi
    exit 0
    ;;
esac
exec "$@"
`

const stepFiveOpenssl = `#!/usr/bin/env bash
echo "operator openssl $*" >> "$FAKE_LOG"
printf '%s\n' "$FAKE_SECRET"
`

// stepFive runs step 5 of "Doing it by hand" in a directory that stands for
// /etc/nhbchain, with a sudo that records who runs what.
type stepFive struct {
	t                *testing.T
	etc, tmp, log    string
	script, bin      string
	keyHex, secret   string
	bash             string
	nodeEnv, keyFile string
}

func newStepFive(t *testing.T) *stepFive {
	t.Helper()
	root := t.TempDir()
	s := &stepFive{t: t, bash: e2eBash(t), secret: strings.Repeat("5e", 32)}
	s.etc = filepath.Join(root, "etc", "nhbchain")
	s.tmp = filepath.Join(root, "envtmp")
	s.bin = filepath.Join(root, "fakebin")
	s.log = filepath.Join(root, "calls.log")
	s.nodeEnv = filepath.Join(s.etc, "node.env")
	s.keyFile = filepath.Join(s.etc, "validator.key")
	for _, d := range []string{s.etc, s.tmp, s.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"sudo": stepFiveSudo, "openssl": stepFiveOpenssl} {
		if err := os.WriteFile(filepath.Join(s.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A key of 32 different bytes: od prints a line that repeats the one before as "*".
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0x10 + i*7)
	}
	s.keyHex = hex.EncodeToString(key)
	if err := os.WriteFile(s.keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	// The step as the page has it, with the directory of the sandbox in place of
	// /etc/nhbchain.
	step := strings.ReplaceAll(byHandStep(t, 5), "/etc/nhbchain", slash(s.etc))
	s.script = filepath.Join(root, "step5.sh")
	program := "FAKE_BIN=$(cd \"$FAKE_BIN\" && pwd)\nexport PATH=\"$FAKE_BIN:$PATH\"\n" + step
	if err := os.WriteFile(s.script, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// run runs the step; links are the names (of the sandbox) that "test -L" answers yes for.
func (s *stepFive) run(links ...string) (string, int) {
	s.t.Helper()
	cmd := exec.Command(s.bash, slash(s.script))
	cmd.Dir = filepath.Dir(s.script)
	cmd.Env = append(os.Environ(),
		"FAKE_BIN="+slash(s.bin), "FAKE_LOG="+slash(s.log), "FAKE_LINKS="+strings.Join(links, " "),
		"FAKE_SECRET="+s.secret, "TMPDIR="+slash(s.tmp),
		"PATH="+s.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	s.t.Fatalf("run bash: %v", err)
	return "", -1
}

func (s *stepFive) calls() string {
	data, _ := os.ReadFile(s.log)
	return string(data)
}

// nothingLeftInTmp fails the test when the run left a file in the temporary directory:
// the file the step builds holds the key and the secret.
func (s *stepFive) nothingLeftInTmp() {
	s.t.Helper()
	if entries, _ := os.ReadDir(s.tmp); len(entries) > 0 {
		s.t.Fatalf("the step left %d file(s) in the temporary directory; the file it builds holds the key and the secret", len(entries))
	}
}

// Step 5 is write_env. The service user owns /etc/nhbchain and can leave a link at
// node.env or validator.key. A root shell that redirects into node.env overwrites what
// a link there leads to (a sudoers file, /etc/passwd); root's od on validator.key
// hex-dumps what a link there leads to into NHB_VALIDATOR_RAW_KEY, which systemd then
// hands to the node. write_env reads the key as the service user, builds the file in a
// private temporary file and installs it over the name; so does the step.
func TestDoingItByHandStepFiveBuildsTheEnvironmentFileAsTheScriptDoes(t *testing.T) {
	t.Run("what it writes, and who does what", func(t *testing.T) {
		s := newStepFive(t)
		out, code := s.run()
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		want := "NHB_ENV=prod\nNHB_RPC_JWT_SECRET=" + s.secret + "\nNHB_VALIDATOR_RAW_KEY=" + s.keyHex + "\n"
		got, err := os.ReadFile(s.nodeEnv)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Fatalf("node.env holds:\n%s\nwant:\n%s", got, want)
		}
		calls := s.calls()
		key, env := slash(s.keyFile), slash(s.nodeEnv)
		for _, wantCall := range []string{
			"root test -L " + env,           // a link stops the step
			"nhb od -An -tx1 " + key,        // the key is read by the user who owns it
			"operator openssl rand -hex 32", // the secret, by whoever runs the step
		} {
			if !strings.Contains(calls, wantCall) {
				t.Fatalf("expected %q in what ran:\n%s", wantCall, calls)
			}
		}
		// The file root installs is one the operator made in the temporary directory (the
		// bash here shows that directory in its own form, so the path is compared by its
		// tail), and it goes over node.env.
		installed := regexp.MustCompile(`(?m)^root install -m 0600 -o root -g root (\S+/envtmp/tmp\.\S+) (\S+)$`).FindStringSubmatch(calls)
		if installed == nil || installed[2] != env {
			t.Fatalf("root did not install a file of the operator's temporary directory over node.env:\n%s", calls)
		}
		// Root opens no name in the directory but to test it and to install over one, and
		// runs no shell: everything else that touched it ran as the service user.
		for _, line := range strings.Split(strings.TrimSpace(calls), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[0] != "root" {
				continue
			}
			if fields[1] != "test" && fields[1] != "install" {
				t.Errorf("root ran %q, which follows a link at a name in the service user's directory", line)
			}
		}
		// Neither the key nor the secret is on a command line (where any user could read
		// it in the process list): they are in the shell and in a file of the operator's.
		if strings.Contains(calls, s.keyHex) || strings.Contains(calls, s.secret) {
			t.Fatalf("the key or the secret was on a command line:\n%s", calls)
		}
		s.nothingLeftInTmp()
	})

	t.Run("a node.env that is a link stops the step", func(t *testing.T) {
		s := newStepFive(t)
		if err := os.WriteFile(s.nodeEnv, []byte("PLANTED\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, code := s.run(slash(s.nodeEnv))
		if code == 0 || !strings.Contains(out, "is a symbolic link") {
			t.Fatalf("a node.env that is a link was accepted (exit %d):\n%s", code, out)
		}
		if got, _ := os.ReadFile(s.nodeEnv); string(got) != "PLANTED\n" {
			t.Fatalf("node.env was written although it is a link: %q", got)
		}
		calls := s.calls()
		if strings.Contains(calls, "install") || strings.Contains(calls, " od ") || strings.Contains(calls, "openssl") {
			t.Fatalf("the step went on after it found a link:\n%s", calls)
		}
		s.nothingLeftInTmp()
	})

	t.Run("a key that cannot be read leaves no environment file", func(t *testing.T) {
		s := newStepFive(t)
		if err := os.Remove(s.keyFile); err != nil {
			t.Fatal(err)
		}
		out, code := s.run()
		if code == 0 {
			t.Fatalf("the step went on without a key:\n%s", out)
		}
		if _, err := os.Stat(s.nodeEnv); err == nil {
			t.Fatalf("node.env was written without a key")
		}
		if strings.Contains(s.calls(), "install") {
			t.Fatalf("the step installed a file without a key:\n%s", s.calls())
		}
		s.nothingLeftInTmp()
	})

	t.Run("a failed install leaves nothing behind", func(t *testing.T) {
		s := newStepFive(t)
		// The place the file is to go is a directory: install cannot put a file there.
		if err := os.MkdirAll(filepath.Join(s.nodeEnv, "inside"), 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := s.run()
		if code == 0 {
			t.Fatalf("the step reported success although the file was not installed:\n%s", out)
		}
		s.nothingLeftInTmp()
	})

	t.Run("a real link", func(t *testing.T) {
		probe := t.TempDir()
		if err := os.Symlink(filepath.Join(probe, "target"), filepath.Join(probe, "link")); err != nil {
			t.Skipf("this machine cannot make symbolic links (%v); the same refusal is tested above with a sudo that answers test -L for the names it is told are links", err)
		}
		s := newStepFive(t)
		victim := filepath.Join(t.TempDir(), "only-root-can-write")
		if err := os.WriteFile(victim, []byte("PRECIOUS\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, victim, s.nodeEnv)
		out, code := s.run()
		if code == 0 || !strings.Contains(out, "is a symbolic link") {
			t.Fatalf("a node.env that is a link was accepted (exit %d):\n%s", code, out)
		}
		if got, _ := os.ReadFile(victim); string(got) != "PRECIOUS\n" {
			t.Fatalf("what the link led to was written: %q", got)
		}
		if info, err := os.Lstat(s.nodeEnv); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the link was replaced or removed: %v", err)
		}
		s.nothingLeftInTmp()
	})

	t.Run("the page and the script do the same", func(t *testing.T) {
		step := strings.Join(strings.Fields(byHandStep(t, 5)), " ")
		script := readRepoFile(t, "scripts/deployvalidator.sh")
		for _, pair := range []struct{ page, script string }{
			{"sudo test -L /etc/nhbchain/node.env", `as_root test -L "${CONFIG_DIR}/node.env"`},
			{"sudo -u nhb od -An -tx1 /etc/nhbchain/validator.key", `as_service od -An -tx1 "${VALIDATOR_KEY_FILE}"`},
			{`sudo install -m 0600 -o root -g root "$ENVF" /etc/nhbchain/node.env`, `as_root install -m 0600 -o root -g root "${tmp}" "${CONFIG_DIR}/node.env"`},
			{`chmod 600 "$ENVF"`, `chmod 600 "${tmp}"`},
			{`NHB_ENV=prod`, `echo "NHB_ENV=prod"`},
			{`NHB_RPC_JWT_SECRET=`, `echo "NHB_RPC_JWT_SECRET=`},
			{`NHB_VALIDATOR_RAW_KEY=`, `echo "NHB_VALIDATOR_RAW_KEY=`},
		} {
			if !strings.Contains(step, pair.page) {
				t.Errorf("step 5 does not have %q", pair.page)
			}
			if !strings.Contains(script, pair.script) {
				t.Errorf("write_env does not have %q, which step 5 copies", pair.script)
			}
		}
	})
}
