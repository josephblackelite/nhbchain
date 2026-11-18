package core

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/config"
	"nhbchain/crypto"
)

// BridgeSettings are the chain rules of the ZNHB bridge escrow (ledger PL-BC-C), parsed from config.BridgeConfig.
// The zero value is off.
type BridgeSettings struct {
	ActivationHeight uint64
	MaxLockWei       *big.Int // nil = no cap
	// Params are the release rules in force from each height, ascending; Params[0].FromHeight is the activation
	// height. A rotation is a later entry: nothing is inherited, and the entry in force at a block's height applies.
	Params []BridgeParams
}

// BridgeParams are the signers, the guardian and the limits of the bridge from FromHeight on (Stage 1 of the trust plan).
type BridgeParams struct {
	FromHeight    uint64
	Signers       [][20]byte
	Threshold     uint32
	Guardian      [20]byte
	MaxReleaseWei *big.Int
	WindowBlocks  uint64
	WindowCapWei  *big.Int
	DelayAboveWei *big.Int // nil = no waiting
	DelayBlocks   uint64
}

func (p BridgeParams) clone() BridgeParams {
	p.Signers = append([][20]byte(nil), p.Signers...)
	for _, f := range []**big.Int{&p.MaxReleaseWei, &p.WindowCapWei, &p.DelayAboveWei} {
		if *f != nil {
			*f = new(big.Int).Set(*f)
		}
	}
	return p
}

// paramsAt is the release rule set in force at height: the last entry whose FromHeight is not above it. It is nil
// before the first entry (the bridge is not active).
func (b BridgeSettings) paramsAt(height uint64) *BridgeParams {
	var found *BridgeParams
	for i := range b.Params {
		if b.Params[i].FromHeight <= height {
			found = &b.Params[i]
		}
	}
	return found
}

// AmmSettings are the chain rules of the native ZNHB/NHB pool (ledger PL-BC-B), parsed from config.AmmConfig.
// The zero value is off.
type AmmSettings struct {
	ActivationHeight uint64
	SwapStartHeight  uint64
	FeeBps           uint32
	MaxSwapBps       uint32 // already defaulted: never zero when the pool is on
	Provider         [20]byte
	HasProvider      bool
	MinReserveZNHB   *big.Int // never nil when the pool is on
	MinReserveNHB    *big.Int
}

func (b BridgeSettings) clone() BridgeSettings {
	if b.MaxLockWei != nil {
		b.MaxLockWei = new(big.Int).Set(b.MaxLockWei)
	}
	if b.Params != nil {
		params := make([]BridgeParams, len(b.Params))
		for i := range b.Params {
			params[i] = b.Params[i].clone()
		}
		b.Params = params
	}
	return b
}

func (a AmmSettings) clone() AmmSettings {
	if a.MinReserveZNHB != nil {
		a.MinReserveZNHB = new(big.Int).Set(a.MinReserveZNHB)
	}
	if a.MinReserveNHB != nil {
		a.MinReserveNHB = new(big.Int).Set(a.MinReserveNHB)
	}
	return a
}

func parseSettingAddress(name, value string) ([20]byte, error) {
	var out [20]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, fmt.Errorf("%s is required once the module is activated", name)
	}
	addr, err := crypto.DecodeAddress(trimmed)
	if err != nil {
		return out, fmt.Errorf("%s is not a valid address: %w", name, err)
	}
	raw := addr.Bytes()
	if len(raw) != len(out) {
		return out, fmt.Errorf("%s is not a 20 byte address", name)
	}
	copy(out[:], raw)
	return out, nil
}

func parseSettingWei(name, value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	n, ok := new(big.Int).SetString(trimmed, 10)
	if !ok || n.Sign() < 0 {
		return nil, fmt.Errorf("%s is not a non-negative decimal amount: %q", name, value)
	}
	return n, nil
}

