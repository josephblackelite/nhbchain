package core

import (
	"fmt"
	"math/big"
	"strconv"

	"nhbchain/core/epoch"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/native/governance"
)

// effectiveBuybackConfig returns the buyback engine's currently-active
// parameters: fee_share_bps/discount_bps/safety_margin_bps read from the
// governance param store when a policy.buybackParams proposal has set them,
// falling back to the genesis-configured defaults (sp.buybackConfig)
// otherwise. Signers/SignerThreshold are never read from the param store --
// they are cloned straight from the genesis-immutable in-memory config,
// since no governance proposal kind is ever allowed to touch them.
func (sp *StateProcessor) effectiveBuybackConfig(manager *nhbstate.Manager) buyback.Config {
	cfg := sp.buybackConfig.Clone()
	if v, ok := readGovernedBps(manager, governance.ParamKeyBuybackFeeShareBps); ok {
		cfg.FeeShareBps = v
	}
	if v, ok := readGovernedBps(manager, governance.ParamKeyBuybackDiscountBps); ok {
		cfg.DiscountBps = v
	}
	if v, ok := readGovernedBps(manager, governance.ParamKeyBuybackSafetyMarginBps); ok {
		cfg.SafetyMarginBps = v
	}
	return cfg
}

func readGovernedBps(manager *nhbstate.Manager, key string) (uint32, bool) {
	raw, ok, err := manager.ParamStoreGet(key)
	if err != nil || !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(string(raw), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(parsed), true
}

// currentBuybackEpoch returns the buyback engine's epoch bucket for the
// current block height: the epoch number that finalizeEpoch will compute
// (height/epochConfig.Length) the next time it fires. Asks and reference
// prices submitted at any height within an epoch settle at that same
// epoch's finalization, never the one after -- ceil(height/length) lands on
// the same number finalizeEpoch computes (a plain floor division) exactly
// at the boundary height, and on that same number for every height leading
// up to it.
func (sp *StateProcessor) currentBuybackEpoch() (uint64, bool) {
	length := sp.epochConfig.Length
	if length == 0 {
		return 0, false
	}
	height := sp.blockHeight()
	if height == 0 {
		return 0, false
	}
	return (height + length - 1) / length, true
}

// currentZNHBCurvePrice returns the Genesis Treasury Distribution Curve's
// exact spot price at the current cumulative-sold position -- the same
// computation node.go's GetZNHBTokenomicsState RPC handler exposes
// read-only, reused here as the settlement engine's own input.
func (sp *StateProcessor) currentZNHBCurvePrice(manager *nhbstate.Manager) (*big.Rat, error) {
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		return nil, fmt.Errorf("buyback: load cumulative sale distributed: %w", err)
	}
	params := curve.Default()
	if cumulative.Cmp(params.SalePoolCapWei()) >= 0 {
		return params.TerminalPrice(), nil
	}
	idx := params.TrancheIndexFor(cumulative)
	price, err := params.TranchePrice(idx)
	if err != nil {
		return nil, fmt.Errorf("buyback: compute current tranche price: %w", err)
	}
	return price, nil
}

