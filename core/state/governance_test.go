package state

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/crypto"
	"nhbchain/native/governance"
)

func TestGovernanceEscrowAndProposalHelpers(t *testing.T) {
	manager := newTestManager(t)
	proposer := crypto.MustNewAddress(crypto.NHBPrefix, make([]byte, 20))
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
	voterA := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 1))
	voterB := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 2))

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

// TestGovernanceFinalizePersistsTally proves that governance.Engine.Finalize
// attaches a real computed tally to the proposal, that GovernancePutProposal
// actually persists it (through the real RLP encode -> trie write path, not
// just an in-memory struct field), and that a subsequent
// Manager.GovernanceGetProposal call -- an entirely independent trie.Get +
// RLP decode -- recovers it.
func TestGovernanceFinalizePersistsTally(t *testing.T) {
	manager := newTestManager(t)
	now := time.Unix(1_700_010_000, 0).UTC()

	var proposerBytes [20]byte
	proposerBytes[19] = 9
	proposer := crypto.MustNewAddress(crypto.NHBPrefix, proposerBytes[:])

	proposalID, err := manager.GovernanceNextProposalID()
	if err != nil {
		t.Fatalf("next proposal id: %v", err)
	}

	proposal := &governance.Proposal{
		ID:          proposalID,
		Submitter:   proposer,
		Status:      governance.ProposalStatusVotingPeriod,
		Deposit:     big.NewInt(0),
		SubmitTime:  now.Add(-time.Hour),
		VotingStart: now.Add(-time.Hour),
		VotingEnd:   now.Add(-time.Minute),
		TimelockEnd: now.Add(time.Hour),
		Target:      governance.ProposalKindParamUpdate,
	}
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("put proposal: %v", err)
	}
	if proposal.Tally != nil {
		t.Fatalf("expected freshly submitted proposal to start with a nil tally")
	}

	yesVoter := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 1))
	noVoter := crypto.MustNewAddress(crypto.NHBPrefix, append(make([]byte, 19), 2))
	if err := manager.GovernancePutVote(&governance.Vote{ProposalID: proposalID, Voter: yesVoter, Choice: governance.VoteChoiceYes, PowerBps: 6000, Timestamp: now}); err != nil {
		t.Fatalf("put yes vote: %v", err)
	}
	if err := manager.GovernancePutVote(&governance.Vote{ProposalID: proposalID, Voter: noVoter, Choice: governance.VoteChoiceNo, PowerBps: 1000, Timestamp: now}); err != nil {
		t.Fatalf("put no vote: %v", err)
	}

	engine := governance.NewEngine()
	engine.SetState(manager)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(governance.ProposalPolicy{QuorumBps: 2000, PassThresholdBps: 5000})

	status, tally, err := engine.Finalize(proposalID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if status != governance.ProposalStatusPassed {
		t.Fatalf("expected passed status, got %v", status)
	}
	if tally == nil {
		t.Fatalf("expected finalize to return a non-nil tally")
	}

	loaded, ok, err := manager.GovernanceGetProposal(proposalID)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}
	if !ok {
		t.Fatalf("expected proposal to exist")
	}
	if loaded.Status != governance.ProposalStatusPassed {
		t.Fatalf("expected persisted status Passed, got %v", loaded.Status)
	}
	if loaded.Tally == nil {
		t.Fatalf("expected persisted tally to be recoverable after finalize, got nil")
	}
	if loaded.Tally.YesPowerBps != 6000 {
		t.Fatalf("unexpected yes power: got %d want 6000", loaded.Tally.YesPowerBps)
	}
	if loaded.Tally.NoPowerBps != 1000 {
		t.Fatalf("unexpected no power: got %d want 1000", loaded.Tally.NoPowerBps)
	}
	if loaded.Tally.TurnoutBps != 7000 {
		t.Fatalf("unexpected turnout: got %d want 7000", loaded.Tally.TurnoutBps)
	}
	if loaded.Tally.QuorumBps != 2000 {
		t.Fatalf("unexpected quorum: got %d want 2000", loaded.Tally.QuorumBps)
	}
	if loaded.Tally.PassThresholdBps != 5000 {
		t.Fatalf("unexpected pass threshold: got %d want 5000", loaded.Tally.PassThresholdBps)
	}
	if loaded.Tally.TotalBallots != 2 {
		t.Fatalf("unexpected total ballots: got %d want 2", loaded.Tally.TotalBallots)
	}
	expectedYesRatio := uint64(6000 * 10_000 / 7000)
	if loaded.Tally.YesRatioBps != expectedYesRatio {
		t.Fatalf("unexpected yes ratio: got %d want %d", loaded.Tally.YesRatioBps, expectedYesRatio)
	}
}

