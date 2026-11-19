package lending

import (
	"math/big"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func decodeLendingConfig(t *testing.T, text string) Config {
	t.Helper()
	var wrapper struct {
		Lending Config `toml:"lending"`
	}
	if _, err := toml.Decode(text, &wrapper); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wrapper.Lending.EnsureDefaults()
	return wrapper.Lending
}

// The configuration that ships names no limit, so the engine is handed none.
func TestTheShippedLimitsAreAllOff(t *testing.T) {
	cfg := decodeLendingConfig(t, `
[lending]
  MaxLTVBps = 6000
  LiquidationThresholdBps = 8500
  OracleMaxAgeBlocks = 1000
  OracleMaxDeviationBps = 5000
  [lending.breaker]
  [lending.risk]
`)
	params, err := cfg.RiskParameters()
	if err != nil {
		t.Fatalf("risk parameters: %v", err)
	}
	if params.LiquidationBonus != 0 || params.CircuitBreakerActive || params.Pauses != (ActionPauses{}) ||
		params.BorrowCaps.PerBlock != nil || params.BorrowCaps.Total != nil || params.BorrowCaps.UtilisationBps != 0 ||
		params.MaxTotalSupply != nil || params.MaxTotalCollateral != nil {
		t.Fatalf("an empty limits section must hand the engine no limit, got %+v", params)
	}
	if params.MaxLTV != 6000 || params.LiquidationThreshold != 8500 || params.Oracle.MaxAgeBlocks != 1000 || params.Oracle.MaxDeviationBps != 5000 {
		t.Fatalf("the loan-to-value and oracle limits were not carried over: %+v", params)
	}
}

// Every limit the file can name reaches the engine's parameters.
func TestEveryNamedLimitReachesTheEngineParameters(t *testing.T) {
	cfg := decodeLendingConfig(t, `
[lending]
  MaxLTVBps = 6000
  LiquidationThresholdBps = 8500
  DeveloperFeeBps = 0
  [lending.breaker]
    MaxTotalSupplyWei = "5000000000000000000000"
    MaxTotalBorrowWei = "3000000000000000000000"
    MaxTotalCollateralWei = "9000000000000000000000"
  [lending.risk]
    LiquidationBonusBps = 800
    BorrowCapPerBlockWei = "100000000000000000000"
    BorrowCapUtilisationBps = 7000
    PauseSupply = true
    PauseBorrow = true
    PauseRepay = true
    PauseLiquidate = true
    CircuitBreakerActive = true
`)
	params, err := cfg.RiskParameters()
	if err != nil {
		t.Fatalf("risk parameters: %v", err)
	}
	mustInt := func(s string) *big.Int {
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			t.Fatal(s)
		}
		return v
	}
	if params.LiquidationBonus != 800 || !params.CircuitBreakerActive ||
		params.Pauses != (ActionPauses{Supply: true, Borrow: true, Repay: true, Liquidate: true}) ||
		params.BorrowCaps.UtilisationBps != 7000 {
		t.Fatalf("bonus, breaker, pauses or utilisation cap missing: %+v", params)
	}
	if params.BorrowCaps.PerBlock == nil || params.BorrowCaps.PerBlock.Cmp(mustInt("100000000000000000000")) != 0 {
		t.Fatalf("per-block cap: %v", params.BorrowCaps.PerBlock)
	}
	if params.BorrowCaps.Total == nil || params.BorrowCaps.Total.Cmp(mustInt("3000000000000000000000")) != 0 {
		t.Fatalf("total borrow cap (MaxTotalBorrowWei): %v", params.BorrowCaps.Total)
	}
	if params.MaxTotalSupply == nil || params.MaxTotalSupply.Cmp(mustInt("5000000000000000000000")) != 0 {
		t.Fatalf("supply cap (MaxTotalSupplyWei): %v", params.MaxTotalSupply)
	}
	if params.MaxTotalCollateral == nil || params.MaxTotalCollateral.Cmp(mustInt("9000000000000000000000")) != 0 {
		t.Fatalf("collateral cap (MaxTotalCollateralWei): %v", params.MaxTotalCollateral)
	}
	// The result owns its numbers: changing the config afterwards does not reach it.
	cfg.Risk.BorrowCapPerBlockWei.SetInt64(1)
	cfg.Breaker.MaxTotalSupplyWei.SetInt64(1)
	if params.BorrowCaps.PerBlock.Cmp(big.NewInt(1)) == 0 || params.MaxTotalSupply.Cmp(big.NewInt(1)) == 0 {
		t.Fatalf("the parameters share their numbers with the config")
	}
}

