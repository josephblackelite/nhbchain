package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/potso"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// forcePotsoRewardConfigForTest installs cfg on sp without the checks in
// SetPotsoRewardConfig. Claim payout mode can no longer be selected through
// the setter, because nothing settles the claim records it creates. A few older
// tests still exercise the claim branch of processPotsoRewardEpoch and
// Node.PotsoRewardClaim, code that is kept for a future settlement
// transaction, and use this to reach it.
func forcePotsoRewardConfigForTest(sp *StateProcessor, cfg potso.RewardConfig) {
	sp.potsoRewardConfig = clonePotsoRewardConfig(cfg)
}

func TestSetPotsoRewardConfigRefusesClaimMode(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    [20]byte{1},
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		PayoutMode:         potso.RewardPayoutModeAuto,
	}
	if err := sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set auto config: %v", err)
	}

	for _, mode := range []potso.RewardPayoutMode{potso.RewardPayoutModeClaim, "CLAIM", " claim "} {
		claimCfg := cfg
		claimCfg.PayoutMode = mode
		claimCfg.EmissionPerEpoch = big.NewInt(1234)
		if err := sp.SetPotsoRewardConfig(claimCfg); !errors.Is(err, potso.ErrClaimModeUnavailable) {
			t.Fatalf("mode %q: expected ErrClaimModeUnavailable, got %v", mode, err)
		}
		// A refused configuration leaves the one in force untouched.
		got := sp.PotsoRewardConfig()
		if got.EffectivePayoutMode() != potso.RewardPayoutModeAuto {
			t.Fatalf("mode %q: payout mode changed to %q by a refused config", mode, got.EffectivePayoutMode())
		}
		if got.EmissionPerEpoch.Cmp(big.NewInt(900)) != 0 {
			t.Fatalf("mode %q: emission changed to %s by a refused config", mode, got.EmissionPerEpoch)
		}
	}
}

func TestNodeSetPotsoRewardConfigRefusesClaimMode(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    [20]byte{1},
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		PayoutMode:         potso.RewardPayoutModeClaim,
	}
	if err := node.SetPotsoRewardConfig(cfg); !errors.Is(err, potso.ErrClaimModeUnavailable) {
		t.Fatalf("expected ErrClaimModeUnavailable, got %v", err)
	}
	if got := node.PotsoRewardConfig(); got.EffectivePayoutMode() != potso.RewardPayoutModeAuto || got.EpochLengthBlocks != 0 {
		t.Fatalf("a refused config was applied: %+v", got)
	}
}

// Closing an epoch must only move ZNHB from the treasury to the winners. Every
// payout mode the node accepts has to keep treasury plus winners unchanged by
// it; a mode that took the payout out of the treasury and credited nobody would
// make the total shrink.
func TestPotsoEpochCloseConservesZNHBForEveryAcceptedMode(t *testing.T) {
	modes := []potso.RewardPayoutMode{
		"",
		potso.RewardPayoutModeAuto,
		potso.RewardPayoutModeClaim,
		"CLAIM",
		" claim ",
	}
	for _, mode := range modes {
		mode := mode
		t.Run("mode="+string(mode), func(t *testing.T) {
			db := storage.NewMemDB()
			t.Cleanup(db.Close)
			trie, err := statetrie.NewTrie(db, nil)
			if err != nil {
				t.Fatalf("new trie: %v", err)
			}
			sp, err := NewStateProcessor(trie)
			if err != nil {
				t.Fatalf("state processor: %v", err)
			}

			treasury := [20]byte{0x71}
			cfg := potso.RewardConfig{
				EpochLengthBlocks:  2,
				AlphaStakeBps:      7000,
				MinPayoutWei:       big.NewInt(0),
				EmissionPerEpoch:   big.NewInt(900),
				TreasuryAddress:    treasury,
				MaxWinnersPerEpoch: 10,
				CarryRemainder:     true,
				PayoutMode:         mode,
			}
			if err := sp.SetPotsoRewardConfig(cfg); err != nil {
				if errors.Is(err, potso.ErrClaimModeUnavailable) {
					t.Skipf("payout mode %q is refused, nothing to conserve", mode)
				}
				t.Fatalf("set potso config: %v", err)
			}

			manager := nhbstate.NewManager(sp.Trie)
			treasuryAcc, err := manager.GetAccount(treasury[:])
			if err != nil {
				t.Fatalf("treasury account: %v", err)
			}
			treasuryAcc.BalanceZNHB = big.NewInt(900)
			if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
				t.Fatalf("store treasury: %v", err)
			}

			winners := [][20]byte{{0x72}, {0x73}}
			stakes := []int64{600, 400}
			for i, winner := range winners {
				if err := manager.PotsoStakeSetBondedTotal(winner, big.NewInt(stakes[i])); err != nil {
					t.Fatalf("set stake %d: %v", i, err)
				}
				if err := manager.PotsoMetricsAddEngagement(0, winner, 0, 0, 30*60); err != nil {
					t.Fatalf("seed engagement %d: %v", i, err)
				}
			}

			total := func() *big.Int {
				sum := big.NewInt(0)
				for _, addr := range append([][20]byte{treasury}, winners...) {
					acc, err := manager.GetAccount(addr[:])
					if err != nil {
						t.Fatalf("read account: %v", err)
					}
					sum.Add(sum, acc.BalanceZNHB)
				}
				return sum
			}
			before := total()

			now := time.Unix(1_700_400_000, 0).UTC()
			if err := sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
				t.Fatalf("process block 1: %v", err)
			}
			if err := sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
				t.Fatalf("process block 2: %v", err)
			}

			meta, ok, err := manager.PotsoRewardsGetMeta(0)
			if err != nil || !ok || meta == nil {
				t.Fatalf("expected epoch meta: ok=%v err=%v", ok, err)
			}
			if meta.TotalPaid.Sign() <= 0 {
				t.Fatalf("expected a positive payout for the epoch, got %s", meta.TotalPaid)
			}
			if after := total(); after.Cmp(before) != 0 {
				t.Fatalf("epoch close changed treasury plus winners from %s to %s", before, after)
			}
		})
	}
}