// parseBridgeParams reads and checks one complete rule set (the activation set or a rotation). Nothing is defaulted:
// a bridge that is switched on with a missing or inconsistent limit must not start.
func parseBridgeParams(name string, fromHeight uint64, signers []string, threshold uint32, guardian, maxRelease string,
	windowBlocks uint64, windowCap, delayAbove string, delayBlocks uint64) (BridgeParams, error) {
	p := BridgeParams{FromHeight: fromHeight, Threshold: threshold, WindowBlocks: windowBlocks, DelayBlocks: delayBlocks}
	if len(signers) < 2 {
		return BridgeParams{}, fmt.Errorf("%s Signers needs at least two addresses: no single key may release ZNHB", name)
	}
	seen := map[[20]byte]bool{}
	for i, s := range signers {
		addr, err := parseSettingAddress(fmt.Sprintf("%s Signers[%d]", name, i), s)
		if err != nil {
			return BridgeParams{}, err
		}
		if seen[addr] {
			return BridgeParams{}, fmt.Errorf("%s Signers[%d] repeats an address", name, i)
		}
		seen[addr] = true
		p.Signers = append(p.Signers, addr)
	}
	if threshold < 2 || int(threshold) > len(signers) {
		return BridgeParams{}, fmt.Errorf("%s SignerThreshold %d must be between 2 and the %d signers", name, threshold, len(signers))
	}
	g, err := parseSettingAddress(name+" Guardian", guardian)
	if err != nil {
		return BridgeParams{}, err
	}
	if seen[g] {
		return BridgeParams{}, fmt.Errorf("%s Guardian is also a signer: the one that can stop the bridge must not be one that can release", name)
	}
	p.Guardian = g
	if p.MaxReleaseWei, err = parseSettingWei(name+" MaxReleaseZNHBWei", maxRelease); err != nil {
		return BridgeParams{}, err
	}
	if p.MaxReleaseWei == nil || p.MaxReleaseWei.Sign() == 0 {
		return BridgeParams{}, fmt.Errorf("%s MaxReleaseZNHBWei is required and must be above zero", name)
	}
	if windowBlocks == 0 {
		return BridgeParams{}, fmt.Errorf("%s WindowBlocks is required and must be above zero", name)
	}
	if p.WindowCapWei, err = parseSettingWei(name+" WindowCapZNHBWei", windowCap); err != nil {
		return BridgeParams{}, err
	}
	if p.WindowCapWei == nil || p.WindowCapWei.Cmp(p.MaxReleaseWei) < 0 {
		return BridgeParams{}, fmt.Errorf("%s WindowCapZNHBWei is required and must be at least MaxReleaseZNHBWei", name)
	}
	if p.DelayAboveWei, err = parseSettingWei(name+" DelayAboveZNHBWei", delayAbove); err != nil {
		return BridgeParams{}, err
	}
	switch {
	case p.DelayAboveWei == nil && delayBlocks != 0:
		return BridgeParams{}, fmt.Errorf("%s DelayBlocks is set but DelayAboveZNHBWei is empty: set both or neither", name)
	case p.DelayAboveWei != nil && delayBlocks == 0:
		return BridgeParams{}, fmt.Errorf("%s DelayAboveZNHBWei is set but DelayBlocks is zero: set both or neither", name)
	case p.DelayAboveWei != nil && p.DelayAboveWei.Cmp(p.MaxReleaseWei) > 0:
		return BridgeParams{}, fmt.Errorf("%s DelayAboveZNHBWei is above MaxReleaseZNHBWei, so no release would ever wait", name)
	}
	return p, nil
}

