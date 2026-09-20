package main

// Tests of what scripts/deployvalidator.sh does when it is run again, and of
// --reset-state. main() runs whole, against a real nhb-snapshot and a snapshot
// host on the loopback interface, with only the steps that need a real server
// (packages, the build, the service, the node's RPC) stubbed.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

const rerunCommit = "1111111111111111111111111111111111111111"

// livePublisher makes and serves snapshots of one chain that has the live
// genesis, the way an operator's snapshot host does.
type livePublisher struct {
	t      *testing.T
	stage  string
	out    string
	server *httptest.Server
	m      *manifest
}

func newLivePublisher(t *testing.T) *livePublisher {
	t.Helper()
	db := filepath.Join(t.TempDir(), "db")
	b := newChainBuilderAt(t, db, true, filepath.Join(repoRoot(t), "config", "genesis.relaunch.json"))
	for i := 0; i < 30; i++ {
		b.addBlock(5)
	}
	b.close()
	p := &livePublisher{t: t, stage: stageChainFiles(t, db), out: filepath.Join(t.TempDir(), "published")}
	if err := os.MkdirAll(p.out, 0o755); err != nil {
		t.Fatal(err)
	}
	p.server = httptest.NewServer(http.FileServer(http.Dir(p.out)))
	t.Cleanup(p.server.Close)
	return p
}

// publish packs a snapshot made by the given binary and commit and makes it the
// one manifest.json names.
func (p *livePublisher) publish(binarySha, commit string) *manifest {
	p.t.Helper()
	m, err := packSnapshot(packOptions{DataDir: p.stage, OutDir: p.out, Latest: true,
		Producer:  producerInfo{BinaryVersion: "test", BinaryCommit: commit, BinarySha256: binarySha},
		CreatedAt: time.Now().UTC().Add(-time.Hour)})
	if err != nil {
		p.t.Fatal(err)
	}
	p.m = m
	return m
}

// writeFake puts an executable script in front of the real command of that name.
func (h *deployHarness) writeFake(name, body string) {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
		h.t.Fatal(err)
	}
}

const fakeCLIScript = `#!/usr/bin/env bash
case "$1" in
  generate-key) echo k > wallet.key ;;
  address) echo "${FAKE_ADDRESS:-%s}" ;;
esac
`

