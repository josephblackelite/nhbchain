package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

const (
	codeP2PInvalidParams = -32021
	codeP2PNotFound      = -32022
	codeP2PForbidden     = -32023
	codeP2PConflict      = -32024
	codeP2PInternal      = -32025
)

type p2pCreateParams struct {
	OfferID     string `json:"offerId"`
	Buyer       string `json:"buyer"`
	Seller      string `json:"seller"`
	BaseToken   string `json:"baseToken"`
	BaseAmount  string `json:"baseAmount"`
	QuoteToken  string `json:"quoteToken"`
	QuoteAmount string `json:"quoteAmount"`
	Deadline    int64  `json:"deadline"`
}

type p2pIDParams struct {
	ID string `json:"tradeId"`
}

type p2pActorParams struct {
	ID     string `json:"tradeId"`
	Caller string `json:"caller"`
}

type p2pDisputeParams struct {
	ID      string `json:"tradeId"`
	Caller  string `json:"caller"`
	Message string `json:"message"`
}

type p2pResolveParams struct {
	ID      string `json:"tradeId"`
	Caller  string `json:"caller"`
	Outcome string `json:"outcome"`
}

type p2pCreateResult struct {
	TradeID       string                        `json:"tradeId"`
	EscrowBaseID  string                        `json:"escrowBaseId"`
	EscrowQuoteID string                        `json:"escrowQuoteId"`
	PayIntents    map[string]p2pPayIntentResult `json:"payIntents"`
}

