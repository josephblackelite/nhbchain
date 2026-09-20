package fuzz

import (
	"errors"
	"math"
	"testing"

	"nhbchain/config"
	"nhbchain/native/fees"
	govcfg "nhbchain/native/gov"
)

func FuzzGovernancePolicyDeltas(f *testing.F) {
	f.Add(uint32(6000), uint32(5000), uint64(604800), uint64(60), uint64(600), int64(16<<20), int64(5000), uint8(0xFF))

	f.Fuzz(func(t *testing.T, quorum uint32, threshold uint32, voting uint64, minWindow uint64, maxWindow uint64, memBytes int64, maxTxs int64, flags uint8) {
		baseline := govcfg.Baseline{
			Governance: govcfg.GovernanceBaseline{
				QuorumBPS:        6000,
				PassThresholdBPS: 5000,
				VotingPeriodSecs: config.MinVotingPeriodSeconds + 7200,
			},
			Slashing: govcfg.SlashingBaseline{MinWindowSecs: 60, MaxWindowSecs: 600},
			Mempool:  govcfg.MempoolBaseline{MaxBytes: 16 << 20},
			Blocks:   govcfg.BlocksBaseline{MaxTxs: 5000},
			Fees: govcfg.FeesBaseline{
				FreeTierTxPerMonth: config.DefaultFreeTierTxPerMonth,
				MDRBasisPoints:     config.DefaultMDRBasisPoints,
				Assets: []govcfg.FeeAssetBaseline{
					{Asset: fees.AssetNHB, MDRBasisPoints: config.DefaultMDRBasisPoints},
					{Asset: fees.AssetZNHB, MDRBasisPoints: config.DefaultMDRBasisPoints},
				},
			},
			Staking: govcfg.StakingBaseline{
				AprBps:                1250,
				PayoutPeriodDays:      30,
				UnbondingDays:         7,
				MinStakeWei:           "0",
				MaxEmissionPerYearWei: "0",
				RewardAsset:           "ZNHB",
			},
		}

		delta := govcfg.PolicyDelta{}

		var govDelta *govcfg.GovernanceDelta
		var slashDelta *govcfg.SlashingDelta
		var memDelta *govcfg.MempoolDelta
		var blockDelta *govcfg.BlocksDelta

		if flags&0x01 != 0 {
			q := quorum % 10001
			if govDelta == nil {
				govDelta = &govcfg.GovernanceDelta{}
			}
			govDelta.QuorumBPS = &q
		}
		if flags&0x02 != 0 {
			th := threshold % 10001
			if govDelta == nil {
				govDelta = &govcfg.GovernanceDelta{}
			}
			govDelta.PassThresholdBPS = &th
		}
		if flags&0x04 != 0 {
			period := voting%(14*24*3600) + 1
			if govDelta == nil {
				govDelta = &govcfg.GovernanceDelta{}
			}
			govDelta.VotingPeriodSecs = &period
		}
		if flags&0x08 != 0 {
			min := normalizePositive(minWindow)
			if slashDelta == nil {
				slashDelta = &govcfg.SlashingDelta{}
			}
			slashDelta.MinWindowSecs = &min
		}
		if flags&0x10 != 0 {
			max := normalizePositive(maxWindow)
			if slashDelta == nil {
				slashDelta = &govcfg.SlashingDelta{}
			}
			slashDelta.MaxWindowSecs = &max
		}
		if flags&0x20 != 0 {
			mb := normalizePositive64(memBytes)%(1<<30) + 1
			if memDelta == nil {
				memDelta = &govcfg.MempoolDelta{}
			}
			memDelta.MaxBytes = &mb
		}
		if flags&0x40 != 0 {
			mt := normalizePositive64(maxTxs)%1_000_000 + 1
			if blockDelta == nil {
				blockDelta = &govcfg.BlocksDelta{}
			}
			blockDelta.MaxTxs = &mt
		}

		delta.Governance = govDelta
		delta.Slashing = slashDelta
		delta.Mempool = memDelta
		delta.Blocks = blockDelta

		err := govcfg.PreflightBaselineApply(baseline, delta)
		if err != nil {
			if !errors.Is(err, govcfg.ErrInvalidPolicyInvariants) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}

		candidate := config.Global{
			Governance: config.Governance{
				QuorumBPS:        baseline.Governance.QuorumBPS,
				PassThresholdBPS: baseline.Governance.PassThresholdBPS,
				VotingPeriodSecs: baseline.Governance.VotingPeriodSecs,
			},
			Slashing: config.Slashing{
				MinWindowSecs: baseline.Slashing.MinWindowSecs,
				MaxWindowSecs: baseline.Slashing.MaxWindowSecs,
			},
			Mempool: config.Mempool{MaxBytes: baseline.Mempool.MaxBytes},
			Blocks:  config.Blocks{MaxTxs: baseline.Blocks.MaxTxs},
			Staking: config.Staking{
				AprBps:                baseline.Staking.AprBps,
				PayoutPeriodDays:      baseline.Staking.PayoutPeriodDays,
				UnbondingDays:         baseline.Staking.UnbondingDays,
				MinStakeWei:           baseline.Staking.MinStakeWei,
				MaxEmissionPerYearWei: baseline.Staking.MaxEmissionPerYearWei,
				RewardAsset:           baseline.Staking.RewardAsset,
				CompoundDefault:       baseline.Staking.CompoundDefault,
			},
			Fees: config.Fees{
				FreeTierTxPerMonth: baseline.Fees.FreeTierTxPerMonth,
				MDRBasisPoints:     baseline.Fees.MDRBasisPoints,
				OwnerWallet:        baseline.Fees.OwnerWallet,
				Assets:             feeAssetsFromBaseline(baseline.Fees.Assets),
			},
		}

		if govDelta != nil {
			if govDelta.QuorumBPS != nil {
				candidate.Governance.QuorumBPS = *govDelta.QuorumBPS
			}
			if govDelta.PassThresholdBPS != nil {
				candidate.Governance.PassThresholdBPS = *govDelta.PassThresholdBPS
			}
			if govDelta.VotingPeriodSecs != nil {
				candidate.Governance.VotingPeriodSecs = *govDelta.VotingPeriodSecs
			}
		}
		if slashDelta != nil {
			if slashDelta.MinWindowSecs != nil {
				candidate.Slashing.MinWindowSecs = *slashDelta.MinWindowSecs
			}
			if slashDelta.MaxWindowSecs != nil {
				candidate.Slashing.MaxWindowSecs = *slashDelta.MaxWindowSecs
			}
		}
		if memDelta != nil && memDelta.MaxBytes != nil {
			candidate.Mempool.MaxBytes = *memDelta.MaxBytes
		}
		if blockDelta != nil && blockDelta.MaxTxs != nil {
			candidate.Blocks.MaxTxs = *blockDelta.MaxTxs
		}

		if err := config.ValidateConfig(candidate); err != nil {
			t.Fatalf("preflight succeeded but config invalid: %v", err)
		}
	})
}

func feeAssetsFromBaseline(list []govcfg.FeeAssetBaseline) []config.FeeAsset {
	if len(list) == 0 {
		return nil
	}
	assets := make([]config.FeeAsset, len(list))
	for i := range list {
		assets[i] = config.FeeAsset{
			Asset:          list[i].Asset,
			MDRBasisPoints: list[i].MDRBasisPoints,
			OwnerWallet:    list[i].OwnerWallet,
		}
	}
	return assets
}

func normalizePositive(v uint64) uint64 {
	if v == 0 {
		return 1
	}
	return v
}

func normalizePositive64(v int64) int64 {
	if v == math.MinInt64 {
		return math.MaxInt64
	}
	if v < 0 {
		return -v
	}
	return v
}
