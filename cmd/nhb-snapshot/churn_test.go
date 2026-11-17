package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// scripts/make-snapshot.sh copies a database that is being written. This runs
// it against a chain database whose buffers are so small that it flushes tables
// and compacts all the time (so table files appear and disappear, the MANIFEST
// grows, and journals rotate while the copy is made). Every snapshot must open,
// and its tip must be a block the writer really wrote at that height: a copy that
// mixed two moments of the database would not open, or would name a tip the writer
// never had.
//
// The writer works in bursts and rests between them, and every snapshot is
// started with a burst, so that each copy has to get through a burst of writes and
// can finish in the rest after it. (A writer that never rests, at this size of
// buffers, changes the database faster than any copy can be made: a copy that
// keeps failing is the script's designed answer to that, and a test that asks for
// success of it is a test of the speed of the machine.)
func TestMakeSnapshotUnderChurn(t *testing.T) {
	bash := e2eBash(t)
	tool := builtTool(t)
	repo := repoRoot(t)

	dir := filepath.Join(t.TempDir(), "node", "data")
	b := newChainBuilder(t, dir, true, testAddress(0x77))

	const (
		burstBlocks = 40
		burstPause  = 15 * time.Millisecond
		restAfter   = 4 * time.Second
	)
	// Standard output belongs to the writer's chatter (core reports every
	// block it adds) for as long as it runs.
	restore := silenceStdout()
	var (
		mu       sync.Mutex
		hashes   = map[uint64]string{}
		stop     int32
		wg       sync.WaitGroup
		werr     error
		bursting = make(chan struct{}, 1)
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stop) == 0 {
			select {
			case bursting <- struct{}{}:
			default:
			}
			for i := 0; i < burstBlocks && atomic.LoadInt32(&stop) == 0; i++ {
				if err := b.tryAddBlock(3); err != nil {
					werr = err
					return
				}
				mu.Lock()
				hashes[b.bc.Height()] = hex0x(b.bc.Tip())
				mu.Unlock()
				time.Sleep(burstPause)
			}
			for waited := time.Duration(0); waited < restAfter && atomic.LoadInt32(&stop) == 0; waited += 50 * time.Millisecond {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()
	var once sync.Once
	stopWriter := func() {
		once.Do(func() {
			atomic.StoreInt32(&stop, 1)
			wg.Wait()
			restore()
			b.close()
		})
	}
	defer stopWriter()

	// Let it write enough for tables to exist before the first snapshot.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		tables := 0
		for _, n := range dbFiles(t, dir) {
			if strings.HasSuffix(n, ".ldb") {
				tables++
			}
		}
		if tables >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	highestTable := func() int {
		best := 0
		for _, n := range dbFiles(t, dir) {
			if num, ok := tableNumber(n); ok && int(num) > best {
				best = int(num)
			}
		}
		return best
	}
	firstTables := highestTable()
	passes := regexp.MustCompile(`pass (\d+): `)
	retries, snapshots := 0, 0
	var heights []uint64
	for i := 0; i < 6; i++ {
		// Start with the next burst of writes: not one that began earlier.
		select {
		case <-bursting:
		default:
		}
		select {
		case <-bursting:
		case <-time.After(30 * time.Second):
			t.Fatalf("the writer did not start a burst")
		}
		out := filepath.Join(t.TempDir(), "out")
		work := filepath.Join(t.TempDir(), "work")
		cmd := runBashScript(t, bash, repo, "scripts/make-snapshot.sh",
			"--data-dir", slash(dir), "--out-dir", slash(out), "--work-dir", slash(work),
			"--tool", slash(tool), "--node-binary", slash(filepath.Join(repo, "nonexistent")), "--binary-commit", testCommit, "--max-passes", "60")
		if cmd.err != nil {
			mu.Lock()
			last := len(hashes)
			mu.Unlock()
			t.Fatalf("snapshot %d failed (the writer had written %d blocks): %v\n%s", i+1, last, cmd.err, cmd.out)
		}
		retries += strings.Count(cmd.out, "retrying")
		if m := passes.FindAllStringSubmatch(cmd.out, -1); len(m) > 0 {
			if last, _ := strconv.Atoi(m[len(m)-1][1]); last > 2 {
				t.Logf("snapshot %d needed %d passes", i+1, last)
			}
		}
		m, err := readManifestFile(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		want, seen := hashes[m.Height]
		mu.Unlock()
		if !seen {
			t.Fatalf("snapshot %d is at height %d, a block the writer never wrote", i+1, m.Height)
		}
		if m.TipHash != want {
			t.Fatalf("snapshot %d: tip %s at height %d, the writer wrote %s", i+1, m.TipHash, m.Height, want)
		}
		// Unpack it the way a new node would and open it again.
		target := filepath.Join(t.TempDir(), "restored")
		if o, code := runToolCode(tool, "extract", "--manifest", filepath.Join(out, "manifest.json"),
			"--archive", filepath.Join(out, m.Archive.Name), "--target", target,
			"--chain-id", m.ChainID, "--genesis-hash", m.GenesisHash); code != 0 {
			t.Fatalf("snapshot %d does not unpack: %s", i+1, o)
		}
		heights = append(heights, m.Height)
		snapshots++
	}
	stopWriter()
	if werr != nil {
		t.Fatalf("the writer failed: %v", werr)
	}
	churned := highestTable() - firstTables
	t.Logf("%d snapshots at heights %v while the writer wrote %d blocks; %d new table files appeared meanwhile; %d passes were retried",
		snapshots, heights, len(hashes), churned, retries)
	if churned < 3 {
		t.Fatalf("only %d table files were written while the snapshots were made: the database did not churn", churned)
	}
	for i := 1; i < len(heights); i++ {
		if heights[i] <= heights[i-1] {
			t.Fatalf("snapshot heights do not increase: %v", heights)
		}
	}
}

// silenceStdout points standard output at the null device and returns what
// puts it back.
func silenceStdout() func() {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	saved := os.Stdout
	os.Stdout = null
	return func() {
		os.Stdout = saved
		_ = null.Close()
	}
}

type scriptRun struct {
	out string
	err error
}

func runBashScript(t *testing.T, bash, repo, script string, args ...string) scriptRun {
	t.Helper()
	cmd := exec.Command(bash, append([]string{slash(filepath.Join(repo, script))}, args...)...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return scriptRun{out: string(out), err: err}
}

// runToolCode runs the built tool and returns its output and exit code.
func runToolCode(tool string, args ...string) (string, int) {
	out, err := exec.Command(tool, args...).CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	return err.Error(), -1
}
