package core

import (
	"encoding/json"
	"math/big"
	"testing"

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
	// never registers any eligible validator, so GetValidatorSet() stays
	// empty -- a test-fixture artifact of this minimal test harness, not a
	// real-world condition (a live chain being upgraded to enforce this
	// check already has a real, populated validator set from actual
	// stake). Seed it directly (the same way other tests in this package
	// seed account state directly rather than replaying a full staking
	// transaction flow), re-applied after the first commit since ordinary
	// block processing recomputes ValidatorSet from real stake/epoch state
	// and would otherwise discard this direct, no-real-stake injection.
	validatorAddr := validatorKey.PubKey().Address().Bytes()
	seedValidatorSet := func() {
		target.state.ValidatorSet = map[string]*big.Int{string(validatorAddr): big.NewInt(1)}
	}
	seedValidatorSet()

	first, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create first block: %v", err)
	}
	if err := target.CommitBlock(first); err != nil {
		t.Fatalf("commit first block: %v", err)
	}
	seedValidatorSet()
	target.SetQuorumCertActivationHeight(target.GetHeight())

	heightBefore := target.GetHeight()

	block, err := target.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if block == nil || block.Header == nil {
		t.Fatalf("create block: nil block/header")
	}

	validatorSet := target.GetValidatorSet()
	if len(validatorSet) == 0 {
		t.Fatalf("expected a non-empty validator set after the first block committed")
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
