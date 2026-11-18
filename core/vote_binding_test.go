package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/p2p"
)

// PL-R5T-31 in the node: the quorum certificate of a synced block and the equivocation
// proof of a report are held to the vote binding.

var foreignGenesisForVoteTests = bytes.Repeat([]byte{0xF0}, 32)

func TestVoteBinding_TheSettingSurvivesCopyAndCarriesTheGenesisHash(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetVoteChainBindingActivationHeight(11)
	sp.SetGenesisHash([]byte{1, 2, 3})
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.voteChainBindingFromHeight != 11 {
		t.Fatalf("Copy lost the setting: %d", copied.voteChainBindingFromHeight)
	}
	if !bytes.Equal(copied.genesisHash, []byte{1, 2, 3}) {
		t.Fatalf("Copy lost the genesis hash: %x", copied.genesisHash)
	}
	if got := copied.voteBinding(); got.FromHeight != 11 || !bytes.Equal(got.Genesis, []byte{1, 2, 3}) {
		t.Fatalf("voteBinding() = %+v", got)
	}
	var nilSP *StateProcessor
	if b := nilSP.voteBinding(); b.FromHeight != 0 || len(b.Genesis) != 0 {
		t.Fatalf("a nil processor must have the zero binding")
	}
}

func TestNodeVoteBinding_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetVoteChainBindingActivationHeight(5)
	genesis := node.GenesisHash()
	if len(genesis) == 0 {
		t.Fatalf("the test node has no genesis hash")
	}
	node.stateMu.Lock()
	got := node.state.voteChainBindingFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.voteChainBindingFromHeight
	rebuiltGenesis := append([]byte(nil), node.state.genesisHash...)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got != 5 || rebuilt != 5 {
		t.Fatalf("setter left %d, rebuild left %d, want 5 and 5", got, rebuilt)
	}
	if !bytes.Equal(rebuiltGenesis, genesis) {
		t.Fatalf("a rebuilt processor lost the genesis hash")
	}
	if b := node.VoteBinding(); b.FromHeight != 5 || !bytes.Equal(b.Genesis, genesis) {
		t.Fatalf("Node.VoteBinding() = %+v", b)
	}
	var nilNode *Node
	if b := nilNode.VoteBinding(); b.FromHeight != 0 {
		t.Fatalf("a nil node must have the zero binding")
	}
}

// ---- the quorum certificate of a synced block ----------------------------------------

// syncCertifiedBlock builds a target node with a real validator set, creates the next block and
// attaches a quorum certificate signed over the digest signWith(height) names, then feeds the
// block in as a peer would. It returns the height before and after.
func syncCertifiedBlock(t *testing.T, bindingFrom uint64, signWith func(height uint64, genesis []byte) (genesisToSign []byte, bound bool)) (before, after uint64, procErr error) {
	t.Helper()
	target, validatorKey := newQuorumCertTestNode(t)
	target.SetNetworkBroadcaster(&testBroadcaster{})
	cfg := target.state.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 1
	cfg.RotationEnabled = true
	cfg.MaxValidators = 1
	cfg.SnapshotHistory = 8
	if err := target.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}
	if err := nhbstate.NewManager(target.state.Trie).SetMinimumValidatorStake(big.NewInt(1000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()
	currentTime := time.Unix(1_950_000_000, 0).UTC()
	target.SetTimeSource(func() time.Time { return currentTime })
	account := &types.Account{
		BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(15_000),
		EngagementLastHeartbeat: uint64(currentTime.Unix()), ValidatorRegistered: true,
	}
	if err := target.state.setAccount(validatorAddr, account); err != nil {
		t.Fatalf("seed validator account: %v", err)
	}
	for i := 0; i < 2; i++ {
		currentTime = currentTime.Add(time.Second)
		block, err := target.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create seeding block %d: %v", i, err)
		}
		if err := target.CommitBlock(block); err != nil {
			t.Fatalf("commit seeding block %d: %v", i, err)
		}
		before = target.GetHeight()
	}
	target.SetQuorumCertActivationHeight(before)
	target.SetVoteChainBindingActivationHeight(bindingFrom)

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	headerHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash header: %v", err)
	}
	vote := &types.PrecommitVote{BlockHash: headerHash, Round: 0, Type: types.VoteTypePrecommit, Height: block.Header.Height}
	genesis, bound := signWith(block.Header.Height, target.GenesisHash())
	var digest [32]byte
	if bound {
		digest = types.BoundVoteDigest(genesis, vote.Bytes())
	} else {
		digest = vote.Digest()
	}
	sig, err := ethcrypto.Sign(digest[:], validatorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	block.QuorumCert = &types.QuorumCert{
		Height: block.Header.Height, Round: 0, BlockHash: headerHash,
		Signatures: []types.QuorumSignature{{Validator: ethcrypto.PubkeyToAddress(validatorKey.PrivateKey.PublicKey).Bytes(), Signature: sig}},
	}
	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: []*types.Block{block}})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}
	procErr = target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload})
	return before, target.GetHeight(), procErr
}

