package amm

import (
	"errors"
	"math/big"
	"math/rand"
	"testing"
)

func n(v int64) *big.Int { return big.NewInt(v) }

func TestAmountOutKnownValues(t *testing.T) {
	// 1,000,000 / 1,000,000 reserves, 30 bps fee, 10,000 in: 10000*9970*1e6 / (1e6*10000 + 10000*9970) = 9871 (floor).
	out, err := AmountOut(n(1_000_000), n(1_000_000), n(10_000), 30)
	if err != nil || out.Cmp(n(9_871)) != 0 {
		t.Fatalf("out = %v, %v; want 9871", out, err)
	}
	// With no fee the result is the plain constant-product figure, floored: 10000*1e6/(1e6+10000) = 9900.99 -> 9900.
	out, err = AmountOut(n(1_000_000), n(1_000_000), n(10_000), 0)
	if err != nil || out.Cmp(n(9_900)) != 0 {
		t.Fatalf("out without fee = %v, %v; want 9900", out, err)
	}
}

func TestAmountOutRefusals(t *testing.T) {
	if _, err := AmountOut(n(0), n(5), n(1), 30); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("empty in-reserve: %v", err)
	}
	if _, err := AmountOut(n(5), n(0), n(1), 30); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("empty out-reserve: %v", err)
	}
	if _, err := AmountOut(n(5), n(5), n(0), 30); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero in: %v", err)
	}
	if _, err := AmountOut(n(5), n(5), n(-1), 30); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("negative in: %v", err)
	}
	if _, err := AmountOut(n(5), n(5), n(1), 10_000); !errors.Is(err, ErrInvalidFee) {
		t.Fatalf("a fee of 100 percent: %v", err)
	}
}

// The properties the chain relies on, over many random pools and swaps: the product of the reserves never shrinks,
// the pool never pays out its whole reserve, and a bigger amount in never pays less.
func TestAmountOutProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(20261117))
	big1e18 := new(big.Int).Exp(n(10), n(18), nil)
	for i := 0; i < 20_000; i++ {
		rIn := new(big.Int).Add(new(big.Int).Rand(rng, new(big.Int).Mul(big1e18, n(1_000_000_000))), n(1))
		rOut := new(big.Int).Add(new(big.Int).Rand(rng, new(big.Int).Mul(big1e18, n(1_000_000_000))), n(1))
		in := new(big.Int).Add(new(big.Int).Rand(rng, new(big.Int).Add(rIn, n(1))), n(1))
		fee := uint32(rng.Intn(1_000))
		out, err := AmountOut(rIn, rOut, in, fee)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if out.Sign() < 0 || out.Cmp(rOut) >= 0 {
			t.Fatalf("case %d: out %s is not in [0, reserveOut %s)", i, out, rOut)
		}
		before := new(big.Int).Mul(rIn, rOut)
		after := new(big.Int).Mul(new(big.Int).Add(rIn, in), new(big.Int).Sub(rOut, out))
		if after.Cmp(before) < 0 {
			t.Fatalf("case %d: the reserve product fell (in %s, out %s, fee %d)", i, in, out, fee)
		}
		bigger, _ := AmountOut(rIn, rOut, new(big.Int).Add(in, n(1)), fee)
		if bigger.Cmp(out) < 0 {
			t.Fatalf("case %d: a larger amount in paid less", i)
		}
	}
}

func TestAddLiquidityFirstDepositSetsThePrice(t *testing.T) {
	d, err := AddLiquidity(n(0), n(0), n(0), n(4_000_000), n(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Znhb.Cmp(n(4_000_000)) != 0 || d.Nhb.Cmp(n(1_000_000)) != 0 || d.Shares.Cmp(n(2_000_000)) != 0 {
		t.Fatalf("first deposit: %+v, want everything taken and sqrt(4e6*1e6)=2,000,000 shares", d)
	}
	if _, err := AddLiquidity(n(0), n(0), n(0), n(1), n(0)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a deposit with a zero NHB side: %v", err)
	}
	if _, err := AddLiquidity(n(0), n(0), n(0), n(0), n(1)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a deposit with a zero ZNHB side: %v", err)
	}
	if _, err := AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(0), n(5)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a zero ZNHB side into a pool with liquidity: %v", err)
	}
	if _, err := AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(5), n(0)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a zero NHB side into a pool with liquidity: %v", err)
	}
	if _, err := AddLiquidity(nil, nil, n(0), nil, n(1)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("a missing amount: %v", err)
	}
}

// Shares outstanding with an empty reserve cannot happen through the chain code, but the arithmetic must refuse it
// (it would divide by zero) rather than trust its caller.
func TestAddLiquidityRefusesSharesAgainstEmptyReserves(t *testing.T) {
	if _, err := AddLiquidity(n(0), n(5), n(10), n(1), n(1)); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("no ZNHB reserve: %v", err)
	}
	if _, err := AddLiquidity(n(5), n(0), n(10), n(1), n(1)); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("no NHB reserve: %v", err)
	}
}

