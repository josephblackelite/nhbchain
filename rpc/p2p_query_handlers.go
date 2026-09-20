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
		writeNetworkUnavailable(w, req.ID)
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
	// The same peer list net_peers returns, so it takes the same credential.
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid_params", "p2p_peers takes no parameters")
		return
	}
	if s.net == nil {
		writeNetworkUnavailable(w, req.ID)
		return
	}
	peers, err := s.net.NetworkPeers(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", err.Error())
		return
	}
	writeResult(w, req.ID, peers)
}
