package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"nhbchain/core/genesis"
	"nhbchain/core/rewards"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// writeAdminWalletGenesis writes a minimal genesis file declaring a real
// admin/treasury wallet funded with exactly znhbExpectedTotalSupplyWei ZNHB
// -- the same precondition EnsureZNHBPoolsBootstrapped enforces in
// production -- so NewNode's real construction path (not a bare
// StateProcessor) can be exercised end to end.
func writeAdminWalletGenesis(t *testing.T, dir string) string {
	t.Helper()
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().String()
	adminAddr := adminKey.PubKey().Address().String()

	spec := genesis.GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{
			{Symbol: "NHB", Name: "NHBCoin", Decimals: 18},
			{Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18},
		},
		Validators: []genesis.ValidatorSpec{
			{Address: validatorAddr, Power: 1},
		},
		Alloc: map[string]map[string]string{
			adminAddr: {
				"NHB":  "0",
				"ZNHB": znhbExpectedTotalSupplyWei.String(),
			},
		},
		AdminWallet: adminAddr,
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	path := filepath.Join(dir, "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}
	return path
}

func TestNewNode_ActivatesRewardHalvingScheduleWhenAdminWalletConfigured(t *testing.T) {
	dir := t.TempDir()
	genesisPath := writeAdminWalletGenesis(t, dir)
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	node, err := NewNode(db, validatorKey, genesisPath, false, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	cfg := node.RewardConfig()
	if !cfg.IsEnabled() {
		t.Fatalf("expected the reward config to be enabled once a real admin wallet is configured")
	}
	baseEmissionWei := new(big.Int).Mul(big.NewInt(rewards.HalvingBaseEmissionZNHB), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	if got := cfg.EmissionForEpoch(1); got.Cmp(baseEmissionWei) != 0 {
		t.Fatalf("epoch 1 emission = %v, want %v (halving schedule's base rate)", got, baseEmissionWei)
	}
	if cfg.ValidatorSplit != 2000 || cfg.StakerSplit != 5000 || cfg.EngagementSplit != 3000 {
		t.Fatalf("unexpected reward split: validator=%d staker=%d engagement=%d", cfg.ValidatorSplit, cfg.StakerSplit, cfg.EngagementSplit)
	}
}

func TestNewNode_NoAdminWalletLeavesRewardScheduleDisabled(t *testing.T) {
	// Mirrors production reality for any node without a genesis-declared
	// admin wallet (e.g. NHB_MASTER_TREASURY unset) -- must stay exactly as
	// dormant as it always has been; this is not an opt-in-by-default change.
	node := newTestNode(t)
	cfg := node.RewardConfig()
	if cfg.IsEnabled() {
		t.Fatalf("expected reward config to stay disabled without a configured admin wallet")
	}
}
