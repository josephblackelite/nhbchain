package rpc

// Reference copies of the scans as they were at release/hardening-r1, before
// the gate, the block summaries and the authoritative transaction index. The
// differential tests run them and the current code over the same chains and
// require identical results. They are kept verbatim (comments aside) on
// purpose: do not "improve" them, or the tests stop proving anything.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func (s *Server) legacyFindTransaction(hash string) (*types.Transaction, string, []byte, uint64, error) {
	if s == nil || s.node == nil {
		return nil, "", nil, 0, fmt.Errorf("node unavailable")
	}
	normalized := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(hash), "0x"))
	if normalized == "" {
		return nil, "", nil, 0, nil
	}
	chain := s.node.Chain()
	if chain == nil {
		return nil, "", nil, 0, fmt.Errorf("chain unavailable")
	}
	hashBytes, hashDecodeErr := hex.DecodeString(normalized)
	if hashDecodeErr == nil {
		if height, ok, indexErr := chain.FindTransactionHeight(hashBytes); indexErr == nil && ok {
			if tx, canonicalHash, blockHash, found := legacyFindTransactionInBlock(chain, height, normalized); found {
				return tx, canonicalHash, blockHash, height, nil
			}
		}
	}
	latest := chain.GetHeight()
	scanned := 0
	for height := latest; scanned < explorerHistoricalBackfillLimit; height-- {
		scanned++
		if tx, canonicalHash, blockHash, found := legacyFindTransactionInBlock(chain, height, normalized); found {
			return tx, canonicalHash, blockHash, height, nil
		}
		if height == 0 {
			break
		}
	}
	return nil, "", nil, 0, nil
}

func legacyFindTransactionInBlock(chain *core.Blockchain, height uint64, normalizedHash string) (*types.Transaction, string, []byte, bool) {
	block, err := chain.GetBlockByHeight(height)
	if err != nil || block == nil || block.Header == nil {
		return nil, "", nil, false
	}
	blockHash, hashErr := block.Header.Hash()
	if hashErr != nil {
		return nil, "", nil, false
	}
	for _, tx := range block.Transactions {
		if tx == nil {
			continue
		}
		txHashBytes, err := tx.Hash()
		if err != nil {
			continue
		}
		canonical := hex.EncodeToString(txHashBytes)
		if strings.EqualFold(canonical, normalizedHash) {
			return tx, ensureHexPrefix(canonical), blockHash, true
		}
	}
	return nil, "", nil, false
}

