package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/storage"
)

// newQuorumCertTestNode is like newTestNode (core/mint_test.go) but also
// returns the node's own validator private key -- needed here to sign a
// genuine QuorumCert matching the node's real, autogenesis-registered
// validator set, which newTestNode's shared helper doesn't expose (and
// shouldn't need to, for the many other tests that don't care about it).
func newQuorumCertTestNode(t *testing.T) (*Node, *crypto.PrivateKey) {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
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
	return node, validatorKey
}

// TestUnauthenticatedPeerBlockBypassesValidatorCheck is the regression test
// for NHB-TRIAGE-C1 (unauthenticated P2P block commit): the P2P block-sync
// path (MsgTypeBlocks/MsgTypeBlock, handled by Node.handleNetworkBlocks ->
// commitSyncedBlock -> commitBlock) used to accept and commit any
// structurally-valid block with no check at all that Header.Validator
// belonged to the actual validator set, and no signature or BFT quorum
// verification of any kind -- unlike the legitimate consensus path
// (MsgTypeProposal/MsgTypeVote, handled by the bft.Engine), which does
// verify both. This was confirmed real earlier in this investigation with
// this same test asserting the vulnerable behavior; it now asserts the fix
// (a QuorumCert, verified against the real validator set, is required once
// quorum-certificate enforcement is enabled -- see
// Node.SetQuorumCertActivationHeight).
//
// This test constructs a block using the node's own CreateBlock (so it is
// structurally self-consistent: correct height, PrevHash, TxRoot, and a
// StateRoot the node will independently re-derive and match), tampers ONLY
// the Header.Validator field to an address with no relationship to the
// node's real validator set, and feeds it back in exactly as if it had
// arrived from an untrusted network peer -- with NO QuorumCert attached at
// all, matching the original, pre-fix vulnerability exactly (nothing
// anywhere ever produced one).
func TestUnauthenticatedPeerBlockBypassesValidatorCheck(t *testing.T) {
	target, _ := newQuorumCertTestNode(t)
	target.SetNetworkBroadcaster(&testBroadcaster{})
	target.SetQuorumCertActivationHeight(0) // enforce from height 1

	heightBefore := target.GetHeight()

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if block == nil || block.Header == nil {
		t.Fatalf("create block: nil block/header")
	}

	attacker := make([]byte, 20)
	for i := range attacker {
		attacker[i] = 0xEE
	}
	block.Header.Validator = attacker
	// No QuorumCert attached -- this is the crux of the original bug: there
	// was nothing anywhere that could have supplied one, and nothing that
	// checked for its absence either.

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: []*types.Block{block}})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}

	procErr := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload})
	heightAfter := target.GetHeight()

	if procErr == nil {
		t.Fatalf("SECURITY: a peer-supplied block with an attacker-controlled Header.Validator and no QuorumCert was accepted with no error -- height %d -> %d", heightBefore, heightAfter)
	}
	if heightAfter != heightBefore {
		t.Fatalf("SECURITY: height advanced (%d -> %d) despite ProcessNetworkMessage returning an error -- partial commit?", heightBefore, heightAfter)
	}
	t.Logf("correctly rejected: %v", procErr)
}

// TestQuorumCertifiedBlockStillSyncsViaP2P is the companion positive case:
// once quorum-certificate enforcement is enabled, a block that DOES carry a
// genuine QuorumCert -- signed by the real validator(s), covering >=2/3 of
// the actual validator set's voting power -- must still sync normally
// through the exact same untrusted P2P path the attack test above uses.
// Without this, the fix above would be indistinguishable from simply
// breaking P2P sync entirely.
func TestQuorumCertifiedBlockStillSyncsViaP2P(t *testing.T) {
	target, validatorKey := newQuorumCertTestNode(t)
	target.SetNetworkBroadcaster(&testBroadcaster{})

	// A bare autogenesis chain (no genesis file, no staking transaction)
	// never registers any eligible validator, so ValidatorSet stays empty
	// -- a test-fixture artifact of this minimal test harness, not a
	// real-world condition (a live chain being upgraded to enforce this
	// check already has a real, populated validator set from actual
	// stake). Directly writing ValidatorSet in memory (or even persisting
	// it once) doesn't survive block processing here -- ProcessBlockLifecycle
	// recomputes it from real EligibleValidators/stake on every block via
	// the real epoch-rollover machinery, discarding an ad hoc injection.
	// So this seeds an ELIGIBLE validator the same way (proven-working
	// pattern from TestCommitBlockSequentialHeightsAdvanceEpochs) and lets
	// two real committed blocks promote it into ValidatorSet through the
	// actual production code path, not a shortcut around it.
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
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(15_000),
		EngagementScore:         0,
		EngagementLastHeartbeat: uint64(currentTime.Unix()),
		ValidatorRegistered:     true,
	}
	if err := target.state.setAccount(validatorAddr, account); err != nil {
		t.Fatalf("seed validator account: %v", err)
	}

	var heightBefore uint64
	for i := 0; i < 2; i++ {
		currentTime = currentTime.Add(time.Second)
		block, err := target.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create seeding block %d: %v", i, err)
		}
		if err := target.CommitBlock(block); err != nil {
			t.Fatalf("commit seeding block %d: %v", i, err)
		}
		heightBefore = target.GetHeight()
	}
	target.SetQuorumCertActivationHeight(heightBefore)

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if block == nil || block.Header == nil {
		t.Fatalf("create block: nil block/header")
	}

	validatorSet, err := target.validatorSetAtHeight(heightBefore)
	if err != nil {
		t.Fatalf("validator set at height %d: %v", heightBefore, err)
	}
	if len(validatorSet) == 0 {
		t.Fatalf("expected a non-empty historical validator set at height %d", heightBefore)
	}

	headerHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash header: %v", err)
	}
	vote := &types.PrecommitVote{BlockHash: headerHash, Round: 0, Type: types.VoteTypePrecommit, Height: block.Header.Height}
	sig, err := vote.Sign(validatorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign quorum vote: %v", err)
	}
	block.QuorumCert = &types.QuorumCert{
		Height:     block.Header.Height,
		Round:      0,
		BlockHash:  headerHash,
		Signatures: []types.QuorumSignature{*sig},
	}

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: []*types.Block{block}})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}

	if err := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload}); err != nil {
		t.Fatalf("expected a genuinely quorum-certified block to sync successfully, got: %v", err)
	}
	if got := target.GetHeight(); got != heightBefore+1 {
		t.Fatalf("expected height to advance by 1 (%d -> %d), got %d", heightBefore, heightBefore+1, got)
	}
}

