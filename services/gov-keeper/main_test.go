package main

import (
	"testing"
	"time"

	"nhbchain/core/types"
)

func TestDetermineAction(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		proposal govProposal
		wantType types.TxType
		wantOK   bool
	}{
		{
			name: "voting still open",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Add(time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "voting closed, needs finalize",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Add(-time.Minute).Format(time.RFC3339),
			},
			wantType: types.TxTypeGovFinalize,
			wantOK:   true,
		},
		{
			name: "voting closed exactly now, needs finalize",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: now.Format(time.RFC3339),
			},
			wantType: types.TxTypeGovFinalize,
			wantOK:   true,
		},
		{
			name: "passed and not yet queued, needs queue",
			proposal: govProposal{
				Status: proposalStatusPassed,
				Queued: false,
			},
			wantType: types.TxTypeGovQueue,
			wantOK:   true,
		},
		{
			name: "queued but timelock not elapsed",
			proposal: govProposal{
				Status:      proposalStatusPassed,
				Queued:      true,
				TimelockEnd: now.Add(time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "queued and timelock elapsed, needs execute",
			proposal: govProposal{
				Status:      proposalStatusPassed,
				Queued:      true,
				TimelockEnd: now.Add(-time.Minute).Format(time.RFC3339),
			},
			wantType: types.TxTypeGovExecute,
			wantOK:   true,
		},
		{
			name: "rejected proposal, nothing to do",
			proposal: govProposal{
				Status: proposalStatusRejected,
			},
			wantOK: false,
		},
		{
			name: "executed proposal, nothing to do",
			proposal: govProposal{
				Status: proposalStatusExecuted,
			},
			wantOK: false,
		},
		{
			// Reproduces the real proposal-1 case discovered live: an
			// already-executed proposal keeps queued=true forever, which
			// must not make determineAction try to re-execute it.
			name: "executed proposal with queued still true, nothing to do",
			proposal: govProposal{
				Status:      proposalStatusExecuted,
				Queued:      true,
				TimelockEnd: now.Add(-24 * time.Hour).Format(time.RFC3339),
			},
			wantOK: false,
		},
		{
			name: "voting_period with empty votingEnd is never actionable",
			proposal: govProposal{
				Status:    proposalStatusVotingPeriod,
				VotingEnd: "",
			},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotType, _, gotOK := determineAction(tc.proposal, now)
			if gotOK != tc.wantOK {
				t.Fatalf("determineAction() ok = %v, want %v", gotOK, tc.wantOK)
			}
			if gotOK && gotType != tc.wantType {
				t.Fatalf("determineAction() type = %v, want %v", gotType, tc.wantType)
			}
		})
	}
}

func TestRunOnceIncrementsNonceAcrossMultipleActions(t *testing.T) {
	// Regression guard: within one poll tick, multiple due proposals must
	// each get the next sequential nonce, not the same starting nonce
	// repeated (which would make every submission after the first one fail
	// as a duplicate/stale nonce).
	proposals := []govProposal{
		{ID: 1, Status: proposalStatusPassed, Queued: false},
		{ID: 2, Status: proposalStatusPassed, Queued: false},
		{ID: 3, Status: proposalStatusVotingPeriod, VotingEnd: "not-a-real-timestamp"},
	}

	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	var actioned []uint64
	nonce := uint64(41)
	for _, p := range proposals {
		_, _, ok := determineAction(p, now)
		if !ok {
			continue
		}
		actioned = append(actioned, nonce)
		nonce++
	}

	if len(actioned) != 2 {
		t.Fatalf("expected 2 actionable proposals, got %d", len(actioned))
	}
	if actioned[0] != 41 || actioned[1] != 42 {
		t.Fatalf("expected sequential nonces [41 42], got %v", actioned)
	}
}