// A bonus larger than the threshold leaves room for would leave bad debt on every
// liquidation at the threshold; a nonsense limit is refused at start-up.
func TestNonsenseLimitsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		toml    string
		wantErr string
	}{
		{"bonus at the most the threshold allows", `[lending]
  LiquidationThresholdBps = 8500
  [lending.risk]
    LiquidationBonusBps = 1764`, ""},
		{"bonus one past it", `[lending]
  LiquidationThresholdBps = 8500
  [lending.risk]
    LiquidationBonusBps = 1765`, "bad debt"},
		{"bonus with a threshold of 100 percent", `[lending]
  LiquidationThresholdBps = 10000
  [lending.risk]
    LiquidationBonusBps = 1`, "bad debt"},
		{"bonus with a threshold above 100 percent", `[lending]
  LiquidationThresholdBps = 12000
  [lending.risk]
    LiquidationBonusBps = 1`, "bad debt"},
		{"no bonus with a threshold of 100 percent", `[lending]
  LiquidationThresholdBps = 10000`, ""},
		{"utilisation cap above 100 percent", `[lending]
  [lending.risk]
    BorrowCapUtilisationBps = 10001`, "above 10000"},
		{"negative supply cap", `[lending]
  [lending.breaker]
    MaxTotalSupplyWei = "-1"`, "negative"},
		{"negative per-block cap", `[lending]
  [lending.risk]
    BorrowCapPerBlockWei = "-5"`, "negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := decodeLendingConfig(t, tc.toml)
			_, err := cfg.RiskParameters()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

func TestWithoutGatedLimitsClearsExactlyTheGatedOnes(t *testing.T) {
	full := RiskParameters{
		MaxLTV: 6000, LiquidationThreshold: 8500, DeveloperFeeCapBps: 300,
		Oracle:               OracleConfig{MaxAgeBlocks: 10, MaxDeviationBps: 20},
		LiquidationBonus:     500,
		CircuitBreakerActive: true,
		BorrowCaps:           BorrowCaps{PerBlock: big.NewInt(1), Total: big.NewInt(2), UtilisationBps: 3},
		Pauses:               ActionPauses{Supply: true, Borrow: true, Repay: true, Liquidate: true},
		MaxTotalSupply:       big.NewInt(4),
		MaxTotalCollateral:   big.NewInt(5),
	}
	got := full.WithoutGatedLimits()
	if got.LiquidationBonus != 0 || got.CircuitBreakerActive || got.Pauses != (ActionPauses{}) ||
		got.BorrowCaps.PerBlock != nil || got.BorrowCaps.Total != nil || got.BorrowCaps.UtilisationBps != 0 ||
		got.MaxTotalSupply != nil || got.MaxTotalCollateral != nil {
		t.Fatalf("a gated limit survived: %+v", got)
	}
	if got.MaxLTV != 6000 || got.LiquidationThreshold != 8500 || got.DeveloperFeeCapBps != 300 || got.Oracle != (OracleConfig{MaxAgeBlocks: 10, MaxDeviationBps: 20}) {
		t.Fatalf("an ungated limit was cleared: %+v", got)
	}
	// The original is untouched and the copy shares nothing with it.
	if full.LiquidationBonus != 500 || full.BorrowCaps.Total == nil || full.MaxTotalSupply == nil {
		t.Fatalf("the original was modified: %+v", full)
	}
	c := full.Clone()
	c.MaxTotalSupply.SetInt64(99)
	c.BorrowCaps.Total.SetInt64(99)
	if full.MaxTotalSupply.Int64() != 4 || full.BorrowCaps.Total.Int64() != 2 {
		t.Fatalf("Clone shares numbers with the original")
	}
}
