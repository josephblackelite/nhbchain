package core

import (
	"errors"
	"io/fs"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/core/types"
)

// PL-BC-B: the native ZNHB/NHB constant-product pool, on the real apply path with the supply invariant checked after
// every operation.

func newPoolFixture(t *testing.T) *bcFixture {
	f := newBCFixture(t, BridgeSettings{}, AmmSettings{ActivationHeight: 100, SwapStartHeight: 150, FeeBps: 30, MaxSwapBps: 500})
	f.fund("provider", wei(2_000_000), wei(500_000))
	f.fund("alice", wei(100_000), wei(100_000))
	f.fund("bob", wei(100_000), wei(100_000))
	return f
}

// seed adds the first liquidity: 1,000,000 ZNHB against 50,000 NHB (a starting price of 0.05).
func (f *bcFixture) seedPool() {
	f.t.Helper()
	if err := f.send("provider", types.TxTypeAmmAddLiquidity, nil, ammAddPayload{Znhb: wei(1_000_000), Nhb: wei(50_000), MinShares: big.NewInt(0)}); err != nil {
		f.t.Fatalf("seed the pool: %v", err)
	}
}

func (f *bcFixture) swap(name string, dir uint8, in, minOut *big.Int) error {
	return f.send(name, types.TxTypeAmmSwap, nil, ammSwapPayload{Dir: dir, AmountIn: in, MinOut: minOut})
}

func TestAmmTypesAreUnknownBeforeTheirHeight(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		height     uint64
	}{{"setting off", 0, 500}, {"one block below the height", 100, 99}} {
		f := newBCFixture(t, BridgeSettings{}, AmmSettings{ActivationHeight: tc.activation, SwapStartHeight: 150, FeeBps: 30})
		f.fund("provider", wei(1_000), wei(1_000))
		f.atHeight(tc.height)
		for _, typ := range []types.TxType{types.TxTypeAmmAddLiquidity, types.TxTypeAmmRemoveLiquidity, types.TxTypeAmmSwap} {
			err := f.send("provider", typ, nil, ammSwapPayload{AmountIn: big.NewInt(1), MinOut: big.NewInt(0)})
			if !errors.Is(err, ErrUnknownTransactionType) {
				t.Fatalf("%s: type 0x%02X: %v, want an unknown transaction type", tc.name, byte(typ), err)
			}
		}
	}
}

func TestAmmOnlyTheProviderAddsAndRemoves(t *testing.T) {
	f := newPoolFixture(t)
	if err := f.send("alice", types.TxTypeAmmAddLiquidity, nil, ammAddPayload{Znhb: wei(10), Nhb: wei(1), MinShares: big.NewInt(0)}); !errors.Is(err, ErrAmmUnauthorized) {
		t.Fatalf("a deposit by someone else: %v, want ErrAmmUnauthorized", err)
	}
	f.seedPool()
	if err := f.send("alice", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: big.NewInt(1), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmUnauthorized) {
		t.Fatalf("a removal by someone else: %v, want ErrAmmUnauthorized", err)
	}
	f.checkInvariants("after refusals")
}

func TestAmmFirstDepositSetsReservesSharesAndThePoolAccount(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	view, err := f.sp.AmmPool()
	if err != nil {
		t.Fatal(err)
	}
	if view.ReserveZnhbWei.Cmp(wei(1_000_000)) != 0 || view.ReserveNhbWei.Cmp(wei(50_000)) != 0 {
		t.Fatalf("reserves %s/%s, want 1,000,000 ZNHB / 50,000 NHB", view.ReserveZnhbWei, view.ReserveNhbWei)
	}
	wantShares := new(big.Int).Sqrt(new(big.Int).Mul(wei(1_000_000), wei(50_000)))
	if view.TotalShares.Cmp(wantShares) != 0 {
		t.Fatalf("shares %s, want sqrt of the product %s", view.TotalShares, wantShares)
	}
	z, nh := f.balances(ammPoolAddress())
	if z.Cmp(wei(1_000_000)) != 0 || nh.Cmp(wei(50_000)) != 0 {
		t.Fatalf("the pool account holds %s/%s, want the reserves", z, nh)
	}
	pz, pn := f.balances(f.addr["provider"])
	if pz.Cmp(wei(1_000_000)) != 0 || pn.Cmp(wei(450_000)) != 0 {
		t.Fatalf("the provider holds %s/%s, want 1,000,000/450,000", pz, pn)
	}
	if view.PriceNhbPerZnhb != "0.050000000000000000" {
		t.Fatalf("price %q, want 0.05", view.PriceNhbPerZnhb)
	}
	f.checkInvariants("after the first deposit")
}

