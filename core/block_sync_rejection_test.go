package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

// A block that this node refuses to commit when a peer sends it is something
// the peer has to answer for: the peer-to-peer server scores it down and
// disconnects it only when the handler reports an invalid payload, and
// otherwise it counts the message as handled. These tests pin which failures
// of commitSyncedBlock are reported that way and which stay plain errors
// (a fault of this node's own, or a chain that has moved on).

// syncRejectionCase builds the block a peer sends, from a node that is about
// to receive it.
type syncRejectionCase struct {
	name string
	// build returns the block to hand to the node. It may prepare the node
	// first, for instance by committing blocks.
	build func(t *testing.T, node *Node) *types.Block
	// want is text the error keeps from before it was classified.
	want string
}

func syncRejectionCases() []syncRejectionCase {
	tamper := func(change func(block *types.Block)) func(t *testing.T, node *Node) *types.Block {
		return func(t *testing.T, node *Node) *types.Block {
			t.Helper()
			block, err := node.CreateBlock(nil)
			if err != nil {
				t.Fatalf("create block: %v", err)
			}
			change(block)
			return block
		}
	}
	unappliableTx := func(t *testing.T) *types.Transaction {
		t.Helper()
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		// The sender has no account on the node, so a nonce far ahead of it
		// cannot apply.
		tx := &types.Transaction{
			ChainID: types.NHBChainID(),
			Type:    types.TxTypeHeartbeat,
			Nonce:   7,
			Value:   big.NewInt(0),
		}
		if err := tx.Sign(key.PrivateKey); err != nil {
			t.Fatalf("sign transaction: %v", err)
		}
		return tx
	}
	withTxs := func(t *testing.T, block *types.Block, txs ...*types.Transaction) {
		t.Helper()
		root, err := ComputeTxRoot(txs)
		if err != nil {
			t.Fatalf("compute tx root: %v", err)
		}
		block.Transactions = txs
		block.Header.TxRoot = root
		// The execution graph root of a block with other transactions is not the
		// one CreateBlock set.
		block.Header.ExecutionGraphRoot = nil
	}

	return []syncRejectionCase{
		{
			name: "state root that does not match",
			build: tamper(func(block *types.Block) {
				block.Header.StateRoot = bytes.Repeat([]byte{0x01}, 32)
			}),
			want: "state root mismatch",
		},
		{
			name: "transaction root that does not match",
			build: tamper(func(block *types.Block) {
				block.Header.TxRoot = bytes.Repeat([]byte{0x02}, 32)
			}),
			want: "tx root mismatch",
		},
		{
			name: "execution graph root that does not match",
			build: tamper(func(block *types.Block) {
				block.Header.ExecutionGraphRoot = bytes.Repeat([]byte{0x03}, 32)
			}),
			want: "execution graph root mismatch",
		},
		{
			name: "transaction that cannot be applied",
			build: func(t *testing.T, node *Node) *types.Block {
				t.Helper()
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block: %v", err)
				}
				withTxs(t, block, unappliableTx(t))
				return block
			},
			want: "apply transaction 0",
		},
		{
			name: "more transactions than a block may hold",
			build: func(t *testing.T, node *Node) *types.Block {
				t.Helper()
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block: %v", err)
				}
				cfg := node.globalConfigSnapshot()
				cfg.Blocks.MaxTxs = 1
				if err := node.SetGlobalConfig(cfg); err != nil {
					t.Fatalf("set global config: %v", err)
				}
				withTxs(t, block, unappliableTx(t), unappliableTx(t))
				return block
			},
			want: "block exceeds max transaction count",
		},
		{
			name: "timestamp before the one of the chain tip",
			build: func(t *testing.T, node *Node) *types.Block {
				t.Helper()
				commitEmptyBlocks(t, node, 1)
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block: %v", err)
				}
				block.Header.Timestamp = node.chain.LastTimestamp() - 1
				return block
			},
			want: ErrBlockTimestampOutOfWindow.Error(),
		},
		{
			name: "block that does not extend the chain tip",
			build: func(t *testing.T, node *Node) *types.Block {
				t.Helper()
				commitEmptyBlocks(t, node, 1)
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block: %v", err)
				}
				block.Header.PrevHash = bytes.Repeat([]byte{0x04}, 32)
				return block
			},
			want: "block prevhash mismatch",
		},
		{
			name: "quorum certificate that is required and missing",
			build: func(t *testing.T, node *Node) *types.Block {
				t.Helper()
				node.SetQuorumCertActivationHeight(0)
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block: %v", err)
				}
				return block
			},
			want: "quorum certificate verification failed",
		},
	}
}

