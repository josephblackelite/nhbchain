package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	explorerDefaultRecentBlocks        = 120
	explorerMaxRecentBlocks            = 400
	explorerHistoricalBackfillLimit    = 50000
	explorerDefaultLatestBlockCount    = 15
	explorerDefaultLatestTxCount       = 20
	// explorerActiveAddressLimit bounds both how many distinct addresses
	// materializeActiveAddresses will return AND how far the backfill loop
	// below tries before giving up -- previously the backfill's exit
	// condition was "found at least one address with activity," which on a
	// low-traffic chain (or one where the initial window's
	// isExplorerUserFacingType-flagged transactions are dominated by
	// senderless system submissions -- see TxTypeBuybackRefPrice/
	// TxTypeLendingRefPrice below) meant the Addresses tab could stop
	// after finding a SINGLE real address, even when 50000 more blocks of
	// backfill budget remained and more real addresses existed further
	// back. Using the same limit on both ends means the backfill actually
	// tries to fill a real page, not just avoid an empty one.
	explorerActiveAddressLimit = 12
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

	// The admin/treasury wallet's BuyZNHB credit (NHB paid by every ZNHB
	// buyer, see applyBuyZNHB) is a real balance mutation that never
	// appears as this address's own tx.From/tx.To -- it's a side effect
	// inside the BUYER's transaction. transactionTouchesAddress below can
	// never find it. Resolving the admin wallet once here (rather than per
	// transaction) lets the loop synthesize a second, admin-side record for
	// exactly the one querying address it's relevant to, closing a real gap
	// reported in production: "wallet correctly credits admin wallet but
	// there is no history whatsoever" for the admin side of a swap.
	adminWallet, hasAdminWallet := chain.AdminWallet()
	queryingAddressIsAdmin := hasAdminWallet && bytes.Equal(addr.Bytes(), adminWallet[:])

	// Scans backward from the chain tip, not forward from genesis: this
	// endpoint only ever needs the most recent `limit` transactions for an
	// address (both callers -- nhb_getTransactionHistory for the wallet's
	// own history view, nhb_getAddressActivity for the public explorer --
	// only display recent activity), and a forward-from-0 scan re-reads
	// every block ever produced on every single call regardless of how
	// little of it is relevant. With the chain now past 240k blocks this
	// took well over 25s and never returned -- a live, node-wide hang, not
	// just a slow UI. explorerHistoricalBackfillLimit (already used for
	// the same bounded-backward-scan tradeoff in the explorer snapshot
	// builder above) caps the worst case for an address with little or no
	// recent activity; within that cap, txCount/firstSeen/lastSeen are
	// exact, and for the common case (an active address) the early exit
	// below returns almost immediately once `limit` results are found,
	// long before the cap is ever reached. Only a genuinely dormant
	// address whose entire history sits beyond the cap sees an
	// under-counted txCount/firstSeen -- a real but bounded regression in
	// aggregate-stat completeness, accepted here because the alternative
	// is every caller hanging indefinitely on every request.
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
				// See queryingAddressIsAdmin's doc comment above: a BuyZNHB
				// transaction affects the admin wallet even though it's
				// never that transaction's own From/To.
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

// mintVoucherRecipient decodes a TxTypeMint transaction's embedded voucher
// (core.MintVoucher, JSON-encoded by encodeMintTransaction in core/mint.go)
// far enough to recover its recipient/token/amount. Unlike every other
// transaction type here, a mint's real recipient never appears in the
// standard tx.To field -- core/node.go's MintWithSignature builds the
// underlying types.Transaction without ever setting To, since the
// authorized recipient is the voucher's own signed Recipient field instead
// -- so callers that only look at tx.To/tx.From (transactionTouchesAddress,
// buildExplorerTransactionResult below) would otherwise treat every mint as
// touching no address and carrying no asset/amount at all. Mirrors
// core.MintVoucher's JSON shape locally rather than importing the
// unexported core.decodeMintTransaction/mintTransactionPayload.
func mintVoucherRecipient(tx *types.Transaction) (recipient []byte, token string, amountWei *big.Int, ok bool) {
	if tx == nil || tx.Type != types.TxTypeMint {
		return nil, "", nil, false
	}
	var payload struct {
		Voucher struct {
			Recipient string `json:"recipient"`
			Token     string `json:"token"`
			Amount    string `json:"amount"`
		} `json:"voucher"`
	}
	if err := json.Unmarshal(tx.Data, &payload); err != nil {
		return nil, "", nil, false
	}
	addr, err := crypto.DecodeAddress(strings.TrimSpace(payload.Voucher.Recipient))
	if err != nil {
		return nil, "", nil, false
	}
	amount, amountOk := new(big.Int).SetString(strings.TrimSpace(payload.Voucher.Amount), 10)
	if !amountOk {
		return nil, "", nil, false
	}
	return addr.Bytes(), strings.ToUpper(strings.TrimSpace(payload.Voucher.Token)), amount, true
}

