package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// txWindowStatsMaxBlocksScanned bounds the backward block scan performed by
// nhb_txWindowStats. It is intentionally far larger than
// explorerHistoricalBackfillLimit (which exists only to keep near-tip
// explorer lookups fast by giving up early) because this endpoint answers a
// fundamentally different question -- "how many transactions actually
// happened in this calendar window" -- and giving up early here would
// silently undercount a real metric rather than merely omit a nice-to-have
// list entry. BackfillTransactionIndex (blockchain.go) already established
// that a full linear scan of every block this chain has ever produced costs
// "low single-digit seconds" at this chain's size, so a bound this size is
// cheap in practice today while still preventing a truly unbounded scan
// against a much larger future chain.
const txWindowStatsMaxBlocksScanned = 2_000_000

// txWindowStatsCacheTTL avoids re-running the scan on every rapid admin
// dashboard poll (the analytics page auto-refreshes every 30s) when the
// underlying answer plainly hasn't changed enough to matter at day-level
// granularity.
const txWindowStatsCacheTTL = 45 * time.Second

// txWindowStatsCacheMaxEntries bounds the result cache. Its key is the
// caller's lookbackSeconds, which is any positive number, so without a bound a
// client could grow it without limit by asking for a new value each time.
const txWindowStatsCacheMaxEntries = 32

type txWindowStatsParams struct {
	LookbackSeconds int64 `json:"lookbackSeconds"`
}

// txWindowStatsResult reports transaction counts for two adjacent windows
// ending now: "latest" is [now-lookbackSeconds, now], "previous" is
// [now-2*lookbackSeconds, now-lookbackSeconds). Each window's *Complete flag
// is false only if the scan hit txWindowStatsMaxBlocksScanned before fully
// accounting for that window -- callers must treat an incomplete window's
// count as a partial, not a true, total and should not present it as one.
type txWindowStatsResult struct {
	LookbackSeconds     int64   `json:"lookbackSeconds"`
	LatestCount         int     `json:"latestCount"`
	PreviousCount       int     `json:"previousCount"`
	LatestComplete      bool    `json:"latestComplete"`
	PreviousComplete    bool    `json:"previousComplete"`
	LatestTps           float64 `json:"latestTps"`
	PreviousTps         float64 `json:"previousTps"`
	BlocksScanned       int     `json:"blocksScanned"`
	OldestHeightScanned uint64  `json:"oldestHeightScanned"`
	NewestHeightScanned uint64  `json:"newestHeightScanned"`
	AsOf                int64   `json:"asOf"`
}

type txWindowStatsCacheEntry struct {
	computedAt time.Time
	result     *txWindowStatsResult
}