// stubNodeBinaries puts in the install root the two binaries main() looks for
// after the build, and returns the sha256 of the node binary.
func (h *deployHarness) stubNodeBinaries() string {
	h.t.Helper()
	body := []byte("fake-nhb-binary")
	if err := os.WriteFile(filepath.Join(h.install, "bin", "nhb"), body, 0o755); err != nil {
		h.t.Fatal(err)
	}
	fresh := crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x5a}, 20)).String()
	cli := strings.Replace(fakeCLIScript, "%s", fresh, 1)
	if err := os.WriteFile(filepath.Join(h.install, "bin", "nhb-cli"), []byte(cli), 0o755); err != nil {
		h.t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// mainSnippet runs the script's main() with the steps that need a real server
// stubbed. wait_until_synced reports the snapshot height it was given, and, like
// the real one when the node has been seen at the tip, forgets the record of it.
func mainSnippet(publisherURL string, extra ...string) string {
	return `
install_prerequisites(){ :; }
install_tree_and_build(){ :; }
install_service(){ :; }
wait_until_synced(){ echo "wait_until_synced: snapshot height [${SNAPSHOT_HEIGHT:-}]"; forget_snapshot_height; }
mint_rpc_token(){ echo tok; }
submit_validator_steps(){ :; }
main --beneficiary ` + goodBeneficiary + ` --snapshot-url ` + publisherURL + ` --bootnode 127.0.0.1:6001 --allow-insecure-http --external-address 1.2.3.4 ` + strings.Join(extra, " ") + "\n"
}

// ownDataDir puts in the host's data directory what a node that ran there
// leaves: a chain database and the files that make it the node it is.
func (h *deployHarness) ownDataDir() (dir string, identity map[string]string) {
	h.t.Helper()
	dir = filepath.Join(h.state, "nhb-data")
	olddb := filepath.Join(h.t.TempDir(), "olddb")
	buildChainDir(h.t, olddb, 5, 1)
	if err := os.MkdirAll(filepath.Join(dir, "p2p"), 0o755); err != nil {
		h.t.Fatal(err)
	}
	for _, name := range dbFiles(h.t, olddb) {
		if allowedFileName(name) {
			copyTestFile(h.t, filepath.Join(olddb, name), filepath.Join(dir, name))
		}
	}
	identity = map[string]string{"p2p/node_key.json": `{"privateKey":"aa"}`, "bft_sign_state.json": `{"votes":[{"height":40}]}`, "polc_lock.json": `{"height":40}`}
	for name, content := range identity {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	return dir, identity
}

// leftovers lists the directories next to the data directory that a
// replacement makes: the old one moved aside, and the unpacked new one.
func leftovers(t *testing.T, state, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// lastLines is the end of a long output: the part that says how a run ended.
func lastLines(s string) string {
	if len(s) > 3000 {
		return s[len(s)-3000:]
	}
	return s
}

func mustContainInOrder(t *testing.T, log string, parts ...string) {
	t.Helper()
	pos := 0
	for _, part := range parts {
		i := strings.Index(log[pos:], part)
		if i < 0 {
			t.Fatalf("expected %q after position %d of the calls:\n%s", part, pos, log)
		}
		pos += i + len(part)
	}
}

// A node that already holds this network's chain is never given a snapshot, so
// it must not depend on the snapshot host: not on what the host publishes now (a
// release or the daily refresh replaces manifest.json with a snapshot of another
// binary), and not on its answering at all.
func TestDeployRerunDoesNotDependOnTheSnapshotHost(t *testing.T) {
	h := newDeployHarness(t)
	sha := h.stubNodeBinaries()
	p := newLivePublisher(t)
	p.publish(sha, rerunCommit)
	dataDir := filepath.Join(h.state, "nhb-data")

	out, code := h.run(mainSnippet(p.server.URL))
	if code != 0 {
		t.Fatalf("the first run exited %d:\n%s", code, lastLines(out))
	}
	first := chainFiles(t, dataDir)
	if len(first) == 0 {
		t.Fatalf("the first run installed no data")
	}

	rerun := func(t *testing.T, env ...string) {
		t.Helper()
		_ = os.Remove(h.log)
		out, code := h.run(mainSnippet(p.server.URL), env...)
		if code != 0 {
			t.Fatalf("running the script again exited %d:\n%s", code, lastLines(out))
		}
		if calls := h.calls(); strings.Contains(calls, "curl") || strings.Contains(calls, "manifest show") {
			t.Fatalf("the run went to the snapshot host or read a manifest although it installs none:\n%s", calls)
		}
		if strings.Join(first, ",") != strings.Join(chainFiles(t, dataDir), ",") {
			t.Fatalf("the data directory changed")
		}
		if found := append(leftovers(t, h.state, "nhb-data."), leftovers(t, h.state, ".nhb-snapshot-extract")...); len(found) > 0 {
			t.Fatalf("the run left %v beside the data directory", found)
		}
		if !strings.Contains(out, "no snapshot is needed") || !strings.Contains(out, "snapshot height []") {
			t.Fatalf("the run behaved as if it installed a snapshot:\n%s", lastLines(out))
		}
	}

	t.Run("the publisher replaced the snapshot with one made by another binary", func(t *testing.T) {
		p.publish(strings.Repeat("ab", 32), "2222222222222222222222222222222222222222")
		rerun(t, "FAKE_ACTIVE=0")
	})
	t.Run("the node is stopped", func(t *testing.T) {
		rerun(t)
	})
	t.Run("the publisher removed the manifest", func(t *testing.T) {
		if err := os.Remove(filepath.Join(p.out, "manifest.json")); err != nil {
			t.Fatal(err)
		}
		rerun(t, "FAKE_ACTIVE=0")
	})
	t.Run("the snapshot host does not answer", func(t *testing.T) {
		p.server.Close()
		rerun(t, "FAKE_ACTIVE=0")
	})
}

// The run that installed the snapshot tells the sync check its height; a run
// that installed none has no height to pass.
func TestDeployHandsTheSnapshotHeightToTheSyncCheck(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	p.publish(h.stubNodeBinaries(), rerunCommit)
	out, code := h.run(mainSnippet(p.server.URL))
	if code != 0 || !strings.Contains(out, "snapshot height ["+strconv.FormatUint(p.m.Height, 10)+"]") {
		t.Fatalf("the run that installed the snapshot did not hand its height to the sync check (exit %d):\n%s", code, lastLines(out))
	}
	out, code = h.run(mainSnippet(p.server.URL), "FAKE_ACTIVE=0")
	if code != 0 || !strings.Contains(out, "snapshot height []") {
		t.Fatalf("a run that installed no snapshot handed a height to the sync check (exit %d):\n%s", code, lastLines(out))
	}
}

// --reset-state on a node that is a validator is the case the flag is for: its
// data directory is broken and its own key is, by then, a validator in every
// snapshot. The snapshot is unpacked next to the old directory first; the node
// is stopped and the directories swapped only after that, and what it voted
// while it stopped is what the new directory carries.
func TestDeployResetStateOfARegisteredValidator(t *testing.T) {
	h := newDeployHarness(t)
	sha := h.stubNodeBinaries()
	p := newLivePublisher(t)
	p.publish(sha, rerunCommit)
	dataDir, identity := h.ownDataDir()
	final := `{"votes":[{"height":41}]}`
	h.writeFake("systemctl", `#!/usr/bin/env bash
echo "systemctl $*" >> "$FAKE_LOG"
case "$1" in
  show) echo "${FAKE_MAINPID:-0}" ;;
  is-active) exit "${FAKE_ACTIVE:-3}" ;;
  stop) printf '%s' "${FAKE_STOP_CONTENT}" > "${FAKE_STOP_WRITE}" ;;
esac
exit 0
`)

	out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=0", "FAKE_ADDRESS="+liveValidatorAddr,
		"FAKE_STOP_WRITE="+slash(filepath.Join(dataDir, "bft_sign_state.json")), "FAKE_STOP_CONTENT="+final)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	id, err := openAndReadIdentity(dataDir, checkOptions{})
	if err != nil {
		t.Fatalf("the new data directory does not open: %v", err)
	}
	if id.Height != p.m.Height || hex0x(id.TipHash) != p.m.TipHash {
		t.Fatalf("the data directory holds height %d, not the snapshot's %d", id.Height, p.m.Height)
	}
	isValidator := false
	for _, v := range id.Validators {
		isValidator = isValidator || v == liveValidatorAddr
	}
	if !isValidator {
		t.Fatalf("the snapshot does not list this node's key as a validator (%v): the test does not test what it says", id.Validators)
	}
	for name, want := range map[string]string{"p2p/node_key.json": identity["p2p/node_key.json"], "bft_sign_state.json": final, "polc_lock.json": identity["polc_lock.json"]} {
		if got := string(mustRead(t, filepath.Join(dataDir, filepath.FromSlash(name)))); got != want {
			t.Fatalf("%s is %q after the reset, want %q", name, got, want)
		}
	}
	aside := leftovers(t, h.state, "nhb-data.replaced-")
	if len(aside) != 1 {
		t.Fatalf("expected the old data directory to be moved aside once, found %v", aside)
	}
	if _, err := os.Stat(filepath.Join(h.state, aside[0], "CURRENT")); err != nil {
		t.Fatalf("the moved-aside directory lost its chain: %v", err)
	}
	if extra := leftovers(t, h.state, "nhb-data.new-"); len(extra) > 0 {
		t.Fatalf("the unpacked snapshot was left behind: %v", extra)
	}
	// The old data is not touched until the new copy is complete, and what the
	// node voted is read only after the node has stopped.
	state := slash(h.state)
	mustContainInOrder(t, h.calls(), " extract --manifest", "systemctl stop nhb.service", "cp -p", "sudo mv "+state+"/nhb-data "+state+"/nhb-data.replaced-", "sudo mv "+state+"/nhb-data.new-")
	for _, call := range strings.Split(h.calls(), "\n") {
		if strings.Contains(call, " rm ") && strings.Contains(call, "nhb-data") {
			t.Fatalf("--reset-state deleted data: %s", call)
		}
	}
	if !strings.Contains(out, "the old data directory is at") {
		t.Fatalf("the output does not say where the old data directory is:\n%s", lastLines(out))
	}
}

// A node whose data directory is broken is often not "active" but restarting
// every few seconds. It is stopped all the same: a restart in the middle of the
// swap would start it on whatever the data directory holds at that moment.
func TestDeployResetStateStopsANodeThatIsRestartingRatherThanActive(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	p.publish(h.stubNodeBinaries(), rerunCommit)
	dataDir, _ := h.ownDataDir()
	// is-active answers 3 for a unit that is inactive, failed or activating.
	out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=3")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	state := slash(h.state)
	mustContainInOrder(t, h.calls(), " extract --manifest", "systemctl stop nhb.service", "sudo mv "+state+"/nhb-data "+state+"/nhb-data.replaced-", "sudo mv "+state+"/nhb-data.new-")
	if _, err := openAndReadIdentity(dataDir, checkOptions{}); err != nil {
		t.Fatalf("the new data directory does not open: %v", err)
	}
}

// A snapshot that does not check out must change nothing: the node keeps
// running, its data directory stays where it is, and the message may not say
// otherwise.
func TestDeployResetStateWithASnapshotThatDoesNotCheckOutChangesNothing(t *testing.T) {
	cases := map[string]func(t *testing.T, p *livePublisher) (url string, want string){
		"an archive that was altered": func(t *testing.T, p *livePublisher) (string, string) {
			dir := t.TempDir()
			for _, name := range []string{"manifest.json", p.m.Archive.Name} {
				data := mustRead(t, filepath.Join(p.out, name))
				if name == p.m.Archive.Name {
					data[len(data)/2] ^= 0xff
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
			t.Cleanup(srv.Close)
			return srv.URL, "did not verify"
		},
		"a database that does not open": func(t *testing.T, p *livePublisher) (string, string) {
			// The archive is genuine and the manifest describes it, but CURRENT names
			// a MANIFEST that is not there: only opening what was unpacked finds out.
			f := &fixture{t: t, stage: p.stage, m: p.m}
			var entries []rawEntry
			for _, e := range f.stagedEntries() {
				if e.hdr.Name == "CURRENT" {
					e = regEntry("CURRENT", []byte("MANIFEST-999999\n"))
				}
				entries = append(entries, e)
			}
			_, archivePath := f.hostile("unopenable", tarGz(t, entries, nil), func(m *manifest) {
				sum := sha256.Sum256([]byte("MANIFEST-999999\n"))
				for i := range m.Archive.Files {
					if m.Archive.Files[i].Name == "CURRENT" {
						m.Archive.UncompressedSize += int64(len("MANIFEST-999999\n")) - m.Archive.Files[i].Size
						m.Archive.Files[i].Size = int64(len("MANIFEST-999999\n"))
						m.Archive.Files[i].Sha256 = hex.EncodeToString(sum[:])
					}
				}
			})
			// hostile writes the archive and a manifest.json that describes it side by side.
			srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(archivePath))))
			t.Cleanup(srv.Close)
			return srv.URL, "could not be installed"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newDeployHarness(t)
			p := newLivePublisher(t)
			p.publish(h.stubNodeBinaries(), rerunCommit)
			url, want := mutate(t, p)
			dataDir, identity := h.ownDataDir()
			before := chainFiles(t, dataDir)

			out, code := h.run(mainSnippet(url, "--reset-state"), "FAKE_ACTIVE=0", "FAKE_ADDRESS="+liveValidatorAddr)
			if code == 0 || !strings.Contains(out, want) {
				t.Fatalf("exit %d, want a refusal saying %q:\n%s", code, want, lastLines(out))
			}
			if _, err := os.Stat(dataDir); err != nil {
				t.Fatalf("the data directory is gone: %v", err)
			}
			if strings.Join(before, ",") != strings.Join(chainFiles(t, dataDir), ",") {
				t.Fatalf("the data directory changed")
			}
			for name, content := range identity {
				if got := string(mustRead(t, filepath.Join(dataDir, filepath.FromSlash(name)))); got != content {
					t.Fatalf("%s changed", name)
				}
			}
			if found := leftovers(t, h.state, "nhb-data."); len(found) > 0 {
				t.Fatalf("the refused run left %v beside the data directory", found)
			}
			if calls := h.calls(); strings.Contains(calls, "systemctl stop") || strings.Contains(calls, "sudo mv ") {
				t.Fatalf("the run stopped the node or moved a directory although the snapshot did not check out:\n%s", calls)
			}
			if !strings.Contains(out, "nothing was") {
				t.Fatalf("the message does not say that nothing was changed:\n%s", out)
			}
		})
	}
}

