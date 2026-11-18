package core

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/config"
	"nhbchain/crypto"
)

func configAddress(t *testing.T) (string, [20]byte) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	var raw [20]byte
	copy(raw[:], key.PubKey().Address().Bytes())
	return crypto.MustNewAddress(crypto.NHBPrefix, raw[:]).String(), raw
}

// Every value of both blocks, read when the block is activated: what is taken as given, what is defaulted, and each
// value the mains must refuse to start with.
func TestBridgeAmmSettingsFromConfigEveryValue(t *testing.T) {
	signer1Text, signer1Raw := configAddress(t)
	signer2Text, _ := configAddress(t)
	signer3Text, _ := configAddress(t)
	guardianText, guardianRaw := configAddress(t)
	providerText, providerRaw := configAddress(t)

	goodBridge := func() *config.Config {
		cfg := newTestConfigForBridge()
		cfg.Bridge.BridgeActivationHeight = 100
		cfg.Bridge.MaxLockZNHBWei = "5000"
		cfg.Bridge.Signers = []string{signer1Text, signer2Text, signer3Text}
		cfg.Bridge.SignerThreshold = 2
		cfg.Bridge.Guardian = guardianText
		cfg.Bridge.MaxReleaseZNHBWei = "1000"
		cfg.Bridge.WindowBlocks = 7200
		cfg.Bridge.WindowCapZNHBWei = "4000"
		cfg.Bridge.DelayAboveZNHBWei = "500"
		cfg.Bridge.DelayBlocks = 600
		return cfg
	}
	goodAmm := func() *config.Config {
		cfg := newTestConfigForBridge()
		cfg.Amm.AmmActivationHeight = 100
		cfg.Amm.SwapStartHeight = 150
		cfg.Amm.FeeBps = 30
		cfg.Amm.MaxSwapBps = 700
		cfg.Amm.LiquidityProvider = providerText
		cfg.Amm.MinReserveZNHBWei = "900"
		cfg.Amm.MinReserveNHBWei = "400"
		return cfg
	}

	// A good bridge block is read as written.
	b, a, err := BridgeAmmSettingsFromConfig(goodBridge())
	if err != nil {
		t.Fatalf("a good bridge block: %v", err)
	}
	if b.ActivationHeight != 100 || b.MaxLockWei == nil || b.MaxLockWei.Cmp(big.NewInt(5000)) != 0 || len(b.Params) != 1 {
		t.Fatalf("bridge read as %+v", b)
	}
	p := b.Params[0]
	if p.FromHeight != 100 || len(p.Signers) != 3 || p.Signers[0] != signer1Raw || p.Threshold != 2 || p.Guardian != guardianRaw ||
		p.MaxReleaseWei.Cmp(big.NewInt(1000)) != 0 || p.WindowBlocks != 7200 || p.WindowCapWei.Cmp(big.NewInt(4000)) != 0 ||
		p.DelayAboveWei == nil || p.DelayAboveWei.Cmp(big.NewInt(500)) != 0 || p.DelayBlocks != 600 {
		t.Fatalf("bridge rules read as %+v", p)
	}
	if a.ActivationHeight != 0 || a.HasProvider {
		t.Fatalf("an unrelated pool block was filled in: %+v", a)
	}
	// No cap written means no cap.
	cfg := goodBridge()
	cfg.Bridge.MaxLockZNHBWei = ""
	if b, _, err = BridgeAmmSettingsFromConfig(cfg); err != nil || b.MaxLockWei != nil {
		t.Fatalf("an empty cap must mean none: %+v %v", b, err)
	}
	for _, bad := range []string{"-1", "ten", "1.5", "0x10"} {
		cfg = goodBridge()
		cfg.Bridge.MaxLockZNHBWei = bad
		if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "MaxLockZNHBWei") {
			t.Fatalf("a cap of %q: %v", bad, err)
		}
	}
	cfg = goodBridge()
	cfg.Bridge.Guardian = ""
	if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "Guardian") {
		t.Fatalf("an activated bridge with no guardian: %v", err)
	}

	// A good pool block is read as written.
	b, a, err = BridgeAmmSettingsFromConfig(goodAmm())
	if err != nil {
		t.Fatalf("a good pool block: %v", err)
	}
	if a.ActivationHeight != 100 || a.SwapStartHeight != 150 || a.FeeBps != 30 || a.MaxSwapBps != 700 || !a.HasProvider || a.Provider != providerRaw ||
		a.MinReserveZNHB.Cmp(big.NewInt(900)) != 0 || a.MinReserveNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("pool read as %+v", a)
	}
	if b.ActivationHeight != 0 || len(b.Params) != 0 {
		t.Fatalf("an unrelated bridge block was filled in: %+v", b)
	}

	// Defaults: no swap cap written means 5 percent; no floor (or a zero floor) means one unit, never zero.
	cfg = goodAmm()
	cfg.Amm.MaxSwapBps = 0
	cfg.Amm.MinReserveZNHBWei, cfg.Amm.MinReserveNHBWei = "", ""
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.MaxSwapBps != 500 || a.MinReserveZNHB.Cmp(big.NewInt(1)) != 0 || a.MinReserveNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("defaults: %+v %v", a, err)
	}
	cfg = goodAmm()
	cfg.Amm.MinReserveZNHBWei, cfg.Amm.MinReserveNHBWei = "0", "0"
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.MinReserveZNHB.Cmp(big.NewInt(1)) != 0 || a.MinReserveNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("a floor of zero must become one: %+v %v", a, err)
	}
	// Swaps may open at the pool's own height, or never (0), but not before it.
	cfg = goodAmm()
	cfg.Amm.SwapStartHeight = 100
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.SwapStartHeight != 100 {
		t.Fatalf("swaps opening with the pool: %+v %v", a, err)
	}
	cfg = goodAmm()
	cfg.Amm.SwapStartHeight = 0
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.SwapStartHeight != 0 {
		t.Fatalf("swaps left closed: %+v %v", a, err)
	}
	cfg = goodAmm()
	cfg.Amm.SwapStartHeight = 99
	if _, _, err = BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "SwapStartHeight") {
		t.Fatalf("swaps opening before the pool: %v", err)
	}
	// A fee must stay below 100 percent.
	cfg = goodAmm()
	cfg.Amm.FeeBps = 9_999
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.FeeBps != 9_999 {
		t.Fatalf("the highest fee: %+v %v", a, err)
	}
	cfg.Amm.FeeBps = 10_000
	if _, _, err = BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "FeeBps") {
		t.Fatalf("a fee of 100 percent: %v", err)
	}
	// The per-swap cap may be anything up to 100 percent.
	cfg = goodAmm()
	cfg.Amm.MaxSwapBps = 10_000
	if _, a, err = BridgeAmmSettingsFromConfig(cfg); err != nil || a.MaxSwapBps != 10_000 {
		t.Fatalf("a cap of 100 percent: %+v %v", a, err)
	}
	cfg.Amm.MaxSwapBps = 10_001
	if _, _, err = BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "MaxSwapBps") {
		t.Fatalf("a cap above 100 percent: %v", err)
	}
	for _, bad := range []string{"-1", "ten", "1.5"} {
		cfg = goodAmm()
		cfg.Amm.MinReserveZNHBWei = bad
		if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "MinReserveZNHBWei") {
			t.Fatalf("a ZNHB floor of %q: %v", bad, err)
		}
		cfg = goodAmm()
		cfg.Amm.MinReserveNHBWei = bad
		if _, _, err := BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "MinReserveNHBWei") {
			t.Fatalf("an NHB floor of %q: %v", bad, err)
		}
	}
	cfg = goodAmm()
	cfg.Amm.LiquidityProvider = ""
	if _, _, err = BridgeAmmSettingsFromConfig(cfg); err == nil || !strings.Contains(err.Error(), "LiquidityProvider") {
		t.Fatalf("an activated pool with no provider: %v", err)
	}
	// A config with nothing at all is off.
	if b, a, err = BridgeAmmSettingsFromConfig(nil); err != nil || b.ActivationHeight != 0 || a.ActivationHeight != 0 {
		t.Fatalf("a missing config: %+v %+v %v", b, a, err)
	}
}

