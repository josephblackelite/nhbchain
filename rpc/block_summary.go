package rpc

// The explorer scans walk history block by block: the address history, the
// snapshot's look-back, the transaction counts of a time window, the latest
// transactions. On a chain whose blocks mostly carry nothing a scan cares about,
// each scan used to decode every block it passed (a JSON parse of the whole
// block) to find that out, hundreds of thousands of times a minute at the rate
// limit.
//
// blockSummaryCache remembers, per height, the few facts those scans branch
// on -- whether the block could be read, its timestamp, how many transactions
// it holds, and whether any of them is user-facing -- so a scan reads a block
// only when the summary says there is something in it. A summary is derived
// from an immutable committed block and carries nothing that changes when the
// tip block's state root is patched (no hash), so an entry never goes stale.
// The cache is a plain array of 8-byte entries covering the heights that have
// been looked at, bounded to the most recent txWindowStatsMaxBlocksScanned
// blocks, which is as far back as any scan reads.

import (
	"context"
	"sync"
	"sync/atomic"

	"nhbchain/core"
	"nhbchain/core/types"
)

type blockSummary struct {
	timestamp int64
	txCount   uint32
	flags     uint8
}

const (
	sumKnown      uint8 = 1 << iota // the block has been examined
	sumLoaded                       // GetBlockByHeight returned it
	sumHasHeader                    // ... and it carries a header
	sumUserFacing                   // one of its transactions is isExplorerUserFacingType
)

// loaded reports whether the block could be read at all.
func (b blockSummary) loaded() bool { return b.flags&sumLoaded != 0 }

// userFacing reports whether the block is readable and holds a user-facing
// transaction: the only kind of block that address and snapshot scans process.
func (b blockSummary) userFacing() bool {
	return b.flags&(sumLoaded|sumHasHeader|sumUserFacing) == sumLoaded|sumHasHeader|sumUserFacing
}

// readable reports whether the block was read and has a header, which is what
// every scan needs from a block before it looks inside.
func (b blockSummary) readable() bool {
	return b.flags&(sumLoaded|sumHasHeader) == sumLoaded|sumHasHeader
}

const (
	maxCachedBlockSummaries = txWindowStatsMaxBlocksScanned
	// blockSummaryGrowStep is how far the cache extends beyond what it was asked
	// for when it has to grow (toward genesis when a scan reaches below what it
	// covers, and above the tip as blocks are added), so that growing is an
	// occasional reallocation and not one per block.
	blockSummaryGrowStep = 8192
)

// A summary is kept as one 64-bit word so that a scan can read entries without
// taking a lock: a scan passes a hundred thousand of them and, under load, many
// scans run at once, and a shared lock word bounced between cores costs more
// than the read. Layout: flags in bits 0-3, transaction count in bits 4-27,
// timestamp (seconds) in bits 28-63. A summary that does not fit (a count of
// 16 million or a timestamp outside 0..2^36) is simply not cached and its block
// is read each time, which is slower but not wrong.
const (
	summaryTxBits = 24
	summaryTsBits = 36
)

func packSummary(b blockSummary) (uint64, bool) {
	if b.txCount >= 1<<summaryTxBits || b.timestamp < 0 || b.timestamp >= 1<<summaryTsBits {
		return 0, false
	}
	return uint64(b.flags&0xF) | uint64(b.txCount)<<4 | uint64(b.timestamp)<<(4+summaryTxBits), true
}

func unpackSummary(v uint64) blockSummary {
	return blockSummary{
		flags:     uint8(v & 0xF),
		txCount:   uint32(v >> 4 & (1<<summaryTxBits - 1)),
		timestamp: int64(v >> (4 + summaryTxBits)),
	}
}

// summaryTable covers the heights base .. base+len(sums)-1. It is never
// modified in shape once published; only its entries change, and each entry is
// written once, from zero (not examined) to its summary.
type summaryTable struct {
	base uint64
	sums []atomic.Uint64
}

type blockSummaryCache struct {
	table atomic.Pointer[summaryTable]
	mu    sync.Mutex // serialises growing the table
	max   int        // most heights kept; zero means maxCachedBlockSummaries
}

func (c *blockSummaryCache) limit() int {
	if c.max > 0 {
		return c.max
	}
	return maxCachedBlockSummaries
}

func (c *blockSummaryCache) lookup(height uint64) (blockSummary, bool) {
	t := c.table.Load()
	if t == nil || height < t.base || height-t.base >= uint64(len(t.sums)) {
		return blockSummary{}, false
	}
	v := t.sums[height-t.base].Load()
	if v == 0 {
		return blockSummary{}, false
	}
	return unpackSummary(v), true
}

