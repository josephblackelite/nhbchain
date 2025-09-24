package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxRequestBody        = 1 << 20
	headerIdempotencyKey  = "Idempotency-Key"
	headerNowPaymentsSig  = "X-Nowpayments-Signature"
	headerNowPaymentsSig2 = "x-nowpayments-sig"
)

// Server exposes HTTP endpoints for fiat-to-token flows.
type Server struct {
	store         *SQLiteStore
	oracle        *Oracle
	nowPayments   NowPaymentsClient
	node          NodeClient
	signer        Signer
	quoteTTL      time.Duration
	quoteCurrency string
	hmacSecret    []byte
	nowFn         func() time.Time
}

// QuoteRequest is the payload accepted by POST /quotes.
type QuoteRequest struct {
	Fiat       string `json:"fiat"`
	Token      string `json:"token"`
	AmountFiat string `json:"amountFiat"`
}

// QuoteResponse is returned to the caller when requesting a quote.
type QuoteResponse struct {
	QuoteID     string `json:"quoteId"`
	Fiat        string `json:"fiat"`
	Token       string `json:"token"`
	AmountFiat  string `json:"amountFiat"`
	AmountToken string `json:"amountToken"`
	Expiry      string `json:"expiry"`
}

// InvoiceCreateRequest is accepted by POST /invoices.
type InvoiceCreateRequest struct {
	QuoteID   string `json:"quoteId"`
	Recipient string `json:"recipient"`
}

// NowPaymentsWebhookPayload models the minimal webhook structure.
type NowPaymentsWebhookPayload struct {
	InvoiceID     string `json:"invoice_id"`
	PaymentStatus string `json:"payment_status"`
	Status        string `json:"status"`
}