func TestAmmSwapsStayClosedUntilTheirOwnHeight(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(149)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); !errors.Is(err, ErrAmmSwapsClosed) {
		t.Fatalf("a swap at 149: %v, want ErrAmmSwapsClosed", err)
	}
	f.atHeight(150)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); err != nil {
		t.Fatalf("a swap at 150: %v", err)
	}
	// With the swap-start setting at zero swaps never open, however high the chain is.
	g := newBCFixture(t, BridgeSettings{}, AmmSettings{ActivationHeight: 100, SwapStartHeight: 0, FeeBps: 30})
	g.fund("provider", wei(2_000_000), wei(500_000))
	g.fund("alice", wei(1_000), wei(1_000))
	g.seedPool()
	g.atHeight(1_000_000)
	if err := g.swap("alice", AmmDirZnhbToNhb, wei(1), big.NewInt(0)); !errors.Is(err, ErrAmmSwapsClosed) {
		t.Fatalf("a swap with SwapStartHeight 0: %v, want ErrAmmSwapsClosed", err)
	}
}

func TestAmmSwapBothWaysMatchesTheQuoteAndKeepsTheFee(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	before, _ := f.sp.AmmPool()
	k0 := new(big.Int).Mul(before.ReserveZnhbWei, before.ReserveNhbWei)

	quote, err := f.sp.AmmQuote(AmmDirZnhbToNhb, wei(10_000))
	if err != nil {
		t.Fatal(err)
	}
	aliceZ0, aliceN0 := f.balances(f.addr["alice"])
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(10_000), quote); err != nil {
		t.Fatalf("swap ZNHB to NHB: %v", err)
	}
	aliceZ1, aliceN1 := f.balances(f.addr["alice"])
	if new(big.Int).Sub(aliceZ0, aliceZ1).Cmp(wei(10_000)) != 0 || new(big.Int).Sub(aliceN1, aliceN0).Cmp(quote) != 0 {
		t.Fatalf("alice paid %s ZNHB and received %s NHB, quote was %s", new(big.Int).Sub(aliceZ0, aliceZ1), new(big.Int).Sub(aliceN1, aliceN0), quote)
	}
	mid, _ := f.sp.AmmPool()
	if mid.ReserveZnhbWei.Cmp(new(big.Int).Add(before.ReserveZnhbWei, wei(10_000))) != 0 {
		t.Fatalf("the ZNHB reserve %s did not take the whole amount in, fee included", mid.ReserveZnhbWei)
	}
	if new(big.Int).Mul(mid.ReserveZnhbWei, mid.ReserveNhbWei).Cmp(k0) < 0 {
		t.Fatalf("the reserve product fell")
	}
	f.checkInvariants("after the first swap")

	// The way back.
	quote2, _ := f.sp.AmmQuote(AmmDirNhbToZnhb, wei(100))
	if err := f.swap("bob", AmmDirNhbToZnhb, wei(100), quote2); err != nil {
		t.Fatalf("swap NHB to ZNHB: %v", err)
	}
	bobZ, bobN := f.balances(f.addr["bob"])
	if new(big.Int).Sub(bobZ, wei(100_000)).Cmp(quote2) != 0 || bobN.Cmp(wei(99_900)) != 0 {
		t.Fatalf("bob holds %s ZNHB and %s NHB after the reverse swap (quote %s)", bobZ, bobN, quote2)
	}
	trades, err := f.sp.AmmTrades(10)
	if err != nil || len(trades) != 2 || trades[0].ID != 2 || trades[1].ID != 1 || trades[1].AmountIn.Cmp(wei(10_000)) != 0 {
		t.Fatalf("trades %+v, %v; want two, newest first", trades, err)
	}
	f.checkInvariants("after both swaps")
}

