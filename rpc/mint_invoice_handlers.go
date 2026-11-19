package rpc

import (
	"encoding/json"
	"errors"
	"net/http"

	"nhbchain/core"
)

// mintInvoiceStatusResult is the answer of mint_getInvoiceStatus.
type mintInvoiceStatusResult struct {
	// InvoiceID is the identifier that was looked up, trimmed as the mint trims it.
	InvoiceID string `json:"invoiceId"`
	// Settled is true when a mint carrying this identifier has been executed in a committed block.
	Settled bool `json:"settled"`
	// Height is the height of the next block: the answer was read from the state that block will be built on.
	Height uint64 `json:"height"`
}

// handleMintGetInvoiceStatus answers mint_getInvoiceStatus: params [invoiceId], the identifier in the same
// string form a mint voucher carries it.
//
// mint_with_sig refuses a repeated invoice identifier with one error (HTTP 409, the duplicate code) both
// when the first mint has been executed in a block and when it is only waiting in this node's mempool.
// This read tells the two apart: it reports the committed state only and never the mempool, so a mint
// that is only waiting reads "settled": false. It changes nothing.
//
// Operator credential only (OperatorOnlyMethods): the gate in Server.handle has already refused every
// other caller before this handler runs. Unlike mint_with_sig the read carries no voucher signature of
// its own, so it is not one of the gate's exemptions.
func (s *Server) handleMintGetInvoiceStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected invoiceId", nil)
		return
	}
	// A JSON null would decode into a string without an error and read as an empty identifier; an identifier
	// must be a JSON string.
	var raw *string
	if err := json.Unmarshal(req.Params[0], &raw); err != nil || raw == nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invoiceId must be a string", nil)
		return
	}
	status, err := s.node.MintInvoiceSettled(*raw)
	if err != nil {
		if errors.Is(err, core.ErrMintInvoiceIDRequired) {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invoiceId required", nil)
			return
		}
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to read invoice status", err.Error())
		return
	}
	writeResult(w, req.ID, mintInvoiceStatusResult{InvoiceID: status.InvoiceID, Settled: status.Settled, Height: status.Height})
}
