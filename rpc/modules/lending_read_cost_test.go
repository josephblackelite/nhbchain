package modules

// What the public lending reads cost the rest of the node when the pool or the
// address they are asked about has nothing stored. These use only the module's
// public methods, WarmReplayIndex, and the test chain's helpers (and the full
// walk kept in legacy_lending_replay_test.go), so they can be pointed at an older
// tree to see what it does there.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/lending"
)

// warmUp brings the index up to the tip the way the node does after start-up.
func warmUp(t *testing.T, m *LendingModule) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.WarmReplayIndex(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute): // only a failure waits this long
		t.Fatalf("the warm-up did not finish")
	}
}

// TestLendingReadsWithNothingStoredReadNoBlocksOnceWarm: a read for an address or
// a pool with no stored position used to read every block that holds a lending
// transaction, decode its transactions and recover their signers, on every call.
func TestLendingReadsWithNothingStoredReadNoBlocksOnceWarm(t *testing.T) {
	lc := newLendingChain(t, 41)
	lc.extend(t, 2500, 2)
	module := NewLendingModule(lc.node)
	warmUp(t, module)

	var nobody [20]byte
	nobody[0] = 0x99
	reads := func() {
		if account, moduleErr := module.GetUserAccount(defaultLendingPoolID, nobody); moduleErr != nil || account != nil {
			t.Fatalf("GetUserAccount: %v %v", account, moduleErr)
		}
		if account, moduleErr := module.GetUserAccount("poolB", nobody); moduleErr != nil || account != nil {
			t.Fatalf("GetUserAccount(poolB): %v %v", account, moduleErr)
		}
		if _, _, moduleErr := module.GetMarket(defaultLendingPoolID); moduleErr != nil {
			t.Fatalf("GetMarket: %v", moduleErr)
		}
		if _, _, moduleErr := module.GetMarket("poolC"); moduleErr != nil {
			t.Fatalf("GetMarket(poolC): %v", moduleErr)
		}
		if _, _, moduleErr := module.GetPools(); moduleErr != nil {
			t.Fatalf("GetPools: %v", moduleErr)
		}
	}
	reads() // the first read of each pool makes its sum
	before := lc.store.gets.Load()
	for i := 0; i < 5; i++ {
		reads()
	}
	got := lc.store.gets.Load() - before
	t.Logf("25 reads read the store %d times; %d blocks hold a lending transaction", got, len(lc.lending))
	// Each round used to read the lending blocks of a pool several times over,
	// thousands of reads; what is left is a handful at most.
	if limit := int64(50); got > limit {
		t.Fatalf("25 reads read the store %d times (limit %d); %d blocks hold a lending transaction", got, limit, len(lc.lending))
	}
}

// TestAReadForAnAddressWithNoPositionDoesNotHoldTheStateLock: the rebuild ran
// inside the state lock, and cost time in the number of lending transactions on
// the chain, so every other state read waited for it.
func TestAReadForAnAddressWithNoPositionDoesNotHoldTheStateLock(t *testing.T) {
	lc := newLendingChain(t, 61)
	lc.extend(t, 2500, 1)
	module := NewLendingModule(lc.node)
	warmUp(t, module)

	// What the rebuild costs when it is done by decoding the chain's transactions.
	start := time.Now()
	if _, _, err := module.legacyReplayCommittedPoolState(defaultLendingPoolID); err != nil {
		t.Fatalf("full walk: %v", err)
	}
	walk := time.Since(start)
	if walk < 30*time.Millisecond {
		t.Skipf("the full walk took only %s on this machine; too fast to tell whether a read held the lock", walk)
	}
	limit := walk / 4
	if limit < stateLockFloor {
		limit = stateLockFloor
	}

	var nobody [20]byte
	nobody[0] = 0x98
	var probes []lockProbe
	for attempt := 0; attempt < stateLockAttempts; attempt++ {
		p := probeLendingRead(t, lc, func() *ModuleError {
			_, moduleErr := module.GetUserAccount(defaultLendingPoolID, nobody)
			return moduleErr
		})
		// The read is short now, so how many state reads finished during it says
		// nothing; a lock held for the rebuild shows as one long wait.
		if p.worst <= limit {
			t.Logf("attempt %d: full walk %s, read %s, longest state read wait %s", attempt+1, walk, p.scan, p.worst)
			return
		}
		t.Logf("attempt %d: full walk %s, read %s, longest state read wait %s (limit %s)", attempt+1, walk, p.scan, p.worst, limit)
		probes = append(probes, p)
	}
	t.Fatalf("in each of %d attempts a state read waited for the read of an address with no position (%+v)", len(probes), probes)
}

