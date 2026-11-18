package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/amm"
)

// The edges of every bridge and pool rule (exactly at a limit, one unit over), and the bookkeeping each successful
// operation leaves behind (records, events, the sender's nonce). Each was added because a deliberate breakage of the code
// at that place was not noticed by the earlier tests.

func lastEvent(t *testing.T, sp *StateProcessor, kind string) types.Event {
	t.Helper()
	events := sp.Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == kind {
			return events[i]
		}
	}
	t.Fatalf("no %q event was emitted", kind)
	return types.Event{}
}

func (f *bcFixture) nonceOf(name string) uint64 {
	f.t.Helper()
	acc, err := f.sp.getAccount(f.addr[name])
	if err != nil {
		f.t.Fatal(err)
	}
	return acc.Nonce
}

func expectAttrs(t *testing.T, ev types.Event, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if ev.Attributes[k] != v {
			t.Fatalf("%s event: %s is %q, want %q (all: %v)", ev.Type, k, ev.Attributes[k], v, ev.Attributes)
		}
	}
	if len(ev.Attributes) != len(want) {
		t.Fatalf("%s event carries %d attributes, want %d: %v", ev.Type, len(ev.Attributes), len(want), ev.Attributes)
	}
}

func one() *big.Int { return big.NewInt(1) }

func plus(a *big.Int, b int64) *big.Int { return new(big.Int).Add(a, big.NewInt(b)) }

// ---- bridge

func TestBridgeLockEdgesRecordEventAndNonce(t *testing.T) {
	f := newBridgeFixture(t) // alice holds 1,000
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, MaxLockWei: wei(500)})
	mixed := "0x" + "AbCdEf" + strings.Repeat("1", 34)
	nonceBefore := f.nonceOf("alice")
	if err := f.send("alice", types.TxTypeBridgeLock, wei(500), bridgeLockPayload{DestChain: bridgeTestChain, Dest: mixed}); err != nil {
		t.Fatalf("a lock exactly at the cap: %v", err)
	}
	if got := f.nonceOf("alice"); got != nonceBefore+1 {
		t.Fatalf("nonce %d, want %d", got, nonceBefore+1)
	}
	var rec bridgeLockRecord
	found, err := nhbstate.NewManager(f.sp.Trie).KVGet(bridgeLockKey(1), &rec)
	if err != nil || !found {
		t.Fatalf("the lock record: %v %v", found, err)
	}
	if !bytes.Equal(rec.Sender, f.addr["alice"]) || rec.Amount.Cmp(wei(500)) != 0 || rec.DestChain != bridgeTestChain || rec.Dest != strings.ToLower(mixed) || rec.Height != 100 {
		t.Fatalf("the lock record is %+v", rec)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.locked"), map[string]string{
		"id": "1", "sender": ammAddrString(f.addr["alice"]), "amountWei": wei(500).String(), "destChain": bridgeTestChain, "dest": strings.ToLower(mixed),
	})
	// One unit above the cap is refused.
	if err := f.send("alice", types.TxTypeBridgeLock, plus(wei(500), 1), lockPayload()); !errors.Is(err, ErrBridgeAboveMax) {
		t.Fatalf("a lock one unit above the cap: %v", err)
	}
	// With no cap, the whole balance can be locked, and one more unit cannot.
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100})
	left, _ := f.balances(f.addr["alice"])
	if err := f.send("alice", types.TxTypeBridgeLock, left, lockPayload()); err != nil {
		t.Fatalf("a lock of the whole balance: %v", err)
	}
	if z, _ := f.balances(f.addr["alice"]); z.Sign() != 0 {
		t.Fatalf("alice still holds %s", z)
	}
	if err := f.send("alice", types.TxTypeBridgeLock, one(), lockPayload()); !errors.Is(err, ErrBridgeInsufficientBalance) {
		t.Fatalf("a lock with nothing left: %v", err)
	}
	if view, _ := f.sp.BridgeStatus(); view.NextLockID != 3 || view.LockedWei.Cmp(wei(1_000)) != 0 {
		t.Fatalf("status %+v, want the next id 3 and 1,000 locked", view)
	}
	f.checkInvariants("after the edge locks")
}

