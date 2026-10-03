package core

// A validator that is one block behind its peer hears the peer's consensus messages for
// the height after the peer's last block. What it does about it is the engine's catch-up
// request, and the peer answers that with its node's block-request handler, so the two
// are tested together: two real nodes, the laggard's real engine, what the engine
// broadcasts carried to the peer through the node's message entry point and what the
// peer answers carried back, as the wire would.

import (
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/bft"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/storage"
)

// singleValidatorNode is a node whose validator set is the one validator the test
// names (the dev genesis does not include the test's key), the rest being the real node.
type singleValidatorNode struct {
	*Node
	set map[string]*big.Int
}

func (n *singleValidatorNode) GetValidatorSet() map[string]*big.Int { return n.set }

func newTestNodeWithKey(t *testing.T, key *crypto.PrivateKey) *Node {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	node, err := NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

func TestValidatorOneBlockBehindItsPeerCatchesUp(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()

	peer := newTestNodeWithKey(t, key)
	laggard := newTestNodeWithKey(t, key)
	peerOut := &testBroadcaster{}
	peer.SetNetworkBroadcaster(peerOut)
	laggard.SetNetworkBroadcaster(&testBroadcaster{})

	// The peer has three blocks. The laggard took the first two and missed the third.
	commitEmptyBlocks(t, peer, 3)
	var held []*types.Block
	for height := uint64(1); height <= 2; height++ {
		block, err := peer.GetBlockByHeight(height)
		if err != nil {
			t.Fatalf("peer block %d: %v", height, err)
		}
		held = append(held, block)
	}
	if err := laggard.handleNetworkBlocks(held); err != nil {
		t.Fatalf("apply the first two blocks: %v", err)
	}
	if peer.GetHeight() != 3 || laggard.GetHeight() != 2 {
		t.Fatalf("test setup: peer at %d and laggard at %d, want 3 and 2", peer.GetHeight(), laggard.GetHeight())
	}

	engineOut := &testBroadcaster{}
	engine := bft.NewEngine(&singleValidatorNode{Node: laggard, set: map[string]*big.Int{string(addr): big.NewInt(1)}}, key, engineOut)

	// The peer is deciding height 4, and its proposal for it reaches the laggard.
	block := types.NewBlock(&types.BlockHeader{Height: 4, Validator: addr}, nil)
	proposal := &bft.SignedProposal{Proposal: &bft.Proposal{Block: block, Round: 0, ValidRound: -1}, Proposer: addr}
	encoded, err := json.Marshal(proposal.Proposal)
	if err != nil {
		t.Fatalf("encode proposal: %v", err)
	}
	digest := sha256.Sum256(encoded)
	signature, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	proposal.Signature = &bft.Signature{Scheme: bft.SignatureSchemeSecp256k1, Signature: signature}
	if err := engine.HandleProposal(proposal); err != nil {
		t.Fatalf("handle proposal: %v", err)
	}

	requests := requestedFrom(t, engineOut)
	if len(requests) != 1 {
		t.Fatalf("expected one block request from the laggard, got %v", requests)
	}
	for _, msg := range engineOut.messages {
		if err := peer.ProcessNetworkMessage(msg); err != nil {
			t.Fatalf("peer handles message type %d: %v", msg.Type, err)
		}
	}
	for _, reply := range peerOut.messages {
		if err := laggard.ProcessNetworkMessage(reply); err != nil {
			t.Fatalf("laggard handles the peer's reply: %v", err)
		}
	}
	if got := laggard.GetHeight(); got != peer.GetHeight() {
		t.Fatalf("the laggard is at height %d, the peer at %d: its block request started at %d, the peer's last block is %d and it answered %d message(s)",
			got, peer.GetHeight(), requests[0], peer.GetHeight(), len(peerOut.messages))
	}
	if sentOfType(peerOut, p2p.MsgTypeBlocks) == 0 {
		t.Fatalf("the peer sent no blocks")
	}
}
