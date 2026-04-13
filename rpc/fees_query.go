package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"nhbchain/crypto"
	"nhbchain/native/fees"
)

type feesTotalsParams struct {
	Domain string `json:"domain"`
}

type feesTotalsRecord struct {
	Domain   string `json:"domain"`
	Wallet   string `json:"wallet"`
	GrossWei string `json:"grossWei"`
	FeeWei   string `json:"feeWei"`
	NetWei   string `json:"netWei"`
}

func (s *Server) handleFeesListTotals(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "domain parameter required", nil)
		return
	}
	var params feesTotalsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	domain := strings.TrimSpace(params.Domain)
	if domain == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "domain parameter required", nil)
		return
	}
	records, err := s.node.FeesTotals(domain)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load fee totals", err.Error())
		return
	}
	results := make([]feesTotalsRecord, len(records))
	for i, entry := range records {
		gross := "0"
		if entry.Gross != nil {
			gross = entry.Gross.String()
		}
		fee := "0"
		if entry.Fee != nil {
			fee = entry.Fee.String()
		}
		net := "0"
		if entry.Net != nil {
			net = entry.Net.String()
		}
		wallet := crypto.MustNewAddress(crypto.NHBPrefix, entry.Wallet[:]).String()
		results[i] = feesTotalsRecord{
			Domain:   fees.NormalizeDomain(entry.Domain),
			Wallet:   wallet,
			GrossWei: gross,
			FeeWei:   fee,
			NetWei:   net,
		}
	}
	writeResult(w, req.ID, results)
}