func buildExplorerTransactionResult(tx *types.Transaction, txHash string, blockHash []byte, blockNumber uint64, timestamp int64) (*ExplorerTransactionResult, error) {
	if tx == nil {
		return nil, fmt.Errorf("transaction unavailable")
	}
	asset := assetLabel(tx.Type)
	decimals := explorerTokenDecimals
	amount := "0"
	displayAmount := "0"
	mintRecipient, mintToken, mintAmount, isMint := mintVoucherRecipient(tx)
	if isMint {
		if mintToken != "" {
			asset = mintToken
		}
		amount = mintAmount.String()
		displayAmount = formatDecimalAmount(mintAmount, decimals)
	} else if tx.Type == types.TxTypeBuyZNHB {
		// applyBuyZNHB never populates tx.Value -- the buyer's requested
		// ZNHB amount lives in the RLP-encoded payload instead. This is the
		// one field reliably recoverable for a historical BuyZNHB
		// transaction without replaying the bonding curve: the purchase
		// only ever succeeds for the full requested ZNHBAmount (no partial
		// fills), unlike the NHB side (nhbCost), which is computed on-chain
		// from cumulative sale state at execution time and isn't otherwise
		// recorded anywhere -- so it's deliberately not shown here rather
		// than approximated from MaxNHBAmount (the buyer's slippage
		// ceiling, not what they actually paid).
		var payload struct {
			ZNHBAmount   *big.Int `json:"znhbAmount"`
			MaxNHBAmount *big.Int `json:"maxNHBAmount"`
			QuoteID      string   `json:"quoteId,omitempty"`
		}
		if err := rlp.DecodeBytes(tx.Data, &payload); err == nil && payload.ZNHBAmount != nil {
			amount = payload.ZNHBAmount.String()
			displayAmount = formatDecimalAmount(payload.ZNHBAmount, decimals)
		}
	} else if tx.Value != nil {
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
	} else if isMint && len(mintRecipient) == 20 {
		// A mint has no natural sender to record as From -- it's a
		// protocol-authorized credit, not a debit from another account --
		// so From stays empty here, which is what makes the Direction
		// classification below (buildAddressActivity) correctly land on
		// "incoming" for the recipient.
		result.To = crypto.MustNewAddress(crypto.NHBPrefix, mintRecipient).String()
	}
	return result, nil
}

// selfDirectedTransactionDirection overrides the naive
// "From==queried-address => outgoing, To==queried-address => incoming"
// heuristic for transaction types where the queried address is always the
// transaction's own signer (these are self-service pool/protocol
// interactions, not payments to a third party) but where that heuristic
// gets the economic direction backwards.
//
// This is the fix for a real, live-reported bug: a successful $150 borrow
// (TxTypeLendingBorrowNHB) rendered in the wallet's own transaction detail
// view as "-150.00 NHB / Outgoing" -- because the borrower is the tx's
// From (they signed it to draw down the pool), the old heuristic called it
// outgoing even though the pool credits the borrower's NHB balance. Verified
// against each type's actual state-transition effect (see
// core/lending_native.go's apply* functions and core/state_transition.go's
// StakeClaim/StakeClaimRewards) rather than inferred from the type's name:
//
//   - LendingBorrowNHB / LendingBorrowFixedTerm: the pool pays out borrowed
//     NHB to the caller -> incoming.
//   - LendingWithdrawNHB: previously supplied NHB liquidity is paid back -> incoming.
//   - LendingWithdrawZNHB: previously deposited ZNHB collateral is paid back -> incoming.
//   - StakeClaim: a matured unbonding entry is credited into BalanceZNHB -> incoming.
//   - StakeClaimRewards: accrued staking rewards are credited into the caller's
//     balance -> incoming.
//
// LendingSupplyNHB, LendingDepositZNHB, LendingRepayNHB, and TxTypeStake are
// deliberately left out of this override: those really do debit the caller
// (NHB/ZNHB leaves their spendable balance into the pool/stake), so the
// existing From==queried-address => "outgoing" fallback already gets them
// right.
//
// TxTypeUnstake is also left out, but for a different reason: it only moves
// ZNHB from the locked "Stake" bucket into a pending-unbond entry, neither of
// which is spendable balance, so no credit or debit happens at that step (the
// real debit already happened at the original TxTypeStake, and the eventual
// credit is a separate, later TxTypeStakeClaim). Falling through to the
// default heuristic would still mislabel it "outgoing" for an operation that
// moves nothing in or out right now; that's a known, lower-priority gap this
// change does not attempt to fix (see the caller's comment for scoping).
func selfDirectedTransactionDirection(txType types.TxType) (direction string, handled bool) {
	switch txType {
	case types.TxTypeLendingBorrowNHB,
		types.TxTypeLendingBorrowFixedTerm,
		types.TxTypeLendingWithdrawNHB,
		types.TxTypeLendingWithdrawZNHB,
		types.TxTypeStakeClaim,
		types.TxTypeStakeClaimRewards:
		return "incoming", true
	}
	return "", false
}