func (s *Server) handleTxWindowStats(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "txWindowStats requires a parameter object", nil)
		return
	}
	var params txWindowStatsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.LookbackSeconds <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "lookbackSeconds must be positive", nil)
		return
	}

	result, err := s.cachedTxWindowStats(r.Context(), params.LookbackSeconds)
	if err != nil {
		if s.writeQueryFailure(w, req.ID, req.Method, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to compute transaction window stats", err.Error())
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) cachedTxWindowStats(ctx context.Context, lookbackSeconds int64) (*txWindowStatsResult, error) {
	s.txWindowStatsMu.Lock()
	if entry, ok := s.txWindowStatsCache[lookbackSeconds]; ok && time.Since(entry.computedAt) < txWindowStatsCacheTTL {
		s.txWindowStatsMu.Unlock()
		return entry.result, nil
	}
	s.txWindowStatsMu.Unlock()

	result, err := s.computeTxWindowStats(ctx, lookbackSeconds)
	if err != nil {
		return nil, err
	}

	s.txWindowStatsMu.Lock()
	if s.txWindowStatsCache == nil {
		s.txWindowStatsCache = make(map[int64]*txWindowStatsCacheEntry)
	}
	if _, kept := s.txWindowStatsCache[lookbackSeconds]; !kept && len(s.txWindowStatsCache) >= txWindowStatsCacheMaxEntries {
		s.evictTxWindowStatsLocked(time.Now())
	}
	s.txWindowStatsCache[lookbackSeconds] = &txWindowStatsCacheEntry{computedAt: time.Now(), result: result}
	s.txWindowStatsMu.Unlock()

	return result, nil
}

// evictTxWindowStatsLocked makes room in the result cache: it drops the entries
// that are past their time to live and, if that is not enough, the oldest one.
func (s *Server) evictTxWindowStatsLocked(now time.Time) {
	var oldestKey int64
	var oldest time.Time
	first := true
	for key, entry := range s.txWindowStatsCache {
		if now.Sub(entry.computedAt) >= txWindowStatsCacheTTL {
			delete(s.txWindowStatsCache, key)
			continue
		}
		if first || entry.computedAt.Before(oldest) {
			oldestKey, oldest, first = key, entry.computedAt, false
		}
	}
	if len(s.txWindowStatsCache) >= txWindowStatsCacheMaxEntries && !first {
		delete(s.txWindowStatsCache, oldestKey)
	}
}

// computeTxWindowStats scans backward from the chain tip, bucketing every
// block's transaction count into the "latest" or "previous" window by the
// block's own committed timestamp. Blocks are strictly non-increasing in
// timestamp as height decreases, so a single backward pass suffices and the
// scan can stop as soon as a block older than the previous window's start is
// seen (or genesis is reached).
func (s *Server) computeTxWindowStats(ctx context.Context, lookbackSeconds int64) (*txWindowStatsResult, error) {
	return s.computeTxWindowStatsAt(ctx, lookbackSeconds, time.Now().UTC().Unix())
}

// computeTxWindowStatsAt is computeTxWindowStats for a given "now" (unix
// seconds), which lets a test pin the two windows down.
func (s *Server) computeTxWindowStatsAt(ctx context.Context, lookbackSeconds, now int64) (*txWindowStatsResult, error) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	chain := s.node.Chain()
	latestHeight := chain.GetHeight()
	latestCutoff := now - lookbackSeconds
	previousCutoff := now - 2*lookbackSeconds

	result := &txWindowStatsResult{
		LookbackSeconds:     lookbackSeconds,
		NewestHeightScanned: latestHeight,
		AsOf:                now,
	}

	if latestHeight == 0 {
		result.LatestComplete = true
		result.PreviousComplete = true
		return result, nil
	}

	var (
		scanned      int
		latestDone   bool
		previousDone bool
		height       = latestHeight
	)

	for height > 0 && scanned < txWindowStatsMaxBlocksScanned {
		// Only the timestamp and the transaction count of each block are used, and
		// those are what a block summary holds, so a block is read at most once
		// however many windows are asked about (see block_summary.go).
		sum, err := s.summaryAt(ctx, chain, height)
		if err != nil {
			return nil, err
		}
		scanned++
		result.OldestHeightScanned = height

		reachedGenesis := height == 1
		if sum.readable() {
			ts := sum.timestamp
			txCount := int(sum.txCount)
			switch {
			case ts >= latestCutoff:
				result.LatestCount += txCount
			case ts >= previousCutoff:
				latestDone = true
				result.PreviousCount += txCount
			default:
				latestDone = true
				previousDone = true
			}
		}
		if reachedGenesis {
			latestDone = true
			previousDone = true
		}
		if previousDone {
			break
		}
		height--
	}

	result.BlocksScanned = scanned
	result.LatestComplete = latestDone
	result.PreviousComplete = previousDone

	if result.LatestComplete {
		result.LatestTps = roundTo(float64(result.LatestCount)/float64(lookbackSeconds), 4)
	}
	if result.PreviousComplete {
		result.PreviousTps = roundTo(float64(result.PreviousCount)/float64(lookbackSeconds), 4)
	}

	return result, nil
}