func TestBridgeReleaseEdgesRecordEventAndNonce(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.send("alice", types.TxTypeBridgeLock, wei(100), lockPayload()); err != nil {
		t.Fatal(err)
	}
	release := func(id, to []byte, amount *big.Int) error {
		p := bridgeReleasePayload{Op: BridgeOpRelease, BurnID: id, Recipient: to, Amount: amount}
		if digest, err := BridgeReleaseDigest(bridgeNetworkID(f.sp), id, to, amount); err == nil {
			for _, s := range []string{"signer1", "signer2"} {
				sig, _ := ethcrypto.Sign(digest[:], f.keys[s].PrivateKey)
				p.Signatures = append(p.Signatures, sig)
			}
		}
		return f.send("relayer", types.TxTypeBridgeRelease, big.NewInt(0), p)
	}
	if err := release(bytes.Repeat([]byte{7}, 64), f.addr["bob"], wei(1)); err != nil {
		t.Fatalf("a burn id of 64 bytes: %v", err)
	}
	if err := release([]byte{9}, f.addr["bob"], wei(1)); err != nil {
		t.Fatalf("a burn id of one byte: %v", err)
	}
	if err := release(bytes.Repeat([]byte{7}, 65), f.addr["bob"], wei(1)); !errors.Is(err, ErrBridgeInvalidPayload) {
		t.Fatalf("a burn id of 65 bytes: %v", err)
	}
	if err := release(nil, f.addr["bob"], wei(1)); !errors.Is(err, ErrBridgeInvalidPayload) {
		t.Fatalf("an empty burn id: %v", err)
	}
	for _, n := range []int{0, 19, 21} {
		if err := release([]byte("len"), bytes.Repeat([]byte{3}, n), wei(1)); !errors.Is(err, ErrBridgeInvalidPayload) {
			t.Fatalf("a recipient of %d bytes: %v", n, err)
		}
	}
	if err := release([]byte("to-escrow"), bridgeEscrowAddress(), wei(1)); !errors.Is(err, ErrBridgeAdminWallet) {
		t.Fatalf("a release to the escrow itself: %v", err)
	}
	nonceBefore := f.nonceOf("relayer")
	id := []byte("burn-record")
	if err := release(id, f.addr["bob"], wei(5)); err != nil {
		t.Fatal(err)
	}
	if got := f.nonceOf("relayer"); got != nonceBefore+1 {
		t.Fatalf("nonce %d, want %d", got, nonceBefore+1)
	}
	var rec bridgeReleaseRecord
	found, err := nhbstate.NewManager(f.sp.Trie).KVGet(bridgeReleaseKey(id), &rec)
	if err != nil || !found {
		t.Fatalf("the release record: %v %v", found, err)
	}
	if !bytes.Equal(rec.Recipient, f.addr["bob"]) || rec.Amount.Cmp(wei(5)) != 0 || rec.Height != 100 {
		t.Fatalf("the release record is %+v", rec)
	}
	expectAttrs(t, lastEvent(t, f.sp, "bridge.released"), map[string]string{
		"burnId": hex.EncodeToString(id), "recipient": ammAddrString(f.addr["bob"]), "amountWei": wei(5).String(),
	})
	if view, _ := f.sp.BridgeStatus(); view.ReleasedWei.Cmp(wei(7)) != 0 || view.OutstandingWei.Cmp(wei(93)) != 0 {
		t.Fatalf("status %+v, want 7 released and 93 outstanding", view)
	}
	f.checkInvariants("after the edge releases")
}