func transactionTouchesAddress(tx *types.Transaction, address []byte) bool {
	if tx == nil || len(address) != 20 {
		return false
	}
	if recipient, _, _, ok := mintVoucherRecipient(tx); ok && bytes.Equal(recipient, address) {
		return true
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
	if len(addresses) > explorerActiveAddressLimit {
		addresses = addresses[:explorerActiveAddressLimit]
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

// buildAdminBuyZNHBCreditRecord synthesizes the admin wallet's side of a
// BuyZNHB transaction: real ZNHB left the admin wallet's balance (see
// applyBuyZNHB), but the admin wallet is never that transaction's own
// From/To, so buildExplorerTransactionResult's ordinary record (built for
// the buyer) never surfaces for the admin's own history. Deliberately shows
// the ZNHB side only (exact -- see buildExplorerTransactionResult's
// BuyZNHB doc comment), not an approximated NHB revenue figure: the real
// NHB cost is computed on-chain from the bonding curve's cumulative-sale
// state at execution time and isn't otherwise recorded anywhere retrievable
// from historical block data alone, so showing MaxNHBAmount (the buyer's
// slippage ceiling) as if it were the settled amount would be a real
// number that's actively wrong, worse for an audit trail than omitting it.
func buildAdminBuyZNHBCreditRecord(tx *types.Transaction, txHash string, blockHash []byte, blockNumber uint64, timestamp int64, adminAddress string) (*ExplorerTransactionResult, error) {
	var payload struct {
		ZNHBAmount   *big.Int `json:"znhbAmount"`
		MaxNHBAmount *big.Int `json:"maxNHBAmount"`
		QuoteID      string   `json:"quoteId,omitempty"`
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil || payload.ZNHBAmount == nil {
		return nil, fmt.Errorf("decode buyZNHB payload: %w", err)
	}
	buyer := ""
	if from, err := tx.From(); err == nil && len(from) == 20 {
		buyer = crypto.MustNewAddress(crypto.NHBPrefix, from).String()
	}
	decimals := explorerTokenDecimals
	result := &ExplorerTransactionResult{
		ID:            txHash,
		Hash:          txHash,
		Type:          formatTxType(tx.Type),
		Asset:         "ZNHB",
		Amount:        payload.ZNHBAmount.String(),
		DisplayAmount: formatDecimalAmount(payload.ZNHBAmount, decimals),
		Decimals:      decimals,
		BlockNumber:   blockNumber,
		Timestamp:     timestamp,
		Nonce:         tx.Nonce,
		GasLimit:      tx.GasLimit,
		GasPrice:      integerString(tx.GasPrice),
		Status:        "confirmed",
		From:          adminAddress,
		To:            buyer,
		Direction:     "outgoing",
	}
	if len(blockHash) > 0 {
		result.BlockHash = ensureHexPrefix(hex.EncodeToString(blockHash))
	}
	return result, nil
}

// isExplorerUserFacingType filters out transaction types that don't
// represent a real user/recipient's own money movement -- see
// types.RequiresSignature's doc comment for the authoritative "senderless"
// list (no real envelope signature, so no real tx.From()).
// TxTypeBuybackRefPrice/TxTypeLendingRefPrice are buybackd's automated,
// senderless oracle price submissions -- frequent (every refresh cycle) and
// carrying no From/To/amount, so leaving them user-facing let them flood
// the fixed-size latestTransactions window and starve out genuine activity
// (confirmed live: 20/20 recent slots were refprice submissions with real
// user transfers pushed entirely out of view). TxTypeMint and
// TxTypeSwapVoucherMint are also senderless but are deliberately NOT
// excluded here: both carry a real voucher recipient (see
// mintVoucherRecipient) who genuinely received real value, so they belong
// in the feed once correctly attributed -- only the two purely-internal
// oracle types are filtered.
func isExplorerUserFacingType(t types.TxType) bool {
	switch t {
	case types.TxTypeHeartbeat, types.TxTypeBuybackRefPrice, types.TxTypeLendingRefPrice:
		return false
	default:
		return true
	}
}