// TestStoredGovernanceProposalDecodesLegacyEncodingWithoutTally is the
// explicit backward-compatibility proof required before appending any field
// to storedGovernanceProposal: it hand-encodes a proposal in the OLD,
// pre-Tally shape (mirrored locally, field-for-field, as
// legacyStoredGovernanceProposal) and confirms the CURRENT
// storedGovernanceProposal decodes that legacy encoding cleanly, with Tally
// coming back nil -- never an error, and never a fabricated non-nil value.
// It also checks the reverse direction: a current-shape record with a nil
// Tally re-encodes to bytes the legacy shape can still read, proving the
// rlp:"optional" field adds no bytes to the list when absent.
func TestStoredGovernanceProposalDecodesLegacyEncodingWithoutTally(t *testing.T) {
	type legacyStoredGovernanceProposal struct {
		ID             uint64
		Title          string
		Summary        string
		MetadataURI    string
		Submitter      [20]byte
		Status         uint8
		Deposit        *big.Int
		SubmitTime     uint64
		VotingStart    uint64
		VotingEnd      uint64
		TimelockEnd    uint64
		Target         string
		ProposedChange string
		Queued         bool
	}

	var submitter [20]byte
	submitter[19] = 7
	legacy := legacyStoredGovernanceProposal{
		ID:             42,
		Title:          "Legacy proposal",
		Summary:        "Encoded before the Tally field existed",
		MetadataURI:    "ipfs://legacy",
		Submitter:      submitter,
		Status:         uint8(governance.ProposalStatusPassed),
		Deposit:        big.NewInt(12345),
		SubmitTime:     1_699_000_000,
		VotingStart:    1_699_000_100,
		VotingEnd:      1_699_003_700,
		TimelockEnd:    1_699_007_300,
		Target:         governance.ProposalKindParamUpdate,
		ProposedChange: `{"fees.baseFee":"1000"}`,
		Queued:         true,
	}

	encoded, err := rlp.EncodeToBytes(&legacy)
	if err != nil {
		t.Fatalf("encode legacy proposal: %v", err)
	}

	var decoded storedGovernanceProposal
	if err := rlp.DecodeBytes(encoded, &decoded); err != nil {
		t.Fatalf("decode legacy proposal into current shape: %v", err)
	}

	if decoded.Tally != nil {
		t.Fatalf("expected nil tally decoding a pre-Tally record, got %+v", decoded.Tally)
	}
	if decoded.ID != legacy.ID {
		t.Fatalf("unexpected id: got %d want %d", decoded.ID, legacy.ID)
	}
	if decoded.Title != legacy.Title {
		t.Fatalf("unexpected title: got %q want %q", decoded.Title, legacy.Title)
	}
	if decoded.Status != legacy.Status {
		t.Fatalf("unexpected status: got %d want %d", decoded.Status, legacy.Status)
	}
	if decoded.Deposit == nil || decoded.Deposit.Cmp(legacy.Deposit) != 0 {
		t.Fatalf("unexpected deposit: got %v want %v", decoded.Deposit, legacy.Deposit)
	}
	if decoded.Queued != legacy.Queued {
		t.Fatalf("unexpected queued: got %v want %v", decoded.Queued, legacy.Queued)
	}
	if decoded.ProposedChange != legacy.ProposedChange {
		t.Fatalf("unexpected proposed change: got %q want %q", decoded.ProposedChange, legacy.ProposedChange)
	}

	proposal, err := decoded.toGovernanceProposal()
	if err != nil {
		t.Fatalf("convert legacy proposal: %v", err)
	}
	if proposal.Tally != nil {
		t.Fatalf("expected nil tally on converted legacy proposal, got %+v", proposal.Tally)
	}

	reEncoded, err := rlp.EncodeToBytes(&decoded)
	if err != nil {
		t.Fatalf("re-encode current-shape proposal: %v", err)
	}
	var roundTripped legacyStoredGovernanceProposal
	if err := rlp.DecodeBytes(reEncoded, &roundTripped); err != nil {
		t.Fatalf("decode current-shape proposal back into legacy shape: %v", err)
	}
	if roundTripped.ID != legacy.ID || roundTripped.Title != legacy.Title || roundTripped.Queued != legacy.Queued {
		t.Fatalf("legacy round-trip mismatch: got %+v want subset of %+v", roundTripped, legacy)
	}
}
