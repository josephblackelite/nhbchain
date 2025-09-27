package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

type stubNetwork struct {
	view  p2p.NetworkView
	peers []p2p.PeerNetInfo
}

func (s *stubNetwork) NetworkView(context.Context) (p2p.NetworkView, []string, error) {
	return s.view, nil, nil
}

func (s *stubNetwork) NetworkPeers(context.Context) ([]p2p.PeerNetInfo, error) {
	return s.peers, nil
}

func (s *stubNetwork) Dial(context.Context, string) error { return nil }

func (s *stubNetwork) Ban(context.Context, string, time.Duration) error { return nil }

func TestP2PInfoUnavailable(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1}
	rec := httptest.NewRecorder()
	env.server.handleP2PInfo(rec, env.newRequest(), req)
	_, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr == nil {
		t.Fatalf("expected error when p2p server is missing")
	}
	if rpcErr.Code != codeServerError {
		t.Fatalf("expected server error code, got %d", rpcErr.Code)
	}
}

func TestP2PInfoAndPeers(t *testing.T) {
	env := newTestEnv(t)
	env.server.net = &stubNetwork{
		view: p2p.NetworkView{
			NetworkID: env.node.ChainID(),
			Genesis:   hex.EncodeToString(env.node.GenesisHash()),
			Counts:    p2p.NetworkCounts{},
			Limits:    p2p.NetworkLimits{},
			Self: p2p.NetworkSelf{
				NodeID:        "test-node",
				ClientVersion: "test/1.0",
			},
		},
		peers: []p2p.PeerNetInfo{},
	}

	infoReq := &RPCRequest{ID: 2}
	infoRec := httptest.NewRecorder()
	env.server.handleP2PInfo(infoRec, env.newRequest(), infoReq)
	result, rpcErr := decodeRPCResponse(t, infoRec)
	if rpcErr != nil {
		t.Fatalf("info error: %+v", rpcErr)
	}
	var view p2p.NetworkView
	if err := json.Unmarshal(result, &view); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	if view.Self.NodeID == "" {
		t.Fatalf("expected nodeId in network view")
	}
	if view.Counts.Total != 0 || view.Counts.Inbound != 0 || view.Counts.Outbound != 0 {
		t.Fatalf("expected zero counts, got %+v", view.Counts)
	}

	peersReq := &RPCRequest{ID: 3}
	peersRec := httptest.NewRecorder()
	env.server.handleP2PPeers(peersRec, env.newRequest(), peersReq)
	peersResult, rpcErr := decodeRPCResponse(t, peersRec)
	if rpcErr != nil {
		t.Fatalf("peers error: %+v", rpcErr)
	}
	var peers []interface{}
	if err := json.Unmarshal(peersResult, &peers); err != nil {
		t.Fatalf("unmarshal peers: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected empty peers list, got %d", len(peers))
	}
}
