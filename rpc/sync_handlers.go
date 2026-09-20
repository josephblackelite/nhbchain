package rpc

import (
	"net/http"
)

const (
	codeSyncInvalidParams = -32060
)

type syncStatusResult struct {
	ChainHeight    uint64 `json:"chainHeight"`
	SnapshotHeight uint64 `json:"snapshotHeight"`
	ManagerReady   bool   `json:"managerReady"`
}

// syncSnapshotRPCDisabledMessage: sync_snapshot_import replaced the state
// processor's trie with the imported one in place, while the node was running.
// It kept no backup of the state it overwrote and did not move the chain head,
// so the imported root matched no block of the node's own chain, and the
// node's drift guard put the state back at the committed head at the next block
// or restart, silently discarding the import. Nothing in the node signs a
// snapshot manifest, and the verification an import needs (a two-thirds
// validator quorum or a governance anchor over the manifest digest) has
// nothing to check, so a snapshot exported this way could not be imported by
// another node either; sync_snapshot_export wrote those unsigned files to a
// directory the caller named. Both are disabled. A new validator is brought up
// from a verified snapshot outside the node; see docs/networking/snapshots.md.
// sync_status (read-only) is left live.
const syncSnapshotRPCDisabledMessage = "this method is disabled -- the node's snapshot pipeline is incomplete (nothing signs a snapshot manifest, and an import overwrote live state in place with no backup and no chain head update); bring a new validator up from a verified snapshot outside the node instead"

func (s *Server) handleSyncSnapshotExport(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, syncSnapshotRPCDisabledMessage, nil)
}

func (s *Server) handleSyncSnapshotImport(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, syncSnapshotRPCDisabledMessage, nil)
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
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