// When the NHB offered is the limit, the ZNHB the pool takes is worked out from it and rounded UP in the pool's favour:
// 10 NHB at 7 ZNHB per 3 NHB needs 23.33 ZNHB, taken as 24.
func TestAddLiquidityNhbLimitedDepositRoundsTheZnhbUp(t *testing.T) {
	d, err := AddLiquidity(n(7_000_000), n(3_000_000), n(4_582_575), n(1_000_000), n(10))
	if err != nil {
		t.Fatal(err)
	}
	if d.Nhb.Cmp(n(10)) != 0 || d.Znhb.Cmp(n(24)) != 0 {
		t.Fatalf("NHB-limited deposit: %+v, want 10 NHB and 24 ZNHB", d)
	}
}

func TestAddLiquidityIntoAPoolTakesTheMatchingPair(t *testing.T) {
	// A pool of 4,000,000 ZNHB / 1,000,000 NHB holding 2,000,000 shares. Offer 400 ZNHB and plenty of NHB.
	d, err := AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(400), n(10_000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Znhb.Cmp(n(400)) != 0 || d.Nhb.Cmp(n(100)) != 0 || d.Shares.Cmp(n(200)) != 0 {
		t.Fatalf("matching deposit: %+v, want 400 ZNHB, 100 NHB, 200 shares", d)
	}
	// Offer little NHB: the ZNHB taken shrinks to match, rounded up in the pool's favour.
	d, err = AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(10_000), n(100))
	if err != nil {
		t.Fatal(err)
	}
	if d.Nhb.Cmp(n(100)) != 0 || d.Znhb.Cmp(n(400)) != 0 {
		t.Fatalf("NHB-limited deposit: %+v, want 100 NHB and 400 ZNHB", d)
	}
	// Rounding goes the pool's way: 3 ZNHB at 4 ZNHB per NHB needs 0.75 NHB, which rounds UP to 1, and the shares
	// minted are the smaller of the two proportional counts, rounded down (1, not 1.5).
	d, err = AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(3), n(1))
	if err != nil || d.Znhb.Cmp(n(3)) != 0 || d.Nhb.Cmp(n(1)) != 0 || d.Shares.Cmp(n(1)) != 0 {
		t.Fatalf("rounding of a tiny deposit: %+v, %v; want 3 ZNHB, 1 NHB, 1 share", d, err)
	}
	// A deposit too small to mint one share is refused rather than donated.
	if _, err := AddLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(1), n(1)); !errors.Is(err, ErrZeroShares) {
		t.Fatalf("a deposit that mints no share: %v", err)
	}
}

// Depositing and withdrawing at once can never pay out more than was put in, however the amounts round.
func TestAddThenRemoveNeverProfits(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 5_000; i++ {
		rZ := new(big.Int).Add(new(big.Int).Rand(rng, n(1_000_000_000_000)), n(1_000))
		rN := new(big.Int).Add(new(big.Int).Rand(rng, n(1_000_000_000_000)), n(1_000))
		total := new(big.Int).Sqrt(new(big.Int).Mul(rZ, rN))
		wantZ := new(big.Int).Add(new(big.Int).Rand(rng, n(1_000_000_000)), n(1))
		wantN := new(big.Int).Add(new(big.Int).Rand(rng, n(1_000_000_000)), n(1))
		d, err := AddLiquidity(rZ, rN, total, wantZ, wantN)
		if err != nil {
			continue // too small to mint a share, or cannot match: refused, which is fine
		}
		z, nh, err := RemoveLiquidity(new(big.Int).Add(rZ, d.Znhb), new(big.Int).Add(rN, d.Nhb), new(big.Int).Add(total, d.Shares), d.Shares)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if z.Cmp(d.Znhb) > 0 || nh.Cmp(d.Nhb) > 0 {
			t.Fatalf("case %d: put in %s/%s, took out %s/%s", i, d.Znhb, d.Nhb, z, nh)
		}
	}
}

func TestRemoveLiquidity(t *testing.T) {
	z, nh, err := RemoveLiquidity(n(4_000_000), n(1_000_000), n(2_000_000), n(500_000))
	if err != nil || z.Cmp(n(1_000_000)) != 0 || nh.Cmp(n(250_000)) != 0 {
		t.Fatalf("a quarter of the shares: %v %v %v", z, nh, err)
	}
	if _, _, err := RemoveLiquidity(n(4), n(1), n(2), n(3)); !errors.Is(err, ErrSharesAboveSupply) {
		t.Fatalf("more shares than exist: %v", err)
	}
	if _, _, err := RemoveLiquidity(n(0), n(0), n(0), n(1)); !errors.Is(err, ErrEmptyPool) {
		t.Fatalf("an empty pool: %v", err)
	}
	if _, _, err := RemoveLiquidity(n(4), n(1), n(2), n(0)); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("zero shares: %v", err)
	}
	// Rounding favours the pool: 1 share of 3 from reserves 10/10 pays 3/3, not 3.33.
	z, nh, _ = RemoveLiquidity(n(10), n(10), n(3), n(1))
	if z.Cmp(n(3)) != 0 || nh.Cmp(n(3)) != 0 {
		t.Fatalf("rounding: %v %v, want 3 3", z, nh)
	}
}