// The escrow must hold what is released even when the books say it should (state that no transaction can produce, but
// the check is the last line of defence), and the bridge invariant must notice either way the books can be wrong.
func TestBridgeReleaseNeedsTheEscrowToHoldItAndTheInvariantNoticesAWrongBook(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.send("alice", types.TxTypeBridgeLock, wei(100), lockPayload()); err != nil {
		t.Fatal(err)
	}
	if err := f.sp.CheckBridgeInvariant(); err != nil {
		t.Fatalf("a healthy escrow: %v", err)
	}
	escrow, _ := f.sp.getAccount(bridgeEscrowAddress())
	escrow.BalanceZNHB = wei(5)
	if err := f.sp.setAccount(bridgeEscrowAddress(), escrow); err != nil {
		t.Fatal(err)
	}
	if err := f.release("x", "bob", wei(10)); !errors.Is(err, ErrBridgeEscrowShort) {
		t.Fatalf("a release the escrow cannot pay: %v", err)
	}
	if err := f.sp.CheckBridgeInvariant(); err == nil || !strings.Contains(err.Error(), "escrow holds") {
		t.Fatalf("an escrow that holds less than what is outstanding: %v", err)
	}

	g := newBridgeFixture(t)
	if err := nhbstate.NewManager(g.sp.Trie).KVPut([]byte("bridge/totals"), bridgeTotals{Locked: wei(1), Released: wei(5)}); err != nil {
		t.Fatal(err)
	}
	if err := g.sp.CheckBridgeInvariant(); err == nil || !strings.Contains(err.Error(), "released") {
		t.Fatalf("more released than locked: %v", err)
	}
}

// ---- pool: who may

func TestNoProviderConfiguredMatchesNobodyAndTheAdminWalletNeverUsesThePool(t *testing.T) {
	f := newBCFixture(t, BridgeSettings{}, AmmSettings{})
	f.sp.SetAmm(AmmSettings{ActivationHeight: 100}) // no provider
	if f.sp.ammIsProvider(make([]byte, 20)) {
		t.Fatalf("the zero address matched a pool with no provider")
	}

	// A provider that is also the admin wallet may still not add or remove liquidity, and the admin wallet and the pool's own
	// account may not swap. (The admin wallet has no key in this fixture, so the guards are called directly.)
	g := newPoolFixture(t)
	s := g.sp.amm.clone()
	copy(s.Provider[:], g.admin[:])
	s.HasProvider = true
	g.sp.SetAmm(s)
	data, err := rlpEncode(ammAddPayload{Znhb: wei(10), Nhb: wei(1), MinShares: big.NewInt(0)})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.sp.applyAmmAddLiquidityTransaction(&types.Transaction{Type: types.TxTypeAmmAddLiquidity, Data: data}, g.admin[:]); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("liquidity from the admin wallet: %v", err)
	}
	if err := g.sp.applyAmmRemoveLiquidityTransaction(&types.Transaction{Type: types.TxTypeAmmRemoveLiquidity, Data: data}, g.admin[:]); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("a removal by the admin wallet: %v", err)
	}
	swapData, err := rlpEncode(ammSwapPayload{Dir: AmmDirZnhbToNhb, AmountIn: wei(1), MinOut: big.NewInt(0)})
	if err != nil {
		t.Fatal(err)
	}
	swapTx := &types.Transaction{Type: types.TxTypeAmmSwap, Data: swapData}
	if err := g.sp.applyAmmSwapTransaction(swapTx, g.admin[:]); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("a swap by the admin wallet: %v", err)
	}
	if err := g.sp.applyAmmSwapTransaction(swapTx, ammPoolAddress()); !errors.Is(err, ErrAmmAdminWallet) {
		t.Fatalf("a swap by the pool's own account: %v", err)
	}
}

// ---- pool: adding

