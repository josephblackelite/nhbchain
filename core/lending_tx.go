package core

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
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
		return fmt.Errorf("lendingRefPrice: submitted timestamp %d is not newer than the last accepted timestamp %d", payload.Timestamp, last.Timestamp)
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
		if err := manager.LendingPutMarket(market.PoolID, market); err != nil {
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
