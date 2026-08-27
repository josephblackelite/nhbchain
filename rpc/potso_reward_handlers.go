package rpc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/crypto"
	"nhbchain/native/potso"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type potsoEpochInfoParams struct {
	Epoch *uint64 `json:"epoch,omitempty"`
}

type potsoEpochInfoResult struct {
	Epoch           uint64 `json:"epoch"`
	Day             string `json:"day"`
	StakeTotal      string `json:"stakeTotal"`
	EngagementTotal string `json:"engagementTotal"`
	AlphaBps        uint64 `json:"alphaBps"`
	Emission        string `json:"emission"`
	Budget          string `json:"budget"`
	TotalPaid       string `json:"totalPaid"`
	Remainder       string `json:"remainder"`
	Winners         uint64 `json:"winners"`
}

type potsoEpochPayoutsParams struct {
	Epoch  uint64 `json:"epoch"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type potsoEpochPayoutEntry struct {
	User   string `json:"user"`
	Amount string `json:"amount"`
}

type potsoEpochPayoutsResult struct {
	Epoch   uint64                  `json:"epoch"`
	Payouts []potsoEpochPayoutEntry `json:"payouts"`
}

type potsoRewardClaimParams struct {
	Epoch     uint64 `json:"epoch"`
	Address   string `json:"address"`
	Signature string `json:"signature"`
}

type potsoRewardClaimResult struct {
	Paid   bool   `json:"paid"`
	Amount string `json:"amount"`
}

type potsoRewardHistoryParams struct {
	Address string `json:"address"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type potsoRewardHistoryEntry struct {
	Epoch  uint64 `json:"epoch"`
	Amount string `json:"amount"`
	Mode   string `json:"mode"`
}

type potsoRewardHistoryResult struct {
	Address    string                    `json:"address"`
	Entries    []potsoRewardHistoryEntry `json:"entries"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}

type potsoRewardExportParams struct {
	Epoch uint64 `json:"epoch"`
}

type potsoRewardExportResult struct {
	Epoch     uint64 `json:"epoch"`
	CSVBase64 string `json:"csvBase64"`
	TotalPaid string `json:"totalPaid"`
	Winners   int    `json:"winners"`
}

func rewardClaimDigest(epoch uint64, addr string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(addr))
	payload := fmt.Sprintf("potso_reward_claim|%d|%s", epoch, normalized)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}

func (s *Server) handlePotsoEpochInfo(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var params potsoEpochInfoParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
			return
		}
	}

	var (
		epoch uint64
		have  bool
		err   error
	)
	if params.Epoch != nil {
		epoch = *params.Epoch
		have = true
	} else {
		var ok bool
		epoch, ok, err = s.node.PotsoLatestRewardEpoch()
		if err != nil {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load latest epoch", err.Error())
			return
		}
		have = ok
	}
	if !have {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch not found", nil)
		return
	}
	meta, ok, err := s.node.PotsoRewardEpochInfo(epoch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load epoch info", err.Error())
		return
	}
	if !ok || meta == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch not found", nil)
		return
	}
	result := potsoEpochInfoResult{
		Epoch:           meta.Epoch,
		Day:             meta.Day,
		StakeTotal:      bigIntString(meta.StakeTotal),
		EngagementTotal: bigIntString(meta.EngagementTotal),
		AlphaBps:        meta.AlphaBps,
		Emission:        bigIntString(meta.Emission),
		Budget:          bigIntString(meta.Budget),
		TotalPaid:       bigIntString(meta.TotalPaid),
		Remainder:       bigIntString(meta.Remainder),
		Winners:         meta.Winners,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoEpochPayouts(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "payouts requires parameter object", nil)
		return
	}
	var params potsoEpochPayoutsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	var cursorPtr *[20]byte
	trimmedCursor := strings.TrimSpace(params.Cursor)
	if trimmedCursor != "" {
		addr, err := decodeBech32(trimmedCursor)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid cursor", err.Error())
			return
		}
		cursorPtr = &addr
	}
	payouts, err := s.node.PotsoRewardEpochPayouts(params.Epoch, cursorPtr, params.Limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load payouts", err.Error())
		return
	}
	result := potsoEpochPayoutsResult{
		Epoch:   params.Epoch,
		Payouts: make([]potsoEpochPayoutEntry, len(payouts)),
	}
	for i, payout := range payouts {
		user := crypto.MustNewAddress(crypto.NHBPrefix, payout.Address[:]).String()
		result.Payouts[i] = potsoEpochPayoutEntry{
			User:   user,
			Amount: bigIntString(payout.Amount),
		}
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoRewardClaim(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "claim requires parameter object", nil)
		return
	}
	var params potsoRewardClaimParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.Address == "" || params.Signature == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address and signature are required", nil)
		return
	}
	addr, err := decodeBech32(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	sig, err := decodeHexBytes(params.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	if len(sig) != 65 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature must be 65 bytes", nil)
		return
	}
	digest := rewardClaimDigest(params.Epoch, params.Address)
	pubKey, err := ethcrypto.SigToPub(digest, sig)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if !strings.EqualFold(recovered.Hex()[2:], hex.EncodeToString(addr[:])) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature does not match address", nil)
		return
	}

	paid, amount, claimErr := s.node.PotsoRewardClaim(params.Epoch, addr)
	if claimErr != nil {
		switch {
		case errors.Is(claimErr, potso.ErrRewardNotFound):
			writeError(w, http.StatusNotFound, req.ID, codeServerError, "reward not found", nil)
		case errors.Is(claimErr, potso.ErrClaimingDisabled):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "claiming disabled", nil)
		case errors.Is(claimErr, potso.ErrInsufficientTreasury):
			writeError(w, http.StatusConflict, req.ID, codeServerError, "INSUFFICIENT_TREASURY", nil)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to claim reward", claimErr.Error())
		}
		return
	}
	result := potsoRewardClaimResult{Paid: paid, Amount: bigIntString(amount)}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoRewardsHistory(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "history requires parameter object", nil)
		return
	}
	var params potsoRewardHistoryParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.Address == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	entries, nextCursor, histErr := s.node.PotsoRewardsHistory(addr, params.Cursor, params.Limit)
	if histErr != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load history", histErr.Error())
		return
	}
	result := potsoRewardHistoryResult{Address: params.Address, Entries: make([]potsoRewardHistoryEntry, len(entries)), NextCursor: nextCursor}
	for i, entry := range entries {
		amount := "0"
		if entry.Amount != nil {
			amount = entry.Amount.String()
		}
		result.Entries[i] = potsoRewardHistoryEntry{Epoch: entry.Epoch, Amount: amount, Mode: string(entry.Mode.Normalise())}
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoExportEpoch(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "export requires parameter object", nil)
		return
	}
	var params potsoRewardExportParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	data, total, winners, err := s.node.PotsoExportEpoch(params.Epoch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to export epoch", err.Error())
		return
	}
	result := potsoRewardExportResult{
		Epoch:     params.Epoch,
		CSVBase64: base64.StdEncoding.EncodeToString(data),
		TotalPaid: bigIntString(total),
		Winners:   winners,
	}
	writeResult(w, req.ID, result)
}

// potsoRewardsOutflowMaxEpochsScanned bounds the backward epoch scan
// performed by potso_rewards_outflow. Epochs settle roughly every 20-30
// minutes (EpochLengthBlocks in config.toml), so even several years of
// history is a small number of epochs compared to this bound -- it exists
// only to stop a pathological scan, not to trade accuracy for speed the way
// explorerHistoricalBackfillLimit does for near-tip explorer lookups.
const potsoRewardsOutflowMaxEpochsScanned = 2_000_000

type potsoRewardsOutflowParams struct {
	LookbackDays int64 `json:"lookbackDays"`
}

// potsoRewardsOutflowResult reports network-wide POTSO reward payouts
// (RewardEpochMeta.TotalPaid, summed across every settled epoch) for two
// adjacent day windows ending today: "latest" covers the most recent
// lookbackDays calendar days, "previous" the lookbackDays before that. Each
// window's *Complete flag is false only if the scan hit
// potsoRewardsOutflowMaxEpochsScanned before fully accounting for that
// window -- callers must treat an incomplete window's total as a partial,
// not a true, sum.
type potsoRewardsOutflowResult struct {
	LookbackDays        int64  `json:"lookbackDays"`
	LatestTotalPaid     string `json:"latestTotalPaid"`
	PreviousTotalPaid   string `json:"previousTotalPaid"`
	LatestEpochs        int    `json:"latestEpochs"`
	PreviousEpochs      int    `json:"previousEpochs"`
	LatestComplete      bool   `json:"latestComplete"`
	PreviousComplete    bool   `json:"previousComplete"`
	EpochsScanned       int    `json:"epochsScanned"`
	OldestEpochScanned  uint64 `json:"oldestEpochScanned"`
	NewestEpochScanned  uint64 `json:"newestEpochScanned"`
	AsOfDay             string `json:"asOfDay"`
}

func (s *Server) handlePotsoRewardsOutflow(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "rewards outflow requires a parameter object", nil)
		return
	}
	var params potsoRewardsOutflowParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	if params.LookbackDays <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "lookbackDays must be positive", nil)
		return
	}

	result, err := s.computePotsoRewardsOutflow(params.LookbackDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to compute rewards outflow", err.Error())
		return
	}
	writeResult(w, req.ID, result)
}

// computePotsoRewardsOutflow walks epochs backward from the latest settled
// one, bucketing each epoch's TotalPaid into the "latest" or "previous"
// window by the epoch's own Day label. Epoch numbers increase monotonically
// with time and Day is derived from the settlement-time timestamp (see
// maybeProcessPotsoRewards), so Day is non-decreasing as epoch number
// increases -- a single backward pass suffices, stopping as soon as an
// epoch older than the previous window's start is seen (or epoch 0 is
// reached).
func (s *Server) computePotsoRewardsOutflow(lookbackDays int64) (*potsoRewardsOutflowResult, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("node unavailable")
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	result := &potsoRewardsOutflowResult{
		LookbackDays:      lookbackDays,
		LatestTotalPaid:   "0",
		PreviousTotalPaid: "0",
		AsOfDay:           today.Format(potso.DayFormat),
	}

	latestEpoch, ok, err := s.node.PotsoLatestRewardEpoch()
	if err != nil {
		return nil, err
	}
	if !ok {
		// No epoch has ever settled -- both windows are trivially complete
		// (there is nothing to scan) and correctly zero.
		result.LatestComplete = true
		result.PreviousComplete = true
		return result, nil
	}
	result.NewestEpochScanned = latestEpoch

	latestTotal := big.NewInt(0)
	previousTotal := big.NewInt(0)
	var (
		scanned      int
		latestDone   bool
		previousDone bool
		epoch        = latestEpoch
	)

	for scanned < potsoRewardsOutflowMaxEpochsScanned {
		meta, ok, metaErr := s.node.PotsoRewardEpochInfo(epoch)
		scanned++
		result.OldestEpochScanned = epoch

		reachedFirstEpoch := epoch == 0
		if metaErr == nil && ok && meta != nil {
			if day, parseErr := time.Parse(potso.DayFormat, meta.Day); parseErr == nil {
				daysAgo := int64(today.Sub(day).Hours() / 24)
				paid := meta.TotalPaid
				switch {
				case daysAgo < lookbackDays:
					if paid != nil {
						latestTotal.Add(latestTotal, paid)
					}
					result.LatestEpochs++
				case daysAgo < 2*lookbackDays:
					latestDone = true
					if paid != nil {
						previousTotal.Add(previousTotal, paid)
					}
					result.PreviousEpochs++
				default:
					latestDone = true
					previousDone = true
				}
			}
		}
		if reachedFirstEpoch {
			latestDone = true
			previousDone = true
		}
		if previousDone {
			break
		}
		epoch--
	}

	result.EpochsScanned = scanned
	result.LatestComplete = latestDone
	result.PreviousComplete = previousDone
	result.LatestTotalPaid = latestTotal.String()
	result.PreviousTotalPaid = previousTotal.String()
	return result, nil
}

func bigIntString(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}
