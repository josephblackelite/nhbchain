package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nhbchain/services/payoutd"
)

// Server exposes unified read-only operator reporting.
type Server struct {
	mint     *MintReader
	merchant *MerchantReader
	treasury *TreasuryReader
	payout   *PayoutExecutionReader
	token    string
	nowFn    func() time.Time
}

// NewServer constructs a unified operator reporting server.
func NewServer(mint *MintReader, merchant *MerchantReader, treasury *TreasuryReader, payout *PayoutExecutionReader, bearerToken string) *Server {
	return &Server{
		mint:     mint,
		merchant: merchant,
		treasury: treasury,
		payout:   payout,
		token:    strings.TrimSpace(bearerToken),
		nowFn:    time.Now,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler := s.auth(http.HandlerFunc(s.route))
	handler.ServeHTTP(w, r)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	case r.Method == http.MethodGet && r.URL.Path == "/summary":
		s.handleSummary(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/mint/invoices":
		s.handleMintInvoices(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/mint/export":
		s.handleMintExport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/merchant/trades":
		s.handleMerchantTrades(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/merchant/export":
		s.handleMerchantExport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/treasury/instructions":
		s.handleTreasuryInstructions(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/treasury/export":
		s.handleTreasuryExport(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/payout/executions":
		s.handlePayoutExecutions(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/payout/export":
		s.handlePayoutExport(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := BuildOpsSummary(r.Context(), s.mint, s.merchant, s.treasury, s.payout, s.nowFn())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleMintInvoices(w http.ResponseWriter, r *http.Request) {
	filter, err := parseMintFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.mint.List(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleMerchantTrades(w http.ResponseWriter, r *http.Request) {
	filter, err := parseMerchantFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.merchant.List(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleMerchantExport(w http.ResponseWriter, r *http.Request) {
	filter, err := parseMerchantFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.merchant.List(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, items)
	case "csv":
		body := marshalMerchantCSV(items)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ops-merchant-report.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func (s *Server) handleMintExport(w http.ResponseWriter, r *http.Request) {
	filter, err := parseMintFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.mint.List(r.Context(), filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, items)
	case "csv":
		body, err := marshalMintCSV(items)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ops-mint-report.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func (s *Server) handleTreasuryInstructions(w http.ResponseWriter, r *http.Request) {
	filter, err := parseTreasuryFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.treasury.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleTreasuryExport(w http.ResponseWriter, r *http.Request) {
	filter, err := parseTreasuryFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.treasury.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, items)
	case "csv":
		body := marshalTreasuryCSV(items)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ops-treasury-report.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func (s *Server) handlePayoutExecutions(w http.ResponseWriter, r *http.Request) {
	filter, err := parsePayoutFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.payout.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handlePayoutExport(w http.ResponseWriter, r *http.Request) {
	filter, err := parsePayoutFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.payout.List(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		writeJSON(w, http.StatusOK, items)
	case "csv":
		body := marshalPayoutCSV(items)
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ops-payout-report.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		expected := "Bearer " + s.token
		if strings.TrimSpace(r.Header.Get("Authorization")) != expected {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseMintFilter(r *http.Request) (MintFilter, error) {
	query := r.URL.Query()
	filter := MintFilter{
		Status:    strings.TrimSpace(query.Get("status")),
		Recipient: strings.TrimSpace(query.Get("recipient")),
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return MintFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	} else {
		filter.Limit = 100
	}
	parseTS := func(key string) (*time.Time, error) {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			return nil, nil
		}
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, err
		}
		utc := ts.UTC()
		return &utc, nil
	}
	var err error
	if filter.CreatedFrom, err = parseTS("created_from"); err != nil {
		return MintFilter{}, err
	}
	if filter.CreatedTo, err = parseTS("created_to"); err != nil {
		return MintFilter{}, err
	}
	if filter.UpdatedFrom, err = parseTS("updated_from"); err != nil {
		return MintFilter{}, err
	}
	if filter.UpdatedTo, err = parseTS("updated_to"); err != nil {
		return MintFilter{}, err
	}
	return filter, nil
}

func parseTreasuryFilter(r *http.Request) (TreasuryFilter, error) {
	query := r.URL.Query()
	filter := TreasuryFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Action: strings.TrimSpace(query.Get("action")),
		Asset:  strings.TrimSpace(query.Get("asset")),
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return TreasuryFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	} else {
		filter.Limit = 100
	}
	return filter, nil
}

func parseMerchantFilter(r *http.Request) (MerchantFilter, error) {
	query := r.URL.Query()
	filter := MerchantFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Seller: strings.TrimSpace(query.Get("seller")),
		Buyer:  strings.TrimSpace(query.Get("buyer")),
		Limit:  100,
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return MerchantFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	}
	return filter, nil
}

func parsePayoutFilter(r *http.Request) (PayoutFilter, error) {
	query := r.URL.Query()
	filter := PayoutFilter{
		Status: strings.TrimSpace(query.Get("status")),
		Asset:  strings.TrimSpace(query.Get("asset")),
		Limit:  100,
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return PayoutFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	}
	return filter, nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	body, err := marshalJSON(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func marshalMintCSV(items []MintInvoiceRow) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("invoice_id,quote_id,recipient,status,fiat,token,mint_asset,pay_currency,amount_fiat,service_fee_fiat,total_fiat,amount_token,estimated_pay_amount,quote_expiry,created_at,updated_at,nowpayments_id,nowpayments_url,tx_hash\n")
	for _, item := range items {
		builder.WriteString(strings.Join([]string{
			csvEscape(item.InvoiceID),
			csvEscape(item.QuoteID),
			csvEscape(item.Recipient),
			csvEscape(item.Status),
			csvEscape(item.Fiat),
			csvEscape(item.Token),
			csvEscape(item.MintAsset),
			csvEscape(item.PayCurrency),
			csvEscape(item.AmountFiat),
			csvEscape(item.ServiceFeeFiat),
			csvEscape(item.TotalFiat),
			csvEscape(item.AmountToken),
			csvEscape(item.EstimatedPayAmount),
			csvEscape(item.QuoteExpiry),
			csvEscape(item.CreatedAt),
			csvEscape(item.UpdatedAt),
			csvEscape(item.NowPaymentsID),
			csvEscape(item.NowPaymentsURL),
			csvEscape(item.TxHash),
		}, ","))
		builder.WriteString("\n")
	}
	return []byte(builder.String()), nil
}

func marshalMerchantCSV(items []MerchantTradeRow) []byte {
	var builder strings.Builder
	builder.WriteString("trade_id,offer_id,buyer,seller,base_token,base_amount,quote_token,quote_amount,escrow_base_id,escrow_quote_id,status,created_at,updated_at\n")
	for _, item := range items {
		builder.WriteString(strings.Join([]string{
			csvEscape(item.TradeID),
			csvEscape(item.OfferID),
			csvEscape(item.Buyer),
			csvEscape(item.Seller),
			csvEscape(item.BaseToken),
			csvEscape(item.BaseAmount),
			csvEscape(item.QuoteToken),
			csvEscape(item.QuoteAmount),
			csvEscape(item.EscrowBaseID),
			csvEscape(item.EscrowQuoteID),
			csvEscape(item.Status),
			csvEscape(item.CreatedAt),
			csvEscape(item.UpdatedAt),
		}, ","))
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}

func marshalTreasuryCSV(items []payoutd.TreasuryInstruction) []byte {
	var builder strings.Builder
	builder.WriteString("id,action,asset,amount,source,destination,status,requested_by,approved_by,rejected_by,notes,review_notes,created_at,approved_at,rejected_at\n")
	for _, item := range items {
		builder.WriteString(strings.Join([]string{
			csvEscape(item.ID),
			csvEscape(item.Action),
			csvEscape(item.Asset),
			csvEscape(item.Amount),
			csvEscape(item.Source),
			csvEscape(item.Destination),
			csvEscape(string(item.Status)),
			csvEscape(item.RequestedBy),
			csvEscape(item.ApprovedBy),
			csvEscape(item.RejectedBy),
			csvEscape(item.Notes),
			csvEscape(item.ReviewNotes),
			csvEscape(item.CreatedAt.UTC().Format(time.RFC3339)),
			csvEscape(formatOptionalTime(item.ApprovedAt)),
			csvEscape(formatOptionalTime(item.RejectedAt)),
		}, ","))
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}

func marshalPayoutCSV(items []payoutd.PayoutExecution) []byte {
	var builder strings.Builder
	builder.WriteString("intent_id,stable_asset,stable_amount,nhb_amount,destination,evidence_uri,tx_hash,status,error,created_at,updated_at,settled_at\n")
	for _, item := range items {
		builder.WriteString(strings.Join([]string{
			csvEscape(item.IntentID),
			csvEscape(item.StableAsset),
			csvEscape(item.StableAmount),
			csvEscape(item.NhbAmount),
			csvEscape(item.Destination),
			csvEscape(item.EvidenceURI),
			csvEscape(item.TxHash),
			csvEscape(string(item.Status)),
			csvEscape(item.Error),
			csvEscape(item.CreatedAt.UTC().Format(time.RFC3339)),
			csvEscape(item.UpdatedAt.UTC().Format(time.RFC3339)),
			csvEscape(formatOptionalTime(item.SettledAt)),
		}, ","))
		builder.WriteString("\n")
	}
	return []byte(builder.String())
}

func formatOptionalTime(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func csvEscape(value string) string {
	if strings.ContainsAny(value, ",\"\n") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}
