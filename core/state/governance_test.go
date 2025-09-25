package state

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/native/governance"
)

func TestGovernanceEscrowAndProposalHelpers(t *testing.T) {
	manager := newTestManager(t)
	proposer := crypto.NewAddress(crypto.NHBPrefix, make([]byte, 20))
	addrBytes := proposer.Bytes()
	if balance, err := manager.GovernanceEscrowBalance(addrBytes); err != nil {
		t.Fatalf("escrow balance: %v", err)
	} else if balance.Sign() != 0 {
		t.Fatalf("expected empty escrow balance, got %s", balance.String())
	}

	first, err := manager.GovernanceEscrowLock(addrBytes, big.NewInt(100))
	if err != nil {
		t.Fatalf("lock first: %v", err)
	}
	if first.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected first balance: %s", first.String())
	}

	second, err := manager.GovernanceEscrowLock(addrBytes, big.NewInt(200))
	if err != nil {
		t.Fatalf("lock second: %v", err)
	}
	if second.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("unexpected second balance: %s", second.String())
	}

	if balance, err := manager.GovernanceEscrowBalance(addrBytes); err != nil {
		t.Fatalf("escrow balance reload: %v", err)
	} else if balance.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("unexpected reload balance: %s", balance.String())
	}

	if updated, err := manager.GovernanceEscrowUnlock(addrBytes, big.NewInt(150)); err != nil {
		t.Fatalf("escrow unlock: %v", err)
	} else if updated.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("unexpected unlock balance: %s", updated.String())
	}
	if _, err := manager.GovernanceEscrowUnlock(addrBytes, big.NewInt(500)); err == nil {
		t.Fatalf("expected unlock overflow error")
	}
	if balance, err := manager.GovernanceEscrowBalance(addrBytes); err != nil {
		t.Fatalf("escrow balance post-unlock: %v", err)
	} else if balance.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("unexpected post-unlock balance: %s", balance.String())
	}

	nextID, err := manager.GovernanceNextProposalID()
	if err != nil {
		t.Fatalf("next proposal id: %v", err)
	}
	if nextID != 1 {
		t.Fatalf("expected first proposal id 1, got %d", nextID)
	}
	nextID, err = manager.GovernanceNextProposalID()
	if err != nil {
		t.Fatalf("next proposal id second: %v", err)
	}
	if nextID != 2 {
		t.Fatalf("expected second proposal id 2, got %d", nextID)
	}

	createdAt := time.Unix(1700000000, 0).UTC()
	proposal := &governance.Proposal{
		ID:             2,
		Submitter:      proposer,
		Status:         governance.ProposalStatusVotingPeriod,
		Deposit:        big.NewInt(300),
		SubmitTime:     createdAt,
		VotingStart:    createdAt,
		VotingEnd:      createdAt.Add(24 * time.Hour),
		TimelockEnd:    createdAt.Add(48 * time.Hour),
		Target:         "param.update",
		ProposedChange: `{"fees.baseFee":"1000"}`,
	}
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("put proposal: %v", err)
	}

	loaded, ok, err := manager.GovernanceGetProposal(2)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}
	if !ok {
		t.Fatalf("expected proposal to exist")
	}
	if loaded.ID != proposal.ID {
		t.Fatalf("unexpected id: got %d want %d", loaded.ID, proposal.ID)
	}
	if loaded.Status != proposal.Status {
		t.Fatalf("unexpected status: got %d want %d", loaded.Status, proposal.Status)
	}
	if loaded.Deposit.Cmp(proposal.Deposit) != 0 {
		t.Fatalf("unexpected deposit: got %s want %s", loaded.Deposit.String(), proposal.Deposit.String())
	}
	if !loaded.SubmitTime.Equal(proposal.SubmitTime) {
		t.Fatalf("unexpected submit time: got %s want %s", loaded.SubmitTime, proposal.SubmitTime)
	}
	if loaded.Target != proposal.Target {
		t.Fatalf("unexpected target: got %s want %s", loaded.Target, proposal.Target)
	}
}

func TestGovernanceVoteIndexing(t *testing.T) {
	manager := newTestManager(t)
	proposalID := uint64(5)
	voterA := crypto.NewAddress(crypto.NHBPrefix, append(make([]byte, 19), 1))
	voterB := crypto.NewAddress(crypto.NHBPrefix, append(make([]byte, 19), 2))

	voteA := &governance.Vote{
		ProposalID: proposalID,
		Voter:      voterA,
		Choice:     governance.VoteChoiceYes,
		PowerBps:   1500,
		Timestamp:  time.Unix(1700000100, 0).UTC(),
	}
	if err := manager.GovernancePutVote(voteA); err != nil {
		t.Fatalf("store vote a: %v", err)
	}

	voteB := &governance.Vote{
		ProposalID: proposalID,
		Voter:      voterB,
		Choice:     governance.VoteChoiceNo,
		PowerBps:   500,
		Timestamp:  time.Unix(1700000200, 0).UTC(),
	}
	if err := manager.GovernancePutVote(voteB); err != nil {
		t.Fatalf("store vote b: %v", err)
	}

	// Overwrite voter A to ensure updates are reflected in the index.
	voteAUpdate := &governance.Vote{
		ProposalID: proposalID,
		Voter:      voterA,
		Choice:     governance.VoteChoiceAbstain,
		PowerBps:   2000,
		Timestamp:  time.Unix(1700000300, 0).UTC(),
	}
	if err := manager.GovernancePutVote(voteAUpdate); err != nil {
		t.Fatalf("update vote a: %v", err)
	}

	votes, err := manager.GovernanceListVotes(proposalID)
	if err != nil {
		t.Fatalf("list votes: %v", err)
	}
	if len(votes) != 2 {
		t.Fatalf("expected 2 votes, got %d", len(votes))
	}

	foundAbstain := false
	foundNo := false
	for _, vote := range votes {
		switch vote.Voter.String() {
		case voterA.String():
			if vote.Choice != governance.VoteChoiceAbstain {
				t.Fatalf("unexpected choice for voter A: %s", vote.Choice)
			}
			if vote.PowerBps != 2000 {
				t.Fatalf("unexpected power for voter A: %d", vote.PowerBps)
			}
			foundAbstain = true
		case voterB.String():
			if vote.Choice != governance.VoteChoiceNo {
				t.Fatalf("unexpected choice for voter B: %s", vote.Choice)
			}
			if vote.PowerBps != 500 {
				t.Fatalf("unexpected power for voter B: %d", vote.PowerBps)
			}
			foundNo = true
		default:
			t.Fatalf("unexpected voter returned: %s", vote.Voter.String())
		}
	}
	if !foundAbstain || !foundNo {
		t.Fatalf("missing expected votes")
	}
}
