package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	SlippageBps uint32 `json:"slippageBps"`
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
	FundedAt    int64  `json:"fundedAt"`
	SlippageBps uint32 `json:"slippageBps"`
	Status      string `json:"status"`
}

var validP2PResolveOutcomes = map[string]struct{}{
	"release_both":              {},
	"refund_both":               {},
	"release_base_refund_quote": {},
	"release_quote_refund_base": {},
}

// p2pRPCDisabledMessage: same guaranteed-fork defect as escrow's RPC handlers
// (see escrowRPCDisabledMessage) -- p2p_createTrade/settle/dispute/resolve
// mutated n.state.Trie directly outside the block pipeline. Disabled as an
// emergency stopgap; p2p_getPeers/getTrade (read-only) are left live. The
// live P2P market feature itself is unaffected -- it uses a separately
// signed native-tx path (TX_TYPE_MARKET_CREATE_LISTING/FILL/CANCEL via
// nhb_sendTransaction), not these RPC methods.
const p2pRPCDisabledMessage = "this method is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending"

func (s *Server) handleP2PCreateTrade(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, p2pRPCDisabledMessage, nil)
}

func (s *Server) handleP2PGetPeers(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
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
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, p2pRPCDisabledMessage, nil)
}

func (s *Server) handleP2PDispute(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, p2pRPCDisabledMessage, nil)
}

func (s *Server) handleP2PResolve(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, p2pRPCDisabledMessage, nil)
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
	buyer := crypto.MustNewAddress(crypto.NHBPrefix, trade.Buyer[:]).String()
	seller := crypto.MustNewAddress(crypto.NHBPrefix, trade.Seller[:]).String()
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
		FundedAt:    trade.FundedAt,
		SlippageBps: trade.SlippageBps,
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