func TestARejectedSyncedBlockIsReportedAsAnInvalidPayload(t *testing.T) {
	for _, tc := range syncRejectionCases() {
		for _, shape := range []string{"block", "blocks"} {
			t.Run(tc.name+"/"+shape, func(t *testing.T) {
				node := newTestNode(t)
				node.SetNetworkBroadcaster(&testBroadcaster{})
				block := tc.build(t, node)

				heightBefore := node.GetHeight()
				node.stateMu.RLock()
				rootBefore := node.state.CurrentRoot()
				node.stateMu.RUnlock()

				var msg *p2p.Message
				var err error
				if shape == "block" {
					msg, err = p2p.NewBlockMessage(block)
				} else {
					msg, err = p2p.NewBlocksMessage([]*types.Block{block})
				}
				if err != nil {
					t.Fatalf("build message: %v", err)
				}
				err = node.ProcessNetworkMessage(msg)
				if err == nil {
					t.Fatalf("the block was accepted")
				}
				if !p2p.IsInvalidPayload(err) {
					t.Fatalf("a rejected block must be reported as an invalid payload, got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("the error lost its reason %q: %v", tc.want, err)
				}
				if tc.want == ErrBlockTimestampOutOfWindow.Error() && !errors.Is(err, ErrBlockTimestampOutOfWindow) {
					t.Fatalf("the error no longer wraps ErrBlockTimestampOutOfWindow: %v", err)
				}

				// Rejecting the block changed nothing.
				if got := node.GetHeight(); got != heightBefore {
					t.Fatalf("height moved from %d to %d", heightBefore, got)
				}
				node.stateMu.RLock()
				rootAfter := node.state.CurrentRoot()
				node.stateMu.RUnlock()
				if rootAfter != rootBefore {
					t.Fatalf("state root moved from %x to %x", rootBefore.Bytes(), rootAfter.Bytes())
				}
			})
		}
	}
}

// The error a block is rejected with must read as before when it is not a
// peer's block: the proposal and commit paths of consensus get the same text.
func TestACommittedBlockRejectionKeepsItsMessageAndCause(t *testing.T) {
	node := newTestNode(t)
	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	block.Header.StateRoot = bytes.Repeat([]byte{0x01}, 32)

	err = node.CommitBlock(block)
	if err == nil {
		t.Fatalf("expected the state root mismatch to fail the commit")
	}
	if got, want := err.Error(), "state root mismatch"; got != want {
		t.Fatalf("commit error text changed: got %q want %q", got, want)
	}
	if p2p.IsInvalidPayload(err) {
		t.Fatalf("a commit from consensus is not a peer's payload: %v", err)
	}
	var rejected *blockRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected the failure to be marked as a rejection of the block, got %T", err)
	}
}

// A node recovers from a rejected block and goes on with the chain.
func TestANodeSyncsOnAfterARejectedBlock(t *testing.T) {
	node := newTestNode(t)
	node.SetNetworkBroadcaster(&testBroadcaster{})
	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	bad := *block.Header
	bad.StateRoot = bytes.Repeat([]byte{0x01}, 32)
	msg, err := p2p.NewBlockMessage(&types.Block{Header: &bad, Transactions: block.Transactions})
	if err != nil {
		t.Fatalf("build bad block message: %v", err)
	}
	if err := node.ProcessNetworkMessage(msg); !p2p.IsInvalidPayload(err) {
		t.Fatalf("expected an invalid payload, got %v", err)
	}

	good, err := p2p.NewBlockMessage(block)
	if err != nil {
		t.Fatalf("build block message: %v", err)
	}
	if err := node.ProcessNetworkMessage(good); err != nil {
		t.Fatalf("the genuine block must still be accepted: %v", err)
	}
	if got := node.GetHeight(); got != 1 {
		t.Fatalf("expected height 1, got %d", got)
	}
}

