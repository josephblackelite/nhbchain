package config_test

import (
	"testing"
	"time"

	"nhbchain/config"
	"nhbchain/native/fees"
)

func validStaking() config.Staking {
	return config.Staking{
		AprBps:                1250,
		PayoutPeriodDays:      30,
		UnbondingDays:         7,
		MinStakeWei:           "0",
		MaxEmissionPerYearWei: "0",
		RewardAsset:           "ZNHB",
	}
}

func validLoyalty() config.Loyalty {
	return config.Loyalty{
		Dynamic: config.LoyaltyDynamic{
			TargetBPS:                   50,
			MinBPS:                      25,
			MaxBPS:                      100,
			SmoothingStepBPS:            5,
			CoverageMax:                 0.5,
			CoverageLookbackDays:        7,
			DailyCapPctOf7dFees:         0.60,
			DailyCapUSD:                 5000,
			YearlyCapPctOfInitialSupply: 10,
			PriceGuard: config.LoyaltyPriceGuard{
				PricePair:          "ZNHB/USD",
				TwapWindowSeconds:  3600,
				MaxDeviationBPS:    500,
				PriceMaxAgeSeconds: 900,
			},
		},
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Global
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
		},
		{
			name: "invalid quorum",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        4000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "voting period too small",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds - 1,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "slashing min window zero",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 0,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "slashing min greater than max",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 11,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "mempool max bytes non-positive",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 0},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "blocks max txs non-positive",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 0},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
			},
			wantErr: true,
		},
		{
			name: "znhb wallet missing",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
				Fees: config.Fees{
					Assets: []config.FeeAsset{
						{Asset: fees.AssetZNHB, MDRBasisPoints: config.DefaultMDRBasisPoints},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "znhb wallet configured",
			cfg: config.Global{
				Governance: config.Governance{
					QuorumBPS:        6000,
					PassThresholdBPS: 5000,
					VotingPeriodSecs: config.MinVotingPeriodSeconds,
				},
				Slashing: config.Slashing{
					MinWindowSecs: 1,
					MaxWindowSecs: 10,
				},
				Mempool: config.Mempool{MaxBytes: 1},
				Blocks:  config.Blocks{MaxTxs: 1},
				Staking: validStaking(),
				Loyalty: validLoyalty(),
				Fees: config.Fees{
					Assets: []config.FeeAsset{
						{
							Asset:          fees.AssetZNHB,
							MDRBasisPoints: config.DefaultMDRBasisPoints,
							OwnerWallet:    "znhb1configuredwallet",
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateConfig(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateConfig() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestValidateConsensus(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Consensus
		wantErr bool
	}{
		{
			name: "valid timeouts",
			cfg: config.Consensus{
				ProposalTimeout:  time.Second,
				PrevoteTimeout:   time.Second,
				PrecommitTimeout: time.Second,
				CommitTimeout:    2 * time.Second,
			},
		},
		{
			name: "proposal timeout non-positive",
			cfg: config.Consensus{
				ProposalTimeout:  0,
				PrevoteTimeout:   time.Second,
				PrecommitTimeout: time.Second,
				CommitTimeout:    2 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "prevote timeout non-positive",
			cfg: config.Consensus{
				ProposalTimeout:  time.Second,
				PrevoteTimeout:   -time.Second,
				PrecommitTimeout: time.Second,
				CommitTimeout:    2 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "precommit timeout non-positive",
			cfg: config.Consensus{
				ProposalTimeout:  time.Second,
				PrevoteTimeout:   time.Second,
				PrecommitTimeout: 0,
				CommitTimeout:    2 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "commit timeout non-positive",
			cfg: config.Consensus{
				ProposalTimeout:  time.Second,
				PrevoteTimeout:   time.Second,
				PrecommitTimeout: time.Second,
				CommitTimeout:    -time.Second,
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateConsensus(tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateConsensus() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}
