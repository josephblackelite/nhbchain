package rpc

// nhb_getExplorerSnapshot serves the default window from the snapshot the
// background loop rebuilds once per block. Any other window used to be
// recomputed in full for every request -- the same work as the loop's, seconds
// of CPU on a small host -- so a client could pick a window the cache did not
// hold and make the node redo it on each call. Each window is now computed once
// per chain height and shared: concurrent requests for a window wait for the one
// computation instead of repeating it, and the number of windows kept is
// bounded.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// explorerSnapshotCacheWindows bounds how many windows other than the default
// are kept (the window is a request parameter between 1 and
// explorerMaxRecentBlocks).
const explorerSnapshotCacheWindows = 8

type explorerSnapshotEntry struct {
	mu       sync.Mutex // guards height, snapshot
	height   uint64
	snapshot *ExplorerSnapshotResult
	// build is a one-slot lock that serialises the computation of this window,
	// which a waiting request can give up on when its context ends (a
	// sync.Mutex does not allow that).
	build chan struct{}
	used  time.Time
}

// fresh returns the cached snapshot if it is for the given chain height.
func (e *explorerSnapshotEntry) fresh(height uint64) *ExplorerSnapshotResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshot != nil && e.height == height {
		return e.snapshot
	}
	return nil
}

type explorerSnapshotCache struct {
	mu      sync.Mutex
	entries map[int]*explorerSnapshotEntry
}

func (c *explorerSnapshotCache) entry(window int) *explorerSnapshotEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int]*explorerSnapshotEntry)
	}
	e, ok := c.entries[window]
	if !ok {
		if len(c.entries) >= explorerSnapshotCacheWindows {
			var oldest int
			var oldestUsed time.Time
			first := true
			for w, candidate := range c.entries {
				if first || candidate.used.Before(oldestUsed) {
					oldest, oldestUsed, first = w, candidate.used, false
				}
			}
			delete(c.entries, oldest)
		}
		e = &explorerSnapshotEntry{build: make(chan struct{}, 1)}
		c.entries[window] = e
	}
	e.used = time.Now()
	return e
}

// explorerSnapshotFor returns the snapshot for a window: the loop's for the
// default window, otherwise the one cached for the current chain height, which
// is computed (once, under a slot in the query pool) when there is none. A
// request the cache can answer never asks the pool for anything; one it cannot
// asks first, so that a full pool refuses it at once instead of after it has
// waited for another request's computation.
func (s *Server) explorerSnapshotFor(ctx context.Context, recentBlocks int) (*ExplorerSnapshotResult, error) {
	if snapshot := s.cachedExplorerSnapshot(recentBlocks); snapshot != nil {
		return snapshot, nil
	}
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	entry := s.snapshotCache.entry(recentBlocks)
	if snapshot := entry.fresh(s.node.Chain().GetHeight()); snapshot != nil {
		return snapshot, nil
	}
	if err := ticketFrom(ctx).require(ctx); err != nil {
		return nil, err
	}
	select {
	case entry.build <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-entry.build }()
	// Another request may have computed it while this one waited.
	if snapshot := entry.fresh(s.node.Chain().GetHeight()); snapshot != nil {
		return snapshot, nil
	}
	if s.explorerLoopOff {
		// Nothing else advances the all-time payment index, so it moves on as
		// snapshots are asked for (one batch of blocks at a time).
		s.advanceExplorerActivityIndex()
	}
	snapshot, err := s.buildExplorerSnapshot(ctx, recentBlocks)
	if err != nil {
		return nil, err
	}
	entry.mu.Lock()
	entry.snapshot, entry.height = snapshot, snapshot.LatestHeight
	entry.mu.Unlock()
	return snapshot, nil
}
