package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

type testP2PHandler struct{}

func (testP2PHandler) HandleMessage(msg *p2p.Message) error { return nil }

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
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	cfg := p2p.ServerConfig{
		ListenAddress:    "127.0.0.1:0",
		ChainID:          env.node.ChainID(),
		GenesisHash:      env.node.GenesisHash(),
		ClientVersion:    "test/1.0",
		MaxPeers:         4,
		MaxInbound:       2,
		MaxOutbound:      2,
		PeerBanDuration:  time.Second,
		ReadTimeout:      time.Second,
		WriteTimeout:     time.Second,
		MaxMessageBytes:  1024,
		RateMsgsPerSec:   5,
		RateBurst:        10,
		BanScore:         20,
		GreyScore:        10,
		HandshakeTimeout: time.Second,
	}
	srv := p2p.NewServer(testP2PHandler{}, key, cfg)
	env.node.SetP2PServer(srv)

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
