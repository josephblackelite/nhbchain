package main

// Tests of the bounds scripts/deployvalidator.sh puts on what a snapshot host
// can make it do (how much it downloads and unpacks), of the tip and state-root
// pins, and of the sync check it hands the snapshot's height to. Like
// deploy_rerun_test.go they run main() whole with the system steps stubbed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// servedManifest serves a manifest.json made from the published one by edit
// (which changes the parsed JSON), and nothing else: an archive is never there
// to be downloaded, so a run that asks for it fails to download.
func servedManifest(t *testing.T, p *livePublisher, edit func(archive map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(mustRead(t, filepath.Join(p.out, "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	edit(m["archive"].(map[string]any))
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL
}

// downloaded reports whether the run went to the host for the archive.
func downloaded(calls string, p *livePublisher) bool {
	for _, call := range strings.Split(calls, "\n") {
		if strings.Contains(call, " curl ") && strings.Contains(call, p.m.Archive.Name) {
			return true
		}
	}
	return false
}

// The sizes in a manifest are nobody's signed word. The script bounds the
// download and the unpack itself, before it downloads anything.
func TestDeployRefusesASnapshotBeyondWhatTheOperatorAccepts(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name  string
		extra []string
		edit  func(archive map[string]any)
		want  string
	}{
		{"an archive larger than accepted", nil, func(a map[string]any) { a["size"] = 20 * gib }, "more than the 16 GiB this run accepts"},
		{"a database that unpacks to more than accepted", nil, func(a map[string]any) {
			files := a["files"].([]any)
			var others int64
			for _, f := range files[1:] {
				others += int64(f.(map[string]any)["size"].(float64))
			}
			files[0].(map[string]any)["size"] = 17*gib - others
			a["uncompressedSize"] = 17 * gib
			a["size"] = gib
		}, "unpacks to"},
		{"the operator's own bound", []string{"--max-snapshot-gib", "1"}, func(a map[string]any) { a["size"] = 2 * gib }, "more than the 1 GiB this run accepts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newDeployHarness(t)
			p := newLivePublisher(t)
			p.publish(h.stubNodeBinaries(), rerunCommit)
			url := servedManifest(t, p, tc.edit)
			out, code := h.run(mainSnippet(url, tc.extra...))
			if code == 0 || !strings.Contains(out, tc.want) || !strings.Contains(out, "nothing was downloaded") {
				t.Fatalf("exit %d, want a refusal saying %q:\n%s", code, tc.want, lastLines(out))
			}
			if downloaded(h.calls(), p) {
				t.Fatalf("the run downloaded the archive of a snapshot it refuses:\n%s", h.calls())
			}
			if entries, _ := os.ReadDir(filepath.Join(h.state, "nhb-data")); len(entries) > 0 {
				t.Fatalf("a refused snapshot left %d files in the data directory", len(entries))
			}
		})
	}
}

// A disk without room for the archive and what it unpacks to is found out before
// the download, not when it is full.
func TestDeployChecksThereIsRoomForTheSnapshotBeforeItDownloadsIt(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	p.publish(h.stubNodeBinaries(), rerunCommit)

	out, code := h.run("free_kib(){ echo 1000; }\n" + mainSnippet(p.server.URL))
	if code == 0 || !strings.Contains(out, "not enough free disk space") || !strings.Contains(out, "nothing was downloaded") {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	if downloaded(h.calls(), p) {
		t.Fatalf("the run downloaded the archive although the disk has no room for it:\n%s", h.calls())
	}

	// The archive plus what it unpacks to plus a GiB for the node is what has to be free.
	need := (p.m.Archive.Size+p.m.Archive.UncompressedSize)/1024 + 1<<20
	if out, code := h.run(fmt.Sprintf("free_kib(){ echo %d; }\n", need-1) + mainSnippet(p.server.URL)); code == 0 || !strings.Contains(out, "not enough free disk space") {
		t.Fatalf("one KiB short of what is needed was accepted (exit %d):\n%s", code, lastLines(out))
	}
	if out, code := h.run(fmt.Sprintf("free_kib(){ echo %d; }\n", need) + mainSnippet(p.server.URL)); code != 0 {
		t.Fatalf("exactly what is needed was refused (exit %d):\n%s", code, lastLines(out))
	}
}

// What verify and extract may read and write is bounded by the operator, in
// every call that reads the archive.
func TestDeployBoundsWhatVerifyAndExtractUnpack(t *testing.T) {
	for _, tc := range []struct {
		extra []string
		bytes string
	}{
		{nil, "17179869184"},
		{[]string{"--max-snapshot-gib", "2"}, "2147483648"},
	} {
		h := newDeployHarness(t)
		p := newLivePublisher(t)
		p.publish(h.stubNodeBinaries(), rerunCommit)
		if out, code := h.run(mainSnippet(p.server.URL, tc.extra...)); code != 0 {
			t.Fatalf("exit %d:\n%s", code, lastLines(out))
		}
		seen := map[string]bool{}
		for _, call := range strings.Split(h.calls(), "\n") {
			for _, verb := range []string{" verify --manifest", " extract --manifest"} {
				if strings.Contains(call, verb) && strings.Contains(call, "nhb-snapshot") {
					seen[verb] = true
					if !strings.Contains(call, "--max-bytes "+tc.bytes) {
						t.Fatalf("a call that reads the archive is not bounded by --max-bytes %s:\n%s", tc.bytes, call)
					}
				}
			}
		}
		if len(seen) != 2 {
			t.Fatalf("expected a verify and an extract call, saw %v:\n%s", seen, h.calls())
		}
	}
}

// A host that sends more than the manifest says is cut off at what it said, and
// what it sent is not left on the disk.
func TestDeployDoesNotKeepMoreThanTheManifestDeclaredFromAHostThatSendsMore(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	p.publish(h.stubNodeBinaries(), rerunCommit)
	var sent int64
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ".tar.gz") {
			http.FileServer(http.Dir(p.out)).ServeHTTP(w, r)
			return
		}
		// The genuine archive, and then more of it than any manifest declares.
		archive, _ := os.ReadFile(filepath.Join(p.out, p.m.Archive.Name))
		chunk := make([]byte, 64<<10)
		w.Header().Set("Content-Type", "application/octet-stream")
		if n, err := w.Write(archive); err == nil {
			atomic.AddInt64(&sent, int64(n))
		}
		for atomic.LoadInt64(&sent) < 1<<30 {
			n, err := w.Write(chunk)
			atomic.AddInt64(&sent, int64(n))
			if err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer hostile.Close()

	out, code := h.run(mainSnippet(hostile.URL))
	if code == 0 || !strings.Contains(out, "could not download") {
		t.Fatalf("exit %d:\n%s", code, lastLines(out))
	}
	if got := atomic.LoadInt64(&sent); got >= 1<<30 {
		t.Fatalf("the script kept reading %d bytes from a host that sent more than the %d bytes the manifest declares", got, p.m.Archive.Size)
	}
	entries, _ := os.ReadDir(filepath.Join(h.state, ".snapshot-download"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			t.Fatalf("what the failed download wrote was left in the download directory: %s", e.Name())
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(h.state, "nhb-data")); len(entries) > 0 {
		t.Fatalf("a refused download left %d files in the data directory", len(entries))
	}
}

// The operator can pin the snapshot's tip and state root to what nodes they trust
// say; a snapshot that is not that block is refused before it is downloaded.
func TestDeployPinsTheSnapshotsTipAndStateRoot(t *testing.T) {
	h := newDeployHarness(t)
	p := newLivePublisher(t)
	m := p.publish(h.stubNodeBinaries(), rerunCommit)
	other := "0x" + strings.Repeat("07", 32)
	upper := "0x" + strings.ToUpper(strings.TrimPrefix(m.TipHash, "0x"))

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"another tip hash", []string{"--tip-hash", other}, "--tip-hash"},
		{"another state root", []string{"--state-root", other}, "--state-root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(h.log)
			out, code := h.run(mainSnippet(p.server.URL, tc.args...))
			if code == 0 || !strings.Contains(out, "pinned with "+tc.want) {
				t.Fatalf("exit %d:\n%s", code, lastLines(out))
			}
			if downloaded(h.calls(), p) {
				t.Fatalf("the archive of a snapshot with another tip was downloaded:\n%s", h.calls())
			}
		})
	}

	t.Run("the pinned block", func(t *testing.T) {
		_ = os.Remove(h.log)
		// Hex is hex in either case, with or without the prefix.
		out, code := h.run(mainSnippet(p.server.URL, "--tip-hash", upper, "--state-root", strings.TrimPrefix(m.StateRoot, "0x")))
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, lastLines(out))
		}
		var verified, extracted bool
		for _, call := range strings.Split(h.calls(), "\n") {
			if !strings.Contains(call, "nhb-snapshot") {
				continue
			}
			for verb, flag := range map[string]*bool{" verify --manifest": &verified, " extract --manifest": &extracted} {
				if strings.Contains(call, verb) {
					*flag = true
					if !strings.Contains(call, "--tip-hash "+m.TipHash) || !strings.Contains(call, "--state-root "+m.StateRoot) {
						t.Fatalf("the pins did not reach %q:\n%s", verb, call)
					}
				}
			}
		}
		if !verified || !extracted {
			t.Fatalf("expected a verify and an extract call:\n%s", h.calls())
		}
	})
}

