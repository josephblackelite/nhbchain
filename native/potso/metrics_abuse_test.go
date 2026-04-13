package potso

import (
	"math/big"
	"testing"
)

func TestMinStakeToEarnZerosEngagement(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           1000,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 0,
		MinStakeToWinWei:      big.NewInt(0),
		MinStakeToEarnWei:     big.NewInt(100),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	inputs := []WeightInput{
		{
			Address:            addr(1),
			Stake:              big.NewInt(50),
			PreviousEngagement: 25,
			Meter:              EngagementMeter{TxCount: 200},
		},
		{
			Address:            addr(2),
			Stake:              big.NewInt(150),
			PreviousEngagement: 0,
			Meter:              EngagementMeter{TxCount: 20},
		},
	}
	snapshot, err := ComputeWeightSnapshot(42, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected both entrants retained, got %d", len(snapshot.Entries))
	}
	if snapshot.Entries[0].Address != addr(2) {
		t.Fatalf("expected staked entrant to rank first")
	}
	if snapshot.Entries[1].Engagement != 0 {
		t.Fatalf("expected engagement reset for under-staked entrant, got %d", snapshot.Entries[1].Engagement)
	}
	if snapshot.TotalEngagement == 0 {
		t.Fatalf("expected non-zero total engagement from eligible participant")
	}
}

func TestZeroValueParticipantDropped(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 0,
		MinStakeToWinWei:      big.NewInt(0),
		MinStakeToEarnWei:     big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	inputs := []WeightInput{{
		Address:            addr(3),
		Stake:              big.NewInt(0),
		PreviousEngagement: 0,
		Meter:              EngagementMeter{},
	}}
	snapshot, err := ComputeWeightSnapshot(7, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 0 {
		t.Fatalf("expected zero entries, got %d", len(snapshot.Entries))
	}
}

func TestQuadraticTxDampening(t *testing.T) {
	params := WeightParams{
		AlphaStakeBps:          0,
		TxWeightBps:            1,
		EscrowWeightBps:        0,
		UptimeWeightBps:        0,
		MaxEngagementPerEpoch:  0,
		MinStakeToWinWei:       big.NewInt(0),
		MinStakeToEarnWei:      big.NewInt(0),
		MinEngagementToWin:     0,
		DecayHalfLifeEpochs:    0,
		TopKWinners:            0,
		QuadraticTxDampenAfter: 10,
		QuadraticTxDampenPower: 2,
	}
	inputs := []WeightInput{{
		Address:            addr(4),
		Stake:              big.NewInt(500),
		PreviousEngagement: 0,
		Meter:              EngagementMeter{TxCount: 110},
	}}
	snapshot, err := ComputeWeightSnapshot(9, inputs, params)
	if err != nil {
		t.Fatalf("compute snapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(snapshot.Entries))
	}
	expected := uint64(20)
	if snapshot.Entries[0].Engagement != expected {
		t.Fatalf("expected dampened engagement %d, got %d", expected, snapshot.Entries[0].Engagement)
	}
}