// A failure of this node, or a block at a height the chain has already reached,
// says nothing about the peer that sent the block.
func TestAFailureThatIsNotTheBlocksIsNotHeldAgainstThePeer(t *testing.T) {
	node := newTestNode(t)
	commitEmptyBlocks(t, node, 2)
	ahead := &types.Block{Header: &types.BlockHeader{Height: node.GetHeight() + 1}}
	settled := &types.Block{Header: &types.BlockHeader{Height: node.GetHeight()}}

	rejection := rejectedBlock(errors.New("state root mismatch"))
	apply := &blockApplyError{Index: 0, Err: errors.New("nonce too high")}
	infra := infrastructureFailure{}

	cases := []struct {
		name  string
		block *types.Block
		err   error
		want  bool
	}{
		{"rejection of the next block", ahead, rejection, true},
		{"transaction that cannot be applied", ahead, apply, true},
		{"rejection wrapped by the caller", ahead, fmt.Errorf("commit synced block: %w", rejection), true},
		{"no error", ahead, nil, false},
		{"plain error", ahead, errors.New("state commit failed"), false},
		{"validator set unavailable", ahead, fmt.Errorf("resolve validator set for quorum verification: %w", errors.New("no state")), false},
		{"rejection caused by this node's storage", ahead, rejectedBlock(fmt.Errorf("block lifecycle: %w", infra)), false},
		{"transaction failure caused by this node's storage", ahead, &blockApplyError{Index: 1, Err: infra}, false},
		{"rejection caused by a cancelled context", ahead, rejectedBlock(context.Canceled), false},
		{"rejection at a height the chain has reached", settled, rejection, false},
		{"transaction failure at a height the chain has reached", settled, apply, false},
		{"block without a header", &types.Block{}, rejection, false},
		{"no block", nil, rejection, false},
	}
	for _, tc := range cases {
		if got := node.syncedBlockRejected(tc.block, tc.err); got != tc.want {
			t.Errorf("%s: syncedBlockRejected = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// infrastructureFailure is an error a package other than core opts in to
// isInfrastructureError with.
type infrastructureFailure struct{}

func (infrastructureFailure) Error() string        { return "storage failure" }
func (infrastructureFailure) Infrastructure() bool { return true }

// Blocks at or below the height of the chain are skipped, whatever they hold.
func TestABlockAtASettledHeightIsNotHeldAgainstThePeer(t *testing.T) {
	node := newTestNode(t)
	node.SetNetworkBroadcaster(&testBroadcaster{})
	commitEmptyBlocks(t, node, 2)

	stale, err := node.GetBlockByHeight(2)
	if err != nil {
		t.Fatalf("get block: %v", err)
	}
	header := *stale.Header
	header.StateRoot = bytes.Repeat([]byte{0x01}, 32)
	msg, err := p2p.NewBlocksMessage([]*types.Block{{Header: &header, Transactions: stale.Transactions}})
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	if err := node.ProcessNetworkMessage(msg); err != nil {
		t.Fatalf("a block at a height the chain has settled must be ignored, got %v", err)
	}
}

// A peer that sends a block this node rejects is disconnected and scored down,
// and a peer that sends the genuine block is not.
func TestAPeerThatSendsARejectedBlockIsDisconnected(t *testing.T) {
	victim := newTestNode(t)
	victimServer, victimAddr := startWireServer(t, victim, victim)
	victim.SetNetworkBroadcaster(victimServer)

	block, err := victim.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	bad := *block.Header
	bad.StateRoot = bytes.Repeat([]byte{0x01}, 32)
	badMsg, err := p2p.NewBlockMessage(&types.Block{Header: &bad, Transactions: block.Transactions})
	if err != nil {
		t.Fatalf("build bad block message: %v", err)
	}
	goodMsg, err := p2p.NewBlockMessage(block)
	if err != nil {
		t.Fatalf("build block message: %v", err)
	}

	badSender, goodSender := &recordingHandler{}, &recordingHandler{}
	badServer, _ := startWireServer(t, victim, badSender)
	goodServer, _ := startWireServer(t, victim, goodSender)
	if err := badServer.Connect(victimAddr); err != nil {
		t.Fatalf("connect first peer: %v", err)
	}
	waitFor(t, 3*time.Second, "the victim to see the first peer", func() bool {
		return victimServer.SnapshotNetwork().Counts.Inbound == 1
	})

	if err := badServer.Broadcast(badMsg); err != nil {
		t.Fatalf("send bad block: %v", err)
	}
	waitFor(t, 3*time.Second, "the victim to drop the peer that sent a rejected block", func() bool {
		return victimServer.SnapshotNetwork().Counts.Total == 0
	})
	score, found := peerScore(victimServer, badServer.NodeID())
	if !found {
		t.Fatalf("no reputation entry for the peer that sent a rejected block")
	}
	if score >= 0 {
		t.Fatalf("the peer that sent a rejected block was left with a score of %d", score)
	}
	if got := victim.GetHeight(); got != 0 {
		t.Fatalf("the rejected block moved the chain to height %d", got)
	}

	if err := goodServer.Connect(victimAddr); err != nil {
		t.Fatalf("connect second peer: %v", err)
	}
	waitFor(t, 3*time.Second, "the victim to see the second peer", func() bool {
		return victimServer.SnapshotNetwork().Counts.Inbound == 1
	})
	if err := goodServer.Broadcast(goodMsg); err != nil {
		t.Fatalf("send block: %v", err)
	}
	waitFor(t, 3*time.Second, "the victim to commit the genuine block", func() bool {
		return victim.GetHeight() == 1
	})
	if got := victimServer.SnapshotNetwork().Counts.Total; got != 1 {
		t.Fatalf("the peer that sent the genuine block was dropped (%d peers connected)", got)
	}
	if score, found := peerScore(victimServer, goodServer.NodeID()); found && score < 0 {
		t.Fatalf("the peer that sent the genuine block was left with a score of %d", score)
	}
}

func peerScore(server *p2p.Server, nodeID string) (int, bool) {
	want := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(nodeID, "0x"), "0X"))
	for _, info := range server.NetPeers() {
		got := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(info.NodeID, "0x"), "0X"))
		if got == want {
			return info.Score, true
		}
	}
	return 0, false
}
