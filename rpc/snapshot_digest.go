package rpc

// The explorer snapshot is rebuilt after every block by the background loop,
// and each rebuild used to read and parse every block of its window (120 by
// default) and the user-facing blocks of its look-back over again, although
// nothing in a committed block ever changes and only one block per rebuild is
// new. A blockDigest keeps what the rebuild takes from one block -- its
// timestamp and transaction count, its summary as the latest-blocks list shows
// it, the transaction records that feed the latest transactions and the address
// and merchant statistics, and the per-block payment count and ZNHB flow of the
// series -- so a rebuild reads only the blocks it has not read before.
//
// The digest of a block is worked out with exactly the per-transaction steps
// the rebuild used to run inline, and is replayed in the same order, so the
// snapshot it produces is the same. It holds nothing that depends on state at
// the time it is used (balances and names are looked up when the snapshot is
// assembled, as before), and the tip block is never kept: an operator recovery
// can rewrite the tip's state root, and with it the block hash that the digest
// carries, but only while the block is the tip.

import (
	"context"
	"encoding/hex"
	"math/big"
	"strings"
	"sync/atomic"

	"nhbchain/core"
	"nhbchain/core/types"
)

// snapshotDigestSlots is how many digests are kept. It must cover the largest
// window (explorerMaxRecentBlocks) with room for the user-facing blocks of the
// look-back; a slot is chosen by height, so a digest that shares a slot with
// another is simply read again.
const snapshotDigestSlots = 1024

// snapshotDigestMaxTxs is the most transaction records a block's digest may hold
// and still be kept. A block can carry as many transactions as consensus allows
// (500 by default) and a record is about half a kilobyte, so without a bound a
// run of full blocks would pin hundreds of megabytes in the ring; with it the
// ring holds a few tens at most. A block above the bound is read again whenever
// a rebuild needs it, as every rebuild did before digests existed.
const snapshotDigestMaxTxs = 128

type digestTx struct {
	record     ExplorerTransactionResult
	userFacing bool
}

type blockDigest struct {
	height    uint64
	timestamp int64
	txCount   int
	// block is the block's entry in the latest-blocks list, nil if it could not
	// be built.
	block *ExplorerBlockResult
	// txs are the transactions the statistics look at: the user-facing ones and
	// those a merchant total counts, in block order.
	txs        []digestTx
	payments   int
	rewardFlow *big.Int
}

type blockDigestCache struct {
	slots [snapshotDigestSlots]atomic.Pointer[blockDigest]
}

func (c *blockDigestCache) get(height uint64) *blockDigest {
	if d := c.slots[height%snapshotDigestSlots].Load(); d != nil && d.height == height {
		return d
	}
	return nil
}

func (c *blockDigestCache) put(d *blockDigest) {
	c.slots[d.height%snapshotDigestSlots].Store(d)
}

// digestBlock is what the snapshot's collectBlock step used to do with a block,
// kept instead of applied. block must have a header.
func digestBlock(block *types.Block) *blockDigest {
	d := &blockDigest{
		height:     block.Header.Height,
		timestamp:  block.Header.Timestamp,
		txCount:    len(block.Transactions),
		rewardFlow: big.NewInt(0),
	}
	if summary, err := buildExplorerBlockResult(block); err == nil {
		d.block = summary
	}
	blockHash, _ := block.Header.Hash()
	for _, tx := range block.Transactions {
		txHashBytes, hashErr := tx.Hash()
		if hashErr != nil {
			continue
		}
		record, err := buildExplorerTransactionResult(tx, ensureHexPrefix(hex.EncodeToString(txHashBytes)), blockHash, block.Header.Height, block.Header.Timestamp)
		if err != nil {
			continue
		}
		userFacing := isExplorerUserFacingType(tx.Type)
		// The same condition recordMerchantActivity applies before it counts a
		// record; every other transaction leaves the statistics alone.
		merchant := record.To != "" && strings.EqualFold(record.Asset, "NHB")
		if userFacing || merchant {
			d.txs = append(d.txs, digestTx{record: *record, userFacing: userFacing})
		}
		if isPaymentLikeType(tx.Type) {
			d.payments++
		}
		if strings.EqualFold(record.Asset, "ZNHB") {
			if amountWei, ok := new(big.Int).SetString(record.Amount, 10); ok {
				d.rewardFlow.Add(d.rewardFlow, amountWei)
			}
		}
	}
	return d
}

// digestAt returns the digest of the block at height, or nil when the block
// cannot be read. tip is the chain height the caller read before asking: only a
// block below it is kept (see the file comment), and only one with at most
// snapshotDigestMaxTxs records. Reading a block is charged to the request's
// ticket.
func (s *Server) digestAt(ctx context.Context, chain *core.Blockchain, height, tip uint64) (*blockDigest, error) {
	if d := s.digests.get(height); d != nil {
		return d, nil
	}
	if err := ticketFrom(ctx).chargeBlocks(ctx, 1); err != nil {
		return nil, err
	}
	block, err := chain.GetBlockByHeight(height)
	if err != nil || block == nil || block.Header == nil {
		return nil, nil
	}
	d := digestBlock(block)
	if height < tip && len(d.txs) <= snapshotDigestMaxTxs {
		s.digests.put(d)
	}
	return d, nil
}

// userFacingDigestAt is digestAt for the look-back, which only has use for a
// block with a user-facing transaction: the summary says whether the block is
// one, so the others cost nothing.
func (s *Server) userFacingDigestAt(ctx context.Context, chain *core.Blockchain, height, tip uint64) (*blockDigest, error) {
	sum, err := s.summaryAt(ctx, chain, height)
	if err != nil {
		return nil, err
	}
	if !sum.userFacing() {
		return nil, nil
	}
	return s.digestAt(ctx, chain, height, tip)
}

// recentTPS is estimateRecentTPS over the block summaries: the same figure from
// the timestamps and transaction counts of the last ten blocks, without reading
// them again on every call.
func (s *Server) recentTPS(ctx context.Context) (float64, error) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return 0, nil
	}
	chain := s.node.Chain()
	height := chain.GetHeight()
	if height == 0 {
		return 0, nil
	}
	const window uint64 = 10
	startHeight := uint64(1)
	if height >= window {
		startHeight = height - window + 1
	}
	var (
		txCount   int
		firstTime int64 = -1
		lastTime  int64 = -1
	)
	for h := startHeight; h <= height; h++ {
		sum, err := s.summaryAt(ctx, chain, h)
		if err != nil {
			return 0, err
		}
		if !sum.readable() {
			continue
		}
		ts := sum.timestamp
		if firstTime < 0 || ts < firstTime {
			firstTime = ts
		}
		if ts > lastTime {
			lastTime = ts
		}
		txCount += int(sum.txCount)
	}
	if txCount == 0 {
		return 0, nil
	}
	if firstTime < 0 || lastTime <= firstTime {
		return float64(txCount), nil
	}
	return float64(txCount) / float64(lastTime-firstTime+1), nil
}