type p2pPayIntentResult struct {
	To     string `json:"to"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
}

type tradeJSON struct {
	ID          string `json:"id"`
	OfferID     string `json:"offerId"`
	Buyer       string `json:"buyer"`
	Seller      string `json:"seller"`
	QuoteToken  string `json:"quoteToken"`
	QuoteAmount string `json:"quoteAmount"`
	EscrowQuote string `json:"escrowQuoteId"`
	BaseToken   string `json:"baseToken"`
	BaseAmount  string `json:"baseAmount"`
	EscrowBase  string `json:"escrowBaseId"`
	Deadline    int64  `json:"deadline"`
	CreatedAt   int64  `json:"createdAt"`
	Status      string `json:"status"`
}

var validP2PResolveOutcomes = map[string]struct{}{
	"release_both":              {},
	"refund_both":               {},
	"release_base_refund_quote": {},
	"release_quote_refund_base": {},
}

func (s *Server) handleP2PCreateTrade(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params p2pCreateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	if strings.TrimSpace(params.OfferID) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "offerId is required")
		return
	}
	buyer, err := parseBech32Address(params.Buyer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	seller, err := parseBech32Address(params.Seller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	normalizedBase, err := escrow.NormalizeToken(params.BaseToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "baseToken must be NHB or ZNHB")
		return
	}
	normalizedQuote, err := escrow.NormalizeToken(params.QuoteToken)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "quoteToken must be NHB or ZNHB")
		return
	}
	baseAmount, err := parsePositiveBigInt(params.BaseAmount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	quoteAmount, err := parsePositiveBigInt(params.QuoteAmount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	now := time.Now().Unix()
	if params.Deadline < now {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "deadline must be in the future")
		return
	}
	tradeID, escrowBaseID, escrowQuoteID, err := s.node.P2PCreateTrade(params.OfferID, buyer, seller, normalizedBase, baseAmount, normalizedQuote, quoteAmount, params.Deadline)
	if err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	buyerVault, err := s.node.EscrowVaultAddress(normalizedQuote)
	if err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	sellerVault, err := s.node.EscrowVaultAddress(normalizedBase)
	if err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	result := p2pCreateResult{
		TradeID:       formatTradeID(tradeID),
		EscrowBaseID:  formatEscrowID(escrowBaseID),
		EscrowQuoteID: formatEscrowID(escrowQuoteID),
		PayIntents: map[string]p2pPayIntentResult{
			"buyer": {
				To:     crypto.NewAddress(crypto.NHBPrefix, buyerVault[:]).String(),
				Token:  normalizedQuote,
				Amount: quoteAmount.String(),
				Memo:   "ESCROW:" + formatEscrowID(escrowQuoteID),
			},
			"seller": {
				To:     crypto.NewAddress(crypto.NHBPrefix, sellerVault[:]).String(),
				Token:  normalizedBase,
				Amount: baseAmount.String(),
				Memo:   "ESCROW:" + formatEscrowID(escrowBaseID),
			},
		},
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleP2PGetTrade(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params p2pIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseTradeID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	trade, err := s.node.P2PGetTrade(id)
	if err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatTradeJSON(trade))
}

func (s *Server) handleP2PSettle(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	handleP2PTransition(w, req, s.node.P2PSettle)
}

func (s *Server) handleP2PDispute(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params p2pDisputeParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseTradeID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	if strings.TrimSpace(params.Message) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "message is required")
		return
	}
	if err := s.node.P2PDispute(id, caller, params.Message); err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}

func (s *Server) handleP2PResolve(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params p2pResolveParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseTradeID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(params.Outcome))
	if _, ok := validP2PResolveOutcomes[outcome]; !ok {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "invalid outcome")
		return
	}
	if err := s.node.P2PResolve(id, caller, outcome); err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}

func handleP2PTransition(w http.ResponseWriter, req *RPCRequest, fn func([32]byte, [20]byte) error) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params p2pActorParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseTradeID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeP2PInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := fn(id, caller); err != nil {
		writeP2PError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
}

func writeP2PError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeP2PInternal
	message := "internal_error"
	data := err.Error()
	switch {
	case errors.Is(err, core.ErrTradeNotFound) || strings.Contains(err.Error(), "trade engine: trade not found"):
		status = http.StatusNotFound
		code = codeP2PNotFound
		message = "not_found"
	case strings.Contains(err.Error(), "caller not participant") || strings.Contains(err.Error(), "lacks arbitrator role"):
		status = http.StatusForbidden
		code = codeP2PForbidden
		message = "forbidden"
	case strings.Contains(err.Error(), "requires resolution") || strings.Contains(err.Error(), "must be funded") || strings.Contains(err.Error(), "status transition not allowed") || strings.Contains(err.Error(), "already exists"):
		status = http.StatusConflict
		code = codeP2PConflict
		message = "conflict"
	}
	writeError(w, status, id, code, message, data)
}

func formatTradeJSON(trade *escrow.Trade) tradeJSON {
	buyer := crypto.NewAddress(crypto.NHBPrefix, trade.Buyer[:]).String()
	seller := crypto.NewAddress(crypto.NHBPrefix, trade.Seller[:]).String()
	quoteAmt := "0"
	if trade.QuoteAmount != nil {
		quoteAmt = trade.QuoteAmount.String()
	}
	baseAmt := "0"
	if trade.BaseAmount != nil {
		baseAmt = trade.BaseAmount.String()
	}
	return tradeJSON{
		ID:          formatTradeID(trade.ID),
		OfferID:     trade.OfferID,
		Buyer:       buyer,
		Seller:      seller,
		QuoteToken:  trade.QuoteToken,
		QuoteAmount: quoteAmt,
		EscrowQuote: formatEscrowID(trade.EscrowQuote),
		BaseToken:   trade.BaseToken,
		BaseAmount:  baseAmt,
		EscrowBase:  formatEscrowID(trade.EscrowBase),
		Deadline:    trade.Deadline,
		CreatedAt:   trade.CreatedAt,
		Status:      tradeStatusString(trade.Status),
	}
}

func formatTradeID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

func tradeStatusString(status escrow.TradeStatus) string {
	switch status {
	case escrow.TradeInit:
		return "init"
	case escrow.TradePartialFunded:
		return "partial_funded"
	case escrow.TradeFunded:
		return "funded"
	case escrow.TradeDisputed:
		return "disputed"
	case escrow.TradeSettled:
		return "settled"
	case escrow.TradeCancelled:
		return "cancelled"
	case escrow.TradeExpired:
		return "expired"
	default:
		return "unknown"
	}
}

func parseTradeID(value string) ([32]byte, error) {
	return parseEscrowID(value)
}
