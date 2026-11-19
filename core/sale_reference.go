package core

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/curve"
)

// The treasury sale follows the outside market (ledger PL-BC-D).
//
// The Genesis curve prices what the treasury sells from the Sale Pool ($0.05 rising to
// $1.00 over 800,000,000 ZNHB), blind to every outside price. Today nobody can sell what
// they buy at a better price elsewhere. Once ZNHB can leave the chain as a bridged token
// that is no longer true: while the market pays more than the curve asks, anyone buys
// from the treasury at the curve price, bridges and sells outside.
//
// From SaleReferenceActivationHeight (zero = off, the live value) the price of every sale
// from the pool (the direct purchase, the voucher mint's curve guard and the quote) is
//
//	max(curve cost of the tranches bought, reference price x amount)
//
// so the curve becomes the floor and the market lifts it. The reference is the same
// signed, time-averaged number the treasury buyback already takes (TxTypeBuybackRefPrice:
// an exact fraction NHB per whole ZNHB, M-of-N signatures from the genesis-declared
// signers); it is NEVER read from the native pool or any peer-to-peer price, which is
// cheap to move. While the rule is on, a reference that is missing or older than
// SaleReferenceMaxAgeSeconds PAUSES sales (it never falls back to the curve), a
// reference dated more than five minutes after the block or already older than the
// maximum age is refused, and, when SaleReferenceMaxStepBps is set, a reference that
// moves further than that from a still-fresh previous one is refused (a stale previous
// reference lets any new one in, so a stuck feed can always be replaced).
//
// Price only: no balance, supply counter or pool changes shape. A chain rule: the same
// values on every validator, above the chain tip, restarted together.

var (
	// ErrSaleReferenceUnavailable is returned by a sale while the rule is on and no fresh
	// reference price exists. Skippable: a fresh reference ends it.
	ErrSaleReferenceUnavailable = errors.New("sale: no fresh outside reference price, sales are paused")
	// ErrSaleReferenceTooFarAhead is returned for a reference dated more than five minutes
	// after the block that carries it. Skippable: block time catches up.
	ErrSaleReferenceTooFarAhead = errors.New("sale: reference price is dated too far after the block")
	// ErrSaleReferenceTooOld is returned for a reference already older than the maximum
	// age. Block time only grows, so it can never become valid.
	ErrSaleReferenceTooOld = errors.New("sale: reference price is already older than the maximum age")
	// ErrSaleReferenceStepTooLarge is returned for a reference that moves further than
	// SaleReferenceMaxStepBps from a fresh previous one.
	ErrSaleReferenceStepTooLarge = errors.New("sale: reference price moved more than the allowed step")
)

// saleReferenceMaxFutureSeconds bounds how far after the block a reference may be dated,
// so a signed timestamp cannot keep a reference fresh for longer than its maximum age.
const saleReferenceMaxFutureSeconds = 300

var weiPerWholeZNHB = new(big.Int).Exp(big.NewInt(10), big.NewInt(curve.Decimals), nil)

// saleReferenceActiveAt reports whether the rule applies at height.
func saleReferenceActiveAt(rules ValidatorRules, height uint64) bool {
	return ruleActiveAt(rules.SaleReferenceActivationHeight, height)
}

// saleReferenceAge is the age in whole seconds of a reference struck at ts, seen at now.
// A reference dated at or after now has age zero.
func saleReferenceAge(ts uint64, now time.Time) uint64 {
	n := now.Unix()
	if n <= 0 || ts > uint64(n) {
		return 0
	}
	return uint64(n) - ts
}

