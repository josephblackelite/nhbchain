package rpc

import (
	"encoding/json"
	"errors"
	"net/http"

	"nhbchain/core/identity"
	"nhbchain/crypto"
)

type identitySetAliasResult struct {
	OK bool `json:"ok"`
}

type identityResolveResult struct {
	Address string `json:"addressBech32"`
}

type identityReverseResult struct {
	Alias string `json:"alias"`
}

func (s *Server) handleIdentitySetAlias(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address and alias parameters", nil)
		return
	}
	var addressParam, aliasParam string
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
		return
	}
	if err := json.Unmarshal(req.Params[1], &aliasParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid alias parameter", err.Error())
		return
	}
	addr, err := decodeBech32(addressParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	if err := s.node.IdentitySetAlias(addr, aliasParam); err != nil {
		switch {
		case errors.Is(err, identity.ErrInvalidAlias):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid alias", err.Error())
		case errors.Is(err, identity.ErrAliasTaken):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "alias already registered", aliasParam)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to set alias", err.Error())
		}
		return
	}
	writeResult(w, req.ID, identitySetAliasResult{OK: true})
}

func (s *Server) handleIdentityResolve(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "alias parameter required", nil)
		return
	}
	var aliasParam string
	if err := json.Unmarshal(req.Params[0], &aliasParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid alias parameter", err.Error())
		return
	}
	normalized, err := identity.NormalizeAlias(aliasParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid alias", err.Error())
		return
	}
	addr, ok := s.node.IdentityResolve(normalized)
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "alias not found", normalized)
		return
	}
	bech32 := crypto.NewAddress(crypto.NHBPrefix, addr[:]).String()
	writeResult(w, req.ID, identityResolveResult{Address: bech32})
}

func (s *Server) handleIdentityReverse(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var addressParam string
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
		return
	}
	addr, err := decodeBech32(addressParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	alias, ok := s.node.IdentityReverse(addr)
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "address has no alias", addressParam)
		return
	}
	writeResult(w, req.ID, identityReverseResult{Alias: alias})
}
