package potso

import (
	"math/big"
	"testing"
)

func addr(index byte) [20]byte {
	var out [20]byte
	out[19] = index
	return out
}

func TestComputeRewardsMixedWeights(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks:  1,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(1000),
		TreasuryAddress:    addr(1),
		MaxWinnersPerEpoch: 0,
		CarryRemainder:     true,
	}
	params := WeightParams{
		AlphaStakeBps:         7000,
		TxWeightBps:           10000,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1_000_000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
		TieBreak:              TieBreakAddrLex,
	}
	snapshot := RewardSnapshot{
		Epoch: 1,
		Entries: []RewardSnapshotEntry{
			{Address: addr(2), Stake: big.NewInt(60), PreviousEngagement: 0, Meter: EngagementMeter{TxCount: 3}},
			{Address: addr(3), Stake: big.NewInt(40), PreviousEngagement: 0, Meter: EngagementMeter{TxCount: 7}},
		},
	}
	budget := big.NewInt(1000)
	outcome, err := ComputeRewards(cfg, params, snapshot, budget)
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if outcome.TotalPaid.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected total paid 1000, got %s", outcome.TotalPaid)
	}
	if len(outcome.Winners) != 2 {
		t.Fatalf("expected 2 winners, got %d", len(outcome.Winners))
	}
	if outcome.Winners[0].Amount.Cmp(big.NewInt(510)) != 0 {
		t.Fatalf("unexpected winner 0 amount: %s", outcome.Winners[0].Amount)
	}
	if outcome.Winners[1].Amount.Cmp(big.NewInt(490)) != 0 {
		t.Fatalf("unexpected winner 1 amount: %s", outcome.Winners[1].Amount)
	}
}

func TestComputeRewardsStakeOnly(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks: 1,
		AlphaStakeBps:     RewardBpsDenominator,
		MinPayoutWei:      big.NewInt(0),
		EmissionPerEpoch:  big.NewInt(1000),
		TreasuryAddress:   addr(1),
	}
	params := WeightParams{
		AlphaStakeBps:         RewardBpsDenominator,
		TxWeightBps:           0,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 1000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	snapshot := RewardSnapshot{
		Epoch: 1,
		Entries: []RewardSnapshotEntry{
			{Address: addr(2), Stake: big.NewInt(75)},
			{Address: addr(3), Stake: big.NewInt(25), Meter: EngagementMeter{TxCount: 10}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(1000))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if len(outcome.Winners) != 2 {
		t.Fatalf("expected 2 winners, got %d", len(outcome.Winners))
	}
	if outcome.Winners[0].Amount.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("expected stake-heavy payout 750, got %s", outcome.Winners[0].Amount)
	}
	if outcome.Winners[1].Amount.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected stake-light payout 250, got %s", outcome.Winners[1].Amount)
	}
}

func TestComputeRewardsEngagementOnly(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks: 1,
		AlphaStakeBps:     0,
		MinPayoutWei:      big.NewInt(0),
		EmissionPerEpoch:  big.NewInt(1000),
		TreasuryAddress:   addr(1),
	}
	params := WeightParams{
		AlphaStakeBps:         0,
		TxWeightBps:           10000,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 100000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	snapshot := RewardSnapshot{
		Epoch: 2,
		Entries: []RewardSnapshotEntry{
			{Address: addr(4), Stake: big.NewInt(500), Meter: EngagementMeter{TxCount: 2}},
			{Address: addr(5), Stake: big.NewInt(100), Meter: EngagementMeter{TxCount: 8}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(1000))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if outcome.Winners[0].Address != addr(5) {
		t.Fatalf("expected engagement winner first")
	}
	if outcome.Winners[0].Amount.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("unexpected payout: %s", outcome.Winners[0].Amount)
	}
}

func TestComputeRewardsMinPayout(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks: 1,
		AlphaStakeBps:     5000,
		MinPayoutWei:      big.NewInt(700),
		EmissionPerEpoch:  big.NewInt(1000),
		TreasuryAddress:   addr(1),
	}
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10000,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 100000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	snapshot := RewardSnapshot{
		Epoch: 3,
		Entries: []RewardSnapshotEntry{
			{Address: addr(6), Stake: big.NewInt(60), Meter: EngagementMeter{TxCount: 4}},
			{Address: addr(7), Stake: big.NewInt(40), Meter: EngagementMeter{TxCount: 6}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(1000))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if len(outcome.Winners) != 0 {
		t.Fatalf("expected no winners due to dust filter, got %d", len(outcome.Winners))
	}
	if outcome.Remainder.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("unexpected remainder: %s", outcome.Remainder)
	}
}

func TestComputeRewardsMaxWinners(t *testing.T) {
	cfg := RewardConfig{
		EpochLengthBlocks:  1,
		AlphaStakeBps:      5000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(1000),
		TreasuryAddress:    addr(1),
		MaxWinnersPerEpoch: 2,
	}
	params := WeightParams{
		AlphaStakeBps:         5000,
		TxWeightBps:           10000,
		EscrowWeightBps:       0,
		UptimeWeightBps:       0,
		MaxEngagementPerEpoch: 100000,
		MinStakeToWinWei:      big.NewInt(0),
		MinEngagementToWin:    0,
		DecayHalfLifeEpochs:   0,
		TopKWinners:           0,
	}
	snapshot := RewardSnapshot{
		Epoch: 4,
		Entries: []RewardSnapshotEntry{
			{Address: addr(1), Stake: big.NewInt(10), Meter: EngagementMeter{TxCount: 1}},
			{Address: addr(2), Stake: big.NewInt(20), Meter: EngagementMeter{TxCount: 2}},
			{Address: addr(3), Stake: big.NewInt(30), Meter: EngagementMeter{TxCount: 3}},
		},
	}
	outcome, err := ComputeRewards(cfg, params, snapshot, big.NewInt(600))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if len(outcome.Winners) != 2 {
		t.Fatalf("expected 2 winners, got %d", len(outcome.Winners))
	}
	if outcome.Winners[0].Address != addr(3) || outcome.Winners[1].Address != addr(2) {
		t.Fatalf("unexpected winners order")
	}
}

func TestRewardConfigValidate(t *testing.T) {
	cfg := RewardConfig{AlphaStakeBps: RewardBpsDenominator + 1}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected alpha validation error")
	}
	cfg = RewardConfig{EpochLengthBlocks: 1, EmissionPerEpoch: big.NewInt(1)}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected treasury validation error")
	}
}

func TestRewardEpochMetaClone(t *testing.T) {
	meta := &RewardEpochMeta{
		Epoch:           5,
		Day:             "2024-01-02",
		StakeTotal:      big.NewInt(10),
		EngagementTotal: big.NewInt(20),
		AlphaBps:        1234,
		Emission:        big.NewInt(300),
		Budget:          big.NewInt(200),
		TotalPaid:       big.NewInt(150),
		Remainder:       big.NewInt(50),
		Winners:         2,
	}
	clone := meta.Clone()
	meta.StakeTotal.SetInt64(99)
	if clone.StakeTotal.String() != "10" {
		t.Fatalf("expected clone to remain unchanged")
	}
	if clone.Epoch != meta.Epoch {
		t.Fatalf("epoch mismatch")
	}
}