// saleReferenceRate returns the exact NHB-per-whole-ZNHB rate of the recorded reference
// when it is fresh at now. No reference, a zero maximum age and a stale reference all
// pause sales.
func saleReferenceRate(manager *nhbstate.Manager, rules ValidatorRules, now time.Time) (*big.Rat, error) {
	if rules.SaleReferenceMaxAgeSeconds == 0 {
		return nil, fmt.Errorf("%w: SaleReferenceMaxAgeSeconds is not set", ErrSaleReferenceUnavailable)
	}
	rec, found, err := manager.SaleReference()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: none has been recorded", ErrSaleReferenceUnavailable)
	}
	if saleReferenceAge(rec.TimestampAt, now) > rules.SaleReferenceMaxAgeSeconds {
		return nil, fmt.Errorf("%w: the last one is older than %d seconds", ErrSaleReferenceUnavailable, rules.SaleReferenceMaxAgeSeconds)
	}
	return new(big.Rat).SetFrac(rec.RateNum, rec.RateDenom), nil
}

// saleCostAt is the price, in whole NHB, of buying amountWei from the pool at a block of
// the given height and time, when the curve alone would ask curveCost. Below the rule's
// height it is curveCost, unchanged.
func saleCostAt(manager *nhbstate.Manager, rules ValidatorRules, height uint64, now time.Time, curveCost *big.Rat, amountWei *big.Int) (*big.Rat, error) {
	if !saleReferenceActiveAt(rules, height) {
		return curveCost, nil
	}
	rate, err := saleReferenceRate(manager, rules, now)
	if err != nil {
		return nil, err
	}
	refCost := new(big.Rat).Mul(rate, new(big.Rat).SetFrac(amountWei, weiPerWholeZNHB))
	if refCost.Cmp(curveCost) > 0 {
		return refCost, nil
	}
	return curveCost, nil
}

// saleCost is saleCostAt for the block being executed.
func (sp *StateProcessor) saleCost(manager *nhbstate.Manager, curveCost *big.Rat, amountWei *big.Int) (*big.Rat, error) {
	return saleCostAt(manager, sp.validatorRules, sp.blockHeight(), sp.blockTimestamp(), curveCost, amountWei)
}

// recordSaleReference checks and stores a reference price accepted by applyBuybackRefPrice
// while the rule is on. rateNum/rateDenom are positive (the caller checked); ts is the
// signed time the number was struck at.
func (sp *StateProcessor) recordSaleReference(manager *nhbstate.Manager, epoch uint64, rateNum, rateDenom *big.Int, ts uint64) error {
	rules := sp.validatorRules
	if !saleReferenceActiveAt(rules, sp.blockHeight()) {
		return nil
	}
	now := sp.blockTimestamp()
	if ts > math.MaxInt64 || int64(ts) > now.Unix()+saleReferenceMaxFutureSeconds {
		return fmt.Errorf("%w: dated %d, block time %d", ErrSaleReferenceTooFarAhead, ts, now.Unix())
	}
	if maxAge := rules.SaleReferenceMaxAgeSeconds; maxAge > 0 && saleReferenceAge(ts, now) > maxAge {
		return fmt.Errorf("%w: dated %d, block time %d, maximum age %d seconds", ErrSaleReferenceTooOld, ts, now.Unix(), maxAge)
	}
	prev, found, err := manager.SaleReference()
	if err != nil {
		return err
	}
	if found && rules.SaleReferenceMaxStepBps > 0 && saleReferenceAge(prev.TimestampAt, now) <= rules.SaleReferenceMaxAgeSeconds {
		next := new(big.Rat).SetFrac(rateNum, rateDenom)
		old := new(big.Rat).SetFrac(prev.RateNum, prev.RateDenom)
		diff := new(big.Rat).Sub(next, old)
		diff.Abs(diff)
		limit := new(big.Rat).Mul(old, new(big.Rat).SetFrac64(int64(rules.SaleReferenceMaxStepBps), 10000))
		if diff.Cmp(limit) > 0 {
			return fmt.Errorf("%w: %s to %s, limit %d basis points", ErrSaleReferenceStepTooLarge, old.FloatString(8), next.FloatString(8), rules.SaleReferenceMaxStepBps)
		}
	}
	return manager.SetSaleReference(nhbstate.SaleReferenceRecord{
		Epoch:       epoch,
		RateNum:     new(big.Int).Set(rateNum),
		RateDenom:   new(big.Int).Set(rateDenom),
		TimestampAt: ts,
	})
}
