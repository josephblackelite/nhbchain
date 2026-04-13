package potso

import (
	"math/big"
	"testing"
)

func TestComputeWeightSnapshotClampAndDecay(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 100,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   1,
		TopKWinners:           0,
		TieBreak:              TieBreakAddrLex,
	}
	inputs := []WeightInput{
		{
			Address:            addr(1),
			Stake:              big.NewInt(100),
			PreviousEngagement: 80,
			Meter:              EngagementMeter{TxCount: 40},
		},
		{
			Address:            addr(2),
			Stake:              big.NewInt(0),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 2},
		},
	}
	snapshot, err := ComputeWeightSnapshot(10, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if snapshot.TotalEngagement != 100+10 {
		t.Fatalf("expected engagement total 110, got %d", snapshot.TotalEngagement)
	}
	if snapshot.Entries[0].Engagement != 100 {
		t.Fatalf("expected first entry engagement capped at 100, got %d", snapshot.Entries[0].Engagement)
	}
	if snapshot.Entries[1].Engagement != 10 {
		t.Fatalf("expected second entry engagement 10, got %d", snapshot.Entries[1].Engagement)
	}
}

func TestComputeWeightSnapshotFilters(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(50),
		MinEngagementToWin:    20,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	inputs := []WeightInput{
		{
			Address:            addr(3),
			Stake:              big.NewInt(40),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 10},
		},
		{
			Address:            addr(4),
			Stake:              big.NewInt(60),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 1},
		},
	}
	snapshot, err := ComputeWeightSnapshot(1, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("expected all entries filtered, got %d", len(snapshot.Entries))
	}
	if snapshot.TotalStake.Sign() != 0 {
		t.Fatalf("expected zero total stake, got %s", snapshot.TotalStake)
	}
}

func TestComputeWeightSnapshotTopK(t *testing.T) {
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
		TieBreak:              TieBreakAddrLex,
	}
	inputs := []WeightInput{
		{Address: addr(5), Stake: big.NewInt(10), Meter: EngagementMeter{TxCount: 1}},
		{Address: addr(6), Stake: big.NewInt(20), Meter: EngagementMeter{TxCount: 2}},
		{Address: addr(7), Stake: big.NewInt(30), Meter: EngagementMeter{TxCount: 3}},
	}
	snapshot, err := ComputeWeightSnapshot(2, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected 2 entries after top-k, got %d", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Address != addr(7) || snapshot.Entries[1].Address != addr(6) {
		t.Fatalf("unexpected ordering after top-k")
	}
}
