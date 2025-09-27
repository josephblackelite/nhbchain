package rpc

import (
	"net/http"
)

func (s *Server) handleP2PInfo(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "p2p_info takes no parameters")
		return
	}
	if s.net == nil {
		// TODO: return a dedicated error or disable this route when the
		// network service is unavailable instead of generic messages.
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	view, _, err := s.net.NetworkView(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", err.Error())
		return
	}
	writeResult(w, req.ID, view)
}

func (s *Server) handleP2PPeers(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "p2p_peers takes no parameters")
		return
	}
	if s.net == nil {
		// TODO: return a dedicated error or disable this route when the
		// network service is unavailable instead of generic messages.
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	peers, err := s.net.NetworkPeers(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", err.Error())
		return
	}
	writeResult(w, req.ID, peers)
}