func TestAmmSwapGuardRails(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	snapshot := func() (string, string) {
		v, _ := f.sp.AmmPool()
		return v.ReserveZnhbWei.String(), v.ReserveNhbWei.String()
	}
	z0, n0 := snapshot()
	huge := wei(60_000) // above 5 percent of the 1,000,000 ZNHB reserve
	cases := map[string]struct {
		name   string
		dir    uint8
		in     *big.Int
		minOut *big.Int
		want   error
	}{
		"one swap above the share of the reserve it may move": {"alice", AmmDirZnhbToNhb, huge, big.NewInt(0), ErrAmmTooLarge},
		"a minimum out the pool cannot meet":                  {"alice", AmmDirZnhbToNhb, wei(1_000), wei(1_000_000), ErrAmmSlippage},
		"an amount that pays nothing out":                     {"alice", AmmDirZnhbToNhb, big.NewInt(1), big.NewInt(0), ErrAmmZeroOutput},
		"more than the sender holds":                          {"alice", AmmDirZnhbToNhb, wei(100_001), big.NewInt(0), nil},
		"a zero amount":                                       {"alice", AmmDirZnhbToNhb, big.NewInt(0), big.NewInt(0), ErrAmmInvalidPayload},
		"an unknown direction":                                {"alice", 7, wei(1), big.NewInt(0), ErrAmmInvalidPayload},
	}
	for name, tc := range cases {
		if tc.want == nil {
			continue // the balance case needs a swap under the size cap: checked below
		}
		if err := f.swap(tc.name, tc.dir, tc.in, tc.minOut); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	// A holder with less than the amount (under the size cap) is a balance error.
	f.fund("alice", wei(5), wei(0))
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("a swap above the sender's balance: %v, want ErrAmmInsufficientBalance", err)
	}
	if z1, n1 := snapshot(); z0 != z1 || n0 != n1 {
		t.Fatalf("a refused swap changed the reserves")
	}
	// The admin wallet and the pool's own address cannot swap.
	data, _ := rlpEncode(ammSwapPayload{Dir: AmmDirZnhbToNhb, AmountIn: wei(1), MinOut: big.NewInt(0)})
	tx := &types.Transaction{Type: types.TxTypeAmmSwap, Data: data}
	if err := f.sp.applyAmmSwapTransaction(tx, f.admin[:]); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("a swap by the admin wallet: %v, want ErrAmmAdminWallet", err)
	}
	if err := f.sp.applyAmmSwapTransaction(tx, ammPoolAddress()); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("a swap by the pool's own address: %v, want ErrAmmAdminWallet", err)
	}
	f.checkInvariants("after refused swaps")
}

func TestAmmEmptyPoolRefusesSwapsAndReserveFloorsHold(t *testing.T) {
	f := newPoolFixture(t)
	f.atHeight(200)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1), big.NewInt(0)); !errors.Is(err, ErrAmmEmptyPool) {
		t.Fatalf("a swap into an empty pool: %v, want ErrAmmEmptyPool", err)
	}
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: big.NewInt(1), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmEmptyPool) {
		t.Fatalf("a removal from an empty pool: %v, want ErrAmmEmptyPool", err)
	}
	// A floor on the NHB reserve that a swap would breach.
	g := newBCFixture(t, BridgeSettings{}, AmmSettings{ActivationHeight: 100, SwapStartHeight: 150, FeeBps: 0, MaxSwapBps: 10_000, MinReserveNHB: wei(49_999)})
	g.fund("provider", wei(2_000_000), wei(500_000))
	g.fund("alice", wei(900_000), wei(0))
	g.seedPool()
	g.atHeight(200)
	if err := g.swap("alice", AmmDirZnhbToNhb, wei(900_000), big.NewInt(0)); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("a swap that takes the NHB reserve below its floor: %v, want ErrAmmReserveFloor", err)
	}
	g.checkInvariants("after the floor refusal")
}

// A partial removal may not leave a reserve under its floor; taking everything is allowed (tested below).
func TestAmmPartialRemovalRespectsTheReserveFloors(t *testing.T) {
	f := newBCFixture(t, BridgeSettings{}, AmmSettings{ActivationHeight: 100, SwapStartHeight: 150, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: wei(900_000)})
	f.fund("provider", wei(2_000_000), wei(500_000))
	f.seedPool()
	view, _ := f.sp.AmmPool()
	fifth := new(big.Int).Div(view.TotalShares, big.NewInt(5))
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: fifth, MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("a removal that leaves the ZNHB reserve at 800,000 under a floor of 900,000: %v, want ErrAmmReserveFloor", err)
	}
	// A small enough removal that stays above the floor goes through.
	twentieth := new(big.Int).Div(view.TotalShares, big.NewInt(20))
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: twentieth, MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); err != nil {
		t.Fatalf("a removal that stays above the floor: %v", err)
	}
	f.checkInvariants("after floor-bound removals")
}

