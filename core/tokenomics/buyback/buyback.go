// Package buyback implements the pure math and signature verification behind
// NHBCoin's treasury ZNHB buyback: a per-epoch, budget-capped repurchase of
// ZNHB from willing sellers, funded by a share of NHB fee revenue
// (core/state_transition.go's applyTransactionFee) and settled by
// core/buyback_settlement.go. Bought-back ZNHB is recycled into the Sale
// Pool (core/tokenomics/curve) -- never burned -- by decrementing
// cumulative_sale_distributed, which makes the curve's next tranche cheaper
// again.
//
// This is a market ask, not a priced bid: sellers commit a ZNHB amount, not
// a minimum price. The treasury independently computes the epoch's max
// price (MaxBuybackPrice) from the curve's own position and an externally
// signed reference price, and fills every pending ask pro-rata against
// whatever budget that price allows -- never at a seller-chosen price, and
// never above the computed max. This is a deliberate simplification of a
// full price-sorted sealed-bid auction: it is simpler to implement and
// verify correctly, and functionally equivalent for sellers, since every
// filled seller receives the exact same uniform clearing price regardless
// of order.
package buyback

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// SplitDenominator is the basis-point denominator used throughout this
// package, matching core/rewards.SplitDenominator.
const SplitDenominator = 10_000

// Config is the buyback engine's full parameter set.
//
// FeeShareBps, DiscountBps, and SafetyMarginBps are adjustable within
// enforced floors via the policy.buybackParams governance proposal kind
// (native/governance). SignerThreshold and Signers are genesis-immutable --
// no code path in this codebase may modify them after construction. This
// split is deliberate: a captured governance vote could otherwise swap in
// colluding reference-price signers and exploit the very next epoch's
// settlement, and a timelock delays that but does not prevent it. Mirrors
// how native/escrow's FrozenArb freezes its signer set at policy-snapshot
// time for the identical reason.
type Config struct {
	FeeShareBps     uint32
	DiscountBps     uint32
	SafetyMarginBps uint32
	SignerThreshold uint32
	Signers         [][20]byte
}

// Validate checks the configuration is internally consistent. Does not
// (and cannot) enforce genesis-immutability -- that is an architectural
// property of which code paths are allowed to construct a Config, not
// something a value type can self-enforce.
func (c Config) Validate() error {
	if c.FeeShareBps > SplitDenominator {
		return fmt.Errorf("buyback: fee share %d bps exceeds %d", c.FeeShareBps, SplitDenominator)
	}
	if c.DiscountBps > SplitDenominator {
		return fmt.Errorf("buyback: discount %d bps exceeds %d", c.DiscountBps, SplitDenominator)
	}
	if c.SafetyMarginBps > SplitDenominator {
		return fmt.Errorf("buyback: safety margin %d bps exceeds %d", c.SafetyMarginBps, SplitDenominator)
	}
	if c.SignerThreshold == 0 {
		return fmt.Errorf("buyback: signer threshold must be positive")
	}
	if len(c.Signers) == 0 {
		return fmt.Errorf("buyback: at least one reference-price signer is required")
	}
	if int(c.SignerThreshold) > len(c.Signers) {
		return fmt.Errorf("buyback: signer threshold %d exceeds signer count %d", c.SignerThreshold, len(c.Signers))
	}
	seen := make(map[[20]byte]struct{}, len(c.Signers))
	for i, signer := range c.Signers {
		if _, dup := seen[signer]; dup {
			return fmt.Errorf("buyback: duplicate signer at index %d", i)
		}
		seen[signer] = struct{}{}
	}
	return nil
}

// Clone returns a deep copy, avoiding accidental aliasing of the Signers slice.
func (c Config) Clone() Config {
	clone := c
	if len(c.Signers) > 0 {
		clone.Signers = make([][20]byte, len(c.Signers))
		copy(clone.Signers, c.Signers)
	}
	return clone
}

// ReferencePriceDomainV1 is the domain separator for buyback reference-price
// signatures -- distinct from native/swap's PriceProofDomainV1 so a
// signature over one can never be replayed as the other.
const ReferencePriceDomainV1 = "NHB_BUYBACK_REFPRICE_V1"

// ReferencePrice is the independently-signed market price for ZNHB, in NHB
// terms (matching the curve's own USD-denominated pricing, since NHB is
// backed 1:1), that a given epoch's settlement checks its clearing price
// against. Scoped to a specific epoch so a stale signature bundle from an
// earlier epoch can never be replayed into a later one.
type ReferencePrice struct {
	Rate      *big.Rat
	Epoch     uint64
	Timestamp time.Time
}

