package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"sync"
)

const (
	// explorerActivityMetaKey names the persisted blob (see
	// core/blockchain.go's GetExplorerMeta/PutExplorerMeta) holding this
	// index's running totals and watermark.
	explorerActivityMetaKey = "explorerPaymentActivityTotals"
)

// explorerActivityBatchSize bounds how many blocks a single
// advanceExplorerActivityIndex call will scan. Each block is read via the
// same chain.GetBlockByHeight the rest of this package already uses for
// windowed scans -- a brief, independently-locked call, never a long-held
// scan lock (see buildAddressActivity's doc comment for the production
// incident that taught this lesson: a full forward scan from genesis, run
// synchronously on demand, caused a live node-wide hang at ~240k blocks).
// Capping the per-call work here means a large catch-up (a fresh deploy of
// this feature, or a long restart gap) spreads itself across many ticks of
// startExplorerSnapshotLoop instead of blocking any one of them, and
// progress is persisted after every call so a restart mid-catch-up resumes
// near where it left off rather than starting over. A var, not a const, so
// tests can shrink it to exercise multi-call batching without needing
// thousands of real blocks.
var explorerActivityBatchSize = 2000

// explorerActivityTotals is the persisted shape of this index. TotalZNHBFlowWei
// is stored as a decimal string because big.Int does not round-trip through
// encoding/json on its own.
type explorerActivityTotals struct {
	ProcessedHeight  uint64 `json:"processedHeight"`
	TotalPayments    uint64 `json:"totalPayments"`
	TotalZNHBFlowWei string `json:"totalZnhbFlowWei"`
}

func (s *Server) loadExplorerActivityTotals() {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return
	}
	raw, err := s.node.Chain().GetExplorerMeta(explorerActivityMetaKey)
	if err != nil || len(raw) == 0 {
		return
	}
	var totals explorerActivityTotals
	if err := json.Unmarshal(raw, &totals); err != nil {
		return
	}
	s.activityMu.Lock()
	s.activityTotals = totals
	s.activityMu.Unlock()
}

// currentExplorerActivityTotals returns the index's current view: total
// payment-like transactions and total ZNHB movement across every block this
// index has processed so far, plus whether that processing has actually
// caught up to the chain's current tip. A false complete flag means the
// totals are a true lower bound (nothing is ever double-counted or
// fabricated), just not yet final -- callers should present that
// distinction to the user rather than silently rounding it away.
func (s *Server) currentExplorerActivityTotals() (payments int, znhbFlowWei *big.Int, complete bool) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return 0, big.NewInt(0), false
	}
	s.activityMu.RLock()
	totals := s.activityTotals
	s.activityMu.RUnlock()

	flow, ok := new(big.Int).SetString(totals.TotalZNHBFlowWei, 10)
	if !ok || flow == nil {
		flow = big.NewInt(0)
	}
	complete = totals.ProcessedHeight >= s.node.Chain().GetHeight()
	return int(totals.TotalPayments), flow, complete
}

var explorerActivityIndexMu sync.Mutex

// advanceExplorerActivityIndex scans forward from the last height this index
// processed, bounded to explorerActivityBatchSize blocks, tallying every
// payment-like transaction (isPaymentLikeType) and every ZNHB-denominated
// movement (assetLabel == "ZNHB", reusing buildExplorerTransactionResult's
// already-correct per-type amount extraction rather than re-deriving it).
// Intended to be called once per tick of startExplorerSnapshotLoop; it is a
// cheap no-op once caught up (the common case, 0-1 new blocks per tick).
// explorerActivityIndexMu (package-level, not per-Server) is defensive
// against this ever being wired to more than one ticker for the same
// process; a single Server's own ticker never calls this concurrently with
// itself since it's a plain sequential for-loop.
func (s *Server) advanceExplorerActivityIndex() {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return
	}
	explorerActivityIndexMu.Lock()
	defer explorerActivityIndexMu.Unlock()

	chain := s.node.Chain()
	latest := chain.GetHeight()

	s.activityMu.RLock()
	from := s.activityTotals.ProcessedHeight + 1
	payments := s.activityTotals.TotalPayments
	flow, ok := new(big.Int).SetString(s.activityTotals.TotalZNHBFlowWei, 10)
	s.activityMu.RUnlock()
	if !ok || flow == nil {
		flow = big.NewInt(0)
	}

	if latest == 0 || from > latest {
		return
	}

	to := latest
	if to-from+1 > uint64(explorerActivityBatchSize) {
		to = from + uint64(explorerActivityBatchSize) - 1
	}

	for height := from; height <= to; height++ {
		block, err := chain.GetBlockByHeight(height)
		if err != nil || block == nil || block.Header == nil {
			continue
		}
		blockHash, hashErr := block.Header.Hash()
		if hashErr != nil {
			continue
		}
		for _, tx := range block.Transactions {
			if tx == nil {
				continue
			}
			if isPaymentLikeType(tx.Type) {
				payments++
			}
			if assetLabel(tx.Type) != "ZNHB" {
				continue
			}
			txHashBytes, hashErr := tx.Hash()
			if hashErr != nil {
				continue
			}
			record, recErr := buildExplorerTransactionResult(tx, ensureHexPrefix(hex.EncodeToString(txHashBytes)), blockHash, height, block.Header.Timestamp)
			if recErr != nil {
				continue
			}
			if amountWei, ok := new(big.Int).SetString(record.Amount, 10); ok {
				flow.Add(flow, amountWei)
			}
		}
	}

	updated := explorerActivityTotals{
		ProcessedHeight:  to,
		TotalPayments:    payments,
		TotalZNHBFlowWei: flow.String(),
	}
	s.activityMu.Lock()
	s.activityTotals = updated
	s.activityMu.Unlock()

	if raw, err := json.Marshal(updated); err == nil {
		_ = chain.PutExplorerMeta(explorerActivityMetaKey, raw)
	}
}
