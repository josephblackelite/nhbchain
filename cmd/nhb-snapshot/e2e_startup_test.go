package main

// How long a node takes to start on a database of a given height: the number
// behind the default of --rpc-timeout in scripts/deployvalidator.sh (see "How long
// the node takes to start" in docs/validators/snapshot-onboarding.md). It runs real
// nodes for several minutes and only runs when asked:
//
//	NHB_SNAPSHOT_E2E_STARTUP=1 NHB_SNAPSHOT_E2E_STARTUP_TARGETS=1000,4000,12000,30000 \
//	  NHB_TEST_BASH=... go test ./cmd/nhb-snapshot -run TestNodeStartupTime -v -timeout 90m

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
)

// txIndexBackfillDoneKey is the key core.Blockchain keeps once it has indexed the
// transactions of every block (core/blockchain.go). A database without it makes
// the next start walk every block, once.
const txIndexBackfillDoneKey = "txHashIndexBackfillDone"

func TestNodeStartupTime(t *testing.T) {
	e2eEnabledFor(t, "NHB_SNAPSHOT_E2E_STARTUP")
	targets := []uint64{1000, 4000, 12000, 30000}
	if v := strings.TrimSpace(os.Getenv("NHB_SNAPSHOT_E2E_STARTUP_TARGETS")); v != "" {
		targets = nil
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(f), 10, 64)
			if err != nil || n == 0 {
				t.Fatalf("NHB_SNAPSHOT_E2E_STARTUP_TARGETS: %q", f)
			}
			targets = append(targets, n)
		}
	}
	e := newE2EEnv(t)
	data := filepath.Join(e.dir, "startup", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	dirBytes := func() int64 {
		var n int64
		entries, _ := os.ReadDir(data)
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && !entry.IsDir() {
				n += info.Size()
			}
		}
		return n
	}
	// restart starts a node with the shipped consensus timeouts on the database and
	// returns how long it took to answer net_info for the first time.
	restart := func(name string) time.Duration {
		n := e.newNode(name, e.keys["validator"], data, nil, "0s", "")
		began := time.Now()
		n.start()
		defer n.kill()
		for time.Since(began) < 10*time.Minute {
			if n.info() != nil {
				return time.Since(began)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s did not answer within ten minutes\n%s", name, n.logTail(20))
		return 0
	}
	for _, target := range targets {
		// The chain grows on fast consensus timeouts, and the node is killed the hard
		// way, as a crash would leave it.
		grow := e.newNode(fmt.Sprintf("grow-%d", target), e.keys["validator"], data, nil, "5ms", "")
		grow.start()
		e.waitUntil(fmt.Sprintf("height %d", target), 40*time.Minute, grow, func() bool { return grow.height() >= target })
		height := grow.height()
		grow.kill()
		size := dirBytes()

		var normal []time.Duration
		for i := 0; i < 3; i++ {
			normal = append(normal, restart(fmt.Sprintf("restart-%d-%d", target, i)))
		}
		// A database that lacks the marker of the one-time transaction index makes the
		// first start walk every block.
		db, err := leveldb.OpenFile(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Delete([]byte(txIndexBackfillDoneKey), nil); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		backfill := restart(fmt.Sprintf("backfill-%d", target))
		t.Logf("height %d, %d bytes: start to answer %v; first start without the index marker %v", height, size, normal, backfill)
	}
}
