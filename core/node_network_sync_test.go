package core

import (
	"encoding/json"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/storage"
)

// A status report or a block ahead of this node comes from a peer that has
// proved nothing, and it is answered with a request that goes to every peer. A
// stream of such messages must therefore not become a stream of requests.

func statusClaiming(t *testing.T, height uint64) *p2p.Message {
	t.Helper()
	msg, err := p2p.NewStatusMessage(height)
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	return msg
}

func blockAt(t *testing.T, height uint64) *p2p.Message {
	t.Helper()
	msg, err := p2p.NewBlockMessage(&types.Block{Header: &types.BlockHeader{Height: height}})
	if err != nil {
		t.Fatalf("build block: %v", err)
	}
	return msg
}

func blocksAt(t *testing.T, height uint64) *p2p.Message {
	t.Helper()
	msg, err := p2p.NewBlocksMessage([]*types.Block{{Header: &types.BlockHeader{Height: height}}})
	if err != nil {
		t.Fatalf("build blocks: %v", err)
	}
	return msg
}

func sentOfType(b *testBroadcaster, msgType byte) int {
	n := 0
	for _, msg := range b.messages {
		if msg.Type == msgType {
			n++
		}
	}
	return n
}

// requestedFrom decodes the starting heights of the block requests sent.
func requestedFrom(t *testing.T, b *testBroadcaster) []uint64 {
	t.Helper()
	var from []uint64
	for _, msg := range b.messages {
		if msg.Type != p2p.MsgTypeGetBlocks {
			continue
		}
		var payload p2p.GetBlocksPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		from = append(from, payload.From)
	}
	return from
}

func TestAStatusFloodIsAnsweredWithOneBlockRequest(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	const flood = 50
	for i := 0; i < flood; i++ {
		if err := node.ProcessNetworkMessage(statusClaiming(t, 1<<40)); err != nil {
			t.Fatalf("process status %d: %v", i, err)
		}
	}
	if got := sentOfType(broadcaster, p2p.MsgTypeGetBlocks); got != 1 {
		t.Fatalf("%d status reports made the node ask every peer for blocks %d times, want once", flood, got)
	}
}

func TestABlockAheadFloodIsAnsweredWithOneBlockRequest(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	const flood = 50
	for i := 0; i < flood; i++ {
		if err := node.ProcessNetworkMessage(blockAt(t, 10)); err != nil {
			t.Fatalf("process block %d: %v", i, err)
		}
	}
	if got := sentOfType(broadcaster, p2p.MsgTypeGetBlocks); got != 1 {
		t.Fatalf("%d blocks ahead made the node ask every peer for blocks %d times, want once", flood, got)
	}
}

func TestStatusReportsAndBlocksAheadShareOneBlockRequest(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)
	sender := &peerSink{id: "sender"}

	for i := 0; i < 20; i++ {
		if err := node.HandlePeerMessage(sender, statusClaiming(t, 1<<40)); err != nil {
			t.Fatalf("handle status: %v", err)
		}
		if err := node.HandlePeerMessage(sender, blockAt(t, 10)); err != nil {
			t.Fatalf("handle block: %v", err)
		}
		if err := node.ProcessNetworkMessage(blocksAt(t, 10)); err != nil {
			t.Fatalf("process blocks: %v", err)
		}
	}
	if got := requestedFrom(t, broadcaster); len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected one request for the blocks from height 1, got %v", got)
	}
}

func TestABlockRequestIsRepeatedOnceTheGapHasPassed(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	if err := node.ProcessNetworkMessage(statusClaiming(t, 100)); err != nil {
		t.Fatalf("process status: %v", err)
	}
	time.Sleep(networkBlockSyncRequestGap + 100*time.Millisecond)
	if err := node.ProcessNetworkMessage(statusClaiming(t, 100)); err != nil {
		t.Fatalf("process status: %v", err)
	}
	if got := requestedFrom(t, broadcaster); len(got) != 2 || got[0] != 1 || got[1] != 1 {
		t.Fatalf("a request that got no answer must be repeated after the gap, got %v", got)
	}
}

func TestBlockRequestsForNewHeightsAreNotHeldBack(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	if err := node.ProcessNetworkMessage(statusClaiming(t, 100)); err != nil {
		t.Fatalf("process status: %v", err)
	}
	commitEmptyBlocks(t, node, 1)
	// The node moved on: what it needs now is a different request, and the one
	// just sent does not stand in for it.
	if err := node.ProcessNetworkMessage(statusClaiming(t, 100)); err != nil {
		t.Fatalf("process status: %v", err)
	}
	commitEmptyBlocks(t, node, 1)
	if err := node.ProcessNetworkMessage(blockAt(t, 50)); err != nil {
		t.Fatalf("process block: %v", err)
	}
	got := requestedFrom(t, broadcaster)
	want := []uint64{1, 2, 3}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("expected requests for %v, got %v", want, got)
	}
}

