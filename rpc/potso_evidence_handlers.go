package rpc

import (
	"encoding/json"
	"net/http"

	"nhbchain/rpc/modules"
)

func (s *Server) handlePotsoSubmitEvidence(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.potsoEvidence == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "evidence module unavailable", nil)
		return
	}
	result, modErr := s.potsoEvidence.Submit(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handlePotsoGetEvidence(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.potsoEvidence == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "evidence module unavailable", nil)
		return
	}
	record, modErr := s.potsoEvidence.Get(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, record)
}

func (s *Server) handlePotsoListEvidence(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "too many parameters", nil)
		return
	}
	if s.potsoEvidence == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "evidence module unavailable", nil)
		return
	}
	var raw json.RawMessage
	if len(req.Params) == 1 {
		raw = req.Params[0]
	}
	result, modErr := s.potsoEvidence.List(raw)
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func writeModuleError(w http.ResponseWriter, id interface{}, err *modules.ModuleError) {
	if err == nil {
		writeError(w, http.StatusInternalServerError, id, codeServerError, "internal error", nil)
		return
	}
	status := err.HTTPStatus
	if status <= 0 {
		status = http.StatusBadRequest
	}
	code := err.Code
	if code == 0 {
		code = codeServerError
	}
	writeError(w, status, id, code, err.Message, err.Data)
}