// NewServer constructs a payments gateway server.
func NewServer(store *SQLiteStore, oracle *Oracle, nowClient NowPaymentsClient, node NodeClient, signer Signer, quoteTTL time.Duration, quoteCurrency string, hmacSecret string) *Server {
	if store == nil {
		panic("store required")
	}
	if oracle == nil {
		panic("oracle required")
	}
	if nowClient == nil {
		panic("nowpayments client required")
	}
	if node == nil {
		panic("node client required")
	}
	if signer == nil {
		panic("kms signer required")
	}
	secret := []byte(strings.TrimSpace(hmacSecret))
	if len(secret) == 0 {
		panic("hmac secret required")
	}
	if quoteTTL <= 0 {
		quoteTTL = 5 * time.Minute
	}
	if strings.TrimSpace(quoteCurrency) == "" {
		quoteCurrency = "USD"
	}
	return &Server{
		store:         store,
		oracle:        oracle,
		nowPayments:   nowClient,
		node:          node,
		signer:        signer,
		quoteTTL:      quoteTTL,
		quoteCurrency: quoteCurrency,
		hmacSecret:    secret,
		nowFn:         time.Now,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/quotes":
		s.handleQuote(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/invoices":
		s.handleInvoiceCreate(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/invoices/"):
		s.handleInvoiceGet(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/webhooks/nowpayments":
		s.handleNowPaymentsWebhook(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(w, r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	var req QuoteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err), body, nil)
		return
	}
	if err := validateQuoteRequest(req, s.quoteCurrency); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	now := s.nowFn().UTC()
	price, err := s.oracle.Price(req.Token, now)
	if err != nil {
		s.writeError(w, r, http.StatusServiceUnavailable, err, body, nil)
		return
	}
	amountToken, err := convertQuote(price, req.AmountFiat)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	quoteID := uuid.NewString()
	expiry := now.Add(s.quoteTTL)
	record := QuoteRecord{
		ID:           quoteID,
		FiatCurrency: s.quoteCurrency,
		Token:        req.Token,
		AmountFiat:   req.AmountFiat,
		AmountToken:  amountToken,
		Expiry:       expiry,
		CreatedAt:    now,
	}
	if err := s.store.InsertQuote(r.Context(), record); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	resp := QuoteResponse{
		QuoteID:     quoteID,
		Fiat:        record.FiatCurrency,
		Token:       record.Token,
		AmountFiat:  record.AmountFiat,
		AmountToken: record.AmountToken,
		Expiry:      expiry.Format(time.RFC3339),
	}
	s.writeJSON(w, r, http.StatusOK, resp, body)
}

func validateQuoteRequest(req QuoteRequest, expectedFiat string) error {
	if strings.TrimSpace(req.Fiat) == "" {
		return errors.New("fiat currency required")
	}
	if expectedFiat != "" && !strings.EqualFold(strings.TrimSpace(req.Fiat), expectedFiat) {
		return fmt.Errorf("unsupported fiat currency: %s", req.Fiat)
	}
	if strings.TrimSpace(req.Token) == "" {
		return errors.New("token required")
	}
	if strings.TrimSpace(req.AmountFiat) == "" {
		return errors.New("amountFiat required")
	}
	if _, ok := new(big.Rat).SetString(req.AmountFiat); !ok {
		return fmt.Errorf("invalid amountFiat: %s", req.AmountFiat)
	}
	return nil
}

func convertQuote(price float64, amountFiat string) (string, error) {
	if price <= 0 {
		return "", fmt.Errorf("invalid oracle price")
	}
	fiat, ok := new(big.Rat).SetString(amountFiat)
	if !ok {
		return "", fmt.Errorf("invalid amountFiat: %s", amountFiat)
	}
	priceRat := new(big.Rat).SetFloat64(price)
	if priceRat.Sign() <= 0 {
		return "", fmt.Errorf("invalid price")
	}
	tokens := new(big.Rat).Quo(fiat, priceRat)
	if tokens.Sign() <= 0 {
		return "", fmt.Errorf("calculated token amount is non-positive")
	}
	return formatRat(tokens, 8), nil
}

func formatRat(r *big.Rat, precision int) string {
	f := new(big.Float).SetRat(r)
	f = f.SetPrec(uint(precision * 4))
	text := f.Text('f', precision)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" {
		text = "0"
	}
	return text
}

func (s *Server) handleInvoiceCreate(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(w, r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if key == "" {
		s.writeError(w, r, http.StatusBadRequest, errors.New("missing Idempotency-Key header"), body, nil)
		return
	}
	requestHash := hashRequest(r.Method, canonicalRequestPath(r), body)
	if cached, err := s.store.LookupIdempotency(r.Context(), key, requestHash); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrIdempotencyConflict) {
			status = http.StatusConflict
		}
		s.writeError(w, r, status, err, body, nil)
		return
	} else if cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cached.Status)
		_, _ = w.Write(cached.Body)
		s.audit(r.Context(), r, body, cached.Body, cached.Status)
		return
	}
	var req InvoiceCreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err), body, nil)
		return
	}
	if err := validateInvoiceCreate(req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	quote, err := s.store.GetQuote(r.Context(), req.QuoteID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	if quote == nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("quote %s not found", req.QuoteID), body, nil)
		return
	}
	now := s.nowFn().UTC()
	if now.After(quote.Expiry) {
		s.writeError(w, r, http.StatusBadRequest, errors.New("quote expired"), body, nil)
		return
	}
	invoiceID := uuid.NewString()
	npReq := &NowPaymentsInvoiceRequest{
		PriceAmount:   quote.AmountFiat,
		PriceCurrency: quote.FiatCurrency,
		PayCurrency:   quote.Token,
		OrderID:       invoiceID,
		OrderDesc:     fmt.Sprintf("Mint %s %s", quote.AmountToken, quote.Token),
		FixedRate:     true,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	invoice, err := s.nowPayments.CreateInvoice(ctx, npReq)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	nowID := firstNonEmpty(invoice.InvoiceID, invoice.ID)
	record := InvoiceRecord{
		ID:        invoiceID,
		QuoteID:   quote.ID,
		Recipient: req.Recipient,
		Status:    "pending",
		NowID:     nowID,
		NowURL:    invoice.InvoiceURL,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.InsertInvoice(r.Context(), record); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	resp := map[string]string{
		"invoiceId":      record.ID,
		"nowpaymentsUrl": record.NowURL,
	}
	respBody, _ := json.Marshal(resp)
	if err := s.store.SaveIdempotency(r.Context(), key, requestHash, http.StatusOK, respBody); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	s.writeJSONBytes(w, r, http.StatusOK, respBody, body)
}

func validateInvoiceCreate(req InvoiceCreateRequest) error {
	if strings.TrimSpace(req.QuoteID) == "" {
		return errors.New("quoteId required")
	}
	if strings.TrimSpace(req.Recipient) == "" {
		return errors.New("recipient required")
	}
	return nil
}

func (s *Server) handleInvoiceGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/invoices/")
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, errors.New("invoice id required"), nil, nil)
		return
	}
	invoice, err := s.store.GetInvoice(r.Context(), id)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	if invoice == nil {
		s.writeError(w, r, http.StatusNotFound, errors.New("invoice not found"), nil, nil)
		return
	}
	quote, err := s.store.GetQuote(r.Context(), invoice.QuoteID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	resp, err := MarshalInvoice(invoice, quote)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	s.writeJSONBytes(w, r, http.StatusOK, resp, nil)
}

