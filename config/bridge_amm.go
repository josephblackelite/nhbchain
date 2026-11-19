package config

// BridgeConfig holds the settings of the ZNHB bridge escrow (ledger PL-BC-C). Every value is a chain rule: set
// it identically on every validator, to a height above the chain tip, restarting all of them together. The
// zero value is off and is what the shipped files carry.
type BridgeConfig struct {
	// BridgeActivationHeight is the first block height from which the bridge transactions
	// (TxTypeBridgeLock 0x4E, TxTypeBridgeRelease 0x4F) are valid. 0 (the default) leaves the two bytes
	// unknown transaction types at every height.
	BridgeActivationHeight uint64 `toml:"BridgeActivationHeight"`
	// MaxLockZNHBWei caps one lock, in ZNHB wei, as a decimal string. Empty means no cap.
	MaxLockZNHBWei string `toml:"MaxLockZNHBWei"`

	// Stage 1 hardening (ledger PL-BC-C, bridge trust plan): no single key can release ZNHB. The values below are the
	// rules in force from BridgeActivationHeight; Rotations replace them from a later height, so a leaked signer is
	// answered without a hard fork. All of them are required once the bridge is activated; the node refuses to start
	// with an activated bridge and a missing or inconsistent value.

	// Signers are the addresses (nhb1...) whose signatures attest an EVM burn. A release needs SignerThreshold distinct
	// ones. They are settings of this file and are never granted by a vote.
	Signers []string `toml:"Signers"`
	// SignerThreshold is how many distinct signers a release needs: at least 2 and at most len(Signers).
	SignerThreshold uint32 `toml:"SignerThreshold"`
	// Guardian is the address (nhb1...) that may pause the bridge and cancel a waiting release. It can never release,
	// must not be a signer, and can only make the bridge stop, not move ZNHB.
	Guardian string `toml:"Guardian"`
	// MaxReleaseZNHBWei is the most one release may pay, in ZNHB wei (decimal string). Required.
	MaxReleaseZNHBWei string `toml:"MaxReleaseZNHBWei"`
	// WindowBlocks and WindowCapZNHBWei bound the total released (paid or waiting) in each window of WindowBlocks
	// blocks, aligned to height / WindowBlocks. Both required; the cap must be at least MaxReleaseZNHBWei.
	WindowBlocks     uint64 `toml:"WindowBlocks"`
	WindowCapZNHBWei string `toml:"WindowCapZNHBWei"`
	// DelayAboveZNHBWei and DelayBlocks make a release above that amount wait DelayBlocks blocks before anyone can
	// execute it; the guardian may cancel it meanwhile. Empty / 0 = no waiting. Set together or not at all.
	DelayAboveZNHBWei string `toml:"DelayAboveZNHBWei"`
	DelayBlocks       uint64 `toml:"DelayBlocks"`
	// Rotations replace the five groups above from FromHeight on. Each needs a FromHeight above the activation height
	// and above the one before, and a complete set of values (nothing is inherited).
	Rotations []BridgeRotationConfig `toml:"Rotations"`
}

// BridgeRotationConfig is one scheduled replacement of the bridge's signers, guardian and limits.
type BridgeRotationConfig struct {
	FromHeight        uint64   `toml:"FromHeight"`
	Signers           []string `toml:"Signers"`
	SignerThreshold   uint32   `toml:"SignerThreshold"`
	Guardian          string   `toml:"Guardian"`
	MaxReleaseZNHBWei string   `toml:"MaxReleaseZNHBWei"`
	WindowBlocks      uint64   `toml:"WindowBlocks"`
	WindowCapZNHBWei  string   `toml:"WindowCapZNHBWei"`
	DelayAboveZNHBWei string   `toml:"DelayAboveZNHBWei"`
	DelayBlocks       uint64   `toml:"DelayBlocks"`
}

// AmmConfig holds the settings of the native ZNHB/NHB constant-product pool (ledger PL-BC-B). Every value is
// a chain rule, as above. The zero value of the heights is off.
type AmmConfig struct {
	// AmmActivationHeight is the first block height from which the pool transactions
	// (TxTypeAmmAddLiquidity 0x50, TxTypeAmmRemoveLiquidity 0x51, TxTypeAmmSwap 0x52) are valid. 0 (the
	// default) leaves the three bytes unknown transaction types at every height.
	AmmActivationHeight uint64 `toml:"AmmActivationHeight"`
	// SwapStartHeight is the first block height at which swaps are accepted. Liquidity can be added from
	// the activation height; trading stays closed until this one, so the pool is paused by default even
	// after it is activated. 0 keeps swaps closed at every height.
	SwapStartHeight uint64 `toml:"SwapStartHeight"`
	// FeeBps is the swap fee in basis points of the amount in. The fee stays in the pool (it accrues to
	// the liquidity provider), so it never moves ZNHB out of the pool's accounts.
	FeeBps uint32 `toml:"FeeBps"`
	// MaxSwapBps caps one swap's amount in as basis points of the reserve it is added to (10000 = the whole
	// reserve). 0 means 500 (5 percent).
	MaxSwapBps uint32 `toml:"MaxSwapBps"`
	// LiquidityProvider is the address (nhb1...) that alone may add or remove liquidity. It must not be the
	// admin wallet: ZNHB reaches the pool only after it has left the admin wallet through the curve or the
	// swap-voucher path. Required once the pool is activated.
	LiquidityProvider string `toml:"LiquidityProvider"`
	// MinReserveZNHBWei and MinReserveNHBWei are floors no swap or removal may take a reserve below, as
	// decimal strings. Empty means one unit of the smallest denomination (a pool is never emptied by a swap).
	MinReserveZNHBWei string `toml:"MinReserveZNHBWei"`
	MinReserveNHBWei  string `toml:"MinReserveNHBWei"`
}
