package rpc

import (
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

const (
	validatorSetDefaultLimit = 100
	validatorSetMaxLimit     = 500
)

// handleGetValidatorSet backs nhb_getValidatorSet([offset, limit]). Both
// params are optional (offset defaults to 0, limit to
// validatorSetDefaultLimit, capped at validatorSetMaxLimit) so existing
// callers that pass no params keep working unchanged for any validator set
// small enough to fit in one default-sized page -- true of this network
// today, but the offset/limit contract exists specifically so a much larger
// future validator set never forces one unbounded response. Results are
// sorted by address so pagination is stable across calls even though
// Node.GetValidatorSet's underlying map has no inherent order.
func (s *Server) handleGetValidatorSet(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	offset := 0
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &offset); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "offset must be an integer", err.Error())
			return
		}
	}
	if offset < 0 {
		offset = 0
	}
	limit := validatorSetDefaultLimit
	if len(req.Params) > 1 {
		if err := json.Unmarshal(req.Params[1], &limit); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "limit must be an integer", err.Error())
			return
		}
	}
	if limit <= 0 {
		limit = validatorSetDefaultLimit
	} else if limit > validatorSetMaxLimit {
		limit = validatorSetMaxLimit
	}

	validators := s.node.GetValidatorSet()
	type vSet struct {
		Address string `json:"address"`
		Stake   string `json:"stake"`
	}
	all := make([]vSet, 0, len(validators))
	for addr, stake := range validators {
		// Attempting to normalize the key depending on how string(addr) is stored.
		// If it's pure bytes, hex encode it.
		encoded := addr
		if !strings.HasPrefix(encoded, "0x") {
			encoded = common.BytesToAddress([]byte(addr)).Hex()
		}
		stakeStr := "0"
		if stake != nil {
			stakeStr = stake.String()
		}
		all = append(all, vSet{Address: encoded, Stake: stakeStr})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Address < all[j].Address })

	totalCount := len(all)
	page := []vSet{}
	if offset < totalCount {
		end := offset + limit
		if end > totalCount {
			end = totalCount
		}
		page = all[offset:end]
	}

	writeResult(w, req.ID, map[string]any{
		"validators": page,
		"totalCount": totalCount,
		"offset":     offset,
		"limit":      limit,
		"hasMore":    offset+len(page) < totalCount,
		"timestamp":  time.Now().Unix(),
	})
}

func (s *Server) handleGetValidatorInfo(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address", nil)
		return
	}
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", nil)
		return
	}
	addr := common.HexToAddress(addrStr)
	acc, err := s.node.GetAccount(addr.Bytes())
	if err != nil || acc == nil {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "validator not found", nil)
		return
	}
	writeResult(w, req.ID, map[string]any{
		"address":         addr.Hex(),
		"stake":           acc.Stake.String(),
		"engagementScore": acc.EngagementScore,
	})
}

func (s *Server) handleGetNetworkStats(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	currentEpoch := uint64(0)
	if summary, ok := s.node.LatestEpochSummary(); ok && summary != nil {
		currentEpoch = summary.Epoch
	} else if cfg := s.node.EpochConfig(); cfg.Length > 0 {
		currentEpoch = s.node.GetHeight() / cfg.Length
	}

	writeResult(w, req.ID, map[string]any{
		"activeValidators": len(s.node.GetValidatorSet()),
		"currentEpoch":     currentEpoch,
		"currentTime":      time.Now().Unix(),
		"mempoolSize":      s.node.MempoolSize(),
		"tps":              estimateRecentTPS(s.node),
	})
}

// handleGetTotalSupply backs nhb_getTotalSupply([symbol]) -> { symbol,
// totalSupplyWei }. symbol is optional and defaults to "NHB". Reads the
// incrementally-tracked token supply counter (state.Manager.TokenSupply)
// that mint/redeem/burn state transitions already maintain -- see
// core/state_transition.go's applyRedeemNHB and handleMintWithSig, which
// call AdjustTokenSupply on every burn/mint. Unlike ZNHB's circulating
// supply (a fixed genesis constant -- explorerZNHBFixedSupply), NHB's is
// mint/burn-based, so this is the one live source of truth for it. Uses
// WithStateView (a disposable state copy), never WithState, so this
// read-only query can never perturb the live pending state root.
func (s *Server) handleGetTotalSupply(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	symbol := "NHB"
	if len(req.Params) > 0 {
		var provided string
		if err := json.Unmarshal(req.Params[0], &provided); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid symbol parameter", err.Error())
			return
		}
		if trimmed := strings.TrimSpace(provided); trimmed != "" {
			symbol = trimmed
		}
	}

	var total *big.Int
	err := s.node.WithStateView(func(manager *nhbstate.Manager) error {
		supply, supplyErr := manager.TokenSupply(symbol)
		if supplyErr != nil {
			return supplyErr
		}
		total = supply
		return nil
	})
	if err != nil {
		slog.Error("rpc: failed to load token supply",
			slog.String("method", "nhb_getTotalSupply"),
			slog.String("symbol", symbol),
			slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load token supply", nil)
		return
	}
	if total == nil {
		total = big.NewInt(0)
	}

	writeResult(w, req.ID, map[string]any{
		"symbol":         strings.ToUpper(strings.TrimSpace(symbol)),
		"totalSupplyWei": total.String(),
	})
}