// TestQuorumCertVerifiesAgainstHistoricalNotLiveValidatorSet proves
// commitBlock's QuorumCert check actually consults the validator set that
// was really active at the block's parent height (via
// Node.validatorSetAtHeight, reading the parent block's own committed
// state) rather than whatever n.state.ValidatorSet happens to hold live in
// memory right now. In this codebase's current sync model the two are
// normally identical (blocks are always applied strictly in order, one
// full commit at a time, so nothing else could have changed
// n.state.ValidatorSet in between) -- so a plain end-to-end sync test
// alone can't tell which source of truth is actually being consulted. This
// test forces them apart deliberately: after a validator is legitimately
// promoted via two real committed blocks, it corrupts the LIVE in-memory
// ValidatorSet map to a different set entirely (simulating what a future
// non-sequential/batched verification path -- e.g. the dormant
// core/sync.RangeSyncer fast-sync machinery, if ever wired up -- could
// otherwise let drift out of sync with committed history) and confirms a
// block whose QuorumCert is signed by the REAL, historically-committed
// validator still verifies correctly, proving it was never checked against
// the corrupted live map at all.
func TestQuorumCertVerifiesAgainstHistoricalNotLiveValidatorSet(t *testing.T) {
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
	currentTime := time.Unix(1_950_100_000, 0).UTC()
	target.SetTimeSource(func() time.Time { return currentTime })
	account := &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(15_000),
		EngagementLastHeartbeat: uint64(currentTime.Unix()),
		ValidatorRegistered:     true,
	}
	if err := target.state.setAccount(validatorAddr, account); err != nil {
		t.Fatalf("seed validator account: %v", err)
	}

	var heightBefore uint64
	for i := 0; i < 2; i++ {
		currentTime = currentTime.Add(time.Second)
		block, err := target.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create seeding block %d: %v", i, err)
		}
		if err := target.CommitBlock(block); err != nil {
			t.Fatalf("commit seeding block %d: %v", i, err)
		}
		heightBefore = target.GetHeight()
	}
	target.SetQuorumCertActivationHeight(heightBefore)

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}

	// Deliberately desync the LIVE in-memory validator set from what was
	// actually committed at heightBefore -- an attacker-controlled address
	// with power, and the real validator removed entirely. If commitBlock
	// verified against this live map instead of the historical one, this
	// legitimate block (signed by the real validator) would be rejected
	// and an attacker-signed one would be accepted.
	corruptedAddr := make([]byte, 20)
	for i := range corruptedAddr {
		corruptedAddr[i] = 0xFF
	}
	target.state.ValidatorSet = map[string]*big.Int{string(corruptedAddr): big.NewInt(1)}

	headerHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash header: %v", err)
	}
	vote := &types.PrecommitVote{BlockHash: headerHash, Round: 0, Type: types.VoteTypePrecommit, Height: block.Header.Height}
	sig, err := vote.Sign(validatorKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign quorum vote: %v", err)
	}
	block.QuorumCert = &types.QuorumCert{
		Height:     block.Header.Height,
		Round:      0,
		BlockHash:  headerHash,
		Signatures: []types.QuorumSignature{*sig},
	}

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: []*types.Block{block}})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}
	if err := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload}); err != nil {
		t.Fatalf("expected the real, historically-committed validator's QuorumCert to still verify despite a corrupted live ValidatorSet map, got: %v", err)
	}
	if got := target.GetHeight(); got != heightBefore+1 {
		t.Fatalf("expected height to advance by 1 (%d -> %d), got %d", heightBefore, heightBefore+1, got)
	}
}