func TestAmmAddLiquidityEdgesRecordEventAndNonce(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	view, _ := f.sp.AmmPool()
	d, err := amm.AddLiquidity(view.ReserveZnhbWei, view.ReserveNhbWei, view.TotalShares, wei(10_000), wei(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	add := func(z, n, minShares *big.Int) error {
		return f.send("provider", types.TxTypeAmmAddLiquidity, nil, ammAddPayload{Znhb: z, Nhb: n, MinShares: minShares})
	}
	pz, pn := f.balances(f.addr["provider"])
	if err := add(wei(10_000), wei(1_000_000), plus(d.Shares, 1)); !errors.Is(err, ErrAmmSlippage) {
		t.Fatalf("one share above what the deposit mints: %v", err)
	}
	if z, n := f.balances(f.addr["provider"]); z.Cmp(pz) != 0 || n.Cmp(pn) != 0 {
		t.Fatalf("a refused deposit moved the provider's balances")
	}
	if err := add(big.NewInt(0), wei(1), big.NewInt(0)); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a zero ZNHB amount: %v", err)
	}
	if err := add(wei(1), big.NewInt(0), big.NewInt(0)); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a zero NHB amount: %v", err)
	}
	nonceBefore := f.nonceOf("provider")
	if err := add(wei(10_000), wei(1_000_000), d.Shares); err != nil {
		t.Fatalf("a deposit that mints exactly the minimum: %v", err)
	}
	if got := f.nonceOf("provider"); got != nonceBefore+1 {
		t.Fatalf("nonce %d, want %d", got, nonceBefore+1)
	}
	owned := new(big.Int)
	if _, err := nhbstate.NewManager(f.sp.Trie).KVGet(ammSharesKey(f.addr["provider"]), &owned); err != nil {
		t.Fatal(err)
	}
	if want := new(big.Int).Add(view.TotalShares, d.Shares); owned.Cmp(want) != 0 {
		t.Fatalf("the provider holds %s shares, want %s", owned, want)
	}
	after, _ := f.sp.AmmPool()
	if after.ReserveZnhbWei.Cmp(new(big.Int).Add(view.ReserveZnhbWei, d.Znhb)) != 0 || after.ReserveNhbWei.Cmp(new(big.Int).Add(view.ReserveNhbWei, d.Nhb)) != 0 || after.TotalShares.Cmp(new(big.Int).Add(view.TotalShares, d.Shares)) != 0 {
		t.Fatalf("pool after the deposit: %+v", after)
	}
	expectAttrs(t, lastEvent(t, f.sp, "amm.liquidity_added"), map[string]string{
		"provider": ammAddrString(f.addr["provider"]), "znhbWei": d.Znhb.String(), "nhbWei": d.Nhb.String(), "shares": d.Shares.String(),
	})
	f.checkInvariants("after the edge deposits")
}

func TestAmmAddLiquidityNeedsTheProviderToHoldBothAssets(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool() // the matching deposit of 10,000 ZNHB needs 500 NHB
	add := func() error {
		return f.send("provider", types.TxTypeAmmAddLiquidity, nil, ammAddPayload{Znhb: wei(10_000), Nhb: wei(1_000_000), MinShares: big.NewInt(0)})
	}
	f.fund("provider", wei(5), wei(500_000))
	if err := add(); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("too little ZNHB: %v", err)
	}
	f.fund("provider", wei(2_000_000), wei(0))
	if err := add(); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("no NHB: %v", err)
	}
}

// ---- pool: removing

