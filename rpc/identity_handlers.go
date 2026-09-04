package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nhbchain/core/claimable"
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

type identityAddressMutationParams struct {
	Owner   string `json:"owner"`
	Alias   string `json:"alias"`
	Address string `json:"address"`
}

type identityRenameParams struct {
	Owner    string `json:"owner"`
	Alias    string `json:"alias"`
	NewAlias string `json:"newAlias"`
}

type identityCreateClaimableParams struct {
	Payer     string `json:"payer"`
	Recipient string `json:"recipient"`
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	Deadline  int64  `json:"deadline"`
	callerMetadataParams
}

type identityCreateClaimableResult struct {
	ClaimID       string `json:"claimId"`
	RecipientHint string `json:"recipientHint"`
	Token         string `json:"token"`
	Amount        string `json:"amount"`
	ExpiresAt     int64  `json:"expiresAt"`
	CreatedAt     int64  `json:"createdAt"`
	Nonce         uint64 `json:"nonce"`
	ChainID       string `json:"chainId"`
}

type identityClaimParams struct {
	ClaimID  string `json:"claimId"`
	Payee    string `json:"payee"`
	Preimage string `json:"preimage"`
	callerMetadataParams
}

type identityClaimResult struct {
	OK     bool   `json:"ok"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
}

func identityRecordToResult(record *identity.AliasRecord) identityResolveResult {
	if record == nil {
		return identityResolveResult{}
	}
	aliasID := record.AliasID()
	addresses := make([]string, 0, len(record.Addresses))
	for _, addr := range record.Addresses {
		if addr == ([20]byte{}) {
			continue
		}
		addresses = append(addresses, crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String())
	}
	primary := ""
	if record.Primary != ([20]byte{}) {
		primary = crypto.MustNewAddress(crypto.NHBPrefix, record.Primary[:]).String()
	} else if len(addresses) > 0 {
		primary = addresses[0]
	}
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
	return result
}

// identityRPCDisabledMessage: same guaranteed-fork defect as escrow's RPC
// handlers (see escrowRPCDisabledMessage) -- identity_setAlias/setAvatar/
// addAddress/removeAddress/setPrimary/rename/createClaimable/claim mutated
// n.state.Trie directly outside the block pipeline. Disabled as an emergency
// stopgap; identity_resolve/reverse (read-only) are left live.
const identityRPCDisabledMessage = "this method is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending"

func (s *Server) handleIdentitySetAlias(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentitySetAvatar(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentityAddAddress(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentityRemoveAddress(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentitySetPrimary(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentityRename(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
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
	writeResult(w, req.ID, identityRecordToResult(record))
}

func (s *Server) handleIdentityCreateClaimable(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

func (s *Server) handleIdentityClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, identityRPCDisabledMessage, nil)
}

// parseRecipientHint also reports WHICH kind of hint the caller supplied --
// a raw 32-byte hash is an opaque secret the caller controls entirely (they
// could have derived it any way they like, including from a private,
// salted, off-chain-shared value), while a plain alias string always
// produces the same, publicly-derivable identity.DeriveAliasID(alias). The
// distinction matters for claim-time authorization (see
// Node.authorizeClaimablePayee / NHB-TRIAGE-C6) -- an alias-derived hint is
// never itself proof of anything, since anyone can compute it from the
// username alone.
func parseRecipientHint(value string) ([32]byte, claimable.RecipientKind, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, claimable.RecipientKindNone, fmt.Errorf("recipient required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) == 64 {
		decoded, err := hex.DecodeString(cleaned)
		if err != nil {
			return out, claimable.RecipientKindNone, err
		}
		copy(out[:], decoded)
		return out, claimable.RecipientKindNone, nil
	}
	normalized, err := identity.NormalizeAlias(trimmed)
	if err != nil {
		return out, claimable.RecipientKindNone, fmt.Errorf("recipient must be alias or 32-byte hash")
	}
	return identity.DeriveAliasID(normalized), claimable.RecipientKindAlias, nil
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