// CanonicalMessage renders the exact message the M-of-N signers sign over.
func (r *ReferencePrice) CanonicalMessage() (string, error) {
	if r == nil {
		return "", fmt.Errorf("buyback: reference price not initialised")
	}
	if r.Rate == nil || r.Rate.Sign() <= 0 {
		return "", fmt.Errorf("buyback: reference price rate must be positive")
	}
	if r.Epoch == 0 {
		return "", fmt.Errorf("buyback: reference price epoch required")
	}
	if r.Timestamp.IsZero() {
		return "", fmt.Errorf("buyback: reference price timestamp required")
	}
	builder := strings.Builder{}
	builder.WriteString(ReferencePriceDomainV1)
	builder.WriteString("|epoch=")
	fmt.Fprintf(&builder, "%d", r.Epoch)
	builder.WriteString("|rate=")
	builder.WriteString(r.Rate.FloatString(18))
	builder.WriteString("|ts=")
	fmt.Fprintf(&builder, "%d", r.Timestamp.UTC().Unix())
	return builder.String(), nil
}

// Hash computes the keccak256 digest of the canonical message -- the digest
// every signer in the bundle must have signed.
func (r *ReferencePrice) Hash() ([32]byte, error) {
	var digest [32]byte
	message, err := r.CanonicalMessage()
	if err != nil {
		return digest, err
	}
	copy(digest[:], ethcrypto.Keccak256([]byte(message)))
	return digest, nil
}

// VerifyReferencePrice checks a bundle of signatures over rp's canonical
// digest against cfg's genesis-immutable signer set, requiring at least
// cfg.SignerThreshold unique valid signatures from distinct authorized
// signers. Reimplements (does not import) native/escrow's
// verifyDecisionSignatures/FrozenArb pattern -- importing an escrow-domain
// package into tokenomics would be a layering violation, and the
// underlying "recover signer, check membership, require a threshold of
// unique matches" logic is generic enough that it doesn't belong owned by
// either domain.
func VerifyReferencePrice(cfg Config, rp *ReferencePrice, signatures [][]byte) ([][20]byte, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	digest, err := rp.Hash()
	if err != nil {
		return nil, err
	}
	if len(signatures) == 0 {
		return nil, fmt.Errorf("buyback: signature bundle required")
	}
	allowed := make(map[[20]byte]struct{}, len(cfg.Signers))
	for _, signer := range cfg.Signers {
		allowed[signer] = struct{}{}
	}
	seen := make(map[[20]byte]struct{})
	unique := make([][20]byte, 0, len(signatures))
	for i, sig := range signatures {
		if len(sig) != 65 {
			return nil, fmt.Errorf("buyback: signature %d must be 65 bytes", i)
		}
		buf := make([]byte, len(sig))
		copy(buf, sig)
		if buf[64] >= 27 {
			buf[64] -= 27
		}
		if buf[64] != 0 && buf[64] != 1 {
			return nil, fmt.Errorf("buyback: signature %d has invalid recovery id", i)
		}
		pubKey, err := ethcrypto.SigToPub(digest[:], buf)
		if err != nil {
			return nil, fmt.Errorf("buyback: invalid signature %d: %w", i, err)
		}
		addr := ethcrypto.PubkeyToAddress(*pubKey)
		var signer [20]byte
		copy(signer[:], addr[:])
		if _, ok := allowed[signer]; !ok {
			return nil, fmt.Errorf("buyback: signature %d not from an authorized reference-price signer", i)
		}
		if _, dup := seen[signer]; dup {
			continue
		}
		seen[signer] = struct{}{}
		unique = append(unique, signer)
	}
	if len(unique) < int(cfg.SignerThreshold) {
		return nil, fmt.Errorf("buyback: insufficient signer quorum: have %d need %d", len(unique), cfg.SignerThreshold)
	}
	return unique, nil
}

// MaxBuybackPrice computes the hard ceiling this epoch's settlement may pay
// per ZNHB, in NHB: the lesser of a discount off the Sale Pool curve's
// current spot price and a safety margin off the independently-signed
// reference price. Taking the minimum of both, rather than either alone,
// means the buyback can never overpay relative to either the treasury's own
// pricing model or the outside market -- whichever is more conservative
// wins.
func MaxBuybackPrice(curvePrice, refPrice *big.Rat, discountBps, safetyMarginBps uint32) (*big.Rat, error) {
	if curvePrice == nil || curvePrice.Sign() <= 0 {
		return nil, fmt.Errorf("buyback: curve price must be positive")
	}
	if refPrice == nil || refPrice.Sign() <= 0 {
		return nil, fmt.Errorf("buyback: reference price must be positive")
	}
	if discountBps > SplitDenominator || safetyMarginBps > SplitDenominator {
		return nil, fmt.Errorf("buyback: discount/safety margin must not exceed %d bps", SplitDenominator)
	}
	denom := big.NewRat(SplitDenominator, 1)
	discountFactor := new(big.Rat).Sub(denom, big.NewRat(int64(discountBps), 1))
	discountFactor.Quo(discountFactor, denom)
	marginFactor := new(big.Rat).Sub(denom, big.NewRat(int64(safetyMarginBps), 1))
	marginFactor.Quo(marginFactor, denom)

	fromCurve := new(big.Rat).Mul(curvePrice, discountFactor)
	fromRef := new(big.Rat).Mul(refPrice, marginFactor)
	if fromCurve.Cmp(fromRef) <= 0 {
		return fromCurve, nil
	}
	return fromRef, nil
}