func (s *Server) legacyBuildExplorerSnapshot(recentBlocks int) (*ExplorerSnapshotResult, error) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	chain := s.node.Chain()
	latestHeight := chain.GetHeight()
	now := time.Now().UTC()
	currentEpoch := uint64(0)
	if summary, ok := s.node.LatestEpochSummary(); ok && summary != nil {
		currentEpoch = summary.Epoch
	} else if cfg := s.node.EpochConfig(); cfg.Length > 0 {
		currentEpoch = latestHeight / cfg.Length
	}

	recent := make([]*types.Block, 0, recentBlocks)
	for i := 0; i < recentBlocks && uint64(i) <= latestHeight; i++ {
		height := latestHeight - uint64(i)
		block, err := chain.GetBlockByHeight(height)
		if err != nil || block == nil || block.Header == nil {
			continue
		}
		recent = append(recent, block)
	}
	sort.Slice(recent, func(i, j int) bool {
		return recent[i].Header.Height < recent[j].Header.Height
	})

	latestBlocks := make([]ExplorerBlockResult, 0, minInt(explorerDefaultLatestBlockCount, len(recent)))
	for i := len(recent) - 1; i >= 0 && len(latestBlocks) < explorerDefaultLatestBlockCount; i-- {
		summary, err := buildExplorerBlockResult(recent[i])
		if err != nil {
			continue
		}
		latestBlocks = append(latestBlocks, *summary)
	}

	latestTransactions := make([]ExplorerTransactionResult, 0, explorerDefaultLatestTxCount*4)
	addressStats := map[string]*explorerAddressStats{}
	merchantStats := map[string]*explorerMerchantStats{}
	throughputHistory := make([]ExplorerSeriesPoint, 0, explorerSeriesPointLimit)
	paymentsHistory := make([]ExplorerSeriesPoint, 0, explorerSeriesPointLimit)
	rewardsHistory := make([]ExplorerSeriesPoint, 0, explorerSeriesPointLimit)

	collectBlock := func(block *types.Block, blockTps float64, includeSeries bool) {
		if block == nil || block.Header == nil {
			return
		}
		blockHash, _ := block.Header.Hash()
		blockRewardFlow := big.NewInt(0)
		blockPaymentCount := 0

		for _, tx := range block.Transactions {
			txHashBytes, hashErr := tx.Hash()
			if hashErr != nil {
				continue
			}
			record, err := buildExplorerTransactionResult(tx, ensureHexPrefix(hex.EncodeToString(txHashBytes)), blockHash, block.Header.Height, block.Header.Timestamp)
			if err != nil {
				continue
			}
			if isExplorerUserFacingType(tx.Type) {
				latestTransactions = append(latestTransactions, *record)
				s.recordAddressActivity(addressStats, record)
			}
			s.recordMerchantActivity(merchantStats, record)
			if isPaymentLikeType(tx.Type) {
				blockPaymentCount++
			}
			if strings.EqualFold(record.Asset, "ZNHB") {
				if amountWei, ok := new(big.Int).SetString(record.Amount, 10); ok {
					blockRewardFlow.Add(blockRewardFlow, amountWei)
				}
			}
		}

		if includeSeries {
			timestamp := time.Unix(block.Header.Timestamp, 0).UTC().Format(time.RFC3339)
			throughputHistory = append(throughputHistory, ExplorerSeriesPoint{Timestamp: timestamp, Value: roundTo(blockTps, 2)})
			paymentsHistory = append(paymentsHistory, ExplorerSeriesPoint{Timestamp: timestamp, Payments: blockPaymentCount})
			rewardsHistory = append(rewardsHistory, ExplorerSeriesPoint{Timestamp: timestamp, Rewards: decimalAsFloat(blockRewardFlow, explorerTokenDecimals)})
		}
	}

	for idx, block := range recent {
		var blockTps float64
		if idx > 0 && recent[idx-1] != nil && recent[idx-1].Header != nil {
			delta := block.Header.Timestamp - recent[idx-1].Header.Timestamp
			if delta > 0 {
				blockTps = float64(len(block.Transactions)) / float64(delta)
			} else {
				blockTps = float64(len(block.Transactions))
			}
		} else {
			blockTps = float64(len(block.Transactions))
		}
		collectBlock(block, blockTps, true)
	}

	if len(latestTransactions) < explorerDefaultLatestTxCount || len(addressStats) < explorerActiveAddressLimit {
		var oldestHeight uint64
		if len(recent) > 0 && recent[0] != nil && recent[0].Header != nil {
			oldestHeight = recent[0].Header.Height
		}
		backfillScanned := 0
		for height := oldestHeight; height > 0 && backfillScanned < explorerHistoricalBackfillLimit; height-- {
			block, err := chain.GetBlockByHeight(height - 1)
			if err != nil || block == nil || block.Header == nil {
				backfillScanned++
				continue
			}
			collectBlock(block, 0, false)
			backfillScanned++
			if len(latestTransactions) >= explorerDefaultLatestTxCount && len(addressStats) >= explorerActiveAddressLimit {
				break
			}
		}
	}

	sort.Slice(latestTransactions, func(i, j int) bool {
		if latestTransactions[i].BlockNumber == latestTransactions[j].BlockNumber {
			return latestTransactions[i].Timestamp > latestTransactions[j].Timestamp
		}
		return latestTransactions[i].BlockNumber > latestTransactions[j].BlockNumber
	})
	if len(latestTransactions) > explorerDefaultLatestTxCount {
		latestTransactions = latestTransactions[:explorerDefaultLatestTxCount]
	}

	activeAddresses := s.materializeActiveAddresses(addressStats)
	topMerchants := s.materializeTopMerchants(merchantStats)
	allTimePayments, allTimeZNHBFlow, activityComplete := s.currentExplorerActivityTotals()

	return &ExplorerSnapshotResult{
		UpdatedAt:             now.Format(time.RFC3339),
		LatestHeight:          latestHeight,
		ActiveValidators:      len(s.node.GetValidatorSet()),
		CurrentEpoch:          currentEpoch,
		CurrentTime:           now.Unix(),
		MempoolSize:           s.node.MempoolSize(),
		CurrentTps:            roundTo(estimateRecentTPS(s.node), 2),
		AverageTps24h:         averageSeriesValue(throughputHistory),
		TotalPayments:         allTimePayments,
		TotalZNHBFlow:         roundTo(decimalAsFloat(allTimeZNHBFlow, explorerTokenDecimals), 6),
		ActivityIndexComplete: activityComplete,
		ZNHBCirculatingSupply: explorerZNHBFixedSupply,
		ThroughputHistory:     trimSeriesPoints(throughputHistory),
		PaymentsHistory:       trimSeriesPoints(paymentsHistory),
		RewardsHistory:        trimSeriesPoints(rewardsHistory),
		TopMerchants:          topMerchants,
		ActiveAddresses:       activeAddresses,
		LatestBlocks:          latestBlocks,
		LatestTransactions:    latestTransactions,
	}, nil
}

