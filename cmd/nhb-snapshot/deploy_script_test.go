package main

// Tests of scripts/deployvalidator.sh. The argument checks run the script
// itself; everything else sources it (its main function only runs when it is
// executed) and calls its functions against fake sudo, systemctl and ps, a
// real nhb-snapshot, and a snapshot of a chain that has the live genesis, so
// the pinned network identity is the real one.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/crypto"
)

const (
	pinnedChainID     = "18346390202490284624"
	pinnedGenesisHash = "0xfe9b78af9223ea50f456f63c41084dd99bac4aaa3a790a10fcc26d1dc63210a2"
	liveValidatorAddr = "nhb1uy6sfhsqaas7mx05mlkv9xwu9ynfwyhyg00lm0"
	goodBeneficiary   = "nhb1c2khwjjf2uphg4220x3swrw0rsnmxezvl8vs6y"
)

var (
	toolOnce sync.Once
	toolFile string
	toolErr  error
)

// builtTool builds nhb-snapshot once for all the tests of the package.
func builtTool(t *testing.T) string {
	t.Helper()
	toolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nhb-snapshot-tool-")
		if err != nil {
			toolErr = err
			return
		}
		name := "nhb-snapshot"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		toolFile = filepath.Join(dir, name)
		out, err := exec.Command("go", "build", "-o", toolFile, ".").CombinedOutput()
		if err != nil {
			toolErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	return toolFile
}

const (
	fakeSudoScript = `#!/usr/bin/env bash
echo "sudo $*" >> "$FAKE_LOG"
while [ "${1:-}" = "-u" ]; do shift 2; done
exec "$@"
`
	fakeSystemctlScript = `#!/usr/bin/env bash
echo "systemctl $*" >> "$FAKE_LOG"
case "$1" in
  show) echo "${FAKE_MAINPID:-0}" ;;
  is-active) exit "${FAKE_ACTIVE:-3}" ;;
esac
exit 0
`
	fakePsScript = `#!/usr/bin/env bash
printf '%s\n' "${FAKE_PS:-}"
`
	fakeInstallScript = `#!/usr/bin/env bash
mode=""; dirs=0; args=()
while [ $# -gt 0 ]; do
  case "$1" in
    -d) dirs=1; shift ;;
    -m) mode="$2"; shift 2 ;;
    -o|-g) shift 2 ;;
    *) args+=("$1"); shift ;;
  esac
done
if [ "$dirs" = 1 ]; then
  for d in "${args[@]}"; do mkdir -p "$d"; if [ -n "$mode" ]; then chmod "$mode" "$d"; fi; done
  exit 0
fi
cp "${args[0]}" "${args[1]}"
if [ -n "$mode" ]; then chmod "$mode" "${args[1]}"; fi
`
	fakeChownScript = "#!/usr/bin/env bash\nexit 0\n"
)

type deployHarness struct {
	t       *testing.T
	bash    string
	repo    string
	root    string
	bin     string
	install string
	config  string
	state   string
	tmp     string
	log     string
	script  string
	// timeout, when set, kills a run that lasts longer: a script that has to give
	// up on something must do so before it.
	timeout time.Duration
}

func copyTestFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func newDeployHarness(t *testing.T) *deployHarness {
	t.Helper()
	h := &deployHarness{t: t, bash: e2eBash(t), repo: repoRoot(t), root: t.TempDir()}
	h.bin = filepath.Join(h.root, "fakebin")
	h.install = filepath.Join(h.root, "opt", "nhbchain")
	h.config = filepath.Join(h.root, "etc", "nhbchain")
	h.state = filepath.Join(h.root, "var", "lib", "nhbchain")
	h.tmp = filepath.Join(h.root, "tmp")
	h.log = filepath.Join(h.root, "calls.log")
	h.script = filepath.Join(h.repo, "scripts", "deployvalidator.sh")
	for _, d := range []string{h.bin, h.install, h.config, h.state, h.tmp, filepath.Join(h.install, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"sudo": fakeSudoScript, "systemctl": fakeSystemctlScript, "ps": fakePsScript, "install": fakeInstallScript, "chown": fakeChownScript} {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	copyTestFile(t, filepath.Join(h.repo, "config.toml"), filepath.Join(h.install, "config.toml"))
	copyTestFile(t, filepath.Join(h.repo, "config", "genesis.relaunch.json"), filepath.Join(h.install, "config", "genesis.relaunch.json"))
	copyTestFile(t, filepath.Join(h.repo, "deploy", "systemd", "nhb.service"), filepath.Join(h.install, "deploy", "systemd", "nhb.service"))
	copyTestFile(t, builtTool(t), filepath.Join(h.install, "bin", filepath.Base(builtTool(t))))
	return h
}

// run sources the script and runs snippet after it, returning the combined
// output and the exit code.
func (h *deployHarness) run(snippet string, env ...string) (string, int) {
	h.t.Helper()
	// The fakes go first on the path, in the form this bash uses for paths.
	program := "set -euo pipefail\nFAKE_BIN=$(cd \"$FAKE_BIN\" && pwd)\nexport PATH=\"$FAKE_BIN:$PATH\"\nsource \"$SCRIPT\"\n" + snippet + "\n"
	path := filepath.Join(h.root, fmt.Sprintf("snippet-%d.sh", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(program), 0o755); err != nil {
		h.t.Fatal(err)
	}
	ctx := context.Background()
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, h.bash, slash(path))
	cmd.Dir = h.root
	cmd.Env = append(os.Environ(),
		"FAKE_BIN="+slash(h.bin), "FAKE_LOG="+slash(h.log), "SCRIPT="+slash(h.script),
		"NHB_INSTALL_ROOT="+slash(h.install), "NHB_CONFIG_DIR="+slash(h.config), "NHB_STATE_DIR="+slash(h.state),
		"TMPDIR="+slash(h.tmp), "CLI_RETRY_DELAY=0", "NHB_MASTER_TREASURY=",
		// The lock of a run lives in the state directory of the user who runs the
		// script: a temporary one here, never the tester's own.
		"HOME="+slash(h.root), "XDG_STATE_HOME="+slash(filepath.Join(h.root, "xdgstate")),
		// The fakes come first on the path, in the form the platform's bash
		// converts (a drive letter's colon would break a list built inside bash).
		"PATH="+h.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Env = append(cmd.Env, env...)
	if h.timeout > 0 {
		// A program the script started (a curl that is still being fed) can outlive
		// the script the timeout killed and keep its output open: do not wait for it.
		cmd.WaitDelay = 5 * time.Second
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	h.t.Fatalf("run bash: %v", err)
	return "", -1
}

func (h *deployHarness) calls() string {
	data, _ := os.ReadFile(h.log)
	return string(data)
}

func TestDeployArgumentChecksRunBeforeAnythingIsTouched(t *testing.T) {
	h := newDeployHarness(t)
	valid := []string{"--beneficiary", goodBeneficiary, "--snapshot-url", "https://snapshots.example.invalid/nhb", "--bootnode", "peer.example.invalid:6001"}
	with := func(edit ...string) []string { return append(append([]string(nil), valid...), edit...) }
	replace := func(flag, value string) []string {
		out := append([]string(nil), valid...)
		for i := range out {
			if out[i] == flag {
				out[i+1] = value
			}
		}
		return out
	}
	cases := []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{"no arguments", nil, nil, "--beneficiary <nhb1...> is required"},
		{"no snapshot url", []string{"--beneficiary", goodBeneficiary, "--bootnode", "peer.example.invalid:6001"}, nil, "--snapshot-url"},
		{"no bootnode", []string{"--beneficiary", goodBeneficiary, "--snapshot-url", "https://snapshots.example.invalid/nhb"}, nil, "--bootnode"},
		{"plain http url", replace("--snapshot-url", "http://snapshots.example.invalid/nhb"), nil, "not https"},
		{"file url", replace("--snapshot-url", "file:///tmp/snapshots"), nil, "not https"},
		{"another scheme", replace("--snapshot-url", "ftp://snapshots.example.invalid/nhb"), nil, "must start with https://"},
		{"a url with shell characters", replace("--snapshot-url", "https://snapshots.example.invalid/nhb;rm"), nil, "characters that are not allowed"},
		{"an enode uri", replace("--bootnode", "enode://abcd@peer.example.invalid:6001"), nil, "not an enode:// URI"},
		{"a bootnode without a port", replace("--bootnode", "peer.example.invalid"), nil, "not host:port"},
		{"a beneficiary that is not an address", replace("--beneficiary", "somebody"), nil, "not an nhb1... address"},
		{"another network id", with("--network-id", "430060579445266314"), nil, "not the network this release is pinned to"},
		{"a tip rpc that is not a url", with("--tip-rpc", "peer.example.invalid"), nil, "not an http(s) URL"},
		{"a snapshot age without a unit", with("--max-snapshot-age", "48"), nil, "must look like 48h"},
		{"an rpc timeout that is not a number", with("--rpc-timeout", "soon"), nil, "--rpc-timeout must be a number of seconds"},
		{"an rpc timeout of zero", with("--rpc-timeout", "0"), nil, "--rpc-timeout must be a number of seconds"},
		{"a treasury override in the environment", valid, []string{"NHB_MASTER_TREASURY=nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj"}, "NHB_MASTER_TREASURY"},
		{"an unknown flag", with("--frobnicate"), nil, "unknown argument: --frobnicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(h.log)
			cmd := exec.Command(h.bash, append([]string{slash(h.script)}, tc.args...)...)
			cmd.Dir = h.root
			cmd.Env = append(os.Environ(), "PATH="+slash(h.bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_LOG="+slash(h.log), "NHB_INSTALL_ROOT="+slash(h.install), "NHB_CONFIG_DIR="+slash(h.config), "NHB_STATE_DIR="+slash(h.state),
				"NHB_SNAPSHOT_URL=", "NHB_BOOTNODE=", "NHB_TIP_RPC_URL=", "NHB_MASTER_TREASURY=")
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("the script accepted %v:\n%s", tc.args, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("expected %q in the output:\n%s", tc.want, out)
			}
			if calls := h.calls(); calls != "" {
				t.Fatalf("the script touched the system before it checked its arguments:\n%s", calls)
			}
		})
	}

	t.Run("valid arguments pass", func(t *testing.T) {
		out, code := h.run(`parse_args --beneficiary ` + goodBeneficiary + ` --snapshot-url https://snapshots.example.invalid/nhb --bootnode peer.example.invalid:6001 --tip-rpc https://rpc.example.invalid
validate_inputs
echo "proto=${CURL_PROTO} url=${SNAPSHOT_URL}"`)
		if code != 0 || !strings.Contains(out, "proto==https") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("the environment can supply the locations", func(t *testing.T) {
		out, code := h.run(`parse_args --beneficiary `+goodBeneficiary+`
validate_inputs
echo "url=${SNAPSHOT_URL} boot=${BOOTNODE}"`, "NHB_SNAPSHOT_URL=https://snapshots.example.invalid/nhb", "NHB_BOOTNODE=peer.example.invalid:6001")
		if code != 0 || !strings.Contains(out, "url=https://snapshots.example.invalid/nhb boot=peer.example.invalid:6001") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("plain http needs the explicit flag", func(t *testing.T) {
		out, code := h.run(`parse_args --beneficiary ` + goodBeneficiary + ` --snapshot-url http://127.0.0.1:1/x --bootnode 127.0.0.1:6001 --allow-insecure-http
validate_inputs
echo "proto=${CURL_PROTO}"`)
		if code != 0 || !strings.Contains(out, "proto==https,http,file") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("help", func(t *testing.T) {
		cmd := exec.Command(h.bash, slash(h.script), "--help")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--snapshot-url") || !strings.Contains(string(out), "as a follower") {
			t.Fatalf("help: %v\n%s", err, out)
		}
	})
}

// The script names no host of its own: where the snapshot is, and whom to sync
// from, are the operator's to say.
func TestDeployScriptNamesNoHostAndPinsTheLiveNetwork(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "deployvalidator.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	// No address that leads to a machine of its own: a loopback, private or
	// link-local address is fine, a public one is not.
	for _, addr := range regexp.MustCompile(`\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`).FindAllString(script, -1) {
		ip := net.ParseIP(addr)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
			continue
		}
		t.Fatalf("scripts/deployvalidator.sh names the public address %s", addr)
	}
	for _, forbidden := range []string{"nhbcoin.com", "amazonaws.com", "s3://"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("scripts/deployvalidator.sh names %q", forbidden)
		}
	}
	for _, pin := range []string{"NETWORK_ID_DEFAULT='" + pinnedChainID + "'", "GENESIS_HASH_DEFAULT='" + pinnedGenesisHash + "'"} {
		if !strings.Contains(script, pin) {
			t.Fatalf("the script does not pin %s", pin)
		}
	}
	for _, name := range []string{"scripts/make-snapshot.sh", "scripts/deployvalidator.sh", "docs/validators/snapshot-onboarding.md"} {
		data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(data); i++ {
			if data[i] > 0x7e || (data[i] < 0x20 && data[i] != '\n' && data[i] != '\t' && data[i] != '\r') {
				t.Fatalf("%s holds a non-ASCII or control byte 0x%02x at offset %d", name, data[i], i)
			}
		}
	}
}

func TestDeployRenderConfigChangesOnlyNodeLocalValues(t *testing.T) {
	h := newDeployHarness(t)
	out := filepath.Join(h.root, "rendered.toml")
	res, code := h.run(`LISTEN_ADDR=0.0.0.0:7001; RPC_ADDR=127.0.0.1:9545; BOOTNODE=peer.example.invalid:6001; EXTERNAL_ADDRESS_HOSTPORT=203.0.113.9:7001
render_config "${NHB_INSTALL_ROOT}/config.toml" "` + slash(out) + `"
check_config "` + slash(out) + `"`)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, res)
	}
	template := strings.Split(strings.ReplaceAll(string(mustRead(t, filepath.Join(h.install, "config.toml"))), "\r\n", "\n"), "\n")
	rendered := strings.Split(strings.ReplaceAll(string(mustRead(t, out)), "\r\n", "\n"), "\n")
	if len(template) != len(rendered) {
		t.Fatalf("the rendered config has %d lines, the template %d", len(rendered), len(template))
	}
	allowed := map[string]bool{"ListenAddress": true, "RPCAddress": true, "DataDir": true, "GenesisFile": true, "ValidatorKeystorePath": true,
		"ValidatorKMSEnv": true, "NetworkName": true, "NetworkId": true, "Bootnodes": true, "PersistentPeers": true, "ExternalAddress": true}
	changed := map[string]string{}
	for i := range template {
		if template[i] == rendered[i] {
			continue
		}
		key := strings.TrimSpace(strings.SplitN(rendered[i], "=", 2)[0])
		if !allowed[key] {
			t.Fatalf("line %d changed although %q is not a node-local key:\n- %s\n+ %s", i+1, key, template[i], rendered[i])
		}
		changed[key] = strings.TrimSpace(strings.SplitN(rendered[i], "=", 2)[1])
	}
	want := map[string]string{
		"ListenAddress":   `"0.0.0.0:7001"`,
		"RPCAddress":      `"127.0.0.1:9545"`,
		"DataDir":         `"` + slash(filepath.Join(h.state, "nhb-data")) + `"`,
		"GenesisFile":     `"` + slash(h.install) + `/config/genesis.relaunch.json"`,
		"ValidatorKMSEnv": `"NHB_VALIDATOR_RAW_KEY"`,
		"NetworkName":     `"nhb-mainnet-validator"`,
		"Bootnodes":       `["peer.example.invalid:6001"]`,
		"PersistentPeers": `["peer.example.invalid:6001"]`,
		"ExternalAddress": `"203.0.113.9:7001"`,
	}
	for key, value := range want {
		if changed[key] != value {
			t.Fatalf("%s is %q, want %q\n%v", key, changed[key], value, changed)
		}
	}
	// The consensus-relevant values are the network's, untouched.
	if got, want := changed["ValidatorKeystorePath"], `""`; got != want {
		t.Fatalf("ValidatorKeystorePath is %q", got)
	}
}

func TestDeployRenderConfigFailsLoudlyOnAStaleTemplateAndRefusesDrift(t *testing.T) {
	h := newDeployHarness(t)
	// A template that lacks a line the script sets.
	stale := strings.Replace(string(mustRead(t, filepath.Join(h.install, "config.toml"))), "  PersistentPeers = []\n", "", 1)
	if err := os.WriteFile(filepath.Join(h.install, "config.toml"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := h.run(`render_config "${NHB_INSTALL_ROOT}/config.toml" "` + slash(filepath.Join(h.root, "x.toml")) + `"`)
	if code == 0 || !strings.Contains(out, "no [p2p] line for PersistentPeers") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// A template whose treasury drifted from the genesis is refused by the check.
	drifted := strings.Replace(string(mustRead(t, filepath.Join(repoRoot(t), "config.toml"))), "QuorumCertActivationHeight = 0", "QuorumCertActivationHeight = 451949", 1)
	if err := os.WriteFile(filepath.Join(h.install, "config.toml"), []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code = h.run(`BOOTNODE=peer.example.invalid:6001; install_config`)
	if code == 0 || !strings.Contains(out, "QuorumCertActivationHeight") || !strings.Contains(out, "does not carry the values consensus depends on") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(h.config, "config.toml")); err == nil {
		t.Fatalf("a config that failed the check was installed")
	}
}

// A hostile value must reach the config as data, never as code.
func TestDeployRenderConfigTreatsValuesAsData(t *testing.T) {
	h := newDeployHarness(t)
	out := filepath.Join(h.root, "hostile.toml")
	marker := filepath.Join(h.root, "pwned")
	res, code := h.run(`BOOTNODE='x"]; system("touch ` + slash(marker) + `"); ["y'
LISTEN_ADDR='$(touch ` + slash(marker) + `)'
render_config "${NHB_INSTALL_ROOT}/config.toml" "` + slash(out) + `"`)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("a value in the config ran as code")
	}
	if !strings.Contains(string(mustRead(t, out)), `touch `) {
		t.Fatalf("the hostile value was not written as data")
	}
}

func TestDeployBinaryIdentityPolicy(t *testing.T) {
	h := newDeployHarness(t)
	same := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	cases := []struct {
		name, args, env string
		wantCode        int
		want            string
	}{
		{"the same binary", same + " c1 " + same + " c2", "", 0, "byte for byte"},
		{"the same commit", other + " c1 " + same + " c1", "", 0, "the commit the snapshot was taken with"},
		{"no record of either", "'' unknown " + same + " c1", "", 1, "not the one the snapshot was taken with"},
		{"another binary and commit", same + " c1 " + other + " c2", "", 1, "git checkout c1"},
		{"the override", same + " c1 " + other + " c2", "ALLOW_BINARY_MISMATCH=1;", 0, "--allow-binary-mismatch"},
		{"an unknown commit is not a match", other + " unknown " + same + " unknown", "", 1, "not the one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := h.run(tc.env + "\ncheck_binary_identity " + tc.args)
			if code != tc.wantCode || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want %d, want %q:\n%s", code, tc.wantCode, tc.want, out)
			}
		})
	}
	// A manifest that names no commit gives the operator nothing to check out, and
	// must not send them to "git checkout unknown".
	t.Run("a snapshot that names no commit is not a commit to check out", func(t *testing.T) {
		for _, commit := range []string{"unknown", "''"} {
			out, code := h.run("check_binary_identity '' " + commit + " " + same + " c1")
			if code != 1 || strings.Contains(out, "git checkout") || !strings.Contains(out, "does not say which commit") {
				t.Fatalf("commit %s: exit %d:\n%s", commit, code, out)
			}
		}
	})
}

func TestDeployRefusesASecondNodeAndAForeignKey(t *testing.T) {
	h := newDeployHarness(t)

	t.Run("no node runs", func(t *testing.T) {
		if out, code := h.run(`refuse_second_node`, "FAKE_PS=  100 /usr/bin/bash scripts/deployvalidator.sh"); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("the unit's own process is fine", func(t *testing.T) {
		if out, code := h.run(`refuse_second_node`, "FAKE_MAINPID=4242", "FAKE_PS=4242 /opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml"); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a node outside the unit", func(t *testing.T) {
		out, code := h.run(`refuse_second_node`, "FAKE_MAINPID=4242", "FAKE_PS=4242 /opt/nhbchain/bin/nhb --config /etc/nhbchain/config.toml\n5151 ./bin/nhb --config /home/x/config.toml")
		if code == 0 || !strings.Contains(out, "another node process is running") || !strings.Contains(out, "5151") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a node while the unit is stopped", func(t *testing.T) {
		out, code := h.run(`refuse_second_node`, "FAKE_PS=777 /usr/local/bin/nhb --config /tmp/c.toml")
		if code == 0 {
			t.Fatalf("a running node was not noticed:\n%s", out)
		}
	})
	t.Run("a consensus daemon counts too", func(t *testing.T) {
		if out, code := h.run(`refuse_second_node`, "FAKE_PS=9 /usr/bin/consensusd --config x"); code == 0 {
			t.Fatalf("consensusd was not noticed:\n%s\ncalls:\n%s", out, h.calls())
		}
	})
	t.Run("other tools of the same family do not", func(t *testing.T) {
		if out, code := h.run(`refuse_second_node`, "FAKE_PS=9 /opt/nhbchain/bin/nhb-cli balance x\n10 /opt/nhbchain/bin/nhb-snapshot info"); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	// Keys.
	keyFile := filepath.Join(h.config, "validator.key")
	t.Run("no key yet", func(t *testing.T) {
		if out, code := h.run(`refuse_foreign_key`); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	if err := os.WriteFile(keyFile, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("a key that was put there by hand", func(t *testing.T) {
		out, code := h.run(`refuse_foreign_key`)
		if code == 0 || !strings.Contains(out, "did not create it") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("the explicit override", func(t *testing.T) {
		out, code := h.run(`ALLOW_EXISTING_KEY=1; refuse_foreign_key`)
		if code != 0 || !strings.Contains(out, "--allow-existing-key") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a key this script made", func(t *testing.T) {
		marker := filepath.Join(h.config, ".validator.key.created-here")
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(marker)
		if out, code := h.run(`refuse_foreign_key`); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a key an earlier run of this host used", func(t *testing.T) {
		data := filepath.Join(h.state, "nhb-data")
		if err := os.MkdirAll(data, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(data, "CURRENT"), []byte("MANIFEST-000001\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(data)
		if out, code := h.run(`refuse_foreign_key`); code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
}

// liveSnapshot is a snapshot of a chain that has the live genesis, served over
// HTTP the way a publisher would serve it.
type liveSnapshot struct {
	dir    string
	server *httptest.Server
	m      *manifest
}

func newLiveSnapshot(t *testing.T) *liveSnapshot {
	t.Helper()
	db := filepath.Join(t.TempDir(), "db")
	b := newChainBuilderAt(t, db, true, filepath.Join(repoRoot(t), "config", "genesis.relaunch.json"))
	for i := 0; i < 90; i++ {
		b.addBlock(10)
	}
	if b.bc.ChainID() == 0 || fmt.Sprintf("%d", b.bc.ChainID()) != pinnedChainID {
		t.Fatalf("the fixture chain is %d, not the live chain", b.bc.ChainID())
	}
	b.close()
	stage := stageChainFiles(t, db)
	out := filepath.Join(t.TempDir(), "published")
	m, err := packSnapshot(packOptions{DataDir: stage, OutDir: out, Latest: true,
		Producer:  producerInfo{BinaryVersion: "test", BinaryCommit: "1111111111111111111111111111111111111111", BinarySha256: strings.Repeat("cd", 32)},
		CreatedAt: time.Now().UTC().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(out)))
	t.Cleanup(srv.Close)
	return &liveSnapshot{dir: out, server: srv, m: m}
}

func TestDeployInstallsAVerifiedSnapshotAndRefusesTheRest(t *testing.T) {
	snap := newLiveSnapshot(t)
	h := newDeployHarness(t)
	prelude := `parse_args --beneficiary ` + goodBeneficiary + ` --snapshot-url ` + snap.server.URL + ` --bootnode 127.0.0.1:6001 --allow-insecure-http
validate_inputs
VALIDATOR_ADDRESS=` + crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x5a}, 20)).String() + `
`
	dataDir := filepath.Join(h.state, "nhb-data")

	t.Run("a manifest for another network is refused", func(t *testing.T) {
		other := newFixture(t) // a different chain: an ephemeral genesis
		srv := httptest.NewServer(http.FileServer(http.Dir(other.outDir)))
		defer srv.Close()
		out, code := h.run(strings.Replace(prelude, snap.server.URL, srv.URL, 1) + `fetch_manifest`)
		if code == 0 || !strings.Contains(out, "not the pinned network") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	t.Run("the pinned network installs", func(t *testing.T) {
		out, code := h.run(prelude + `fetch_manifest
prepare_data_dir
echo done`)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		id, err := openAndReadIdentity(dataDir, checkOptions{})
		if err != nil {
			t.Fatalf("the installed data directory does not open: %v", err)
		}
		if fmt.Sprintf("%d", id.ChainID) != pinnedChainID || hex0x(id.GenesisHash) != pinnedGenesisHash || id.Height != snap.m.Height {
			t.Fatalf("installed chain %d %x height %d", id.ChainID, id.GenesisHash, id.Height)
		}
		for _, name := range dbFiles(t, dataDir) {
			if !allowedFileName(name) && !housekeepingFiles[name] {
				t.Fatalf("the data directory holds %s", name)
			}
		}
		if strings.Contains(out, "snapshot did not verify") {
			t.Fatalf("unexpected output:\n%s", out)
		}
	})

	t.Run("running it again installs nothing", func(t *testing.T) {
		before := chainFiles(t, dataDir)
		out, code := h.run(prelude + `fetch_manifest
prepare_data_dir`)
		if code != 0 || !strings.Contains(out, "not installing a snapshot") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if strings.Join(before, ",") != strings.Join(chainFiles(t, dataDir), ",") {
			t.Fatalf("the data directory changed")
		}
		if strings.Contains(out, "downloading") {
			t.Fatalf("it downloaded again:\n%s", out)
		}
	})

	t.Run("a running node is left alone", func(t *testing.T) {
		out, code := h.run(prelude+`fetch_manifest
prepare_data_dir`, "FAKE_ACTIVE=0")
		if code != 0 || !strings.Contains(out, "nhb.service is running; not installing") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	t.Run("data of another chain is refused, not replaced", func(t *testing.T) {
		otherDir := filepath.Join(t.TempDir(), "other")
		buildChainDir(t, filepath.Join(otherDir, "db"), 10, 2)
		h2 := newDeployHarness(t)
		if err := os.MkdirAll(filepath.Join(h2.state, "nhb-data"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range dbFiles(t, filepath.Join(otherDir, "db")) {
			if allowedFileName(name) {
				copyTestFile(t, filepath.Join(otherDir, "db", name), filepath.Join(h2.state, "nhb-data", name))
			}
		}
		before := chainFiles(t, filepath.Join(h2.state, "nhb-data"))
		out, code := h2.run(prelude + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "holds another chain") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if strings.Join(before, ",") != strings.Join(chainFiles(t, filepath.Join(h2.state, "nhb-data")), ",") {
			t.Fatalf("the other chain's data was touched")
		}
	})

	t.Run("data that is not a chain database is refused", func(t *testing.T) {
		h2 := newDeployHarness(t)
		junk := filepath.Join(h2.state, "nhb-data")
		if err := os.MkdirAll(junk, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(junk, "notes.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := h2.run(prelude + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "does not open as a chain database") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	t.Run("a key that is a validator in the snapshot is refused", func(t *testing.T) {
		h2 := newDeployHarness(t)
		out, code := h2.run(strings.Replace(prelude, "VALIDATOR_ADDRESS="+crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x5a}, 20)).String(), "VALIDATOR_ADDRESS="+liveValidatorAddr, 1) + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "is a validator in this snapshot") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if entries, _ := os.ReadDir(filepath.Join(h2.state, "nhb-data")); len(entries) > 0 {
			t.Fatalf("a refused snapshot left %d files in the data directory", len(entries))
		}
	})

	t.Run("a tampered archive is refused and nothing is installed", func(t *testing.T) {
		h2 := newDeployHarness(t)
		tampered := t.TempDir()
		for _, name := range []string{"manifest.json", snap.m.Archive.Name} {
			data := mustRead(t, filepath.Join(snap.dir, name))
			if name == snap.m.Archive.Name {
				data[len(data)/2] ^= 0xff
			}
			if err := os.WriteFile(filepath.Join(tampered, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		srv := httptest.NewServer(http.FileServer(http.Dir(tampered)))
		defer srv.Close()
		out, code := h2.run(strings.Replace(prelude, snap.server.URL, srv.URL, 1) + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "did not verify") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if entries, _ := os.ReadDir(filepath.Join(h2.state, "nhb-data")); len(entries) > 0 {
			t.Fatalf("a refused snapshot left %d files in the data directory", len(entries))
		}
	})

	t.Run("a snapshot older than allowed is refused", func(t *testing.T) {
		h2 := newDeployHarness(t)
		out, code := h2.run(strings.Replace(prelude, "--allow-insecure-http", "--allow-insecure-http --max-snapshot-age 1m", 1) + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "older than") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	// The manifest says the snapshot was made an hour ago, well inside the limit,
	// and nobody signs that. The newest block it holds is years old.
	t.Run("a manifest that claims to be recent does not get a stale snapshot past the age limit", func(t *testing.T) {
		h2 := newDeployHarness(t)
		out, code := h2.run(strings.Replace(prelude, "--allow-insecure-http", "--allow-insecure-http --max-snapshot-age 48h", 1) + `fetch_manifest
prepare_data_dir`)
		if code == 0 || !strings.Contains(out, "newest block is dated") || !strings.Contains(out, "older than the allowed 48h") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if entries, _ := os.ReadDir(filepath.Join(h2.state, "nhb-data")); len(entries) > 0 {
			t.Fatalf("a refused snapshot left %d files in the data directory", len(entries))
		}
	})

	t.Run("a missing manifest is a loud failure", func(t *testing.T) {
		h2 := newDeployHarness(t)
		empty := httptest.NewServer(http.NotFoundHandler())
		defer empty.Close()
		out, code := h2.run(strings.Replace(prelude, snap.server.URL, empty.URL, 1) + `fetch_manifest`)
		if code == 0 || !strings.Contains(out, "could not download") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})

	t.Run("--reset-state moves the old data aside and keeps the node's identity", func(t *testing.T) {
		h2 := newDeployHarness(t)
		old := filepath.Join(h2.state, "nhb-data")
		buildChainDir(t, filepath.Join(t.TempDir(), "unused"), 3, 1)
		// an old data directory with a chain database and identity files
		olddb := filepath.Join(t.TempDir(), "olddb")
		buildChainDir(t, olddb, 5, 1)
		if err := os.MkdirAll(filepath.Join(old, "p2p"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range dbFiles(t, olddb) {
			if allowedFileName(name) {
				copyTestFile(t, filepath.Join(olddb, name), filepath.Join(old, name))
			}
		}
		for name, content := range map[string]string{"p2p/node_key.json": `{"privateKey":"aa"}`, "bft_sign_state.json": `{"votes":1}`, "polc_lock.json": `{"h":1}`} {
			if err := os.WriteFile(filepath.Join(old, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, code := h2.run(strings.Replace(prelude, "--allow-insecure-http", "--allow-insecure-http --reset-state", 1) + `fetch_manifest
prepare_data_dir`)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		id, err := openAndReadIdentity(old, checkOptions{})
		if err != nil || fmt.Sprintf("%d", id.ChainID) != pinnedChainID {
			t.Fatalf("the new data directory: %v", err)
		}
		for name, want := range map[string]string{"p2p/node_key.json": `{"privateKey":"aa"}`, "bft_sign_state.json": `{"votes":1}`, "polc_lock.json": `{"h":1}`} {
			if got := string(mustRead(t, filepath.Join(old, filepath.FromSlash(name)))); got != want {
				t.Fatalf("%s is %q after the reset, want %q", name, got, want)
			}
		}
		entries, _ := os.ReadDir(h2.state)
		aside := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "nhb-data.replaced-") {
				aside++
				if _, err := os.Stat(filepath.Join(h2.state, e.Name(), "CURRENT")); err != nil {
					t.Fatalf("the moved-aside directory lost its chain: %v", err)
				}
			}
		}
		if aside != 1 {
			t.Fatalf("expected the old data directory to be moved aside, found %d", aside)
		}
		for _, call := range strings.Split(h2.calls(), "\n") {
			if strings.Contains(call, " rm ") && strings.Contains(call, "nhb-data") {
				t.Fatalf("--reset-state deleted the old data: %s", call)
			}
		}
	})
}

func TestDeployWaitUntilSynced(t *testing.T) {
	h := newDeployHarness(t)
	now := func() int64 { return time.Now().Unix() }
	run := func(node *fakeNode, extra ...string) (string, int) {
		srv := httptest.NewServer(node.handler())
		defer srv.Close()
		addr := strings.TrimPrefix(srv.URL, "http://")
		return h.run(`RPC_ADDR=`+addr+`; SYNC_INTERVAL=100ms
`+strings.Join(extra, "\n")+`
wait_until_synced
echo reached`, "NHB_SYNC_INTERVAL=100ms")
	}

	t.Run("a node at the tip", func(t *testing.T) {
		node := &fakeNode{height: 100, max: 100, chainID: 18346390202490284624, genesis: pinnedGenesisHash[2:], timestamp: now}
		out, code := run(node)
		if code != 0 || !strings.Contains(out, "reached") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a node on another chain", func(t *testing.T) {
		node := &fakeNode{height: 100, max: 100, chainID: 7, genesis: "aa", timestamp: now}
		out, code := run(node)
		if code == 0 || !strings.Contains(out, "not on the pinned network") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if strings.Contains(out, "reached") {
			t.Fatalf("success was reported for the wrong chain")
		}
	})
	t.Run("a node that never gets there", func(t *testing.T) {
		node := &fakeNode{height: 5, step: 1, max: 1 << 40, chainID: 18346390202490284624, genesis: pinnedGenesisHash[2:], timestamp: func() int64 { return now() - 7200 }}
		out, code := run(node, `SYNC_TIMEOUT_SECS=1`)
		if code == 0 || !strings.Contains(out, "did not reach the network tip within 1 seconds") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if !strings.Contains(out, "journalctl") || strings.Contains(out, "reached") {
			t.Fatalf("no diagnostics, or success reported:\n%s", out)
		}
	})

	// A service that crash-loops never answers. The script must say so after the
	// RPC's own bound, with the same diagnostics as every other failure, and not
	// hold the operator for the whole sync timeout.
	t.Run("a node whose RPC never comes up", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close() // nothing listens any more
		started := time.Now()
		out, code := h.run(`RPC_ADDR=`+addr+`; SYNC_INTERVAL=100ms; SYNC_TIMEOUT_SECS=40; RPC_UP_TIMEOUT_SECS=1
wait_until_synced
echo reached`, "NHB_SYNC_INTERVAL=100ms")
		if code == 0 || !strings.Contains(out, "the node's RPC did not come up within 1 seconds") || !strings.Contains(out, "crash-looping") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if !strings.Contains(out, "systemctl status nhb.service") || !strings.Contains(out, "journalctl -u nhb.service") || strings.Contains(out, "reached") {
			t.Fatalf("no diagnostics, or success reported:\n%s", out)
		}
		if strings.Contains(out, "--reset-state") {
			t.Fatalf("a node that never answered was told its snapshot is too old:\n%s", out)
		}
		if took := time.Since(started); took > 30*time.Second {
			t.Fatalf("the script waited %s although the RPC was given 1 s and the sync timeout is 40 s", took)
		}
	})
	t.Run("the default bound on the RPC is far below the sync timeout", func(t *testing.T) {
		out, code := h.run(`echo "rpc=${RPC_UP_TIMEOUT_SECS} sync=${SYNC_TIMEOUT_SECS}"`)
		if code != 0 || !strings.Contains(out, "rpc=180 sync=7200") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
}

func TestDeployScriptMentionsTheDocumentedOperatorFlow(t *testing.T) {
	raw := mustRead(t, filepath.Join(repoRoot(t), "scripts", "deployvalidator.sh"))
	var have = map[string]bool{}
	for _, f := range []string{"--snapshot-url", "--bootnode", "--tip-rpc", "--reset-state", "--allow-binary-mismatch", "--allow-existing-key", "--max-snapshot-age", "--allow-insecure-http", "--tip-hash", "--state-root", "--max-snapshot-gib", "--rpc-timeout"} {
		have[f] = strings.Contains(string(raw), f)
	}
	for f, ok := range have {
		if !ok {
			t.Errorf("the script does not document %s", f)
		}
	}
	// The registration steps run after the node is at the tip, not before.
	text := string(raw)
	if strings.Index(text, "wait_until_synced\n\n  if ! RPC_TOKEN") < 0 {
		t.Errorf("the registration steps no longer follow the wait for the network tip")
	}
}
