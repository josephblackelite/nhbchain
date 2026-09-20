package modules

// The public lending reads (lending_getMarket, lend_getPools,
// lending_getUserAccount) rebuild a pool's state from the committed lending
// transactions whenever the stored market has nothing in it -- which is always,
// on a chain where nobody has supplied or borrowed. The rebuild walked every
// block from height 1 to the tip and decoded each one, and it did so inside the
// node's exclusive state lock: on a long chain that is hundreds of thousands of
// blocks, seconds of CPU with block production and every state read waiting.
//
// The rebuild only ever looks at blocks that hold a committed lending
// transaction, and blocks never change once committed, so the heights of those
// blocks are remembered here. Each read examines only the blocks committed since
// the previous one, and the first read after the process starts (which has to
// look at all of them) does that before it takes the state lock, see
// warmReplayIndex; WarmReplayIndex does it in the background after start-up so
// that not even that read waits.
//
// A block that cannot be read when the index looks at it is not one that holds no
// lending transaction: the read may have failed once (a store error) and succeed
// the next time, and remembering the failure as an answer would hide that block
// from every later replay until the process restarts. Such heights are kept apart
// and read again, all of them together, once lendingUnreadRetryAfter has passed,
// when a read next asks; until then a call does not touch them, so a block that
// stays unreadable costs one read every few seconds and not one per call.

import (
	"context"
	"sort"
	"sync"
	"time"

	"nhbchain/core"
)

// lendingUnreadRetryAfter is how long heights that could not be read are left
// alone before they are read again.
const lendingUnreadRetryAfter = 5 * time.Second

type lendingHeightIndex struct {
	mu      sync.Mutex
	scanned uint64   // every height 1..scanned has been examined
	heights []uint64 // ascending heights whose block holds a committed lending transaction
	// unread holds, ascending, the heights up to scanned whose block could not be
	// read when it was examined, and retryAt is when they are next read again.
	unread  []uint64
	retryAt time.Time
	now     func() time.Time // the clock; nil means time.Now
}

func (x *lendingHeightIndex) clock() time.Time {
	if x.now != nil {
		return x.now()
	}
	return time.Now()
}

// holdsCommittedLendingTx reports whether the block at height h could be read
// and carries a committed lending transaction; readable is false when it could
// not be read.
func holdsCommittedLendingTx(node *core.Node, h uint64) (holds, readable bool) {
	block, err := node.GetBlockByHeight(h)
	if err != nil || block == nil {
		return false, false
	}
	for _, tx := range block.Transactions {
		if tx != nil && isCommittedLendingTxType(tx.Type) {
			return true, true
		}
	}
	return false, true
}

// scanLocked examines the blocks above what has been scanned, up to height and,
// when limit is not zero, at most limit of them. A block that cannot be read is
// passed over for now, as the full walk passed it over, and is read again later
// (see retryUnreadLocked).
func (x *lendingHeightIndex) scanLocked(node *core.Node, height, limit uint64) {
	end := height
	if limit > 0 && x.scanned+limit < end {
		end = x.scanned + limit
	}
	hadUnread := len(x.unread) > 0
	for h := x.scanned + 1; h <= end; h++ {
		holds, readable := holdsCommittedLendingTx(node, h)
		switch {
		case !readable:
			x.unread = append(x.unread, h)
		case holds:
			x.heights = append(x.heights, h)
		}
		x.scanned = h
	}
	if !hadUnread && len(x.unread) > 0 {
		x.retryAt = x.clock().Add(lendingUnreadRetryAfter)
	}
}

// retryUnreadLocked reads again the heights that could not be read, if it is
// time to, and adds those that hold a lending transaction to the index. Heights
// that still cannot be read wait another lendingUnreadRetryAfter.
func (x *lendingHeightIndex) retryUnreadLocked(node *core.Node) {
	if len(x.unread) == 0 || x.clock().Before(x.retryAt) {
		return
	}
	var found []uint64
	still := x.unread[:0]
	for _, h := range x.unread {
		holds, readable := holdsCommittedLendingTx(node, h)
		switch {
		case !readable:
			still = append(still, h)
		case holds:
			found = append(found, h)
		}
	}
	x.unread = still
	x.retryAt = x.clock().Add(lendingUnreadRetryAfter)
	if len(found) == 0 {
		return
	}
	// The slices through hands out are read while later calls go on, so the
	// heights are merged into a new one and never inserted into the old.
	merged := make([]uint64, 0, len(x.heights)+len(found))
	i, j := 0, 0
	for i < len(x.heights) || j < len(found) {
		if j == len(found) || i < len(x.heights) && x.heights[i] < found[j] {
			merged = append(merged, x.heights[i])
			i++
		} else {
			merged = append(merged, found[j])
			j++
		}
	}
	x.heights = merged
}

// through returns the ascending heights up to and including height that hold a
// committed lending transaction, examining first whatever has not been. The
// result is read-only.
func (x *lendingHeightIndex) through(node *core.Node, height uint64) []uint64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scanLocked(node, height, 0)
	x.retryUnreadLocked(node)
	n := sort.Search(len(x.heights), func(i int) bool { return x.heights[i] > height })
	return x.heights[:n:n]
}

// warmReplayIndex brings the index up to the chain tip. The lending reads call
// it before they take the state lock, so that work the index has not done yet is
// not spent inside the lock; under the lock a read only examines the blocks
// committed since.
func (m *LendingModule) warmReplayIndex() {
	if m == nil || m.node == nil {
		return
	}
	m.replayHeights.through(m.node, m.node.GetHeight())
}

// WarmReplayIndex brings the index up to the chain tip a slice of blocks at a
// time, resting between slices for three times as long as the slice took so as
// to use at most a quarter of one CPU, and returns when it has caught up or ctx
// ends. It is meant to run once in the background after start-up.
func (m *LendingModule) WarmReplayIndex(ctx context.Context) {
	if m == nil || m.node == nil {
		return
	}
	const slice = 1024
	x := &m.replayHeights
	for {
		started := time.Now()
		x.mu.Lock()
		height := m.node.GetHeight()
		x.scanLocked(m.node, height, slice)
		done := x.scanned >= height
		x.mu.Unlock()
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Since(started)):
		}
	}
}