func TestAmmRemoveLiquidityEdges(t *testing.T) {
	// On an empty pool, zero shares is a payload error (judged from the bytes alone), one share is an empty-pool error.
	e := newPoolFixture(t)
	if err := e.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: big.NewInt(0), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("zero shares from an empty pool: %v", err)
	}
	if err := e.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: one(), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmEmptyPool) {
		t.Fatalf("one share from an empty pool: %v", err)
	}

	f := newPoolFixture(t)
	f.seedPool()
	view, _ := f.sp.AmmPool()
	k := new(big.Int).Div(view.TotalShares, big.NewInt(10))
	z, n, err := amm.RemoveLiquidity(view.ReserveZnhbWei, view.ReserveNhbWei, view.TotalShares, k)
	if err != nil {
		t.Fatal(err)
	}
	remove := func(shares, minZ, minN *big.Int) error {
		return f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: shares, MinZnhb: minZ, MinNhb: minN})
	}
	if err := remove(k, big.NewInt(0), plus(n, 1)); !errors.Is(err, ErrAmmSlippage) {
		t.Fatalf("one unit above the NHB paid: %v", err)
	}
	if err := remove(k, plus(z, 1), big.NewInt(0)); !errors.Is(err, ErrAmmSlippage) {
		t.Fatalf("one unit above the ZNHB paid: %v", err)
	}
	pz, pn := f.balances(f.addr["provider"])
	nonceBefore := f.nonceOf("provider")
	if err := remove(k, z, n); err != nil {
		t.Fatalf("a removal asking exactly for what it gets: %v", err)
	}
	if got := f.nonceOf("provider"); got != nonceBefore+1 {
		t.Fatalf("nonce %d, want %d", got, nonceBefore+1)
	}
	if az, an := f.balances(f.addr["provider"]); az.Cmp(new(big.Int).Add(pz, z)) != 0 || an.Cmp(new(big.Int).Add(pn, n)) != 0 {
		t.Fatalf("the provider received %s/%s, want %s/%s", new(big.Int).Sub(az, pz), new(big.Int).Sub(an, pn), z, n)
	}
	owned := new(big.Int)
	if _, err := nhbstate.NewManager(f.sp.Trie).KVGet(ammSharesKey(f.addr["provider"]), &owned); err != nil {
		t.Fatal(err)
	}
	want := new(big.Int).Sub(view.TotalShares, k)
	after, _ := f.sp.AmmPool()
	if owned.Cmp(want) != 0 || after.TotalShares.Cmp(want) != 0 {
		t.Fatalf("shares held %s, in total %s, want %s", owned, after.TotalShares, want)
	}
	expectAttrs(t, lastEvent(t, f.sp, "amm.liquidity_removed"), map[string]string{
		"provider": ammAddrString(f.addr["provider"]), "znhbWei": z.String(), "nhbWei": n.String(), "shares": k.String(),
	})
	f.checkInvariants("after the removal")

	// An account that holds no shares cannot take liquidity out, even if it is configured as the provider.
	s := f.sp.amm.clone()
	copy(s.Provider[:], f.addr["bob"])
	f.sp.SetAmm(s)
	bz, bn := f.balances(f.addr["bob"])
	if err := f.send("bob", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: one(), MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)}); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a removal by an account with no shares: %v", err)
	}
	if az, an := f.balances(f.addr["bob"]); az.Cmp(bz) != 0 || an.Cmp(bn) != 0 {
		t.Fatalf("an account with no shares was paid")
	}
	f.checkInvariants("after the refused removal")
}

func TestAmmRemoveLiquidityStopsExactlyAtTheFloors(t *testing.T) {
	run := func(mod func(s *AmmSettings, remainZ, remainN *big.Int)) error {
		f := newPoolFixture(t)
		f.seedPool()
		view, _ := f.sp.AmmPool()
		k := new(big.Int).Div(view.TotalShares, big.NewInt(10))
		z, n, err := amm.RemoveLiquidity(view.ReserveZnhbWei, view.ReserveNhbWei, view.TotalShares, k)
		if err != nil {
			t.Fatal(err)
		}
		s := f.sp.amm.clone()
		mod(&s, new(big.Int).Sub(view.ReserveZnhbWei, z), new(big.Int).Sub(view.ReserveNhbWei, n))
		f.sp.SetAmm(s)
		return f.send("provider", types.TxTypeAmmRemoveLiquidity, nil, ammRemovePayload{Shares: k, MinZnhb: big.NewInt(0), MinNhb: big.NewInt(0)})
	}
	if err := run(func(s *AmmSettings, rz, rn *big.Int) { s.MinReserveZNHB = rz }); err != nil {
		t.Fatalf("landing exactly on the ZNHB floor: %v", err)
	}
	if err := run(func(s *AmmSettings, rz, rn *big.Int) { s.MinReserveZNHB = plus(rz, 1) }); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("one unit under the ZNHB floor: %v", err)
	}
	if err := run(func(s *AmmSettings, rz, rn *big.Int) { s.MinReserveNHB = rn }); err != nil {
		t.Fatalf("landing exactly on the NHB floor: %v", err)
	}
	if err := run(func(s *AmmSettings, rz, rn *big.Int) { s.MinReserveNHB = plus(rn, 1) }); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("one unit under the NHB floor: %v", err)
	}
}