// settleBuybackEpoch runs the treasury buyback engine's settlement for the
// epoch snapshot's epoch number: fills pending market asks pro-rata against
// the accrued NHB budget (never above buyback.MaxBuybackPrice), recycles
// bought-back ZNHB into the Sale Pool (never burns it), and refunds
// whatever wasn't filled. A no-op unless a real buyback signer quorum was
// configured at genesis (hasBuybackConfig) -- so it changes nothing for any
// node that hasn't opted in. Called from finalizeEpoch (core/epochs.go),
// once per epoch, right alongside reward settlement.
func (sp *StateProcessor) settleBuybackEpoch(snapshot epoch.Snapshot) error {
	if !sp.hasBuybackConfig || sp.buybackAccrualAddr.Bytes() == nil {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	epochNumber := snapshot.Epoch
	asks, err := manager.BuybackAsksForEpoch(epochNumber)
	if err != nil {
		return err
	}
	if len(asks) == 0 {
		return nil
	}

	refRecord, hasRef, err := manager.BuybackRefPriceForEpoch(epochNumber)
	if err != nil {
		return fmt.Errorf("buyback: load reference price: %w", err)
	}

	accrualAcc, err := sp.getAccount(sp.buybackAccrualAddr.Bytes())
	if err != nil {
		return fmt.Errorf("buyback: load accrual account: %w", err)
	}
	if accrualAcc.BalanceNHB == nil {
		accrualAcc.BalanceNHB = big.NewInt(0)
	}
	if accrualAcc.BalanceZNHB == nil {
		accrualAcc.BalanceZNHB = big.NewInt(0)
	}

	buybackAsks := make([]buyback.Ask, 0, len(asks))
	totalAskedZNHB := big.NewInt(0)
	for _, a := range asks {
		buybackAsks = append(buybackAsks, buyback.Ask{Seller: a.Seller, AmountWei: a.AmountWei})
		if a.AmountWei != nil {
			totalAskedZNHB.Add(totalAskedZNHB, a.AmountWei)
		}
	}

	var fills []buyback.Fill
	var clearingPrice *big.Rat
	if !hasRef {
		// No verified reference price was signed for this epoch -- the
		// treasury cannot safely compute a max price, so no purchase
		// happens and every seller gets their escrowed ZNHB back in full.
		fills = make([]buyback.Fill, 0, len(buybackAsks))
		for _, a := range buybackAsks {
			fills = append(fills, buyback.Fill{Seller: a.Seller, ZNHBFilled: big.NewInt(0), NHBPaid: big.NewInt(0), ZNHBRefunded: new(big.Int).Set(a.AmountWei)})
		}
	} else {
		curvePrice, err := sp.currentZNHBCurvePrice(manager)
		if err != nil {
			return err
		}
		refPriceRat := new(big.Rat).SetFrac(refRecord.RateNum, refRecord.RateDenom)
		cfg := sp.effectiveBuybackConfig(manager)
		maxPrice, err := buyback.MaxBuybackPrice(curvePrice, refPriceRat, cfg.DiscountBps, cfg.SafetyMarginBps)
		if err != nil {
			return fmt.Errorf("buyback: compute max price: %w", err)
		}
		clearingPrice = maxPrice
		budget := new(big.Int).Set(accrualAcc.BalanceNHB)
		fills, err = buyback.FillAsksProRata(buybackAsks, budget, maxPrice)
		if err != nil {
			return fmt.Errorf("buyback: fill asks: %w", err)
		}
	}

	totalFilledZNHB := big.NewInt(0)
	totalPaidNHB := big.NewInt(0)
	for _, fill := range fills {
		sellerAcc, err := sp.getAccount(fill.Seller[:])
		if err != nil {
			return fmt.Errorf("buyback: load seller %x: %w", fill.Seller, err)
		}
		if fill.NHBPaid != nil && fill.NHBPaid.Sign() > 0 {
			if sellerAcc.BalanceNHB == nil {
				sellerAcc.BalanceNHB = big.NewInt(0)
			}
			sellerAcc.BalanceNHB = new(big.Int).Add(sellerAcc.BalanceNHB, fill.NHBPaid)
			totalPaidNHB.Add(totalPaidNHB, fill.NHBPaid)
		}
		if fill.ZNHBRefunded != nil && fill.ZNHBRefunded.Sign() > 0 {
			if sellerAcc.BalanceZNHB == nil {
				sellerAcc.BalanceZNHB = big.NewInt(0)
			}
			sellerAcc.BalanceZNHB = new(big.Int).Add(sellerAcc.BalanceZNHB, fill.ZNHBRefunded)
		}
		if err := sp.setAccount(fill.Seller[:], sellerAcc); err != nil {
			return fmt.Errorf("buyback: persist seller %x: %w", fill.Seller, err)
		}
		if fill.ZNHBFilled != nil {
			totalFilledZNHB.Add(totalFilledZNHB, fill.ZNHBFilled)
		}
	}

	// The escrow account held every seller's full asked ZNHB amount. Every
	// wei of it leaves this function either as a refund (credited above) or
	// as a filled sale (recycled into the Sale Pool below) -- so the escrow
	// account's ZNHB balance drops by exactly the sum of every ask, and the
	// NHB balance drops by exactly what was paid out.
	if accrualAcc.BalanceZNHB.Cmp(totalAskedZNHB) < 0 {
		return fmt.Errorf("buyback: internal error: escrow ZNHB balance %s less than total asked %s", accrualAcc.BalanceZNHB, totalAskedZNHB)
	}
	accrualAcc.BalanceZNHB = new(big.Int).Sub(accrualAcc.BalanceZNHB, totalAskedZNHB)
	if accrualAcc.BalanceNHB.Cmp(totalPaidNHB) < 0 {
		return fmt.Errorf("buyback: internal error: accrual NHB balance %s less than total paid %s", accrualAcc.BalanceNHB, totalPaidNHB)
	}
	accrualAcc.BalanceNHB = new(big.Int).Sub(accrualAcc.BalanceNHB, totalPaidNHB)
	if err := sp.setAccount(sp.buybackAccrualAddr.Bytes(), accrualAcc); err != nil {
		return fmt.Errorf("buyback: persist accrual account: %w", err)
	}
	if err := manager.ZNHBSetBuybackAccrualBalance(new(big.Int).Set(accrualAcc.BalanceNHB)); err != nil {
		return fmt.Errorf("buyback: update accrual ledger: %w", err)
	}

	if totalFilledZNHB.Sign() > 0 {
		// Recycling means the bought-back ZNHB rejoins the treasury's own
		// sellable inventory: it must actually move into the admin wallet's
		// real ZNHB balance, not just the Sale Pool sub-ledger counter, or
		// CheckZNHBSupplyInvariant (SalePoolBalance + RewardPoolBalance ==
		// adminAccount.BalanceZNHB) would fail on the very next block.
		if !sp.hasAdminWallet {
			return fmt.Errorf("buyback: cannot recycle filled ZNHB into the Sale Pool -- no admin wallet configured")
		}
		adminAccount, err := sp.getAccount(sp.adminWallet[:])
		if err != nil {
			return fmt.Errorf("buyback: load admin wallet: %w", err)
		}
		if adminAccount.BalanceZNHB == nil {
			adminAccount.BalanceZNHB = big.NewInt(0)
		}
		adminAccount.BalanceZNHB = new(big.Int).Add(adminAccount.BalanceZNHB, totalFilledZNHB)
		if err := sp.setAccount(sp.adminWallet[:], adminAccount); err != nil {
			return fmt.Errorf("buyback: persist admin wallet: %w", err)
		}

		salePoolBalance, err := manager.ZNHBSalePoolBalance()
		if err != nil {
			return fmt.Errorf("buyback: load sale pool balance: %w", err)
		}
		if err := manager.ZNHBSetSalePoolBalance(new(big.Int).Add(salePoolBalance, totalFilledZNHB)); err != nil {
			return fmt.Errorf("buyback: recycle into sale pool: %w", err)
		}

		cumulative, err := manager.ZNHBCumulativeSaleDistributed()
		if err != nil {
			return fmt.Errorf("buyback: load cumulative sale distributed: %w", err)
		}
		newCumulative := new(big.Int).Sub(cumulative, totalFilledZNHB)
		if newCumulative.Sign() < 0 {
			newCumulative = big.NewInt(0)
		}
		if err := manager.ZNHBSetCumulativeSaleDistributed(newCumulative); err != nil {
			return fmt.Errorf("buyback: decrement cumulative sale distributed: %w", err)
		}
	}

	if err := manager.BuybackClearAsksForEpoch(epochNumber); err != nil {
		return fmt.Errorf("buyback: clear settled asks: %w", err)
	}
	if hasRef {
		if err := manager.BuybackClearRefPriceForEpoch(epochNumber); err != nil {
			return fmt.Errorf("buyback: clear settled reference price: %w", err)
		}
	}

	settled := events.BuybackEpochSettled{
		Epoch:              epochNumber,
		AsksFilled:         uint64(len(fills)),
		TotalZNHBFilled:    new(big.Int).Set(totalFilledZNHB),
		TotalNHBPaid:       new(big.Int).Set(totalPaidNHB),
		ReferencePriceUsed: hasRef,
	}
	if clearingPrice != nil {
		settled.ClearingPrice = clearingPrice.FloatString(curve.Decimals)
	}
	if evt := settled.Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}
