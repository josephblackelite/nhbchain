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