// TestAReadThatNeedsTheIndexAsksToRetryWhileItIsBehind: the first read after a
// restart used to look at every block of the chain itself, however long that
// took, holding a slot of the query pool for it. Now it looks for a bounded time
// and, if the index is still behind the chain, asks to be retried.
func TestAReadThatNeedsTheIndexAsksToRetryWhileItIsBehind(t *testing.T) {
	lc := newLendingChain(t, 62)
	lc.extend(t, 100_000, 0)
	var stored [20]byte
	stored[0] = 0x55
	if err := lc.node.WithState(func(manager *nhbstate.Manager) error {
		supplied := big.NewInt(1_000)
		market := &lending.Market{
			PoolID:            "funded",
			TotalNHBSupplied:  supplied,
			TotalSupplyShares: supplied,
			TotalNHBBorrowed:  big.NewInt(0),
			SupplyIndex:       new(big.Int).Set(lendingRay),
			BorrowIndex:       new(big.Int).Set(lendingRay),
		}
		if err := manager.LendingPutMarket("funded", market); err != nil {
			return err
		}
		return manager.LendingPutUserAccount("funded", &lending.UserAccount{
			Address:        toCryptoAddress(stored),
			CollateralZNHB: big.NewInt(0),
			SupplyShares:   supplied,
			DebtNHB:        big.NewInt(0),
			ScaledDebt:     big.NewInt(0),
		})
	}); err != nil {
		t.Fatalf("store a position: %v", err)
	}

	var nobody [20]byte
	nobody[0] = 0x97
	t.Run("a read that needs the sum does not look at the whole chain", func(t *testing.T) {
		module := NewLendingModule(lc.node)
		start := time.Now()
		account, moduleErr := module.GetUserAccount(defaultLendingPoolID, nobody)
		took := time.Since(start)
		switch {
		case moduleErr == nil:
			// Only a machine that can look at the whole chain within one step of
			// catching up answers at once; looking at it for much longer than a step
			// before answering is what a read used to do.
			if took > 300*time.Millisecond {
				t.Fatalf("a read looked at %d blocks for %s and then answered %v; one step of catching up is 100 ms", lc.node.GetHeight(), took, account)
			}
		case moduleErr.HTTPStatus != http.StatusServiceUnavailable:
			t.Fatalf("GetUserAccount: %v", moduleErr)
		case took > 5*time.Second:
			t.Fatalf("the read took %s to ask for a retry", took)
		}
		// Asking again goes on from where the last call stopped, and the answer comes.
		for call := 0; moduleErr != nil; call++ {
			start = time.Now()
			account, moduleErr = module.GetUserAccount(defaultLendingPoolID, nobody)
			if moduleErr != nil && (moduleErr.HTTPStatus != http.StatusServiceUnavailable || call > 1000) {
				t.Fatalf("call %d: %v", call, moduleErr)
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("call %d took %s", call, took)
			}
		}
		if account != nil {
			t.Fatalf("an address that never sent a lending transaction has an account: %v", account)
		}
	})

	t.Run("a read the stored state answers does not wait for the index", func(t *testing.T) {
		module := NewLendingModule(lc.node)
		account, moduleErr := module.GetUserAccount("funded", stored)
		if moduleErr != nil || account == nil || account.SupplyShares.Cmp(big.NewInt(1_000)) != 0 {
			t.Fatalf("GetUserAccount of a stored position: %v, %v", account, moduleErr)
		}
		market, _, moduleErr := module.GetMarket("funded")
		if moduleErr != nil || market == nil || market.TotalNHBSupplied.Cmp(big.NewInt(1_000)) != 0 {
			t.Fatalf("GetMarket of a funded pool: %v, %v", market, moduleErr)
		}
	})
}

// scenario describes a node's lending state for the comparison below.
type readsScenario struct {
	name   string
	legacy bool // a lender also holds a position recorded the old way
	stored bool // a pool and an account are stored
}

