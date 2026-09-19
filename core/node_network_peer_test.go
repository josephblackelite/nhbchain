package core

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

// peerSink stands in for the send path to one connected peer.
type peerSink struct {
	id   string
	mu   sync.Mutex
	msgs []*p2p.Message
}

func (s *peerSink) ID() string { return s.id }

func (s *peerSink) Enqueue(msg *p2p.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *peerSink) received() []*p2p.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*p2p.Message(nil), s.msgs...)
}

func commitEmptyBlocks(t *testing.T, node *Node, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		block, err := node.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create block %d: %v", i, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit block %d: %v", i, err)
		}
	}
}

func TestHandlePeerMessageAnswersAStatusRequestToTheRequesterOnly(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)
	requester := &peerSink{id: "requester"}

	if err := node.HandlePeerMessage(requester, &p2p.Message{Type: p2p.MsgTypeGetStatus, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("handle get status: %v", err)
	}
	got := requester.received()
	if len(got) != 1 || got[0].Type != p2p.MsgTypeStatus {
		t.Fatalf("expected one status message for the requester, got %v", got)
	}
	if len(broadcaster.messages) != 0 {
		t.Fatalf("a status request must not be answered to every peer, %d messages broadcast", len(broadcaster.messages))
	}
}

func TestHandlePeerMessageAnswersABlockRequestToTheRequesterOnly(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)
	commitEmptyBlocks(t, node, 3)
	requester := &peerSink{id: "requester"}

	payload, err := json.Marshal(p2p.GetBlocksPayload{From: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := node.HandlePeerMessage(requester, &p2p.Message{Type: p2p.MsgTypeGetBlocks, Payload: payload}); err != nil {
		t.Fatalf("handle get blocks: %v", err)
	}
	got := requester.received()
	if len(got) != 1 || got[0].Type != p2p.MsgTypeBlocks {
		t.Fatalf("expected one blocks message for the requester, got %v", got)
	}
	var reply p2p.BlocksPayload
	if err := json.Unmarshal(got[0].Payload, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if len(reply.Blocks) != 3 {
		t.Fatalf("expected the 3 committed blocks, got %d", len(reply.Blocks))
	}
	if len(broadcaster.messages) != 0 {
		t.Fatalf("a block request must not be answered to every peer, %d messages broadcast", len(broadcaster.messages))
	}
}

func TestHandlePeerMessageRoutesOtherMessagesLikeProcessNetworkMessage(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)
	sender := &peerSink{id: "sender"}

	// A malformed transaction is rejected the same way on both paths.
	bad := &p2p.Message{Type: p2p.MsgTypeTx, Payload: []byte(`{"nonce":`)}
	direct := node.ProcessNetworkMessage(bad)
	viaPeer := node.HandlePeerMessage(sender, bad)
	if direct == nil || viaPeer == nil || direct.Error() != viaPeer.Error() {
		t.Fatalf("expected the same error on both paths, got %v and %v", direct, viaPeer)
	}
	var invalid = &p2p.Message{Type: p2p.MsgTypeTx, Payload: []byte(`{}`)}
	if err := node.HandlePeerMessage(sender, invalid); !errors.Is(err, p2p.ErrInvalidPayload) {
		t.Fatalf("expected an invalid transaction to surface as ErrInvalidPayload, got %v", err)
	}

	// Without a sender there is nobody to answer: fall back to broadcasting.
	if err := node.HandlePeerMessage(nil, &p2p.Message{Type: p2p.MsgTypeGetStatus, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("handle get status without a sender: %v", err)
	}
	if len(broadcaster.messages) != 1 || broadcaster.messages[0].Type != p2p.MsgTypeStatus {
		t.Fatalf("expected the fallback to broadcast the status, got %v", broadcaster.messages)
	}
	if got := sender.received(); len(got) != 0 {
		t.Fatalf("nothing should have been sent to the sender, got %v", got)
	}
}

func TestSyncBatchStopsBeforeTheBlockThatBreaksTheByteCap(t *testing.T) {
	node := newTestNode(t)
	commitEmptyBlocks(t, node, 6)

	first, err := node.chain.GetBlockByHeight(1)
	if err != nil {
		t.Fatalf("get block: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("encode block: %v", err)
	}
	blockSize := len(encoded)

	if got := node.syncBatch(1, 6, 1<<20); len(got) != 6 {
		t.Fatalf("expected all 6 blocks under a generous cap, got %d", len(got))
	}
	if got := node.syncBatch(1, 6, 2*blockSize); len(got) < 1 || len(got) > 2 {
		t.Fatalf("expected the cap of two blocks' worth to stop the batch at 2 blocks, got %d", len(got))
	}
	if got := node.syncBatch(1, 6, 1); len(got) != 1 {
		t.Fatalf("expected the first block to be sent whatever its size, got %d", len(got))
	}
	if got := node.syncBatch(3, 5, 1<<20); len(got) != 3 || got[0].Header.Height != 3 {
		t.Fatalf("expected blocks 3 to 5, got %d blocks", len(got))
	}
}

// recordingHandler counts what a plain peer-to-peer node receives.
type recordingHandler struct {
	mu    sync.Mutex
	types map[byte]int
}

func (h *recordingHandler) HandleMessage(msg *p2p.Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.types == nil {
		h.types = make(map[byte]int)
	}
	h.types[msg.Type]++
	return nil
}

func (h *recordingHandler) count(msgType byte) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.types[msgType]
}

// startWireServer runs a peer-to-peer server on an ephemeral loopback port that
// belongs to the same chain as node, and returns it with its address.
func startWireServer(t *testing.T, node *Node, handler p2p.MessageHandler) (*p2p.Server, string) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	server := p2p.NewServer(handler, key, p2p.ServerConfig{
		ListenAddress:    "127.0.0.1:0",
		ChainID:          node.ChainID(),
		GenesisHash:      node.GenesisHash(),
		ClientVersion:    "wire-test/1.0",
		MaxPeers:         8,
		MaxInbound:       8,
		MaxOutbound:      8,
		MaxMessageBytes:  1 << 20,
		RateMsgsPerSec:   1000,
		RateBurst:        1000,
		ReadTimeout:      30 * time.Second,
		WriteTimeout:     5 * time.Second,
		HandshakeTimeout: 3 * time.Second,
		PingInterval:     time.Hour,
	})
	go server.Start()
	t.Cleanup(func() { server.Stop() })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, addr := range server.ListenAddresses() {
			if !strings.HasSuffix(addr, ":0") {
				return server, addr
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server did not start listening")
	return nil, ""
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestChainDataRequestsOverTheWireAreAnsweredToTheRequesterOnly connects a
// node to two peers and lets one of them ask for blocks and for the status: the
// other must not be sent anything it did not ask for.
func TestChainDataRequestsOverTheWireAreAnsweredToTheRequesterOnly(t *testing.T) {
	node := newTestNode(t)
	commitEmptyBlocks(t, node, 3)
	nodeServer, nodeAddr := startWireServer(t, node, node)
	node.SetNetworkBroadcaster(nodeServer)

	asker, bystander := &recordingHandler{}, &recordingHandler{}
	askerServer, _ := startWireServer(t, node, asker)
	bystanderServer, _ := startWireServer(t, node, bystander)
	for _, peer := range []*p2p.Server{askerServer, bystanderServer} {
		if err := peer.Connect(nodeAddr); err != nil {
			t.Fatalf("connect: %v", err)
		}
	}
	waitFor(t, 3*time.Second, "both peers to be connected", func() bool {
		return nodeServer.SnapshotNetwork().Counts.Inbound == 2
	})

	getBlocks, err := p2p.NewGetBlocksMessage(1)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	getStatus, err := p2p.NewGetStatusMessage()
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for _, msg := range []*p2p.Message{getBlocks, getStatus} {
		if err := askerServer.Broadcast(msg); err != nil {
			t.Fatalf("send request: %v", err)
		}
	}

	waitFor(t, 3*time.Second, "the requester to be answered", func() bool {
		return asker.count(p2p.MsgTypeBlocks) == 1 && asker.count(p2p.MsgTypeStatus) == 1
	})
	time.Sleep(300 * time.Millisecond)
	if got := bystander.count(p2p.MsgTypeBlocks) + bystander.count(p2p.MsgTypeStatus); got != 0 {
		t.Fatalf("a peer that asked for nothing was sent %d answers meant for another peer", got)
	}
}