// ---- pool: swapping

func swapFixture(t *testing.T) *bcFixture {
	t.Helper()
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(150)
	return f
}

func TestAmmSwapRefusesAWrongDirectionOrAmountBeforeLookingAtTheState(t *testing.T) {
	f := newPoolFixture(t)
	f.seedPool()
	f.atHeight(149) // swaps are still closed
	if err := f.swap("alice", 2, wei(1), big.NewInt(0)); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a direction of 2: %v", err)
	}
	if err := f.swap("alice", AmmDirZnhbToNhb, big.NewInt(0), big.NewInt(0)); !errors.Is(err, ErrAmmInvalidPayload) {
		t.Fatalf("a zero amount while swaps are closed: %v", err)
	}
	if _, err := f.sp.AmmQuote(2, wei(1)); err == nil {
		t.Fatalf("a quote in direction 2 on a pool with liquidity")
	}
}

func TestAmmSwapInputIsCappedExactly(t *testing.T) {
	f := swapFixture(t) // 5 percent of 1,000,000 ZNHB is 50,000
	if err := f.swap("alice", AmmDirZnhbToNhb, plus(wei(50_000), 1), big.NewInt(0)); !errors.Is(err, ErrAmmTooLarge) {
		t.Fatalf("one unit above the cap: %v", err)
	}
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(50_000), big.NewInt(0)); err != nil {
		t.Fatalf("exactly the cap: %v", err)
	}
	g := swapFixture(t) // 5 percent of 50,000 NHB is 2,500, measured on the reserve the swap adds to
	if err := g.swap("alice", AmmDirNhbToZnhb, plus(wei(2_500), 1), big.NewInt(0)); !errors.Is(err, ErrAmmTooLarge) {
		t.Fatalf("NHB in, one unit above the cap: %v", err)
	}
	if err := g.swap("alice", AmmDirNhbToZnhb, wei(2_500), big.NewInt(0)); err != nil {
		t.Fatalf("NHB in, exactly the cap: %v", err)
	}
}

func TestAmmSwapStopsExactlyAtTheOutputReserveFloor(t *testing.T) {
	for _, dir := range []uint8{AmmDirZnhbToNhb, AmmDirNhbToZnhb} {
		run := func(extra int64) error {
			f := swapFixture(t)
			in := wei(1_000)
			out, err := f.sp.AmmQuote(dir, in)
			if err != nil {
				t.Fatal(err)
			}
			view, _ := f.sp.AmmPool()
			reserveOut := view.ReserveNhbWei
			if dir == AmmDirNhbToZnhb {
				reserveOut = view.ReserveZnhbWei
			}
			floor := plus(new(big.Int).Sub(reserveOut, out), extra)
			s := f.sp.amm.clone()
			if dir == AmmDirZnhbToNhb {
				s.MinReserveNHB = floor
			} else {
				s.MinReserveZNHB = floor
			}
			f.sp.SetAmm(s)
			return f.swap("alice", dir, in, big.NewInt(0))
		}
		if err := run(0); err != nil {
			t.Fatalf("direction %d landing exactly on the floor: %v", dir, err)
		}
		if err := run(1); !errors.Is(err, ErrAmmReserveFloor) {
			t.Fatalf("direction %d one unit under the floor: %v", dir, err)
		}
	}
}

