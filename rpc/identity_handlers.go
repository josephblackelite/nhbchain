package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nhbchain/core/identity"
	"nhbchain/crypto"
)

type identitySetAliasResult struct {
	OK bool `json:"ok"`
}

type identityResolveResult struct {
	Alias     string   `json:"alias"`
	AliasID   string   `json:"aliasId"`
	Primary   string   `json:"primary"`
	Addresses []string `json:"addresses"`
	AvatarRef string   `json:"avatarRef,omitempty"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

type identityReverseResult struct {
	Alias   string `json:"alias"`
	AliasID string `json:"aliasId"`
}

type identitySetAvatarResult struct {
	OK        bool   `json:"ok"`
	Alias     string `json:"alias"`
	AliasID   string `json:"aliasId"`
	AvatarRef string `json:"avatarRef"`
	UpdatedAt int64  `json:"updatedAt"`
}

type identityCreateClaimableParams struct {
	Payer     string `json:"payer"`
	Recipient string `json:"recipient"`
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	Deadline  int64  `json:"deadline"`
}

type identityCreateClaimableResult struct {
	ClaimID       string `json:"claimId"`
	RecipientHint string `json:"recipientHint"`
	Token         string `json:"token"`
	Amount        string `json:"amount"`
	ExpiresAt     int64  `json:"expiresAt"`
	CreatedAt     int64  `json:"createdAt"`
}

type identityClaimParams struct {
	ClaimID  string `json:"claimId"`
	Payee    string `json:"payee"`
	Preimage string `json:"preimage"`
}

type identityClaimResult struct {
	OK     bool   `json:"ok"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
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

func (s *Server) handleIdentitySetAvatar(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address and avatarRef parameters", nil)
		return
	}
	var addressParam, avatarParam string
	if err := json.Unmarshal(req.Params[0], &addressParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
		return
	}
	if err := json.Unmarshal(req.Params[1], &avatarParam); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid avatar parameter", err.Error())
		return
	}
	addr, err := decodeBech32(addressParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	normalizedAvatar, err := identity.NormalizeAvatarRef(avatarParam)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid avatarRef", err.Error())
		return
	}
	record, err := s.node.IdentitySetAvatar(addr, normalizedAvatar)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrAliasNotFound):
			writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "alias not registered", addressParam)
		case errors.Is(err, identity.ErrInvalidAvatarRef):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid avatarRef", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to set avatar", err.Error())
		}
		return
	}
	aliasID := record.AliasID()
	writeResult(w, req.ID, identitySetAvatarResult{
		OK:        true,
		Alias:     record.Alias,
		AliasID:   "0x" + hex.EncodeToString(aliasID[:]),
		AvatarRef: record.AvatarRef,
		UpdatedAt: record.UpdatedAt,
	})
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
	record, ok := s.node.IdentityResolve(normalized)
	if !ok || record == nil {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "alias not found", normalized)
		return
	}
	primary := crypto.NewAddress(crypto.NHBPrefix, record.Primary[:]).String()
	addresses := make([]string, 0, len(record.Addresses))
	for _, addr := range record.Addresses {
		addresses = append(addresses, crypto.NewAddress(crypto.NHBPrefix, addr[:]).String())
	}
	aliasID := record.AliasID()
	result := identityResolveResult{
		Alias:     record.Alias,
		AliasID:   "0x" + hex.EncodeToString(aliasID[:]),
		Primary:   primary,
		Addresses: addresses,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
	if record.AvatarRef != "" {
		result.AvatarRef = record.AvatarRef
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleIdentityCreateClaimable(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params identityCreateClaimableParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	payer, err := parseBech32Address(params.Payer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	token := strings.ToUpper(strings.TrimSpace(params.Token))
	if token != "NHB" && token != "ZNHB" {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "token must be NHB or ZNHB")
		return
	}
	amount, err := parsePositiveBigInt(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	hint, err := parseRecipientHint(params.Recipient)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	now := time.Now().Unix()
	if params.Deadline < now-deadlineSkewSeconds {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "deadline must be in the future")
		return
	}
	record, err := s.node.IdentityCreateClaimable(payer, token, amount, hint, params.Deadline)
	if err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	amountStr := "0"
	if record.Amount != nil {
		amountStr = record.Amount.String()
	}
	writeResult(w, req.ID, identityCreateClaimableResult{
		ClaimID:       formatClaimableID(record.ID),
		RecipientHint: "0x" + hex.EncodeToString(record.RecipientHint[:]),
		Token:         record.Token,
		Amount:        amountStr,
		ExpiresAt:     record.Deadline,
		CreatedAt:     record.CreatedAt,
	})
}

func (s *Server) handleIdentityClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params identityClaimParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseClaimableID(params.ClaimID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	payee, err := parseBech32Address(params.Payee)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	preimage, err := parseHexBytes(params.Preimage)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	record, err := s.node.IdentityClaim(id, preimage, payee)
	if err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	amountStr := "0"
	token := ""
	if record != nil {
		token = record.Token
		if record.Amount != nil {
			amountStr = record.Amount.String()
		}
	}
	writeResult(w, req.ID, identityClaimResult{OK: true, Token: token, Amount: amountStr})
}

func parseRecipientHint(value string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, fmt.Errorf("recipient required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) == 64 {
		decoded, err := hex.DecodeString(cleaned)
		if err != nil {
			return out, err
		}
		copy(out[:], decoded)
		return out, nil
	}
	normalized, err := identity.NormalizeAlias(trimmed)
	if err != nil {
		return out, fmt.Errorf("recipient must be alias or 32-byte hash")
	}
	return identity.DeriveAliasID(normalized), nil
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
	aliasID := identity.DeriveAliasID(alias)
	writeResult(w, req.ID, identityReverseResult{Alias: alias, AliasID: "0x" + hex.EncodeToString(aliasID[:])})
}