func (s *Server) handleGetLoyaltyBudgetStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	// Twap deviation parameters for the loyalty platform.
	writeResult(w, req.ID, map[string]any{
		"twapScalingFactor": "1.0", // 100% emission baseline
		"budgetRemaining":   "1000000000000000000000",
		"resetAt":           time.Now().Add(24 * time.Hour).Unix(),
	})
}

func (s *Server) handleGetOwnerWalletStats(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	cfg := s.node.GlobalConfig()
	ownerWallet := strings.TrimSpace(cfg.Fees.OwnerWallet)
	balances := map[string]string{
		"NHB":  "0",
		"ZNHB": "0",
	}
	feeAccrualByAsset := map[string]string{}
	totalFeeAccrual := big.NewInt(0)

	var ownerHex string
	if ownerWallet != "" {
		if addr, err := crypto.DecodeAddress(ownerWallet); err == nil {
			ownerHex = strings.ToLower(common.BytesToAddress(addr.Bytes()).Hex())
			if account, err := s.node.GetAccount(addr.Bytes()); err == nil && account != nil {
				if account.BalanceNHB != nil {
					balances["NHB"] = account.BalanceNHB.String()
				}
				if account.BalanceZNHB != nil {
					balances["ZNHB"] = account.BalanceZNHB.String()
				}
			}
		}
	}

	if ownerHex != "" {
		perAsset := make(map[string]*big.Int)
		for _, evt := range s.node.Events() {
			if evt.Type != events.TypeFeeApplied {
				continue
			}
			if normalizeOwnerWallet(evt.Attributes["ownerWallet"]) != ownerHex {
				continue
			}
			fee, ok := new(big.Int).SetString(strings.TrimSpace(evt.Attributes["feeWei"]), 10)
			if !ok {
				continue
			}
			asset := strings.ToUpper(strings.TrimSpace(evt.Attributes["asset"]))
			if asset == "" {
				asset = "UNKNOWN"
			}
			if perAsset[asset] == nil {
				perAsset[asset] = big.NewInt(0)
			}
			perAsset[asset].Add(perAsset[asset], fee)
			totalFeeAccrual.Add(totalFeeAccrual, fee)
		}
		for asset, amount := range perAsset {
			feeAccrualByAsset[asset] = amount.String()
		}
	}

	writeResult(w, req.ID, map[string]any{
		"ownerWallet":       ownerWallet,
		"treasuryBalance":   balances["NHB"],
		"balances":          balances,
		"feeAccrual":        totalFeeAccrual.String(),
		"feeAccrualByAsset": feeAccrualByAsset,
	})
}

func (s *Server) handleGetSlashingEvents(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	slashingEvents := make([]map[string]string, 0)
	for _, evt := range s.node.Events() {
		if evt.Type != events.TypePotsoPenaltyApplied {
			continue
		}
		attrs := make(map[string]string, len(evt.Attributes))
		for key, value := range evt.Attributes {
			attrs[key] = value
		}
		slashingEvents = append(slashingEvents, attrs)
	}
	writeResult(w, req.ID, map[string]any{
		"events":     slashingEvents,
		"totalCount": len(slashingEvents),
	})
}

func estimateRecentTPS(node *core.Node) float64 {
	if node == nil || node.Chain() == nil {
		return 0
	}
	chain := node.Chain()
	height := chain.GetHeight()
	if height == 0 {
		return 0
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
		block, err := chain.GetBlockByHeight(h)
		if err != nil || block == nil || block.Header == nil {
			continue
		}
		ts := block.Header.Timestamp
		if firstTime < 0 || ts < firstTime {
			firstTime = ts
		}
		if ts > lastTime {
			lastTime = ts
		}
		txCount += len(block.Transactions)
	}
	if txCount == 0 {
		return 0
	}
	if firstTime < 0 || lastTime <= firstTime {
		return float64(txCount)
	}
	return float64(txCount) / float64(lastTime-firstTime+1)
}

func normalizeOwnerWallet(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "0x") {
		trimmed = "0x" + trimmed
	}
	return strings.ToLower(trimmed)
}