// Ask is a single seller's pending market ask for this epoch: a ZNHB
// amount, no minimum price.
type Ask struct {
	Seller    [20]byte
	AmountWei *big.Int
}

// Fill is one seller's settled portion of an epoch's buyback.
type Fill struct {
	Seller       [20]byte
	ZNHBFilled   *big.Int
	NHBPaid      *big.Int
	ZNHBRefunded *big.Int
}

// FillAsksProRata fills every pending ask proportionally to its size,
// bounded by how much ZNHB budgetNHBWei can actually buy at priceNHBPerZNHB
// (NHB per whole ZNHB). If total demand is within budget, every ask fills
// in full. If demand exceeds budget, every ask is scaled down by the same
// ratio, so no single seller is filled in full while another is starved.
//
// Both ZNHBFilled and NHBPaid always round DOWN (protocol-favoring, the
// same direction as curve.RoundZNHBDown/RoundCostUp elsewhere in this
// codebase) -- the sum of every Fill.NHBPaid is guaranteed, by explicit
// check before returning, to never exceed budgetNHBWei, regardless of any
// accumulated rounding across many asks.
func FillAsksProRata(asks []Ask, budgetNHBWei *big.Int, priceNHBPerZNHB *big.Rat) ([]Fill, error) {
	if budgetNHBWei == nil || budgetNHBWei.Sign() < 0 {
		return nil, fmt.Errorf("buyback: budget must not be negative")
	}
	if priceNHBPerZNHB == nil || priceNHBPerZNHB.Sign() <= 0 {
		return nil, fmt.Errorf("buyback: price must be positive")
	}
	fills := make([]Fill, 0, len(asks))
	if budgetNHBWei.Sign() == 0 || len(asks) == 0 {
		for _, ask := range asks {
			fills = append(fills, Fill{Seller: ask.Seller, ZNHBFilled: big.NewInt(0), NHBPaid: big.NewInt(0), ZNHBRefunded: new(big.Int).Set(ask.AmountWei)})
		}
		return fills, nil
	}

	// priceNHBPerZNHB (NHB per whole ZNHB) is a dimensionless ratio -- since
	// both NHB and ZNHB share the same 18-decimal convention, it applies
	// identically to atto-scaled (wei) amounts with no further conversion:
	// costWei = amountWei * priceNHBPerZNHB, exactly as it would for whole
	// tokens. maxZNHBWeiBuyable = floor(budgetNHBWei / priceNHBPerZNHB).
	budgetRat := new(big.Rat).SetInt(budgetNHBWei)
	maxZNHBWeiBuyableRat := new(big.Rat).Quo(budgetRat, priceNHBPerZNHB)
	maxZNHBWeiBuyable := new(big.Int).Quo(maxZNHBWeiBuyableRat.Num(), maxZNHBWeiBuyableRat.Denom())

	totalAsk := big.NewInt(0)
	for _, ask := range asks {
		if ask.AmountWei == nil || ask.AmountWei.Sign() <= 0 {
			continue
		}
		totalAsk.Add(totalAsk, ask.AmountWei)
	}

	fullyFunded := totalAsk.Cmp(maxZNHBWeiBuyable) <= 0
	spentTotal := big.NewInt(0)
	filledTotal := big.NewInt(0)
	for _, ask := range asks {
		if ask.AmountWei == nil || ask.AmountWei.Sign() <= 0 {
			fills = append(fills, Fill{Seller: ask.Seller, ZNHBFilled: big.NewInt(0), NHBPaid: big.NewInt(0), ZNHBRefunded: big.NewInt(0)})
			continue
		}
		var filled *big.Int
		if fullyFunded || totalAsk.Sign() == 0 {
			filled = new(big.Int).Set(ask.AmountWei)
		} else {
			// filled = floor(ask.AmountWei * maxZNHBWeiBuyable / totalAsk)
			filled = new(big.Int).Mul(ask.AmountWei, maxZNHBWeiBuyable)
			filled.Quo(filled, totalAsk)
		}
		if filled.Cmp(ask.AmountWei) > 0 {
			filled = new(big.Int).Set(ask.AmountWei)
		}
		paidRat := new(big.Rat).Mul(new(big.Rat).SetInt(filled), priceNHBPerZNHB)
		paid := new(big.Int).Quo(paidRat.Num(), paidRat.Denom())
		refunded := new(big.Int).Sub(ask.AmountWei, filled)
		fills = append(fills, Fill{Seller: ask.Seller, ZNHBFilled: filled, NHBPaid: paid, ZNHBRefunded: refunded})
		spentTotal.Add(spentTotal, paid)
		filledTotal.Add(filledTotal, filled)
	}

	if spentTotal.Cmp(budgetNHBWei) > 0 {
		return nil, fmt.Errorf("buyback: internal error: computed spend %s exceeds budget %s", spentTotal, budgetNHBWei)
	}
	return fills, nil
}
