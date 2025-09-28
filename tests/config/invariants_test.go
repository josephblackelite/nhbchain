package config_test

import (
	"testing"

	"nhbchain/config"
)

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
			},
			wantErr: true,
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
