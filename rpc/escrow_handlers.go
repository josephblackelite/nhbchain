package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/core"
	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

const (
	codeEscrowInvalidParams = -32021
	codeEscrowNotFound      = -32022
	codeEscrowForbidden     = -32023
	codeEscrowConflict      = -32024
	codeEscrowInternal      = -32025
)

const deadlineSkewSeconds int64 = 5

type escrowCreateParams struct {
	Payer    string `json:"payer"`
	Payee    string `json:"payee"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	FeeBps   uint32 `json:"feeBps"`
	Deadline int64  `json:"deadline"`
	Nonce    uint64 `json:"nonce"`
	Mediator string `json:"mediator,omitempty"`
	MetaHex  string `json:"meta,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

type escrowIDParams struct {
	ID string `json:"id"`
}

type escrowActorParams struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
}

type escrowDisputeParams struct {
	ID      string `json:"id"`
	Caller  string `json:"caller"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type escrowFundParams struct {
	ID   string `json:"id"`
	From string `json:"from"`
}

type escrowResolveParams struct {
	ID      string `json:"id"`
	Caller  string `json:"caller"`
	Outcome string `json:"outcome"`
}

type escrowCreateResult struct {
	ID string `json:"id"`
}

type escrowJSON struct {
	ID            string   `json:"id"`
	Payer         string   `json:"payer"`
	Payee         string   `json:"payee"`
	Mediator      *string  `json:"mediator,omitempty"`
	Token         string   `json:"token"`
	Amount        string   `json:"amount"`
	FeeBps        uint32   `json:"feeBps"`
	Deadline      int64    `json:"deadline"`
	CreatedAt     int64    `json:"createdAt"`
	Nonce         uint64   `json:"nonce"`
	Status        string   `json:"status"`
	Meta          string   `json:"meta"`
	DisputeReason *string  `json:"disputeReason,omitempty"`
	Realm         *string  `json:"realm,omitempty"`
	RealmVersion  *uint64  `json:"realmVersion,omitempty"`
	PolicyNonce   *uint64  `json:"policyNonce,omitempty"`
	ArbScheme     *uint8   `json:"arbScheme,omitempty"`
	ArbThreshold  *uint32  `json:"arbThreshold,omitempty"`
	FrozenAt      *int64   `json:"frozenAt,omitempty"`
	Arbitrators   []string `json:"arbitrators,omitempty"`
}

// escrowRPCDisabledMessage: escrow_create/fund/release/refund/dispute/
// expire/resolve used to call s.node.Escrow* directly -- a bare
// n.stateMu.Lock()+nhbstate.NewManager(n.state.Trie) mutation of the live
// state trie OUTSIDE the transaction/block pipeline entirely (no tx created,
// signed, or added to any block). CreateBlock/ValidateBlock both derive their
// working state from that SAME n.state via n.state.Copy() before applying
// only the block's tx list and requiring the result to match the proposer's
// header.StateRoot -- so any validator-local RPC mutation here is invisible
// to every OTHER validator's independently-derived state, and the very next
// block either validator proposes is guaranteed to be rejected by the other
// on a state-root mismatch. With exactly 2 validators and zero quorum slack,
// that is not a risk of a fork -- it is a guaranteed, permanent halt at that
// height, on the very next legitimate authenticated call to ANY of these
// methods (see project memory: this class of incident has already happened
// once on this chain). Disabled immediately as an emergency stopgap (mirrors
// the identical fix already applied to lending_supply/withdraw/borrow/...
// and stake_delegate/undelegate/claim/claimRewards) pending a real
// signed-transaction conversion that applies through
// StateProcessor.ApplyTransaction inside the normal consensus path instead.
// escrow_get (read-only) is deliberately left live.
const escrowRPCDisabledMessage = "this method is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending"

func (s *Server) handleEscrowCreate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowGet(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	esc, err := s.node.EscrowGet(id)
	if err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatEscrowJSON(esc))
}

func (s *Server) handleEscrowFund(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowRelease(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowRefund(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowDispute(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowExpire(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func (s *Server) handleEscrowResolve(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, escrowRPCDisabledMessage, nil)
}

func parseBech32Address(addr string) ([20]byte, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return [20]byte{}, fmt.Errorf("address required")
	}
	return genesis.ParseBech32Account(trimmed)
}

func parsePositiveBigInt(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required")
	}
	amount, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return amount, nil
}

func parseMetaHex(value string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, nil
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		return out, fmt.Errorf("meta must be 0x-prefixed")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned)%2 != 0 {
		return out, fmt.Errorf("meta hex length must be even")
	}
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	if len(bytes) > len(out) {
		return out, fmt.Errorf("meta must be <= 32 bytes")
	}
	copy(out[:], bytes)
	return out, nil
}

func parseEscrowID(id string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return out, fmt.Errorf("id required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) != 64 {
		return out, fmt.Errorf("id must be 32 bytes")
	}
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	copy(out[:], bytes)
	return out, nil
}

func formatEscrowID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

func formatEscrowJSON(esc *escrow.Escrow) escrowJSON {
	payer := crypto.MustNewAddress(crypto.NHBPrefix, esc.Payer[:]).String()
	payee := crypto.MustNewAddress(crypto.NHBPrefix, esc.Payee[:]).String()
	var mediatorPtr *string
	if esc.Mediator != ([20]byte{}) {
		mediator := crypto.MustNewAddress(crypto.NHBPrefix, esc.Mediator[:]).String()
		mediatorPtr = &mediator
	}
	amount := "0"
	if esc.Amount != nil {
		amount = esc.Amount.String()
	}
	meta := "0x" + hex.EncodeToString(esc.MetaHash[:])
	var disputeReasonPtr *string
	if trimmed := strings.TrimSpace(esc.DisputeReason); trimmed != "" {
		disputeReason := trimmed
		disputeReasonPtr = &disputeReason
	}
	var realmPtr *string
	var realmVersionPtr *uint64
	var policyNoncePtr *uint64
	var schemePtr *uint8
	var thresholdPtr *uint32
	var frozenAtPtr *int64
	var arbitrators []string
	if trimmed := strings.TrimSpace(esc.RealmID); trimmed != "" {
		realm := trimmed
		realmPtr = &realm
		if esc.FrozenArb != nil {
			realmVersion := esc.FrozenArb.RealmVersion
			realmVersionPtr = &realmVersion
			policyNonce := esc.FrozenArb.PolicyNonce
			policyNoncePtr = &policyNonce
			scheme := uint8(esc.FrozenArb.Scheme)
			schemePtr = &scheme
			threshold := esc.FrozenArb.Threshold
			thresholdPtr = &threshold
			if esc.FrozenArb.FrozenAt != 0 {
				frozen := esc.FrozenArb.FrozenAt
				frozenAtPtr = &frozen
			}
			if len(esc.FrozenArb.Members) > 0 {
				arbitrators = make([]string, 0, len(esc.FrozenArb.Members))
				for _, member := range esc.FrozenArb.Members {
					arbitrators = append(arbitrators, crypto.MustNewAddress(crypto.NHBPrefix, member[:]).String())
				}
			}
		}
	}
	return escrowJSON{
		ID:            formatEscrowID(esc.ID),
		Payer:         payer,
		Payee:         payee,
		Mediator:      mediatorPtr,
		Token:         esc.Token,
		Amount:        amount,
		FeeBps:        esc.FeeBps,
		Deadline:      esc.Deadline,
		CreatedAt:     esc.CreatedAt,
		Nonce:         esc.Nonce,
		Status:        escrowStatusString(esc.Status),
		Meta:          meta,
		DisputeReason: disputeReasonPtr,
		Realm:         realmPtr,
		RealmVersion:  realmVersionPtr,
		PolicyNonce:   policyNoncePtr,
		ArbScheme:     schemePtr,
		ArbThreshold:  thresholdPtr,
		FrozenAt:      frozenAtPtr,
		Arbitrators:   arbitrators,
	}
}

func escrowStatusString(status escrow.EscrowStatus) string {
	switch status {
	case escrow.EscrowInit:
		return "init"
	case escrow.EscrowFunded:
		return "funded"
	case escrow.EscrowReleased:
		return "released"
	case escrow.EscrowRefunded:
		return "refunded"
	case escrow.EscrowExpired:
		return "expired"
	case escrow.EscrowDisputed:
		return "disputed"
	default:
		return "unknown"
	}
}

func writeEscrowError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeEscrowInternal
	message := "internal_error"
	data := err.Error()
	switch {
	case errors.Is(err, core.ErrEscrowNotFound) || strings.Contains(err.Error(), "escrow engine: escrow not found"):
		status = http.StatusNotFound
		code = codeEscrowNotFound
		message = "not_found"
	case strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "requires mediator"):
		status = http.StatusForbidden
		code = codeEscrowForbidden
		message = "forbidden"
	case strings.Contains(err.Error(), "cannot ") || strings.Contains(err.Error(), "deadline not reached") || strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "refund deadline passed"):
		status = http.StatusConflict
		code = codeEscrowConflict
		message = "conflict"
	case strings.Contains(err.Error(), "escrow engine: fee treasury not configured") || strings.Contains(err.Error(), "escrow: nil escrow") || strings.Contains(err.Error(), "escrow: unsupported token"):
		status = http.StatusInternalServerError
		code = codeEscrowInternal
		message = "internal_error"
	case strings.Contains(err.Error(), "escrow: amount must be positive"):
		status = http.StatusConflict
		code = codeEscrowConflict
		message = "conflict"
	}
	writeError(w, status, id, code, message, data)
}