// Each direction is held only by the floor of the reserve it takes from.
func TestAmmSwapUsesTheFloorOfTheAssetItPaysOut(t *testing.T) {
	high := wei(10_000_000) // above the whole reserve of either asset
	f := swapFixture(t)
	s := f.sp.amm.clone()
	s.MinReserveZNHB = high
	f.sp.SetAmm(s)
	if err := f.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); err != nil {
		t.Fatalf("paying out NHB under a huge ZNHB floor: %v", err)
	}
	if err := f.swap("alice", AmmDirNhbToZnhb, wei(1_000), big.NewInt(0)); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("paying out ZNHB under a huge ZNHB floor: %v", err)
	}
	g := swapFixture(t)
	s = g.sp.amm.clone()
	s.MinReserveNHB = high
	g.sp.SetAmm(s)
	if err := g.swap("alice", AmmDirNhbToZnhb, wei(1_000), big.NewInt(0)); err != nil {
		t.Fatalf("paying out ZNHB under a huge NHB floor: %v", err)
	}
	if err := g.swap("alice", AmmDirZnhbToNhb, wei(1_000), big.NewInt(0)); !errors.Is(err, ErrAmmReserveFloor) {
		t.Fatalf("paying out NHB under a huge NHB floor: %v", err)
	}
}

func TestAmmSwapNeedsTheInputAssetAndExactlyEnoughIsEnough(t *testing.T) {
	in := wei(1_000)
	f := swapFixture(t)
	f.fund("alice", wei(100_000), wei(0)) // ZNHB but no NHB
	if err := f.swap("alice", AmmDirNhbToZnhb, in, big.NewInt(0)); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("NHB in with no NHB: %v", err)
	}
	f.fund("alice", wei(0), wei(100_000)) // NHB but no ZNHB
	if err := f.swap("alice", AmmDirZnhbToNhb, in, big.NewInt(0)); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("ZNHB in with no ZNHB: %v", err)
	}
	f.fund("alice", plus(in, -1), wei(0))
	if err := f.swap("alice", AmmDirZnhbToNhb, in, big.NewInt(0)); !errors.Is(err, ErrAmmInsufficientBalance) {
		t.Fatalf("one unit short of the ZNHB: %v", err)
	}
	f.fund("alice", in, wei(0))
	if err := f.swap("alice", AmmDirZnhbToNhb, in, big.NewInt(0)); err != nil {
		t.Fatalf("exactly the ZNHB: %v", err)
	}
	if z, _ := f.balances(f.addr["alice"]); z.Sign() != 0 {
		t.Fatalf("ZNHB left over: %s", z)
	}
	g := swapFixture(t)
	g.fund("alice", wei(0), in)
	if err := g.swap("alice", AmmDirNhbToZnhb, in, big.NewInt(0)); err != nil {
		t.Fatalf("exactly the NHB: %v", err)
	}
	if _, n := g.balances(g.addr["alice"]); n.Sign() != 0 {
		t.Fatalf("NHB left over: %s", n)
	}
}