func (s *Server) handleNowPaymentsWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := s.readBody(w, r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	sig := strings.TrimSpace(r.Header.Get(headerNowPaymentsSig))
	if sig == "" {
		sig = strings.TrimSpace(r.Header.Get(headerNowPaymentsSig2))
	}
	if !s.verifyHMAC(body, sig) {
		s.writeError(w, r, http.StatusUnauthorized, errors.New("invalid webhook signature"), body, nil)
		return
	}
	var payload NowPaymentsWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid webhook payload: %w", err), body, nil)
		return
	}
	nowID := strings.TrimSpace(firstNonEmpty(payload.InvoiceID))
	if nowID == "" {
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ignored"}, body)
		return
	}
	invoice, err := s.store.GetInvoiceByNowID(r.Context(), nowID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	if invoice == nil {
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "unknown"}, body)
		return
	}
	if strings.EqualFold(invoice.Status, "minted") {
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "already minted"}, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	latest, err := s.nowPayments.GetInvoice(ctx, nowID)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	if !latest.Paid() {
		_ = s.store.UpdateInvoiceStatus(r.Context(), invoice.ID, "processing", nil)
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "pending"}, body)
		return
	}
	quote, err := s.store.GetQuote(r.Context(), invoice.QuoteID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	if quote == nil {
		s.writeError(w, r, http.StatusInternalServerError, fmt.Errorf("quote %s missing", invoice.QuoteID), body, nil)
		return
	}
	txHash, err := s.mintWithVoucher(ctx, invoice, quote)
	if err != nil {
		_ = s.store.UpdateInvoiceStatus(r.Context(), invoice.ID, "error", nil)
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	_ = s.store.UpdateInvoiceStatus(r.Context(), invoice.ID, "minted", &txHash)
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "minted", "txHash": txHash}, body)
}

func (s *Server) mintWithVoucher(ctx context.Context, invoice *InvoiceRecord, quote *QuoteRecord) (string, error) {
	payload := strings.Join([]string{invoice.Recipient, quote.Token, quote.AmountToken, invoice.ID}, "|")
	sig, err := s.signer.Sign(ctx, []byte(payload))
	if err != nil {
		return "", err
	}
	voucher := MintVoucher{
		Recipient: invoice.Recipient,
		Token:     quote.Token,
		Amount:    quote.AmountToken,
		Nonce:     invoice.ID,
		Signature: hex.EncodeToString(sig),
	}
	return s.node.MintWithSig(ctx, voucher)
}

func (s *Server) verifyHMAC(body []byte, signature string) bool {
	if strings.TrimSpace(signature) == "" {
		return false
	}
	mac := hmac.New(sha512.New, s.hmacSecret)
	mac.Write(body)
	expected := mac.Sum(nil)
	decoded, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	if len(decoded) != len(expected) {
		return false
	}
	if hmac.Equal(decoded, expected) {
		return true
	}
	return false
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBody)
	defer func() {
		_ = r.Body.Close()
	}()
	return io.ReadAll(reader)
}

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, payload interface{}, reqBody []byte) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, reqBody, nil)
		return
	}
	s.writeJSONBytes(w, r, status, body, reqBody)
}

func (s *Server) writeJSONBytes(w http.ResponseWriter, r *http.Request, status int, body []byte, reqBody []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	s.audit(r.Context(), r, reqBody, body, status)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, err error, reqBody []byte, extra map[string]interface{}) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	payload := map[string]interface{}{"error": err.Error()}
	if extra != nil {
		for k, v := range extra {
			payload[k] = v
		}
	}
	body, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	s.audit(r.Context(), r, reqBody, body, status)
}

func (s *Server) audit(ctx context.Context, r *http.Request, requestBody, responseBody []byte, status int) {
	if s.store == nil {
		return
	}
	entry := AuditEntry{
		Method:         r.Method,
		Path:           canonicalRequestPath(r),
		RequestBody:    requestBody,
		ResponseStatus: status,
		ResponseBody:   responseBody,
		Timestamp:      s.nowFn().UTC(),
	}
	_ = s.store.InsertAudit(ctx, entry)
}

func canonicalRequestPath(r *http.Request) string {
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		parts := strings.Split(r.URL.RawQuery, "&")
		sort.Strings(parts)
		path += "?" + strings.Join(parts, "&")
	}
	return path
}

func hashRequest(method, path string, body []byte) string {
	payload := strings.Join([]string{strings.ToUpper(method), path, string(body)}, "\n")
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
