package potso

import (
	"crypto/sha256"
	"math/big"
	"sort"
	"testing"
)

func TestRewardsTieBreakLexicographic(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks: 1,
		AlphaStakeBps:     5000,
		MinPayoutWei:      big.NewInt(0),
		EmissionPerEpoch:  big.NewInt(1000),
		TreasuryAddress:   addr(1),
	}
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           3,
		TieBreak:              TieBreakAddrLex,
	}
	snapshot := RewardSnapshot{
		Epoch: 5,
		Entries: []RewardSnapshotEntry{
			{Address: addr(3), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
			{Address: addr(1), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
			{Address: addr(2), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(1000))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if outcome.WeightSnapshot == nil {
		t.Fatalf("expected weight snapshot")
	}
	expected := [][20]byte{addr(1), addr(2), addr(3)}
	for i, entry := range outcome.WeightSnapshot.Entries {
		if entry.Address != expected[i] {
			t.Fatalf("unexpected order at %d", i)
		}
	}
}

func TestRewardsTieBreakHash(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks: 1,
		AlphaStakeBps:     5000,
		MinPayoutWei:      big.NewInt(0),
		EmissionPerEpoch:  big.NewInt(1000),
		TreasuryAddress:   addr(1),
	}
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           2,
		TieBreak:              TieBreakAddrHash,
	}
	snapshot := RewardSnapshot{
		Epoch: 6,
		Entries: []RewardSnapshotEntry{
			{Address: addr(4), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
			{Address: addr(5), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
			{Address: addr(6), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 1}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(1000))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if outcome.WeightSnapshot == nil {
		t.Fatalf("expected weight snapshot")
	}
	hashes := make([]struct {
		addr   [20]byte
		digest [32]byte
	}, len(snapshot.Entries))
	for i, entry := range snapshot.Entries {
		hashes[i] = struct {
			addr   [20]byte
			digest [32]byte
		}{addr: entry.Address, digest: sha256.Sum256(entry.Address[:])}
	}
	sort.Slice(hashes, func(i, j int) bool {
		return bytesCompare(hashes[i].digest[:], hashes[j].digest[:]) < 0
	})
	for i, entry := range outcome.WeightSnapshot.Entries {
		if entry.Address != hashes[i].addr {
			t.Fatalf("expected address %x got %x", hashes[i].addr, entry.Address)
		}
	}
}

func bytesCompare(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
