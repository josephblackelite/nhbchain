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

import (
	"context"
	"sort"
	"sync"
	"time"

	"nhbchain/core"
)

type lendingHeightIndex struct {
	mu      sync.Mutex
	scanned uint64   // every height 1..scanned has been examined
	heights []uint64 // ascending heights whose block holds a committed lending transaction
}

// scanLocked examines the blocks above what has been scanned, up to height and,
// when limit is not zero, at most limit of them. A block that cannot be read is
// passed over, exactly as the full walk passed it over.
func (x *lendingHeightIndex) scanLocked(node *core.Node, height, limit uint64) {
	end := height
	if limit > 0 && x.scanned+limit < end {
		end = x.scanned + limit
	}
	for h := x.scanned + 1; h <= end; h++ {
		block, err := node.GetBlockByHeight(h)
		if err == nil && block != nil {
			for _, tx := range block.Transactions {
				if tx != nil && isCommittedLendingTxType(tx.Type) {
					x.heights = append(x.heights, h)
					break
				}
			}
		}
		x.scanned = h
	}
}

// through returns the ascending heights up to and including height that hold a
// committed lending transaction, examining first whatever has not been. The
// result is read-only.
func (x *lendingHeightIndex) through(node *core.Node, height uint64) []uint64 {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scanLocked(node, height, 0)
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
