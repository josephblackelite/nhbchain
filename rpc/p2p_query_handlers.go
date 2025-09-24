package rpc

import (
	"net/http"

	"nhbchain/p2p"
)

type p2pInfoResult struct {
	Network p2p.NetworkView `json:"network"`
	Peers   []p2p.PeerInfo  `json:"peers"`
}

func (s *Server) handleP2PInfo(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "p2p_info takes no parameters")
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	result := p2pInfoResult{Network: srv.SnapshotNetwork(), Peers: srv.SnapshotPeers()}
	writeResult(w, req.ID, result)
}

func (s *Server) handleP2PPeers(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "p2p_peers takes no parameters")
		return
	}
	srv := s.node.P2PServer()
	if srv == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	writeResult(w, req.ID, srv.SnapshotPeers())
}