// The settings a processor or node holds are its own copies: changing the caller's big numbers afterwards changes
// nothing on the chain side, and a copy of the processor does not share them either.
func TestBridgeAndPoolSettingsAreNeverSharedWithTheCaller(t *testing.T) {
	bridgeCap := big.NewInt(100)
	floorZ, floorN := big.NewInt(900), big.NewInt(400)
	bridge := BridgeSettings{ActivationHeight: 7, MaxLockWei: bridgeCap}
	pool := AmmSettings{ActivationHeight: 8, MinReserveZNHB: floorZ, MinReserveNHB: floorN}

	if c := bridge.clone(); c.MaxLockWei == bridge.MaxLockWei || c.MaxLockWei.Cmp(bridgeCap) != 0 {
		t.Fatalf("a bridge clone shares or loses the cap")
	}
	if c := pool.clone(); c.MinReserveZNHB == pool.MinReserveZNHB || c.MinReserveNHB == pool.MinReserveNHB || c.MinReserveZNHB.Cmp(floorZ) != 0 || c.MinReserveNHB.Cmp(floorN) != 0 {
		t.Fatalf("a pool clone shares or loses a floor")
	}

	f := newBCFixture(t, BridgeSettings{}, AmmSettings{})
	f.sp.SetBridge(bridge)
	f.sp.SetAmm(pool)
	bridgeCap.SetInt64(1)
	floorZ.SetInt64(1)
	floorN.SetInt64(1)
	if f.sp.bridge.MaxLockWei.Int64() != 100 || f.sp.amm.MinReserveZNHB.Int64() != 900 || f.sp.amm.MinReserveNHB.Int64() != 400 {
		t.Fatalf("the processor shares the caller's numbers: cap %v, floors %v / %v", f.sp.bridge.MaxLockWei, f.sp.amm.MinReserveZNHB, f.sp.amm.MinReserveNHB)
	}
	cp, err := f.sp.Copy()
	if err != nil {
		t.Fatal(err)
	}
	f.sp.bridge.MaxLockWei.SetInt64(5)
	f.sp.amm.MinReserveZNHB.SetInt64(5)
	if cp.bridge.MaxLockWei.Int64() != 100 || cp.amm.MinReserveZNHB.Int64() != 900 {
		t.Fatalf("a copy of the processor shares the original's numbers")
	}

	node := newTestNode(t)
	nodeCap, nodeFloor := big.NewInt(77), big.NewInt(66)
	node.SetBridgeAndAmm(BridgeSettings{ActivationHeight: 1, MaxLockWei: nodeCap}, AmmSettings{ActivationHeight: 1, MinReserveZNHB: nodeFloor, MinReserveNHB: big.NewInt(1)})
	nodeCap.SetInt64(1)
	nodeFloor.SetInt64(1)
	node.stateMu.Lock()
	gotCap, gotFloor := node.bridgeSettings.MaxLockWei.Int64(), node.ammSettings.MinReserveZNHB.Int64()
	liveCap, liveFloor := node.state.bridge.MaxLockWei.Int64(), node.state.amm.MinReserveZNHB.Int64()
	node.stateMu.Unlock()
	if gotCap != 77 || gotFloor != 66 || liveCap != 77 || liveFloor != 66 {
		t.Fatalf("the node shares the caller's numbers: %d %d %d %d", gotCap, gotFloor, liveCap, liveFloor)
	}
}