func TestNodeVoteBinding_ASyncedBlockNeedsACertificateSignedOverTheGenesisHash(t *testing.T) {
	// Binding from height 1: the synced block is above it.
	legacy := func(uint64, []byte) ([]byte, bool) { return nil, false }
	own := func(_ uint64, genesis []byte) ([]byte, bool) { return genesis, true }
	foreign := func(uint64, []byte) ([]byte, bool) { return foreignGenesisForVoteTests, true }

	if before, after, err := syncCertifiedBlock(t, 1, own); err != nil || after != before+1 {
		t.Fatalf("a certificate signed over the node's genesis hash must sync: before %d after %d err %v", before, after, err)
	}
	if before, after, _ := syncCertifiedBlock(t, 1, legacy); after != before {
		t.Fatalf("a legacy certificate at the binding height synced (%d -> %d)", before, after)
	}
	if before, after, _ := syncCertifiedBlock(t, 1, foreign); after != before {
		t.Fatalf("a certificate signed for another network synced (%d -> %d)", before, after)
	}
}

func TestNodeVoteBinding_ASyncedBlockBelowTheHeightKeepsTheLegacyCertificate(t *testing.T) {
	legacy := func(uint64, []byte) ([]byte, bool) { return nil, false }
	own := func(_ uint64, genesis []byte) ([]byte, bool) { return genesis, true }
	const farAway = 1_000_000
	if before, after, err := syncCertifiedBlock(t, farAway, legacy); err != nil || after != before+1 {
		t.Fatalf("a legacy certificate below the binding height must sync: before %d after %d err %v", before, after, err)
	}
	if before, after, _ := syncCertifiedBlock(t, farAway, own); after != before {
		t.Fatalf("a bound certificate below the binding height synced (%d -> %d)", before, after)
	}
	// Off entirely.
	if before, after, err := syncCertifiedBlock(t, 0, legacy); err != nil || after != before+1 {
		t.Fatalf("with the binding off a legacy certificate must sync: before %d after %d err %v", before, after, err)
	}
}

// ---- the equivocation proof of a report ---------------------------------------------

// signBoundVote is evidenceTestKey.signVote over the genesis-bound digest.
func (k *evidenceTestKey) signBoundVote(genesis, blockHash []byte, round int, voteType evidence.EquivocationVoteType, height uint64) evidence.EquivocationSignedVote {
	payload := &equivocationVotePayloadMirror{BlockHash: blockHash, Round: round, Type: voteType, Height: height}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	digest := types.BoundVoteDigest(genesis, b)
	sig, err := ethcrypto.Sign(digest[:], k.priv)
	if err != nil {
		panic(err)
	}
	return evidence.EquivocationSignedVote{BlockHash: "0x" + hex.EncodeToString(blockHash), Signature: "0x" + hex.EncodeToString(sig)}
}