// The new arguments are checked before anything on the host is touched.
func TestDeployChecksTheNewArgumentsBeforeItTouchesTheSystem(t *testing.T) {
	h := newDeployHarness(t)
	valid := []string{"--beneficiary", goodBeneficiary, "--snapshot-url", "https://snapshots.example.invalid/nhb", "--bootnode", "peer.example.invalid:6001"}
	for _, tc := range []struct {
		name string
		edit []string
		want string
	}{
		{"a size that is not a number", []string{"--max-snapshot-gib", "big"}, "whole number of GiB"},
		{"no size at all", []string{"--max-snapshot-gib", "0"}, "whole number of GiB"},
		{"a tip hash that is not hex", []string{"--tip-hash", "0x1234"}, "--tip-hash must be 32 bytes of hex"},
		{"a state root that is not hex", []string{"--state-root", strings.Repeat("zz", 32)}, "--state-root must be 32 bytes of hex"},
		{"a lag that cannot be compared with the reference", []string{"--tip-rpc", "https://rpc.example.invalid", "--max-lag-blocks", "16"}, "15 blocks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(h.log)
			cmd := exec.Command(h.bash, append([]string{slash(h.script)}, append(append([]string(nil), valid...), tc.edit...)...)...)
			cmd.Dir = h.root
			cmd.Env = append(os.Environ(), "PATH="+slash(h.bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_LOG="+slash(h.log), "NHB_INSTALL_ROOT="+slash(h.install), "NHB_CONFIG_DIR="+slash(h.config), "NHB_STATE_DIR="+slash(h.state),
				"NHB_SNAPSHOT_URL=", "NHB_BOOTNODE=", "NHB_TIP_RPC_URL=", "NHB_MASTER_TREASURY=")
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("expected a refusal saying %q, got %v:\n%s", tc.want, err, out)
			}
			if calls := h.calls(); calls != "" {
				t.Fatalf("the script touched the system before it checked its arguments:\n%s", calls)
			}
		})
	}
}