func TestAmmRemoveLiquidityPartialFullAndMinimums(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	view, _ := f.sp.AmmPool()
	quarter := new(big.Int).Div(view.TotalShares, big.NewInt(4))
	// Minimums that cannot be met refuse the removal.
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: quarter, MinZnhb: wei(1_000_000), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmSlippage) {
		t.Fatalf("a removal with an unmeetable minimum: %v, want ErrAmmSlippage", err)
	}
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: quarter, MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); err != nil {
		t.Fatalf("a partial removal: %v", err)
	}
	mid, _ := f.sp.AmmPool()
	// A quarter of the shares (rounded down) takes a quarter of the reserve (rounded down): the pool keeps the dust, never
	// the provider, so the reserve is at least 750,000 ZNHB and no more than a few wei above it.
	if mid.ReserveZnhbWei.Cmp(wei(750_000)) < 0 || mid.ReserveZnhbWei.Cmp(new(big.Int).Add(wei(750_000), big.NewInt(1_000_000))) > 0 {
		t.Fatalf("after taking a quarter the ZNHB reserve is %s, want 750,000 ZNHB plus rounding dust", mid.ReserveZnhbWei)
	}
	f.checkInvariants("after a partial removal")
	// More shares than the provider holds.
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: new(big.Int).Add(mid.TotalShares, big.NewInt(1)), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a removal above the provider's shares: %v, want ErrAmmInvalidPayload", err)
	}
	// Taking every remaining share empties the pool, which is allowed and leaves nothing behind.
	if err := f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: mid.TotalShares, MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); err != nil {
		t.Fatalf("removing every share: %v", err)
	}
	end, _ := f.sp.AmmPool()
	if end.TotalShares.Sign() != 0 || end.ReserveZnhbWei.Sign() != 0 || end.ReserveNhbWei.Sign() != 0 {
		t.Fatalf("a drained pool still shows %+v", end)
	}
	f.checkInvariants("after draining the pool")
}

// A long random run of swaps in both directions, with the invariants and the reserve product checked after every one.
func TestAmmRandomRunKeepsEveryInvariant(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(200)
	rng := rand.New(rand.NewSource(424242))
	prev, _ := f.sp.AmmPool()
	prevK := new(big.Int).Mul(prev.ReserveZnhbWei, prev.ReserveNhbWei)
	done := 0
	for i := 0; i < 400; i++ {
		name := []string{"alice", "bob"}[rng.Intn(2)]
		dir := uint8(rng.Intn(2))
		in := wei(int64(1 + rng.Intn(200)))
		if dir == AmmDirNhbToZnhb {
			in = new(big.Int).Div(in, big.NewInt(20))
		}
		if err := f.swap(name, dir, in, big.NewInt(0)); err != nil {
			if errors.Is(err, ErrAmmInsufficientBalance) || errors.Is(err, ErrAmmTooLarge) || errors.Is(err, ErrAmmZeroOutput) {
				continue
			}
			t.Fatalf("swap %d: %v", i, err)
		}
		done++
		v, _ := f.sp.AmmPool()
		k := new(big.Int).Mul(v.ReserveZnhbWei, v.ReserveNhbWei)
		if k.Cmp(prevK) < 0 {
			t.Fatalf("swap %d lowered the reserve product", i)
		}
		prevK = k
		f.checkInvariants("during the random run")
		if z, n := f.balances(ammPoolAddress()); z.Cmp(v.ReserveZnhbWei) != 0 || n.Cmp(v.ReserveNhbWei) != 0 {
			t.Fatalf("swap %d: the pool account %s/%s differs from the reserves %s/%s", i, z, n, v.ReserveZnhbWei, v.ReserveNhbWei)
		}
	}
	if done < 100 {
		t.Fatalf("only %d of 400 swaps ran: the run proves little", done)
	}
}

// Nothing that sets a price reads the pool: the curve, the buyback and the lending valuation never import the
// pool's package or its state keys. (The pool's price is information on the read RPC and nothing else.)
func TestNothingThatPricesReadsThePool(t *testing.T) {
	roots := []string{"tokenomics", "../native/lending", "../native/swap"}
	var files []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
	}
	matches, _ := filepath.Glob("lending*.go")
	files = append(files, matches...)
	buyback, _ := filepath.Glob("buyback*.go")
	files = append(files, buyback...)
	if len(files) < 10 {
		t.Fatalf("the scan found only %d files: the roots are wrong", len(files))
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(filepath.Base(path), "bridge") || strings.HasPrefix(filepath.Base(path), "amm") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, needle := range []string{"nhbchain/native/amm", "amm/pool", "AmmPool(", "AmmQuote(", "ammPoolAddress"} {
			if strings.Contains(text, needle) {
				t.Fatalf("%s reads the pool (%q): the pool price must never feed a price anywhere else", path, needle)
			}
		}
	}
}