func (c *blockSummaryCache) store(height uint64, sum blockSummary) {
	v, ok := packSummary(sum)
	if !ok {
		return
	}
	if t := c.table.Load(); t != nil && height >= t.base && height-t.base < uint64(len(t.sums)) {
		t.sums[height-t.base].Store(v)
		return
	}
	c.grow(height, v)
}

// grow publishes a table that covers height (if it is within what is worth
// remembering) and holds v for it. An entry written to the old table while the
// new one is being built may be lost, which only means that block is read again.
func (c *blockSummaryCache) grow(height, v uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.table.Load()
	limit := uint64(c.limit())
	var newBase, newLen uint64
	switch {
	case old == nil:
		newBase = height
		newLen = 1 + blockSummaryGrowStep
		if newLen > limit {
			newLen = limit
		}
	case height >= old.base && height-old.base < uint64(len(old.sums)):
		old.sums[height-old.base].Store(v) // another writer grew it first
		return
	case height >= old.base:
		newBase = old.base
		top := height
		if top-newBase+1 > limit {
			newBase = top - limit + 1
		}
		newLen = top - newBase + 1 + blockSummaryGrowStep
		if newLen > limit {
			newLen = limit
		}
	default:
		need := old.base - height
		if need < blockSummaryGrowStep {
			need = blockSummaryGrowStep
		}
		if need > old.base {
			need = old.base
		}
		if uint64(len(old.sums))+need > limit {
			return // older than anything worth remembering
		}
		newBase = old.base - need
		newLen = uint64(len(old.sums)) + need
	}
	fresh := &summaryTable{base: newBase, sums: make([]atomic.Uint64, newLen)}
	if old != nil {
		for i := range old.sums {
			h := old.base + uint64(i)
			if h < newBase || h-newBase >= newLen {
				continue
			}
			if e := old.sums[i].Load(); e != 0 {
				fresh.sums[h-newBase].Store(e)
			}
		}
	}
	if height >= newBase && height-newBase < newLen {
		fresh.sums[height-newBase].Store(v)
	}
	c.table.Store(fresh)
}

func summarizeBlock(block *types.Block, err error) blockSummary {
	sum := blockSummary{flags: sumKnown}
	if err != nil || block == nil {
		return sum
	}
	sum.flags |= sumLoaded
	sum.txCount = uint32(len(block.Transactions))
	if block.Header != nil {
		sum.flags |= sumHasHeader
		sum.timestamp = block.Header.Timestamp
	}
	for _, tx := range block.Transactions {
		if tx != nil && isExplorerUserFacingType(tx.Type) {
			sum.flags |= sumUserFacing
			break
		}
	}
	return sum
}

// summaryAt returns the summary of the block at height, reading the block and
// remembering what it holds when the cache does not have it yet. Reading a
// block is charged to the request's ticket (see queryTicket), which is also
// where a finished context or a full pool stops the scan; from a caller with no
// ticket it is free.
func (s *Server) summaryAt(ctx context.Context, chain *core.Blockchain, height uint64) (blockSummary, error) {
	if sum, ok := s.blockSummaries.lookup(height); ok {
		return sum, nil
	}
	if err := ticketFrom(ctx).chargeBlocks(ctx, 1); err != nil {
		return blockSummary{}, err
	}
	// The tip is read before the block: a height above it has no block yet, which
	// is not worth remembering, and reading it the other way round could take a
	// block committed in between for a missing one.
	tip := chain.GetHeight()
	block, err := chain.GetBlockByHeight(height)
	sum := summarizeBlock(block, err)
	if height <= tip {
		s.blockSummaries.store(height, sum)
	}
	return sum, nil
}

// userFacingBlockAt returns the block at height when it holds a user-facing
// transaction, which is the only kind of block the address and snapshot scans
// do anything with, and nil for every other block, including one that cannot
// be read. An error means the query was stopped, not that a block was missing.
func (s *Server) userFacingBlockAt(ctx context.Context, chain *core.Blockchain, height uint64) (*types.Block, error) {
	sum, err := s.summaryAt(ctx, chain, height)
	if err != nil {
		return nil, err
	}
	if !sum.userFacing() {
		return nil, nil
	}
	if err := ticketFrom(ctx).chargeBlocks(ctx, 1); err != nil {
		return nil, err
	}
	block, err := chain.GetBlockByHeight(height)
	if err != nil {
		return nil, nil
	}
	return block, nil
}
