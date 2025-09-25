package rpc

import (
	"net/http"
)

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
	writeResult(w, req.ID, srv.SnapshotNetwork())
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
