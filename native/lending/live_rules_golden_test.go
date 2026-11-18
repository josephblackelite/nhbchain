package lending

import (
	"math/big"
	"testing"
)

// PL-R5T-51: four lending rules changed with no activation height and are live on
// the running line: the accrual basis (block time, 31,536,000 seconds a year, where
// the older release charged by block count), the market record (a trailing block-time
// stamp), the sweep mark written on the first use of a pool, and the pricing of
// seized collateral at the oracle price. They cannot be gated now: the chain has
// already run them. What can be done is to pin them, so that the NEXT change to any
// of them cannot slip in without a gate. These tests are that pin: each holds the
// exact numbers the running line produces. If one fails, do not edit the number: the
// change is a consensus change and needs an activation height (docs/consensus/invariants.md,
// "Lending rules already live without an activation height").

func mustDecimal(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad number %q", s)
	}
	return v
}

// goldenAccrual returns the market after the engine accrues it at the given block
// time and height, from a fixed starting market under the default curve and a 10 percent
// reserve factor.
func goldenAccrual(t *testing.T, lastBlock, lastStamp uint64, atHeight uint64, atTime int64) (*Market, *FeeAccrual) {
	t.Helper()
	engine, state := accrualEngine(t)
	engine.SetInterestModel(DefaultInterestModel)
	engine.SetReserveFactor(1_000)
	market := &Market{
		TotalNHBSupplied:    mustDecimal(t, "1000000000000000000000"),
		TotalSupplyShares:   mustDecimal(t, "1000000000000000000000"),
		TotalNHBBorrowed:    mustDecimal(t, "500000000000000000000"),
		SupplyIndex:         new(big.Int).Set(ray),
		BorrowIndex:         new(big.Int).Set(ray),
		LastUpdateBlock:     lastBlock,
		LastUpdateTimestamp: lastStamp,
	}
	state.market = market
	engine.SetBlockHeight(atHeight)
	engine.SetBlockTimestamp(atTime)
	fees, _, err := engine.accrueInterest(market)
	if err != nil {
		t.Fatalf("accrue: %v", err)
	}
	return market, fees
}

func TestLiveAccrualBasisIsBlockTimeOverAYearOf31536000Seconds(t *testing.T) {
	const start = int64(1_700_000_000)
	// One day of block time, however many blocks carried it.
	market, fees := goldenAccrual(t, 100, uint64(start), 43_300, start+86_400)
	got := map[string]string{
		"borrow index":    market.BorrowIndex.String(),
		"supply index":    market.SupplyIndex.String(),
		"total borrowed":  market.TotalNHBBorrowed.String(),
		"total supplied":  market.TotalNHBSupplied.String(),
		"protocol fees":   fees.ProtocolFeesWei.String(),
		"stamp":           big.NewInt(int64(market.LastUpdateTimestamp)).String(),
		"stamped block":   big.NewInt(int64(market.LastUpdateBlock)).String(),
		"year in seconds": big.NewInt(secondsPerYear).String(),
	}
	want := goldenAccrualDay
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, the running line produces %s", k, got[k], v)
		}
	}
}

// A market that was never stamped accrues its first step by block count, one second a
// block, and is stamped by it.
func TestLiveAccrualOfAnUnstampedMarketFallsBackToOneSecondPerBlockOnce(t *testing.T) {
	const start = int64(1_700_000_000)
	market, fees := goldenAccrual(t, 1_000, 0, 1_100, start)
	got := map[string]string{
		"borrow index":   market.BorrowIndex.String(),
		"supply index":   market.SupplyIndex.String(),
		"total borrowed": market.TotalNHBBorrowed.String(),
		"protocol fees":  fees.ProtocolFeesWei.String(),
		"stamp":          big.NewInt(int64(market.LastUpdateTimestamp)).String(),
	}
	for k, v := range goldenAccrualHundredBlocks {
		if got[k] != v {
			t.Errorf("%s = %s, the running line produces %s", k, got[k], v)
		}
	}
}

// Seized collateral is the repaid debt plus the bonus, converted at the oracle price,
// rounded down; with no price it is 1:1.
func TestLiveSeizurePricingIsTheBonusInclusiveDebtAtTheOraclePrice(t *testing.T) {
	half := &Market{OracleMedianWei: mustDecimal(t, "500000000000000000")}
	none := &Market{OracleMedianWei: big.NewInt(0)}
	for _, tc := range []struct {
		name   string
		market *Market
		debt   int64
		factor uint64
		want   int64
	}{
		{"half price, no bonus", half, 900_000, 10_000, 1_800_000},
		{"half price, 10 percent bonus", half, 900_000, 11_000, 1_980_000},
		{"no price is one to one", none, 900_000, 10_000, 900_000},
		{"no price, 10 percent bonus", none, 900_000, 11_000, 990_000},
		{"rounds down", half, 7, 10_001, 14},
		{"a nil market is one to one", nil, 900_000, 10_000, 900_000},
	} {
		if got := CollateralForDebtValue(tc.market, big.NewInt(tc.debt), tc.factor); got.Cmp(big.NewInt(tc.want)) != 0 {
			t.Errorf("%s: seized %s, the running line seizes %d", tc.name, got, tc.want)
		}
	}
}

// The part of the collateral valuation that decides eligibility uses the same price.
func TestLiveCollateralValuationUsesTheOraclePriceOrOneToOne(t *testing.T) {
	half := &Market{OracleMedianWei: mustDecimal(t, "500000000000000000")}
	if got := OracleAdjustedCollateralValue(half, big.NewInt(2_000_000)); got.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Errorf("valued %s, the running line values 1,000,000", got)
	}
	if got := OracleAdjustedCollateralValue(&Market{OracleMedianWei: big.NewInt(0)}, big.NewInt(2_000_000)); got.Cmp(big.NewInt(2_000_000)) != 0 {
		t.Errorf("no price valued %s, the running line values one to one", got)
	}
}
