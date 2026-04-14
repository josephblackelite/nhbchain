package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	explorerDefaultRecentBlocks        = 120
	explorerMaxRecentBlocks            = 400
	explorerHistoricalBackfillLimit    = 50000
	explorerDefaultLatestBlockCount    = 15
	explorerDefaultLatestTxCount       = 20
	explorerDefaultAddressHistoryLimit = 50
	explorerMaxAddressHistoryLimit     = 200
	explorerSeriesPointLimit           = 24
	explorerTokenDecimals              = 18
	explorerZNHBFixedSupply            = "1000000000"
)

type explorerAddressStats struct {
	address       string
	label         string
	segment       string
	txCount24h    int
	znhbInflow24h *big.Int
	balanceNHB    string
	balanceZNHB   string
}

type explorerMerchantStats struct {
	address  string
	name     string
	slug     string
	payments int
	volume   *big.Int
}

func (s *Server) handleGetExplorerSnapshot(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	recentBlocks := explorerDefaultRecentBlocks
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &recentBlocks); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "recentBlocks must be an integer", err.Error())
			return
		}
	}
	if recentBlocks <= 0 {
		recentBlocks = explorerDefaultRecentBlocks
	} else if recentBlocks > explorerMaxRecentBlocks {
		recentBlocks = explorerMaxRecentBlocks
	}

	snapshot := s.cachedExplorerSnapshot(recentBlocks)
	var err error
	if snapshot == nil {
		snapshot, err = s.buildExplorerSnapshot(recentBlocks)
		if err != nil {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to build explorer snapshot", err.Error())
			return
		}
	}
	writeResult(w, req.ID, snapshot)
}

func (s *Server) handleGetTransactionHistory(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var address string
	if err := json.Unmarshal(req.Params[0], &address); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address must be a string", err.Error())
		return
	}
	limit := explorerDefaultAddressHistoryLimit
	if len(req.Params) > 1 {
		if err := json.Unmarshal(req.Params[1], &limit); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "limit must be an integer", err.Error())
			return
		}
	}
	if limit <= 0 {
		limit = explorerDefaultAddressHistoryLimit
	} else if limit > explorerMaxAddressHistoryLimit {
		limit = explorerMaxAddressHistoryLimit
	}

	result, err := s.buildAddressActivity(address, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to resolve address history", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]any{
		"address":      result.Address,
		"transactions": result.Transactions,
	})
}

func (s *Server) handleGetAddressActivity(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var address string
	if err := json.Unmarshal(req.Params[0], &address); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address must be a string", err.Error())
		return
	}
	limit := explorerDefaultAddressHistoryLimit
	if len(req.Params) > 1 {
		if err := json.Unmarshal(req.Params[1], &limit); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "limit must be an integer", err.Error())
			return
		}
	}
	if limit <= 0 {
		limit = explorerDefaultAddressHistoryLimit
	} else if limit > explorerMaxAddressHistoryLimit {
		limit = explorerMaxAddressHistoryLimit
	}

	result, err := s.buildAddressActivity(address, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to resolve address activity", err.Error())
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleSearchExplorer(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "query parameter required", nil)
		return
	}
	var query string
	if err := json.Unmarshal(req.Params[0], &query); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "query must be a string", err.Error())
		return
	}
	query = strings.TrimSpace(query)
	if query == "" {
		writeResultAllowNil(w, req.ID, nil)
		return
	}

	result, err := s.searchExplorer(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to search explorer", err.Error())
		return
	}
	writeResultAllowNil(w, req.ID, result)
}