// Catching up on a long chain asks for the next batch as soon as the previous
// one is applied, however soon after the request for that one.
func TestCatchUpAsksForTheNextBatchAsSoonAsTheLastOneIsApplied(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	sourceDB := storage.NewMemDB()
	t.Cleanup(func() { sourceDB.Close() })
	source, err := NewNode(sourceDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new source node: %v", err)
	}
	targetDB := storage.NewMemDB()
	t.Cleanup(func() { targetDB.Close() })
	target, err := NewNode(targetDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new target node: %v", err)
	}
	broadcaster := &testBroadcaster{}
	target.SetNetworkBroadcaster(broadcaster)

	commitEmptyBlocks(t, source, networkBlockSyncBatchSize+2)
	var batches [2][]*types.Block
	for height := uint64(1); height <= source.GetHeight(); height++ {
		block, err := source.GetBlockByHeight(height)
		if err != nil {
			t.Fatalf("get block %d: %v", height, err)
		}
		i := 0
		if height > networkBlockSyncBatchSize {
			i = 1
		}
		batches[i] = append(batches[i], block)
	}

	if err := target.ProcessNetworkMessage(statusClaiming(t, source.GetHeight())); err != nil {
		t.Fatalf("process status: %v", err)
	}
	if err := target.handleNetworkBlocks(batches[0]); err != nil {
		t.Fatalf("apply the first batch: %v", err)
	}
	if err := target.handleNetworkBlocks(batches[1]); err != nil {
		t.Fatalf("apply the second batch: %v", err)
	}
	if got := target.GetHeight(); got != source.GetHeight() {
		t.Fatalf("expected the target at height %d, got %d", source.GetHeight(), got)
	}
	got := requestedFrom(t, broadcaster)
	want := []uint64{1, networkBlockSyncBatchSize + 1}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected the request for the first blocks and then for the next batch %v, got %v", want, got)
	}
}

// One anonymous connection that keeps claiming a huge height must not get the
// node disconnected by its own peers: to them every request the node sends
// counts against the budget of the node's address.
func TestAStatusFloodFromAnAnonymousPeerDoesNotGetTheNodeDroppedByItsPeers(t *testing.T) {
	victim := newTestNode(t)
	victimServer, victimAddr := startWireServer(t, victim, victim)
	victim.SetNetworkBroadcaster(victimServer)

	honest := &recordingHandler{}
	honestServer, honestAddr := startWireServer(t, victim, honest)
	if err := victimServer.Connect(honestAddr); err != nil {
		t.Fatalf("connect victim to honest peer: %v", err)
	}
	waitFor(t, 3*time.Second, "the honest peer to see the victim", func() bool {
		return honestServer.SnapshotNetwork().Counts.Inbound == 1
	})

	attackerServer, _ := startWireServer(t, victim, &recordingHandler{})
	if err := attackerServer.Connect(victimAddr); err != nil {
		t.Fatalf("connect attacker to victim: %v", err)
	}
	waitFor(t, 3*time.Second, "the victim to see the attacker", func() bool {
		return victimServer.SnapshotNetwork().Counts.Inbound == 1
	})

	claim := statusClaiming(t, 1<<40)
	const flood = 300
	for i := 0; i < flood; i++ {
		if err := attackerServer.Broadcast(claim); err != nil {
			t.Fatalf("send claim: %v", err)
		}
		if i%16 == 15 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	// The claim is still acted on once, and then the flood is waited out.
	waitFor(t, 3*time.Second, "the victim to ask its peer for blocks", func() bool {
		return honest.count(p2p.MsgTypeGetBlocks) >= 1
	})
	time.Sleep(700 * time.Millisecond)

	if got := honestServer.SnapshotNetwork().Counts.Total; got != 1 {
		t.Fatalf("the honest peer dropped the victim over requests the attacker induced (%d peers left), it saw %d block requests",
			got, honest.count(p2p.MsgTypeGetBlocks))
	}
	if got := honest.count(p2p.MsgTypeGetBlocks); got > 3 {
		t.Fatalf("the honest peer received %d block requests for %d claims", got, flood)
	}
}
