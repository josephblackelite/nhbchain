package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"nhbchain/core"
)

// Read-only views of single bridge locks and release records (ledger PL-BC-C): bridge_getLock, bridge_listLocks and
// bridge_getRelease. Anonymous like bridge_getStatus: they report what the chain holds, change nothing and add no
// setting. A client learns from them what was locked and what became of a burn id.

type bridgeLockResult struct {
	ID        uint64 `json:"id"`
	Sender    string `json:"sender"`
	AmountWei string `json:"amountWei"`
	DestChain string `json:"destChain"`
	Dest      string `json:"dest"`
	Height    uint64 `json:"height"`
}

type bridgeLocksResult struct {
	Locks []bridgeLockResult `json:"locks"`
	// LastLockID is the id of the newest lock (0 when there is none): more are waiting when the last one listed is below it.
	LastLockID uint64 `json:"lastLockId"`
}

type bridgeReleaseResult struct {
	BurnID       string `json:"burnId"`
	Status       string `json:"status"`
	Recipient    string `json:"recipient"`
	AmountWei    string `json:"amountWei"`
	Height       uint64 `json:"height"`
	ExecutableAt uint64 `json:"executableAt"`
}

func bridgeLockToResult(v core.BridgeLockView) bridgeLockResult {
	return bridgeLockResult{ID: v.ID, Sender: v.Sender, AmountWei: weiOrZero(v.AmountWei), DestChain: v.DestChain, Dest: v.Dest, Height: v.Height}
}

// bridgeUintParam reads one JSON number or decimal string as an unsigned integer (an id or a count).
func bridgeUintParam(raw json.RawMessage) (uint64, error) {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
		text = strings.TrimSpace(s)
	}
	if text == "" {
		return 0, errors.New("empty")
	}
	return strconv.ParseUint(text, 10, 64)
}

func (s *Server) handleBridgeGetLock(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [lockId]", nil)
		return
	}
	id, err := bridgeUintParam(req.Params[0])
	if err != nil || id == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "lockId must be a positive integer", nil)
		return
	}
	view, err := s.node.BridgeLock(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to read the lock", err.Error())
		return
	}
	if view == nil {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "no such lock", nil)
		return
	}
	writeResult(w, req.ID, bridgeLockToResult(*view))
}

// handleBridgeListLocks answers bridge_listLocks: params [] or [afterId] or [afterId, limit].
func (s *Server) handleBridgeListLocks(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) > 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [afterId, limit]", nil)
		return
	}
	var after uint64
	limit := 0
	if len(req.Params) >= 1 {
		v, err := bridgeUintParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "afterId must be a non-negative integer", nil)
			return
		}
		after = v
	}
	if len(req.Params) == 2 {
		v, err := bridgeUintParam(req.Params[1])
		if err != nil || v == 0 {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "limit must be a positive integer", nil)
			return
		}
		if v > core.BridgeListLocksMax {
			v = core.BridgeListLocksMax
		}
		limit = int(v)
	}
	locks, last, err := s.node.BridgeLocksAfter(after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list the locks", err.Error())
		return
	}
	out := bridgeLocksResult{Locks: make([]bridgeLockResult, 0, len(locks)), LastLockID: last}
	for _, l := range locks {
		out.Locks = append(out.Locks, bridgeLockToResult(l))
	}
	writeResult(w, req.ID, out)
}

// handleBridgeGetRelease answers bridge_getRelease: params [burnIdHex], with or without a 0x prefix.
func (s *Server) handleBridgeGetRelease(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [burnIdHex]", nil)
		return
	}
	var text string
	if json.Unmarshal(req.Params[0], &text) != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "burnId must be a hex string", nil)
		return
	}
	text = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(text), "0x"), "0X")
	id, err := hex.DecodeString(text)
	if err != nil || len(id) == 0 || len(id) > 64 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "burnId must be 1 to 64 bytes of hex", nil)
		return
	}
	view, err := s.node.BridgeRelease(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to read the release", err.Error())
		return
	}
	writeResult(w, req.ID, bridgeReleaseResult{
		BurnID: view.BurnID, Status: view.Status, Recipient: view.Recipient, AmountWei: weiOrZero(view.AmountWei),
		Height: view.Height, ExecutableAt: view.ExecutableAt,
	})
}