func (s *Server) buildExplorerSnapshot(recentBlocks int) (*ExplorerSnapshotResult, error) {
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
	totalPayments24h := 0
	totalZNHBFlow := big.NewInt(0)

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
				totalPayments24h++
			}
			if strings.EqualFold(record.Asset, "ZNHB") {
				if amountWei, ok := new(big.Int).SetString(record.Amount, 10); ok {
					blockRewardFlow.Add(blockRewardFlow, amountWei)
					totalZNHBFlow.Add(totalZNHBFlow, amountWei)
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

	if len(latestTransactions) < explorerDefaultLatestTxCount || len(addressStats) == 0 {
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
			if len(latestTransactions) >= explorerDefaultLatestTxCount && len(addressStats) > 0 {
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

	return &ExplorerSnapshotResult{
		UpdatedAt:             now.Format(time.RFC3339),
		LatestHeight:          latestHeight,
		ActiveValidators:      len(s.node.GetValidatorSet()),
		CurrentEpoch:          currentEpoch,
		CurrentTime:           now.Unix(),
		MempoolSize:           s.node.MempoolSize(),
		CurrentTps:            roundTo(estimateRecentTPS(s.node), 2),
		AverageTps24h:         averageSeriesValue(throughputHistory),
		Payments24h:           totalPayments24h,
		TotalRewards24h:       roundTo(decimalAsFloat(totalZNHBFlow, explorerTokenDecimals), 6),
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

func (s *Server) buildAddressActivity(address string, limit int) (*ExplorerAddressResult, error) {
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

	for height := uint64(0); height <= latestHeight; height++ {
		block, err := chain.GetBlockByHeight(height)
		if err != nil || block == nil || block.Header == nil {
			continue
		}
		blockHash, _ := block.Header.Hash()
		for _, tx := range block.Transactions {
			if !transactionTouchesAddress(tx, addr.Bytes()) {
				continue
			}
			if !isExplorerUserFacingType(tx.Type) {
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
			txCount++
			if firstSeen == 0 || block.Header.Timestamp < firstSeen {
				firstSeen = block.Header.Timestamp
			}
			if block.Header.Timestamp > lastSeen {
				lastSeen = block.Header.Timestamp
			}
			history = append(history, *record)
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

func (s *Server) searchExplorer(query string) (*ExplorerSearchResult, error) {
	if s == nil || s.node == nil || s.node.Chain() == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, nil
	}
	if height, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err == nil && block != nil {
			summary, buildErr := buildExplorerBlockResult(block)
			if buildErr != nil {
				return nil, buildErr
			}
			return &ExplorerSearchResult{Query: trimmed, Kind: "block", Block: summary}, nil
		}
	}
	if addr, err := crypto.DecodeAddress(trimmed); err == nil {
		activity, buildErr := s.buildAddressActivity(addr.String(), explorerDefaultAddressHistoryLimit)
		if buildErr != nil {
			return nil, buildErr
		}
		return &ExplorerSearchResult{Query: trimmed, Kind: "address", Address: activity}, nil
	}
	normalized := strings.ToLower(strings.TrimPrefix(trimmed, "0x"))
	if len(normalized) == 64 {
		tx, canonicalHash, blockHash, blockNumber, err := s.findTransaction(trimmed)
		if err != nil {
			return nil, err
		}
		if tx != nil {
			block, blockErr := s.node.Chain().GetBlockByHeight(blockNumber)
			timestamp := int64(0)
			if blockErr == nil && block != nil && block.Header != nil {
				timestamp = block.Header.Timestamp
			}
			result, buildErr := buildExplorerTransactionResult(tx, canonicalHash, blockHash, blockNumber, timestamp)
			if buildErr != nil {
				return nil, buildErr
			}
			return &ExplorerSearchResult{Query: trimmed, Kind: "transaction", Transaction: result}, nil
		}
		blockHashBytes, _ := hex.DecodeString(normalized)
		if len(blockHashBytes) > 0 {
			if block, err := s.node.Chain().GetBlockByHash(blockHashBytes); err == nil && block != nil {
				summary, buildErr := buildExplorerBlockResult(block)
				if buildErr != nil {
					return nil, buildErr
				}
				return &ExplorerSearchResult{Query: trimmed, Kind: "block", Block: summary}, nil
			}
		}
	}
	return nil, nil
}

func buildExplorerBlockResult(block *types.Block) (*ExplorerBlockResult, error) {
	if block == nil || block.Header == nil {
		return nil, fmt.Errorf("block unavailable")
	}
	hash, err := block.Header.Hash()
	if err != nil {
		return nil, err
	}
	result := &ExplorerBlockResult{
		Height:    block.Header.Height,
		Hash:      ensureHexPrefix(hex.EncodeToString(hash)),
		Timestamp: block.Header.Timestamp,
		TxCount:   len(block.Transactions),
	}
	if len(block.Header.Validator) == 20 {
		result.Validator = crypto.MustNewAddress(crypto.NHBPrefix, block.Header.Validator).String()
	}
	if len(block.Header.PrevHash) > 0 {
		result.PrevHash = ensureHexPrefix(hex.EncodeToString(block.Header.PrevHash))
	}
	if len(block.Header.StateRoot) > 0 {
		result.StateRoot = ensureHexPrefix(hex.EncodeToString(block.Header.StateRoot))
	}
	if len(block.Header.TxRoot) > 0 {
		result.TxRoot = ensureHexPrefix(hex.EncodeToString(block.Header.TxRoot))
	}
	if len(block.Header.ExecutionGraphRoot) > 0 {
		result.ExecutionGraphRoot = ensureHexPrefix(hex.EncodeToString(block.Header.ExecutionGraphRoot))
	}
	return result, nil
}

func buildExplorerTransactionResult(tx *types.Transaction, txHash string, blockHash []byte, blockNumber uint64, timestamp int64) (*ExplorerTransactionResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction unavailable")
	}
	asset := assetLabel(tx.Type)
	decimals := explorerTokenDecimals
	amount := "0"
	displayAmount := "0"
	if tx.Value != nil {
		amount = tx.Value.String()
		displayAmount = formatDecimalAmount(tx.Value, decimals)
	}
	result := &ExplorerTransactionResult{
		ID:            txHash,
		Hash:          txHash,
		Type:          formatTxType(tx.Type),
		Asset:         asset,
		Amount:        amount,
		DisplayAmount: displayAmount,
		Decimals:      decimals,
		BlockNumber:   blockNumber,
		Timestamp:     timestamp,
		Nonce:         tx.Nonce,
		GasLimit:      tx.GasLimit,
		GasPrice:      integerString(tx.GasPrice),
		Status:        "confirmed",
	}
	if len(blockHash) > 0 {
		result.BlockHash = ensureHexPrefix(hex.EncodeToString(blockHash))
	}
	if from, err := tx.From(); err == nil && len(from) == 20 {
		result.From = crypto.MustNewAddress(crypto.NHBPrefix, from).String()
	}
	if len(tx.To) == 20 {
		result.To = crypto.MustNewAddress(crypto.NHBPrefix, tx.To).String()
	}
	return result, nil
}

func transactionTouchesAddress(tx *types.Transaction, address []byte) bool {
	if tx == nil || len(address) != 20 {
		return false
	}
	if from, err := tx.From(); err == nil && len(from) == 20 && strings.EqualFold(hex.EncodeToString(from), hex.EncodeToString(address)) {
		return true
	}
	if len(tx.To) == 20 && strings.EqualFold(hex.EncodeToString(tx.To), hex.EncodeToString(address)) {
		return true
	}
	return false
}

func (s *Server) recordAddressActivity(stats map[string]*explorerAddressStats, record *ExplorerTransactionResult) {
	if record == nil {
		return
	}
	if record.From != "" {
		entry := ensureExplorerAddressStats(stats, record.From)
		entry.txCount24h++
	}
	if record.To != "" {
		entry := ensureExplorerAddressStats(stats, record.To)
		entry.txCount24h++
		if strings.EqualFold(record.Asset, "ZNHB") {
			if amount, ok := new(big.Int).SetString(record.Amount, 10); ok {
				entry.znhbInflow24h.Add(entry.znhbInflow24h, amount)
			}
		}
	}
}

func (s *Server) recordMerchantActivity(stats map[string]*explorerMerchantStats, record *ExplorerTransactionResult) {
	if record == nil || record.To == "" || !strings.EqualFold(record.Asset, "NHB") {
		return
	}
	entry, ok := stats[record.To]
	if !ok {
		entry = &explorerMerchantStats{
			address:  record.To,
			name:     record.To,
			slug:     slugifyExplorer(record.To),
			payments: 0,
			volume:   big.NewInt(0),
		}
		if addr, err := crypto.DecodeAddress(record.To); err == nil {
			if account, accountErr := s.node.GetAccount(addr.Bytes()); accountErr == nil && account != nil {
				if username := strings.TrimSpace(account.Username); username != "" {
					entry.name = username
					entry.slug = slugifyExplorer(username)
				}
			}
		}
		stats[record.To] = entry
	}
	entry.payments++
	if amount, ok := new(big.Int).SetString(record.Amount, 10); ok {
		entry.volume.Add(entry.volume, amount)
	}
}

func (s *Server) materializeActiveAddresses(stats map[string]*explorerAddressStats) []ExplorerActiveAddressResult {
	addresses := make([]*explorerAddressStats, 0, len(stats))
	for _, entry := range stats {
		if addr, err := crypto.DecodeAddress(entry.address); err == nil {
			if account, accountErr := s.node.GetAccount(addr.Bytes()); accountErr == nil && account != nil {
				entry.balanceNHB = formatDecimalAmount(account.BalanceNHB, explorerTokenDecimals)
				entry.balanceZNHB = formatDecimalAmount(account.BalanceZNHB, explorerTokenDecimals)
				entry.segment = explorerSegmentForAccount(account, s.node.GetValidatorSet(), entry.address)
				if username := strings.TrimSpace(account.Username); username != "" {
					entry.label = username
				}
			}
		}
		addresses = append(addresses, entry)
	}
	sort.Slice(addresses, func(i, j int) bool {
		if addresses[i].txCount24h == addresses[j].txCount24h {
			return addresses[i].address < addresses[j].address
		}
		return addresses[i].txCount24h > addresses[j].txCount24h
	})
	if len(addresses) > 12 {
		addresses = addresses[:12]
	}
	result := make([]ExplorerActiveAddressResult, 0, len(addresses))
	for _, entry := range addresses {
		result = append(result, ExplorerActiveAddressResult{
			Address:        entry.address,
			Label:          entry.label,
			Segment:        defaultString(entry.segment, "Account"),
			BalanceNHB:     defaultString(entry.balanceNHB, "0"),
			BalanceZNHB:    defaultString(entry.balanceZNHB, "0"),
			RewardsZNHB24h: formatDecimalAmount(entry.znhbInflow24h, explorerTokenDecimals),
			TxCount24h:     entry.txCount24h,
		})
	}
	return result
}

func (s *Server) materializeTopMerchants(stats map[string]*explorerMerchantStats) []ExplorerMerchantResult {
	merchants := make([]*explorerMerchantStats, 0, len(stats))
	for _, entry := range stats {
		merchants = append(merchants, entry)
	}
	sort.Slice(merchants, func(i, j int) bool {
		if merchants[i].payments == merchants[j].payments {
			return merchants[i].name < merchants[j].name
		}
		return merchants[i].payments > merchants[j].payments
	})
	if len(merchants) > 6 {
		merchants = merchants[:6]
	}
	result := make([]ExplorerMerchantResult, 0, len(merchants))
	for _, entry := range merchants {
		result = append(result, ExplorerMerchantResult{
			Name:        entry.name,
			Slug:        entry.slug,
			Sector:      "Settlement Endpoint",
			Address:     entry.address,
			Payments24h: entry.payments,
			VolumeNHB:   formatDecimalAmount(entry.volume, explorerTokenDecimals),
			Href:        "/explorer?q=" + entry.address,
		})
	}
	return result
}

func ensureExplorerAddressStats(stats map[string]*explorerAddressStats, address string) *explorerAddressStats {
	entry, ok := stats[address]
	if ok {
		return entry
	}
	entry = &explorerAddressStats{
		address:       address,
		txCount24h:    0,
		znhbInflow24h: big.NewInt(0),
	}
	stats[address] = entry
	return entry
}

func explorerBalancesFromAccount(account *types.Account) ExplorerAddressBalances {
	if account == nil {
		return ExplorerAddressBalances{NHB: "0", ZNHB: "0", Stake: "0", LockedZNHB: "0", PendingRewardsZNHB: "0"}
	}
	pending := big.NewInt(0)
	if account.StakingRewards.AccruedZNHB != nil {
		pending = account.StakingRewards.AccruedZNHB
	}
	return ExplorerAddressBalances{
		NHB:                formatDecimalAmount(account.BalanceNHB, explorerTokenDecimals),
		ZNHB:               formatDecimalAmount(account.BalanceZNHB, explorerTokenDecimals),
		Stake:              formatDecimalAmount(account.Stake, explorerTokenDecimals),
		LockedZNHB:         formatDecimalAmount(account.LockedZNHB, explorerTokenDecimals),
		PendingRewardsZNHB: formatDecimalAmount(pending, explorerTokenDecimals),
	}
}

func explorerSegmentForAccount(account *types.Account, validatorSet map[string]*big.Int, address string) string {
	if account == nil {
		return "Account"
	}
	if _, ok := validatorSet[address]; ok {
		return "Validator"
	}
	if account.Stake != nil && account.Stake.Sign() > 0 {
		return "Staker"
	}
	if strings.TrimSpace(account.Username) != "" {
		return "Identity"
	}
	return "Account"
}

func formatDecimalAmount(value *big.Int, decimals int) string {
	if value == nil {
		return "0"
	}
	if value.Sign() == 0 || decimals <= 0 {
		return value.String()
	}
	sign := ""
	if value.Sign() < 0 {
		sign = "-"
		value = new(big.Int).Abs(value)
	}
	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart := new(big.Int).Quo(value, base)
	fracPart := new(big.Int).Mod(value, base)
	if fracPart.Sign() == 0 {
		return sign + intPart.String()
	}
	fracStr := fracPart.String()
	if padding := decimals - len(fracStr); padding > 0 {
		fracStr = strings.Repeat("0", padding) + fracStr
	}
	fracStr = strings.TrimRight(fracStr, "0")
	if fracStr == "" {
		return sign + intPart.String()
	}
	return sign + intPart.String() + "." + fracStr
}

func integerString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}

func decimalAsFloat(value *big.Int, decimals int) float64 {
	if value == nil {
		return 0
	}
	parsed, err := strconv.ParseFloat(formatDecimalAmount(value, decimals), 64)
	if err != nil {
		return 0
	}
	return parsed
}

func roundTo(value float64, digits int) float64 {
	multiplier := mathPow10(digits)
	return float64(int64(value*multiplier+0.5)) / multiplier
}

func averageSeriesValue(series []ExplorerSeriesPoint) float64 {
	if len(series) == 0 {
		return 0
	}
	total := 0.0
	count := 0
	for _, point := range series {
		if point.Value == 0 {
			continue
		}
		total += point.Value
		count++
	}
	if count == 0 {
		return 0
	}
	return roundTo(total/float64(count), 2)
}

func trimSeriesPoints(series []ExplorerSeriesPoint) []ExplorerSeriesPoint {
	if len(series) <= explorerSeriesPointLimit {
		return series
	}
	return series[len(series)-explorerSeriesPointLimit:]
}

func slugifyExplorer(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	replacer := strings.NewReplacer(" ", "-", ".", "-", "_", "-", "/", "-", ":", "-", "@", "-")
	slug := replacer.Replace(lower)
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "account"
	}
	return slug
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mathPow10(digits int) float64 {
	result := 1.0
	for i := 0; i < digits; i++ {
		result *= 10
	}
	return result
}

func isPaymentLikeType(t types.TxType) bool {
	switch t {
	case types.TxTypeTransfer, types.TxTypePOSAuthorize, types.TxTypePOSCapture, types.TxTypePOSVoid:
		return true
	default:
		return false
	}
}

func isExplorerUserFacingType(t types.TxType) bool {
	switch t {
	case types.TxTypeHeartbeat:
		return false
	default:
		return true
	}
}
