package rpc

import (
	"encoding/json"
	"net/http"

	syncmgr "nhbchain/core/sync"
)

const (
	codeSyncInvalidParams = -32060
	codeSyncUnavailable   = -32061
)

type syncSnapshotExportParams struct {
	OutDir string `json:"outDir"`
}

type syncSnapshotImportParams struct {
	ChunkDir string                   `json:"chunkDir"`
	Manifest syncmgr.SnapshotManifest `json:"manifest"`
}

type syncStatusResult struct {
	ChainHeight    uint64 `json:"chainHeight"`
	SnapshotHeight uint64 `json:"snapshotHeight"`
	ManagerReady   bool   `json:"managerReady"`
}

func (s *Server) handleSyncSnapshotExport(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params syncSnapshotExportParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.OutDir == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", "outDir is required")
		return
	}
	manifest, err := s.node.SnapshotExport(r.Context(), params.OutDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeSyncUnavailable, "snapshot_error", err.Error())
		return
	}
	writeResult(w, req.ID, manifest)
}

func (s *Server) handleSyncSnapshotImport(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params syncSnapshotImportParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.ChunkDir == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", "chunkDir is required")
		return
	}
	root, err := s.node.SnapshotImport(r.Context(), &params.Manifest, params.ChunkDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeSyncUnavailable, "snapshot_error", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]string{"stateRoot": root.Hex()})
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeSyncInvalidParams, "invalid_params", "sync_status takes no parameters")
		return
	}
	mgr := s.node.SyncManager()
	if mgr == nil {
		writeResult(w, req.ID, syncStatusResult{ChainHeight: s.node.GetHeight(), SnapshotHeight: 0, ManagerReady: false})
		return
	}
	result := syncStatusResult{
		ChainHeight:    s.node.GetHeight(),
		SnapshotHeight: mgr.Height(),
		ManagerReady:   true,
	}
	writeResult(w, req.ID, result)
}
