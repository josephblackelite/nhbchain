package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/network"
	"nhbchain/p2p"
	networkv1 "nhbchain/proto/network/v1"
	"nhbchain/storage"
)

// recordingPeer keeps what a plain peer-to-peer node is sent.
type recordingPeer struct {
	mu   sync.Mutex
	msgs []*p2p.Message
}

func (r *recordingPeer) HandleMessage(msg *p2p.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, &p2p.Message{Type: msg.Type, Payload: append([]byte(nil), msg.Payload...)})
	return nil
}

func (r *recordingPeer) all(msgType byte) []*p2p.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*p2p.Message
	for _, msg := range r.msgs {
		if msg.Type == msgType {
			out = append(out, msg)
		}
	}
	return out
}

func startWirePeer(t *testing.T, node *core.Node, handler p2p.MessageHandler) (*p2p.Server, string) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	server := p2p.NewServer(handler, key, p2p.ServerConfig{
		ListenAddress:    "127.0.0.1:0",
		ChainID:          node.ChainID(),
		GenesisHash:      bytes.Clone(node.GenesisHash()),
		ClientVersion:    "split-topology-test/1.0",
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
	t.Fatal("server did not start listening")
	return nil, ""
}

func waitForCondition(t *testing.T, d time.Duration, what string, cond func() bool) {
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

// TestSplitTopologyAnswersChainRequestsToTheRequesterOnly runs the pieces of the
// split deployment against each other: a p2p server whose handler is the relay,
// the gRPC bridge, and a consensus node attached through maintainNetworkStream.
// One of two connected peers asks for the status and for blocks; only that peer
// may be sent the answers.
func TestSplitTopologyAnswersChainRequestsToTheRequesterOnly(t *testing.T) {
	cases := []struct {
		name string
		// withNodeBroadcaster also attaches the broadcaster to the node, as the
		// single-binary node does for its p2p server.
		withNodeBroadcaster bool
	}{
		{name: "node as wired by consensusd"},
		{name: "node with a broadcaster attached", withNodeBroadcaster: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NHB_ENV", "dev")
			db := storage.NewMemDB()
			t.Cleanup(func() { db.Close() })
			validatorKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate validator key: %v", err)
			}
			node, err := core.NewNode(db, validatorKey, "", true, false)
			if err != nil {
				t.Fatalf("new node: %v", err)
			}
			const committed = 3
			for i := 0; i < committed; i++ {
				block, err := node.CreateBlock(nil)
				if err != nil {
					t.Fatalf("create block %d: %v", i, err)
				}
				if err := node.CommitBlock(block); err != nil {
					t.Fatalf("commit block %d: %v", i, err)
				}
			}

			// The p2pd side: the relay is the p2p server's handler and is served
			// over gRPC.
			relay := network.NewRelay()
			hub, hubAddr := startWirePeer(t, node, relay)
			relay.SetServer(hub)
			svc, err := network.NewService(relay, nil, network.WithAllowUnauthenticatedReads(true))
			if err != nil {
				t.Fatalf("new service: %v", err)
			}
			grpcServer := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
			networkv1.RegisterNetworkServiceServer(grpcServer, svc)
			lis, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			go func() { _ = grpcServer.Serve(lis) }()
			t.Cleanup(grpcServer.Stop)

			// The consensusd side.
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			broadcaster := newResilientBroadcaster(ctx)
			if tc.withNodeBroadcaster {
				node.SetNetworkBroadcaster(broadcaster)
			}
			go maintainNetworkStream(ctx, lis.Addr().String(), broadcaster, node, true, nil, 0)

			// A message of a type nobody handles tells when the stream is up.
			waitForCondition(t, 5*time.Second, "the consensus stream to attach", func() bool {
				return relay.HandleMessage(&p2p.Message{Type: 0xEE, Payload: []byte(`{}`)}) == nil
			})

			asker, bystander := &recordingPeer{}, &recordingPeer{}
			askerServer, _ := startWirePeer(t, node, asker)
			bystanderServer, _ := startWirePeer(t, node, bystander)
			for _, peer := range []*p2p.Server{askerServer, bystanderServer} {
				if err := peer.Connect(hubAddr); err != nil {
					t.Fatalf("connect: %v", err)
				}
			}
			waitForCondition(t, 3*time.Second, "both peers to be connected", func() bool {
				return hub.SnapshotNetwork().Counts.Inbound == 2
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

			waitForCondition(t, 5*time.Second, "the requester to be answered", func() bool {
				return len(asker.all(p2p.MsgTypeBlocks)) == 1 && len(asker.all(p2p.MsgTypeStatus)) == 1
			})
			var reply p2p.BlocksPayload
			if err := json.Unmarshal(asker.all(p2p.MsgTypeBlocks)[0].Payload, &reply); err != nil {
				t.Fatalf("decode blocks reply: %v", err)
			}
			if len(reply.Blocks) != committed {
				t.Fatalf("expected the %d committed blocks, got %d", committed, len(reply.Blocks))
			}

			time.Sleep(300 * time.Millisecond)
			if got := len(bystander.all(p2p.MsgTypeBlocks)) + len(bystander.all(p2p.MsgTypeStatus)); got != 0 {
				t.Fatalf("a peer that asked for nothing was sent %d answers meant for another peer", got)
			}
			if got := len(asker.all(p2p.MsgTypeBlocks)) + len(asker.all(p2p.MsgTypeStatus)); got != 2 {
				t.Fatalf("expected exactly one answer to each request, got %d messages", got)
			}
		})
	}
}