func TestAmmSwapRecordsTheTradeTheFeeTheEventAndTheNonce(t *testing.T) {
	f := swapFixture(t)
	in := wei(1_000)
	quote, err := f.sp.AmmQuote(AmmDirZnhbToNhb, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.swap("alice", AmmDirZnhbToNhb, in, plus(quote, 1)); !errors.Is(err, ErrAmmSlippage) {
		t.Fatalf("a minimum one unit above the payout: %v", err)
	}
	nonceBefore := f.nonceOf("alice")
	if err := f.swap("alice", AmmDirZnhbToNhb, in, quote); err != nil {
		t.Fatalf("a minimum exactly at the payout: %v", err)
	}
	if got := f.nonceOf("alice"); got != nonceBefore+1 {
		t.Fatalf("nonce %d, want %d", got, nonceBefore+1)
	}
	fee := new(big.Int).Div(new(big.Int).Mul(in, big.NewInt(30)), big.NewInt(10_000))
	expectAttrs(t, lastEvent(t, f.sp, "amm.swap"), map[string]string{
		"id": "1", "trader": ammAddrString(f.addr["alice"]), "dir": "0", "amountIn": in.String(), "amountOut": quote.String(), "feeWei": fee.String(),
	})
	if err := f.swap("bob", AmmDirNhbToZnhb, wei(100), big.NewInt(0)); err != nil {
		t.Fatal(err)
	}
	trades, err := f.sp.AmmTrades(10)
	if err != nil || len(trades) != 2 {
		t.Fatalf("trades %v %v", trades, err)
	}
	newest, oldest := trades[0], trades[1]
	if newest.ID != 2 || oldest.ID != 1 || newest.Direction != AmmDirNhbToZnhb || oldest.Direction != AmmDirZnhbToNhb {
		t.Fatalf("trades %+v, want the NHB-in trade 2 first and the ZNHB-in trade 1 after it", trades)
	}
	if oldest.Height != 150 || oldest.Trader != ammAddrString(f.addr["alice"]) || oldest.AmountIn.Cmp(in) != 0 || oldest.AmountOut.Cmp(quote) != 0 || oldest.FeeWei.Cmp(fee) != 0 {
		t.Fatalf("the first trade is %+v", oldest)
	}
	if view, _ := f.sp.AmmPool(); view.TradeCount != 2 {
		t.Fatalf("trade count %d, want 2", view.TradeCount)
	}
	f.checkInvariants("after the recorded swaps")
}

func TestAmmTradesAreCappedAtTwoHundred(t *testing.T) {
	f := swapFixture(t)
	for i := 0; i < 210; i++ {
		if err := f.swap("alice", AmmDirZnhbToNhb, wei(10), big.NewInt(0)); err != nil {
			t.Fatalf("swap %d: %v", i, err)
		}
	}
	for limit, want := range map[int]int{0: 50, -1: 50, 5: 5, 200: 200, 500: 200} {
		got, err := f.sp.AmmTrades(limit)
		if err != nil || len(got) != want {
			t.Fatalf("AmmTrades(%d) returned %d trades (%v), want %d", limit, len(got), err, want)
		}
	}
}

// ---- pool: the invariant

func TestAmmInvariantNoticesAPoolThatIsShortOrInconsistent(t *testing.T) {
	healthy := swapFixture(t)
	if err := healthy.sp.CheckAmmInvariant(); err != nil {
		t.Fatalf("a healthy pool: %v", err)
	}
	shortBy := func(znhb, nhb int64) error {
		f := swapFixture(t)
		view, _ := f.sp.AmmPool()
		acc, _ := f.sp.getAccount(ammPoolAddress())
		acc.BalanceZNHB = plus(view.ReserveZnhbWei, -znhb)
		acc.BalanceNHB = plus(view.ReserveNhbWei, -nhb)
		if err := f.sp.setAccount(ammPoolAddress(), acc); err != nil {
			t.Fatal(err)
		}
		return f.sp.CheckAmmInvariant()
	}
	if err := shortBy(1, 0); err == nil {
		t.Fatalf("a pool account one unit short of ZNHB")
	}
	if err := shortBy(0, 1); err == nil {
		t.Fatalf("a pool account one unit short of NHB")
	}
	if err := shortBy(0, 0); err != nil {
		t.Fatalf("a pool account that holds exactly the reserves: %v", err)
	}
	write := func(p ammPoolState) error {
		f := swapFixture(t)
		if err := nhbstate.NewManager(f.sp.Trie).KVPut([]byte("amm/pool"), p); err != nil {
			t.Fatal(err)
		}
		return f.sp.CheckAmmInvariant()
	}
	if err := write(ammPoolState{ReserveZnhb: new(big.Int), ReserveNhb: new(big.Int), TotalShares: big.NewInt(5)}); err == nil {
		t.Fatalf("shares with no reserves")
	}
	if err := write(ammPoolState{ReserveZnhb: wei(1), ReserveNhb: wei(1), TotalShares: new(big.Int)}); err == nil {
		t.Fatalf("reserves with no shares")
	}
}