func (s *Server) legacyBuildAddressActivity(address string, limit int) (*ExplorerAddressResult, error) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	addr, err := crypto.DecodeAddress(address)
	if err != nil {
		return nil, fmt.Errorf("decode address: %w", err)
	}
	canonical := addr.String()
	account, err := s.node.GetAccount(addr.Bytes())
	if err != nil {
		return nil, fmt.Errorf("load account: %w", err)
	}

	chain := s.node.Chain()
	latestHeight := chain.GetHeight()
	history := make([]ExplorerTransactionResult, 0, limit)
	var txCount uint64
	var firstSeen int64
	var lastSeen int64

	adminWallet, hasAdminWallet := chain.AdminWallet()
	queryingAddressIsAdmin := hasAdminWallet && bytes.Equal(addr.Bytes(), adminWallet[:])

	scanned := 0
	for height := latestHeight; scanned < explorerHistoricalBackfillLimit; height-- {
		block, err := chain.GetBlockByHeight(height)
		scanned++
		if err == nil && block != nil && block.Header != nil {
			blockHash, _ := block.Header.Hash()
			for _, tx := range block.Transactions {
				if !isExplorerUserFacingType(tx.Type) {
					continue
				}
				touchesDirectly := transactionTouchesAddress(tx, addr.Bytes())
				touchesAsAdminCredit := queryingAddressIsAdmin && tx.Type == types.TxTypeBuyZNHB
				if !touchesDirectly && !touchesAsAdminCredit {
					continue
				}
				txHashBytes, hashErr := tx.Hash()
				if hashErr != nil {
					continue
				}
				txHash := ensureHexPrefix(hex.EncodeToString(txHashBytes))
				recordedOne := false
				if touchesDirectly {
					record, recErr := buildExplorerTransactionResult(tx, txHash, blockHash, height, block.Header.Timestamp)
					if recErr == nil {
						if direction, handled := selfDirectedTransactionDirection(tx.Type); handled {
							record.Direction = direction
						} else if strings.EqualFold(record.From, canonical) {
							record.Direction = "outgoing"
						} else if strings.EqualFold(record.To, canonical) {
							record.Direction = "incoming"
						}
						history = append(history, *record)
						recordedOne = true
					}
				}
				if touchesAsAdminCredit {
					if record, recErr := buildAdminBuyZNHBCreditRecord(tx, txHash, blockHash, height, block.Header.Timestamp, canonical); recErr == nil {
						history = append(history, *record)
						recordedOne = true
					}
					if debit, debitErr := buildAdminBuyZNHBDebitRecord(chain, tx, txHash, blockHash, height, block.Header.Timestamp, canonical); debitErr == nil {
						history = append(history, *debit)
						recordedOne = true
					}
				}
				if recordedOne {
					txCount++
					if firstSeen == 0 || block.Header.Timestamp < firstSeen {
						firstSeen = block.Header.Timestamp
					}
					if block.Header.Timestamp > lastSeen {
						lastSeen = block.Header.Timestamp
					}
				}
			}
		}
		if height == 0 {
			break
		}
		if len(history) >= limit {
			break
		}
	}

	sort.Slice(history, func(i, j int) bool {
		if history[i].BlockNumber == history[j].BlockNumber {
			return history[i].Timestamp > history[j].Timestamp
		}
		return history[i].BlockNumber > history[j].BlockNumber
	})
	if len(history) > limit {
		history = history[:limit]
	}

	username := ""
	label := canonical
	segment := "Account"
	balances := ExplorerAddressBalances{
		NHB:                "0",
		ZNHB:               "0",
		Stake:              "0",
		LockedZNHB:         "0",
		PendingRewardsZNHB: "0",
	}
	if account != nil {
		username = strings.TrimSpace(account.Username)
		if username != "" {
			label = username
		}
		balances = explorerBalancesFromAccount(account)
		segment = explorerSegmentForAccount(account, s.node.GetValidatorSet(), canonical)
	}

	return &ExplorerAddressResult{
		Address:      canonical,
		Username:     username,
		Label:        label,
		Segment:      segment,
		TxCount:      txCount,
		FirstSeen:    firstSeen,
		LastSeen:     lastSeen,
		Balances:     balances,
		Transactions: history,
	}, nil
}

func (s *Server) legacyComputeTxWindowStats(lookbackSeconds int64, now int64) (*txWindowStatsResult, error) {
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
		block, err := chain.GetBlockByHeight(height)
		scanned++
		result.OldestHeightScanned = height

		reachedGenesis := height == 1
		if err == nil && block != nil && block.Header != nil {
			ts := block.Header.Timestamp
			txCount := len(block.Transactions)
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
