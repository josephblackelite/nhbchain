package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
)

// TestGovernanceProposalLiveTallyForVotingPeriod covers the in-progress
// (not-yet-finalized) tally-query case: Node.GovernanceProposal and
// Node.GovernanceListProposals must surface a live, on-demand tally for a
// proposal still in ProposalStatusVotingPeriod, computed over the votes
// cast so far, WITHOUT ever persisting that value to state -- only
// governance.Engine.Finalize is allowed to do that. Votes and the proposal
// are seeded directly via Node.WithState (mirroring
// core/state/governance_test.go's TestGovernanceVoteIndexing) rather than
// through CastVote, since CastVote requires a POTSO weight snapshot that is
// irrelevant to what this test is proving.
func TestGovernanceProposalLiveTallyForVotingPeriod(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	node.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 100,
		TimelockSeconds:     100,
		QuorumBps:           2000,
		PassThresholdBps:    5000,
	})

	now := time.Unix(1_800_100_000, 0).UTC()
	node.SetTimeSource(func() time.Time { return now })

	var proposerBytes [20]byte
	proposerBytes[19] = 3
	submitter := crypto.MustNewAddress(crypto.NHBPrefix, proposerBytes[:])

	yesVoter := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 1))
	noVoter := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 2))

	var proposalID uint64
	if err := node.WithState(func(m *nhbstate.Manager) error {
		// GovernanceListProposals walks backward from the sequence counter
		// (GovernanceNextProposalID), so the proposal ID must actually come
		// from it -- a hardcoded ID would leave that counter at 0 and make
		// the list read come back empty regardless of what's stored.
		id, err := m.GovernanceNextProposalID()
		if err != nil {
			return err
		}
		proposalID = id
		proposal := &governance.Proposal{
			ID:          proposalID,
			Submitter:   submitter,
			Status:      governance.ProposalStatusVotingPeriod,
			Deposit:     big.NewInt(0),
			SubmitTime:  now.Add(-time.Hour),
			VotingStart: now.Add(-time.Hour),
			VotingEnd:   now.Add(time.Hour), // still open -- not finalizable yet
			TimelockEnd: now.Add(2 * time.Hour),
			Target:      governance.ProposalKindParamUpdate,
		}
		if err := m.GovernancePutProposal(proposal); err != nil {
			return err
		}
		if err := m.GovernancePutVote(&governance.Vote{ProposalID: proposalID, Voter: yesVoter, Choice: governance.VoteChoiceYes, PowerBps: 4000, Timestamp: now}); err != nil {
			return err
		}
		return m.GovernancePutVote(&governance.Vote{ProposalID: proposalID, Voter: noVoter, Choice: governance.VoteChoiceNo, PowerBps: 1000, Timestamp: now})
	}); err != nil {
		t.Fatalf("seed proposal and votes directly: %v", err)
	}

	// GovernanceProposal must show a LIVE tally reflecting the votes cast so
	// far, even though the proposal has not been finalized.
	loaded, ok, err := node.GovernanceProposal(proposalID)
	if err != nil || !ok {
		t.Fatalf("load proposal: ok=%v err=%v", ok, err)
	}
	if loaded.Status != governance.ProposalStatusVotingPeriod {
		t.Fatalf("expected status to remain VotingPeriod, got %v", loaded.Status)
	}
	if loaded.Tally == nil {
		t.Fatalf("expected a live tally for an in-progress proposal, got nil")
	}
	if loaded.Tally.YesPowerBps != 4000 || loaded.Tally.NoPowerBps != 1000 {
		t.Fatalf("unexpected live tally power: yes=%d no=%d", loaded.Tally.YesPowerBps, loaded.Tally.NoPowerBps)
	}
	if loaded.Tally.TurnoutBps != 5000 {
		t.Fatalf("unexpected live turnout: got %d want 5000", loaded.Tally.TurnoutBps)
	}
	if loaded.Tally.QuorumBps != 2000 || loaded.Tally.PassThresholdBps != 5000 {
		t.Fatalf("unexpected live policy fields: quorum=%d passThreshold=%d", loaded.Tally.QuorumBps, loaded.Tally.PassThresholdBps)
	}

	// GovernanceListProposals must show the same live decoration.
	listed, _, err := node.GovernanceListProposals(0, 10)
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(listed) != 1 || listed[0].Tally == nil {
		t.Fatalf("expected listed proposal to carry a live tally too, got %+v", listed)
	}
	if listed[0].Tally.YesPowerBps != 4000 || listed[0].Tally.NoPowerBps != 1000 {
		t.Fatalf("unexpected listed live tally power: yes=%d no=%d", listed[0].Tally.YesPowerBps, listed[0].Tally.NoPowerBps)
	}

	// Critically: the live tally must never have been persisted. A raw read
	// straight off the state trie -- bypassing GovernanceProposal's live
	// decoration entirely -- must still show a nil Tally, proving that only
	// Finalize commits a real tally to state.
	if err := node.WithState(func(m *nhbstate.Manager) error {
		raw, ok, err := m.GovernanceGetProposal(proposalID)
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("expected proposal to exist in raw state")
		}
		if raw.Tally != nil {
			t.Fatalf("expected raw persisted proposal to have a nil tally before finalize, got %+v", raw.Tally)
		}
		return nil
	}); err != nil {
		t.Fatalf("raw state check: %v", err)
	}
}
