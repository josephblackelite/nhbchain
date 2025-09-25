package rpc

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"nhbchain/p2p"
)

const (
	codeNetInvalidParams = -32040
	codeNetUnknownPeer   = -32041
	codeNetPeerBanned    = -32042
)

type netInfoResult struct {
	NodeID      string            `json:"nodeId"`
	PeerCounts  p2p.NetworkCounts `json:"peerCounts"`
	ChainID     uint64            `json:"chainId"`
	GenesisHash string            `json:"genesisHash"`
	ListenAddrs []string          `json:"listenAddrs"`
}

type netDialParams struct {
	Target string `json:"target"`
}

type netBanParams struct {
	NodeID string `json:"nodeId"`
	Secs   int64  `json:"secs"`
}

func (s *Server) handleNetInfo(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "net_info takes no parameters")
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	view := srv.SnapshotNetwork()
	result := netInfoResult{
		NodeID:      srv.NodeID(),
		PeerCounts:  view.Counts,
		ChainID:     view.NetworkID,
		GenesisHash: view.Genesis,
		ListenAddrs: srv.ListenAddresses(),
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleNetPeers(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "net_peers takes no parameters")
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	writeResult(w, req.ID, srv.NetPeers())
}

func (s *Server) handleNetDial(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params netDialParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", err.Error())
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	if err := srv.DialPeer(params.Target); err != nil {
		switch {
		case errors.Is(err, p2p.ErrInvalidAddress) || errors.Is(err, p2p.ErrDialTargetEmpty):
			writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", err.Error())
		case errors.Is(err, p2p.ErrPeerUnknown):
			writeError(w, http.StatusNotFound, req.ID, codeNetUnknownPeer, "unknown_peer", err.Error())
		case errors.Is(err, p2p.ErrPeerBanned):
			writeError(w, http.StatusConflict, req.ID, codeNetPeerBanned, "peer_banned", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "server_error", err.Error())
		}
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}

func (s *Server) handleNetBan(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params netBanParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.Secs < 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "secs must be non-negative")
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	duration := time.Duration(params.Secs) * time.Second
	if err := srv.BanPeer(params.NodeID, duration); err != nil {
		switch {
		case errors.Is(err, p2p.ErrPeerUnknown):
			writeError(w, http.StatusNotFound, req.ID, codeNetUnknownPeer, "unknown_peer", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "server_error", err.Error())
		}
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}
