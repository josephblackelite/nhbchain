package core

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
)

// ErrLendingRefPriceStaleTimestamp indicates a lending reference price was
// submitted with a timestamp that is not strictly newer than the last
// accepted one. Lending reference prices carry no epoch -- replay protection
// is this monotonic timestamp instead -- so this is the counterpart of
// ErrBuybackRefPriceStaleEpoch: the last accepted timestamp only moves
// forward, so once a submission is at or behind it, it can never become valid
// again, and it is a permanently dead transaction that classifyProposalError
// (core/node.go) prunes. Two submissions can both pass admission (which
// simulates against committed state) while neither has landed; once the
// newer one is applied, the older one is stale. As a bare fmt.Errorf it
// would fall through to proposalDispositionAbort and, never being pruned,
// fail every block build on every validator until the mempool was cleared.
var ErrLendingRefPriceStaleTimestamp = errors.New("stale reference-price timestamp")

var (
	// ErrLendingRefPriceTimestampRange: the bundle's timestamp does not fit a signed
	// 64-bit number, which wraps negative in the signed message while the stored
	// value would stay huge (PL-EXT-13). A pure function of the payload; pruned.
	ErrLendingRefPriceTimestampRange = errors.New("reference-price timestamp does not fit a signed 64-bit number")
	// ErrLendingRefPriceTooFarAhead: the bundle is dated further after the block time
	// than LendingRefPriceMaxAheadSeconds allows (PL-EXT-13). It becomes valid when the
	// block time catches up, so it is skipped, not pruned.
	ErrLendingRefPriceTooFarAhead = errors.New("reference-price timestamp is too far ahead of the block")
	// ErrLendingRefPriceTooOld: the bundle is dated further before the block time than
	// LendingRefPriceMaxAgeSeconds allows (PL-R5T-53). The block time only grows, so it
	// can never become valid; pruned.
	ErrLendingRefPriceTooOld = errors.New("reference-price timestamp is too old for the block")
	// ErrLendingRefPriceRateTooSmall: the rate, scaled by 1e18, floors to zero, which the
	// engine reads as "no price" and values collateral 1:1 (PL-R5T-53). A pure function of
	// the payload; pruned.
	ErrLendingRefPriceRateTooSmall = errors.New("reference-price rate is below one wei per whole token")
)

// oneWholeZNHBWei is 1e18: both the ZNHB-wei scale of one whole ZNHB and the
// scale Market.OracleMedianWei is stored at (the NHB-wei value of exactly
// one whole ZNHB) -- see native/lending/engine.go's
// oracleAdjustedCollateralValue, which this transaction's output feeds.
var oneWholeZNHBWei = big.NewInt(1_000_000_000_000_000_000)