// BridgeAmmSettingsFromConfig reads both blocks. A block whose activation height is zero is off and its other
// values are not looked at (so an unfinished block can sit in the file); a block that is activated and has a bad
// value is an error, which the mains turn into a refusal to start: a half-configured chain rule must never run.
func BridgeAmmSettingsFromConfig(cfg *config.Config) (BridgeSettings, AmmSettings, error) {
	var bridge BridgeSettings
	var amm AmmSettings
	if cfg == nil {
		return bridge, amm, nil
	}
	if cfg.Bridge.BridgeActivationHeight != 0 {
		bridge.ActivationHeight = cfg.Bridge.BridgeActivationHeight
		maxLock, err := parseSettingWei("[bridge] MaxLockZNHBWei", cfg.Bridge.MaxLockZNHBWei)
		if err != nil {
			return BridgeSettings{}, AmmSettings{}, err
		}
		bridge.MaxLockWei = maxLock
		first, err := parseBridgeParams("[bridge]", cfg.Bridge.BridgeActivationHeight, cfg.Bridge.Signers, cfg.Bridge.SignerThreshold,
			cfg.Bridge.Guardian, cfg.Bridge.MaxReleaseZNHBWei, cfg.Bridge.WindowBlocks, cfg.Bridge.WindowCapZNHBWei,
			cfg.Bridge.DelayAboveZNHBWei, cfg.Bridge.DelayBlocks)
		if err != nil {
			return BridgeSettings{}, AmmSettings{}, err
		}
		bridge.Params = []BridgeParams{first}
		for i, r := range cfg.Bridge.Rotations {
			name := fmt.Sprintf("[bridge] Rotations[%d]", i)
			if r.FromHeight <= bridge.Params[len(bridge.Params)-1].FromHeight {
				return BridgeSettings{}, AmmSettings{}, fmt.Errorf("%s FromHeight %d is not above the one before it (%d)", name, r.FromHeight, bridge.Params[len(bridge.Params)-1].FromHeight)
			}
			rot, err := parseBridgeParams(name, r.FromHeight, r.Signers, r.SignerThreshold, r.Guardian, r.MaxReleaseZNHBWei,
				r.WindowBlocks, r.WindowCapZNHBWei, r.DelayAboveZNHBWei, r.DelayBlocks)
			if err != nil {
				return BridgeSettings{}, AmmSettings{}, err
			}
			bridge.Params = append(bridge.Params, rot)
		}
	}
	if cfg.Amm.AmmActivationHeight != 0 {
		amm.ActivationHeight = cfg.Amm.AmmActivationHeight
		amm.SwapStartHeight = cfg.Amm.SwapStartHeight
		if cfg.Amm.SwapStartHeight != 0 && cfg.Amm.SwapStartHeight < cfg.Amm.AmmActivationHeight {
			return BridgeSettings{}, AmmSettings{}, fmt.Errorf("[amm] SwapStartHeight %d is below AmmActivationHeight %d", cfg.Amm.SwapStartHeight, cfg.Amm.AmmActivationHeight)
		}
		if cfg.Amm.FeeBps >= 10_000 {
			return BridgeSettings{}, AmmSettings{}, fmt.Errorf("[amm] FeeBps must be below 10000")
		}
		amm.FeeBps = cfg.Amm.FeeBps
		amm.MaxSwapBps = cfg.Amm.MaxSwapBps
		if amm.MaxSwapBps == 0 {
			amm.MaxSwapBps = 500
		}
		if amm.MaxSwapBps > 10_000 {
			return BridgeSettings{}, AmmSettings{}, fmt.Errorf("[amm] MaxSwapBps must be at most 10000")
		}
		provider, err := parseSettingAddress("[amm] LiquidityProvider", cfg.Amm.LiquidityProvider)
		if err != nil {
			return BridgeSettings{}, AmmSettings{}, err
		}
		amm.Provider, amm.HasProvider = provider, true
		minZ, err := parseSettingWei("[amm] MinReserveZNHBWei", cfg.Amm.MinReserveZNHBWei)
		if err != nil {
			return BridgeSettings{}, AmmSettings{}, err
		}
		minN, err := parseSettingWei("[amm] MinReserveNHBWei", cfg.Amm.MinReserveNHBWei)
		if err != nil {
			return BridgeSettings{}, AmmSettings{}, err
		}
		if minZ == nil || minZ.Sign() == 0 {
			minZ = big.NewInt(1)
		}
		if minN == nil || minN.Sign() == 0 {
			minN = big.NewInt(1)
		}
		amm.MinReserveZNHB, amm.MinReserveNHB = minZ, minN
	}
	return bridge, amm, nil
}

// SetBridge sets the bridge's chain rules. The zero value leaves the bridge off at every height.
func (sp *StateProcessor) SetBridge(settings BridgeSettings) {
	if sp == nil {
		return
	}
	sp.bridge = settings.clone()
}

// SetAmm sets the pool's chain rules. The zero value leaves the pool off at every height.
func (sp *StateProcessor) SetAmm(settings AmmSettings) {
	if sp == nil {
		return
	}
	sp.amm = settings.clone()
}

func (sp *StateProcessor) bridgeActive() bool {
	return sp != nil && sp.bridge.ActivationHeight != 0 && sp.blockHeight() >= sp.bridge.ActivationHeight
}

func (sp *StateProcessor) ammActive() bool {
	return sp != nil && sp.amm.ActivationHeight != 0 && sp.blockHeight() >= sp.amm.ActivationHeight
}

// SetBridgeAndAmm sets both blocks on the node, and on the processor now and after every rebuild.
func (n *Node) SetBridgeAndAmm(bridge BridgeSettings, amm AmmSettings) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.bridgeSettings = bridge.clone()
	n.ammSettings = amm.clone()
	if n.state != nil {
		n.state.SetBridge(n.bridgeSettings)
		n.state.SetAmm(n.ammSettings)
	}
}