// When the swap itself fails, the message says where everything is, and the old
// data directory is back where it was.
func TestDeployResetStateSwapFailureIsReportedTruthfully(t *testing.T) {
	// A move that fails when its source matches FAKE_MV_FAIL: what the swap does
	// is two renames, which are all that can go wrong once the snapshot is ready.
	fakeMv := `#!/usr/bin/env bash
if [ -n "${FAKE_MV_FAIL:-}" ]; then
  for pattern in ${FAKE_MV_FAIL}; do
    case "$1" in *"${pattern}"*) echo "mv: simulated failure moving $1" >&2; exit 1 ;; esac
  done
fi
real=""
for candidate in $(type -ap mv); do
  if ! [ "$candidate" -ef "$0" ]; then real="$candidate"; break; fi
done
exec "$real" "$@"
`
	setup := func(t *testing.T) (h *deployHarness, p *livePublisher, dataDir string) {
		h = newDeployHarness(t)
		sha := h.stubNodeBinaries()
		p = newLivePublisher(t)
		p.publish(sha, rerunCommit)
		dataDir, _ = h.ownDataDir()
		h.writeFake("mv", fakeMv)
		return h, p, dataDir
	}

	t.Run("the new directory cannot be moved into place: the old one is put back", func(t *testing.T) {
		h, p, dataDir := setup(t)
		before := chainFiles(t, dataDir)
		out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=0", "FAKE_MV_FAIL=nhb-data.new-")
		if code == 0 || !strings.Contains(out, "the old one was put back") {
			t.Fatalf("exit %d:\n%s", code, lastLines(out))
		}
		if strings.Join(before, ",") != strings.Join(chainFiles(t, dataDir), ",") {
			t.Fatalf("the old data directory is not back in place")
		}
		if len(leftovers(t, h.state, "nhb-data.new-")) != 1 || !strings.Contains(out, "the unpacked snapshot is at") {
			t.Fatalf("the message does not say where the unpacked snapshot is:\n%s", lastLines(out))
		}
		if !strings.Contains(out, "nhb.service is stopped") || !strings.Contains(out, "sudo systemctl start nhb.service") {
			t.Fatalf("the message does not say that the node was stopped and how to start it:\n%s", lastLines(out))
		}
		if strings.Contains(out, "left as it was") {
			t.Fatalf("the message still claims the data directory was left alone:\n%s", lastLines(out))
		}
	})

	t.Run("neither can the old one be put back: the message says where both are", func(t *testing.T) {
		h, p, dataDir := setup(t)
		out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=0", "FAKE_MV_FAIL=nhb-data.new- nhb-data.replaced-")
		if code == 0 || !strings.Contains(out, "could not put the old one back") {
			t.Fatalf("exit %d:\n%s", code, lastLines(out))
		}
		aside := leftovers(t, h.state, "nhb-data.replaced-")
		staged := leftovers(t, h.state, "nhb-data.new-")
		if len(aside) != 1 || len(staged) != 1 {
			t.Fatalf("expected one directory aside and one unpacked, found %v and %v", aside, staged)
		}
		if _, err := os.Stat(dataDir); err == nil {
			t.Fatalf("a data directory is in place although the swap failed halfway")
		}
		if !strings.Contains(out, aside[0]) || !strings.Contains(out, staged[0]) || !strings.Contains(out, "sudo mv ") {
			t.Fatalf("the message does not say where the directories are and how to put the old one back:\n%s", lastLines(out))
		}
	})

	t.Run("the old directory cannot be moved aside: nothing moved", func(t *testing.T) {
		h, p, dataDir := setup(t)
		before := chainFiles(t, dataDir)
		out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=0", "FAKE_MV_FAIL=nhb-data")
		if code == 0 || !strings.Contains(out, "could not move") {
			t.Fatalf("exit %d:\n%s", code, lastLines(out))
		}
		if strings.Join(before, ",") != strings.Join(chainFiles(t, dataDir), ",") {
			t.Fatalf("the data directory changed")
		}
		if len(leftovers(t, h.state, "nhb-data.replaced-")) != 0 {
			t.Fatalf("a directory was moved aside although the move failed")
		}
	})
}

