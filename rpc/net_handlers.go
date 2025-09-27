package rpc

import (
	"encoding/json"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	if s.net == nil {
		// TODO: return a dedicated error or disable this route when the
		// network service is unavailable instead of generic messages.
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	view, listen, err := s.net.NetworkView(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", err.Error())
		return
	}
	result := netInfoResult{
		NodeID:      view.Self.NodeID,
		PeerCounts:  view.Counts,
		ChainID:     view.NetworkID,
		GenesisHash: view.Genesis,
		ListenAddrs: listen,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleNetPeers(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeNetInvalidParams, "invalid_params", "net_peers takes no parameters")
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
	if s.net == nil {
		// TODO: return a dedicated error or disable this route when the
		// network service is unavailable instead of generic messages.
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	if err := s.net.Dial(r.Context(), params.Target); err != nil {
		writeNetError(w, req.ID, err)
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
	if s.net == nil {
		// TODO: return a dedicated error or disable this route when the
		// network service is unavailable instead of generic messages.
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "unavailable", "p2p server not running")
		return
	}
	duration := time.Duration(params.Secs) * time.Second
	if err := s.net.Ban(r.Context(), params.NodeID, duration); err != nil {
		writeNetError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}

func writeNetError(w http.ResponseWriter, id any, err error) {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.InvalidArgument:
			writeError(w, http.StatusBadRequest, id, codeNetInvalidParams, "invalid_params", st.Message())
			return
		case codes.NotFound:
			writeError(w, http.StatusNotFound, id, codeNetUnknownPeer, "unknown_peer", st.Message())
			return
		case codes.FailedPrecondition:
			writeError(w, http.StatusConflict, id, codeNetPeerBanned, "peer_banned", st.Message())
			return
		case codes.Unavailable:
			writeError(w, http.StatusServiceUnavailable, id, codeServerError, "unavailable", st.Message())
			return
		}
	}
	writeError(w, http.StatusInternalServerError, id, codeServerError, "server_error", err.Error())
}