func applyEvidenceReport(t *testing.T, node *Node, offender, reporter *evidenceTestKey, a, b evidence.EquivocationSignedVote, height uint64) error {
	t.Helper()
	proof := &evidence.EquivocationProof{Height: height, Round: 1, VoteType: evidence.EquivocationVotePrevote, VoteA: a, VoteB: b}
	ev, _ := buildAndSignEvidenceTx(t, offender.address(), reporter, proof, height)
	tx := signedSubmitEvidenceTx(t, ev, reporter, 0)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	node.state.BeginBlock(200, time.Unix(1_700_000_000, 0).UTC())
	defer node.state.EndBlock()
	return node.state.ApplyTransaction(tx)
}

func TestNodeVoteBinding_AnEquivocationReportIsHeldToTheBinding(t *testing.T) {
	const evidenceHeight = 50
	newNode := func(t *testing.T, bindingFrom uint64) (*Node, *evidenceTestKey, *evidenceTestKey) {
		genesisPath, _ := writeEvidenceTestGenesis(t)
		node := buildSwapAdminTestNode(t, genesisPath)
		offender, reporter := newEvidenceTestKey(t), newEvidenceTestKey(t)
		seedEvidenceReporterBond(t, node, reporter.address())
		node.SetVoteChainBindingActivationHeight(bindingFrom)
		return node, offender, reporter
	}
	isInvalidProof := func(err error) bool {
		var verr *evidence.ValidationError
		return errors.As(err, &verr) && verr.Reason == evidence.RejectReasonInvalidEquivocationProof
	}
	one, two := []byte{0x01}, []byte{0x02}

	t.Run("two legacy votes at the binding height are not a proof", func(t *testing.T) {
		node, offender, reporter := newNode(t, 40)
		err := applyEvidenceReport(t, node, offender, reporter,
			offender.signVote(one, 1, evidence.EquivocationVotePrevote, evidenceHeight),
			offender.signVote(two, 1, evidence.EquivocationVotePrevote, evidenceHeight), evidenceHeight)
		if !isInvalidProof(err) {
			t.Fatalf("legacy votes at the binding height: want an invalid-proof rejection, got %v", err)
		}
	})
	t.Run("a vote of this network and a vote of another are not a proof", func(t *testing.T) {
		node, offender, reporter := newNode(t, 40)
		err := applyEvidenceReport(t, node, offender, reporter,
			offender.signBoundVote(node.GenesisHash(), one, 1, evidence.EquivocationVotePrevote, evidenceHeight),
			offender.signBoundVote(foreignGenesisForVoteTests, two, 1, evidence.EquivocationVotePrevote, evidenceHeight), evidenceHeight)
		if !isInvalidProof(err) {
			t.Fatalf("a cross-network pair: want an invalid-proof rejection, got %v", err)
		}
	})
	t.Run("two conflicting votes over this network's genesis hash are a proof", func(t *testing.T) {
		node, offender, reporter := newNode(t, 40)
		err := applyEvidenceReport(t, node, offender, reporter,
			offender.signBoundVote(node.GenesisHash(), one, 1, evidence.EquivocationVotePrevote, evidenceHeight),
			offender.signBoundVote(node.GenesisHash(), two, 1, evidence.EquivocationVotePrevote, evidenceHeight), evidenceHeight)
		if err != nil {
			t.Fatalf("a genuine bound double-sign: %v", err)
		}
	})
	t.Run("a legacy proof about a height below the binding height still stands", func(t *testing.T) {
		node, offender, reporter := newNode(t, 60)
		err := applyEvidenceReport(t, node, offender, reporter,
			offender.signVote(one, 1, evidence.EquivocationVotePrevote, evidenceHeight),
			offender.signVote(two, 1, evidence.EquivocationVotePrevote, evidenceHeight), evidenceHeight)
		if err != nil {
			t.Fatalf("a legacy proof about height %d with the binding from 60: %v", evidenceHeight, err)
		}
	})
	t.Run("with the binding off a legacy proof stands", func(t *testing.T) {
		node, offender, reporter := newNode(t, 0)
		err := applyEvidenceReport(t, node, offender, reporter,
			offender.signVote(one, 1, evidence.EquivocationVotePrevote, evidenceHeight),
			offender.signVote(two, 1, evidence.EquivocationVotePrevote, evidenceHeight), evidenceHeight)
		if err != nil {
			t.Fatalf("a legacy proof with the binding off: %v", err)
		}
	})
}