// applyLendingRefPriceTransaction handles TxTypeLendingRefPrice: a
// senderless, envelope-unsigned transaction (like TxTypeBuybackRefPrice)
// whose payload carries its own M-of-N signature bundle instead of a single
// envelope signature, verified via core/tokenomics/lendingoracle
// (domain-separated from buyback's own reference-price mechanism -- see
// that package's doc comment). Deliberately reuses the SAME
// genesis-declared buyback signer quorum (sp.buybackConfig) rather than
// provisioning a second, separate signer set: today both are the same
// single operator's keys, and splitting custody into two quorums for two
// purposes with identical trust assumptions would add real operational risk
// (twice the keys to secure, twice the rotation ceremonies) for no present
// security benefit. On success, the verified rate is written into every
// currently-configured lending market's
// OracleMedianWei/OraclePrevMedianWei/OracleUpdatedBlock
// (core/state.Manager.LendingListMarkets), since there is conceptually one
// real ZNHB/NHB rate even though the engine stores it per-market.
func (sp *StateProcessor) applyLendingRefPriceTransaction(tx *types.Transaction) error {
	if !sp.hasBuybackConfig {
		return fmt.Errorf("lendingRefPrice: reference-price signer quorum is not configured for this network")
	}
	var payload struct {
		RateNum    *big.Int
		RateDenom  *big.Int
		Timestamp  uint64
		Signatures [][]byte
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("lendingRefPrice: decode payload: %w", err)
	}
	if payload.RateNum == nil || payload.RateNum.Sign() <= 0 || payload.RateDenom == nil || payload.RateDenom.Sign() <= 0 {
		return fmt.Errorf("lendingRefPrice: rate must be a positive fraction")
	}

	manager := nhbstate.NewManager(sp.Trie)
	last, exists, err := manager.LendingRefPriceLast()
	if err != nil {
		return fmt.Errorf("lendingRefPrice: check existing record: %w", err)
	}
	if exists && payload.Timestamp <= last.Timestamp {
		return fmt.Errorf("lendingRefPrice: submitted timestamp %d is not newer than the last accepted timestamp %d: %w", payload.Timestamp, last.Timestamp, ErrLendingRefPriceStaleTimestamp)
	}

	// From the guards height the bundle is also checked against the block that
	// carries it (PL-EXT-13, PL-R5T-53); before it, nothing is, as in every earlier
	// release.
	if err := sp.checkLendingRefPriceGuards(payload.Timestamp, payload.RateNum, payload.RateDenom); err != nil {
		return err
	}

	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(payload.RateNum, payload.RateDenom),
		Timestamp: time.Unix(int64(payload.Timestamp), 0).UTC(),
	}
	signers, err := lendingoracle.VerifyReferencePrice(sp.buybackConfig.Signers, sp.buybackConfig.SignerThreshold, rp, payload.Signatures)
	if err != nil {
		return fmt.Errorf("lendingRefPrice: %w", err)
	}

	markets, err := manager.LendingListMarkets()
	if err != nil {
		return fmt.Errorf("lendingRefPrice: list markets: %w", err)
	}
	height := sp.blockHeight()
	medianWei := new(big.Int).Mul(payload.RateNum, oneWholeZNHBWei)
	medianWei.Quo(medianWei, payload.RateDenom)
	for _, market := range markets {
		if market == nil {
			continue
		}
		prev := market.OracleMedianWei
		if prev == nil {
			prev = big.NewInt(0)
		}
		market.OraclePrevMedianWei = prev
		market.OracleMedianWei = new(big.Int).Set(medianWei)
		market.OracleUpdatedBlock = height
		// The market was listed from the pool index, so it is indexed: write it
		// without scanning the index again (PL-R5T-13; the state left is identical).
		if err := manager.LendingUpdateIndexedMarket(market.PoolID, market); err != nil {
			return fmt.Errorf("lendingRefPrice: persist market %q: %w", market.PoolID, err)
		}
	}

	rec := nhbstate.LendingRefPriceRecord{
		RateNum:      new(big.Int).Set(payload.RateNum),
		RateDenom:    new(big.Int).Set(payload.RateDenom),
		Timestamp:    payload.Timestamp,
		Signers:      signers,
		AppliedBlock: height,
		MarketCount:  uint64(len(markets)),
	}
	if err := manager.LendingSetRefPriceLast(rec); err != nil {
		return fmt.Errorf("lendingRefPrice: persist last record: %w", err)
	}

	if evt := (events.LendingRefPriceRecorded{
		RateNum:     payload.RateNum,
		RateDenom:   payload.RateDenom,
		Timestamp:   payload.Timestamp,
		SignerCount: len(signers),
		MarketCount: len(markets),
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

// checkLendingRefPriceGuards refuses, from the guards height, a reference-price
// bundle that does not fit the block carrying it (ledger PL-EXT-13, PL-R5T-53):
//
//   - a timestamp above the largest signed 64-bit number, which wraps negative in
//     the signed message while the stored last timestamp would become that huge
//     value, so that no later bundle could ever be newer;
//   - a rate whose median, scaled by 1e18, floors to zero, which the engine reads as
//     "no price" and answers by valuing collateral 1:1 and skipping the deviation guard;
//   - a timestamp more than LendingRefPriceMaxAgeSeconds before the block time, so a
//     signed bundle that never landed cannot be applied, and stamped fresh, much later;
//   - a timestamp more than LendingRefPriceMaxAheadSeconds after the block time, which
//     would otherwise make every honest bundle after it stale until the clock caught up.
//
// The block time is the header timestamp of the block being applied, never a clock.
// A size of zero turns that one check off. Before the guards height nothing here applies.
func (sp *StateProcessor) checkLendingRefPriceGuards(timestamp uint64, rateNum, rateDenom *big.Int) error {
	if !sp.lendingRefPriceGuardsActive() {
		return nil
	}
	if timestamp > math.MaxInt64 {
		return fmt.Errorf("lendingRefPrice: timestamp %d: %w", timestamp, ErrLendingRefPriceTimestampRange)
	}
	median := new(big.Int).Mul(rateNum, oneWholeZNHBWei)
	median.Quo(median, rateDenom)
	if median.Sign() == 0 {
		return fmt.Errorf("lendingRefPrice: %w", ErrLendingRefPriceRateTooSmall)
	}
	blockTime := sp.blockTimestamp().Unix()
	if blockTime < 0 {
		blockTime = 0
	}
	now := uint64(blockTime)
	if maxAge := sp.lendingRefPriceMaxAge; maxAge > 0 && now > maxAge && timestamp < now-maxAge {
		return fmt.Errorf("lendingRefPrice: timestamp %d is more than %d s before the block time %d: %w", timestamp, maxAge, now, ErrLendingRefPriceTooOld)
	}
	if maxAhead := sp.lendingRefPriceMaxAhead; maxAhead > 0 && maxAhead <= math.MaxUint64-now && timestamp > now+maxAhead {
		return fmt.Errorf("lendingRefPrice: timestamp %d is more than %d s after the block time %d: %w", timestamp, maxAhead, now, ErrLendingRefPriceTooFarAhead)
	}
	return nil
}