// TestLendingReadsAnswerWhatTheFullWalkAnswered: the reads give the same markets
// and accounts, field for field, as when each call rebuilt the pool by walking
// the chain and writing every account it found into the state view.
func TestLendingReadsAnswerWhatTheFullWalkAnswered(t *testing.T) {
	for _, sc := range []readsScenario{
		{name: "nothing stored"},
		{name: "a legacy position held by a lender", legacy: true},
		{name: "a stored pool and account", stored: true},
		{name: "both", legacy: true, stored: true},
	} {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			lc := newLendingChain(t, 71)
			lc.extend(t, 600, 3)
			module := NewLendingModule(lc.node)

			var lenders [][20]byte
			for _, key := range lc.users {
				var addr [20]byte
				copy(addr[:], key.PubKey().Address().Bytes())
				lenders = append(lenders, addr)
			}
			var nobody [20]byte
			nobody[0] = 0x96
			addrs := append(append([][20]byte{}, lenders...), nobody)
			if sc.legacy {
				seedLegacyLendingAccount(t, lc.node, lenders[0])
				seedLegacyLendingAccount(t, lc.node, nobody)
			}
			if sc.stored {
				if err := lc.node.WithState(func(manager *nhbstate.Manager) error {
					supplied := big.NewInt(7_000)
					market := &lending.Market{
						PoolID:            "poolC",
						TotalNHBSupplied:  supplied,
						TotalSupplyShares: supplied,
						TotalNHBBorrowed:  big.NewInt(100),
						SupplyIndex:       new(big.Int).Set(lendingRay),
						BorrowIndex:       new(big.Int).Set(lendingRay),
					}
					if err := manager.LendingPutMarket("poolC", market); err != nil {
						return err
					}
					return manager.LendingPutUserAccount("poolC", &lending.UserAccount{
						Address:        toCryptoAddress(lenders[1]),
						CollateralZNHB: big.NewInt(3),
						SupplyShares:   big.NewInt(4),
						DebtNHB:        big.NewInt(5),
						ScaledDebt:     big.NewInt(6),
					})
				}); err != nil {
					t.Fatalf("store state: %v", err)
				}
			}
			warmUp(t, module)

			show := func(v any, moduleErr *ModuleError) string {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				status := 0
				if moduleErr != nil {
					status = moduleErr.HTTPStatus
				}
				return fmt.Sprintf("%s|%d", raw, status)
			}
			for _, pool := range []string{"", "default", "poolB", "poolC", "nobody"} {
				wantMarket, wantParams, wantErr := module.legacyGetMarket(pool)
				gotMarket, gotParams, gotErr := module.GetMarket(pool)
				if want, got := show([]any{wantMarket, wantParams}, wantErr), show([]any{gotMarket, gotParams}, gotErr); want != got {
					t.Fatalf("GetMarket(%q) differs\nwant %s\n got %s", pool, want, got)
				}
				for _, addr := range addrs {
					wantAccount, wantErr := module.legacyGetUserAccount(pool, addr)
					gotAccount, gotErr := module.GetUserAccount(pool, addr)
					if want, got := show(wantAccount, wantErr), show(gotAccount, gotErr); want != got {
						t.Fatalf("GetUserAccount(%q, %x) differs\nwant %s\n got %s", pool, addr, want, got)
					}
				}
			}
			wantPools, wantParams, wantErr := module.legacyGetPools()
			gotPools, gotParams, gotErr := module.GetPools()
			if want, got := show([]any{wantPools, wantParams}, wantErr), show([]any{gotPools, gotParams}, gotErr); want != got {
				t.Fatalf("GetPools differs\nwant %s\n got %s", want, got)
			}
		})
	}
}

// Reads, the warm-up and new blocks may all go on at once: every read either
// answers or asks to be retried, and when the dust settles the answers are those
// of the full walk.
func TestLendingReadsRunWhileTheChainGrows(t *testing.T) {
	lc := newLendingChain(t, 81)
	lc.extend(t, 3000, 3)
	module := NewLendingModule(lc.node)

	stop := make(chan struct{})
	errs := make(chan string, 64)
	var wg sync.WaitGroup
	report := func(msg string) {
		select {
		case errs <- msg:
		default:
		}
	}
	for worker := 0; worker < 4; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			var addr [20]byte
			addr[0] = byte(0xA0 + worker)
			pools := []string{"", "poolB", "poolC", "nobody"}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				pool := pools[(i+worker)%len(pools)]
				var moduleErr *ModuleError
				switch (i + worker) % 3 {
				case 0:
					_, _, moduleErr = module.GetMarket(pool)
				case 1:
					_, moduleErr = module.GetUserAccount(pool, addr)
				default:
					_, _, moduleErr = module.GetPools()
				}
				if moduleErr != nil && moduleErr.HTTPStatus != http.StatusServiceUnavailable {
					report(fmt.Sprintf("worker %d: %v", worker, moduleErr))
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		module.WarmReplayIndex(context.Background())
	}()
	for round := 0; round < 5; round++ {
		lc.extend(t, 100, 2)
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}

	for _, pool := range []string{"", "poolB", "poolC", "nobody"} {
		wantMarket, wantUsers, wantErr := module.legacyReplayCommittedPoolState(pool)
		if wantErr != nil {
			t.Fatalf("full walk: %v", wantErr)
		}
		gotMarket, gotUsers := module.replayAll(t, pool)
		if want, got := describeReplay(wantMarket, wantUsers), describeReplay(gotMarket, gotUsers); want != got {
			t.Fatalf("pool %q: replay differs after concurrent reads\nwant %s\n got %s", pool, want, got)
		}
	}
}