// Without data to replace, --reset-state installs a snapshot like any first run,
// and a first run refuses a key that is already a validator: that check is only
// waived for a node's own data directory.
func TestDeployResetStateWithNoDataStillRefusesAKeyThatIsAlreadyAValidator(t *testing.T) {
	h := newDeployHarness(t)
	sha := h.stubNodeBinaries()
	p := newLivePublisher(t)
	p.publish(sha, rerunCommit)
	out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ADDRESS="+liveValidatorAddr)
	if code == 0 || !strings.Contains(out, "is a validator in this snapshot") {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	if entries, _ := os.ReadDir(filepath.Join(h.state, "nhb-data")); len(entries) > 0 {
		t.Fatalf("a refused snapshot left %d files in the data directory", len(entries))
	}
}

// A run that installs a snapshot still checks, before anything else on the host
// changes, that the node built here is the one that made it, and --reset-state
// does not stop the node or move its data for a snapshot it will refuse.
func TestDeployResetStateChecksTheBinaryBeforeItTouchesTheNode(t *testing.T) {
	h := newDeployHarness(t)
	h.stubNodeBinaries()
	p := newLivePublisher(t)
	p.publish(strings.Repeat("ab", 32), "2222222222222222222222222222222222222222")
	dataDir, _ := h.ownDataDir()
	before := chainFiles(t, dataDir)
	out, code := h.run(mainSnippet(p.server.URL, "--reset-state"), "FAKE_ACTIVE=0")
	if code == 0 || !strings.Contains(out, "not the one the snapshot was taken with") {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	if strings.Join(before, ",") != strings.Join(chainFiles(t, dataDir), ",") {
		t.Fatalf("the data directory changed")
	}
	if calls := h.calls(); strings.Contains(calls, "systemctl stop") || strings.Contains(calls, "sudo mv ") {
		t.Fatalf("the run stopped the node or moved a directory:\n%s", calls)
	}
}

// The data directory and the key file belong to the service user and are closed
// to whoever runs the script. Whether a node's data or key is there is asked as
// root: a directory full of chain data read as an empty one would send a running
// node a snapshot, and a key that is not seen would be made again over the old one.
func TestDeployLooksForTheDataAndTheKeyAsRoot(t *testing.T) {
	h := newDeployHarness(t)
	dataDir, _ := h.ownDataDir()
	keyFile := filepath.Join(h.config, "validator.key")
	if err := os.WriteFile(keyFile, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	// Listing the service user's directory fails for anyone who is not root.
	h.writeFake("sudo", `#!/usr/bin/env bash
echo "sudo $*" >> "$FAKE_LOG"
if [ "${1:-}" = "-u" ]; then shift 2; exec "$@"; fi
FAKE_AS_ROOT=1 exec "$@"
`)
	h.writeFake("ls", `#!/usr/bin/env bash
if [ -z "${FAKE_AS_ROOT:-}" ]; then
  for arg in "$@"; do
    case "$arg" in *nhb-data) echo "ls: cannot open directory '$arg': Permission denied" >&2; exit 2 ;; esac
  done
fi
real=""
for candidate in $(type -ap ls); do
  if ! [ "$candidate" -ef "$0" ]; then real="$candidate"; break; fi
done
exec "$real" "$@"
`)
	if out, code := h.run(`data_dir_has_data && echo HAS-DATA`); code != 0 || !strings.Contains(out, "HAS-DATA") {
		t.Fatalf("a data directory full of chain data was read as an empty one (exit %d):\n%s", code, out)
	}
	if out, code := h.run(`snapshot_needed && echo NEEDED || echo NOT-NEEDED`); code != 0 || !strings.Contains(out, "NOT-NEEDED") {
		t.Fatalf("a node with data was to be given a snapshot (exit %d):\n%s", code, out)
	}
	// With the data gone, a snapshot is needed.
	if err := os.RemoveAll(dataDir); err != nil {
		t.Fatal(err)
	}
	if out, code := h.run(`snapshot_needed && echo NEEDED || echo NOT-NEEDED`); code != 0 || !strings.Contains(out, "NEEDED") || strings.Contains(out, "NOT-NEEDED") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	_ = os.Remove(h.log)
	if out, code := h.run(`refuse_foreign_key`); code == 0 || !strings.Contains(out, "did not create it") {
		t.Fatalf("a key this script did not make, on a host with no data, was accepted (exit %d):\n%s", code, out)
	}
	if calls := h.calls(); !strings.Contains(calls, "sudo test -f "+slash(keyFile)) {
		t.Fatalf("the key file was looked for as the user who runs the script, not as root:\n%s", calls)
	}
}