// A node that started from a snapshot in this run is at the tip only once it has
// applied a block past the snapshot; a node this run installed no snapshot on has
// no such height to pass.
func TestDeployWaitUntilSyncedNeedsBlocksPastTheSnapshot(t *testing.T) {
	h := newDeployHarness(t)
	now := func() int64 { return time.Now().Unix() }
	run := func(node *fakeNode, snapshotHeight string) (string, int) {
		srv := httptest.NewServer(node.handler())
		defer srv.Close()
		addr := strings.TrimPrefix(srv.URL, "http://")
		return h.run(`RPC_ADDR=`+addr+`; SYNC_INTERVAL=100ms; SYNC_TIMEOUT_SECS=2
SNAPSHOT_HEIGHT=`+snapshotHeight+`
wait_until_synced
echo reached`, "NHB_SYNC_INTERVAL=100ms")
	}
	node := func(height uint64) *fakeNode {
		return &fakeNode{height: height, max: height, chainID: 18346390202490284624, genesis: pinnedGenesisHash[2:], timestamp: now}
	}

	t.Run("a node still at its snapshot's height", func(t *testing.T) {
		out, code := run(node(100), "100")
		if code == 0 || !strings.Contains(out, "did not reach the network tip") || strings.Contains(out, "reached\n") {
			t.Fatalf("a node that has not passed its snapshot was reported at the tip (exit %d):\n%s", code, out)
		}
	})
	t.Run("a node one block past it", func(t *testing.T) {
		if out, code := run(node(101), "100"); code != 0 || !strings.Contains(out, "reached") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("no snapshot was installed in this run", func(t *testing.T) {
		if out, code := run(node(100), ""); code != 0 || !strings.Contains(out, "reached") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
	})
	t.Run("a node without a peer", func(t *testing.T) {
		none := 0
		lonely := node(101)
		lonely.peers = &none
		out, code := run(lonely, "100")
		if code == 0 || strings.Contains(out, "reached\n") {
			t.Fatalf("a node with no peer was reported at the tip (exit %d):\n%s", code, out)
		}
	})
}
