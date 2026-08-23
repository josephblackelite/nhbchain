package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"nhbchain/core"
)

const (
	maxRequestBody        = 1 << 20
	headerIdempotencyKey  = "Idempotency-Key"
	headerNowPaymentsSig  = "X-Nowpayments-Signature"
	headerNowPaymentsSig2 = "x-nowpayments-sig"
	mintVoucherTTL        = 10 * time.Minute

	// paymentClaimingStatus marks a payments-table row that InsertPayment
	// has claimed (via idx_payments_active_quote_currency) but that hasn't
	// been filled in with real NOWPayments data yet. Deliberately not in
	// isTerminalPaymentStatus's terminal set, so the unique index keeps
	// blocking a second concurrent claim for the same (quote_id,
	// pay_currency) slot while this status is in place.
	paymentClaimingStatus = "claiming"

	// claimPaymentPollInterval/claimPaymentPollTimeout bound how long a
	// request that lost the initial (quote_id, pay_currency) claim race
	// spends waiting for either the claim winner to finish filling in its
	// row (so this request can reuse it), or for the slot to free up (the
	// winner's NOWPayments call failed) so this request can claim it
	// itself.
	claimPaymentPollInterval = 25 * time.Millisecond
	claimPaymentPollTimeout  = 9 * time.Second
)

// errClaimPaymentTimedOut is returned by resolvePayment when a caller that
// lost the initial claim race still hasn't obtained a usable payment --
// either by reuse or by claiming the slot itself -- after
// claimPaymentPollTimeout. handlePaymentCreate maps this to a 503 rather
// than leaving the caller to hang until the outer request timeout.
var errClaimPaymentTimedOut = errors.New("timed out waiting for a concurrent payment creation to finish")

// nowPaymentsCreateError wraps a NOWPayments CreatePayment failure so it
// keeps mapping to 502 Bad Gateway (matching the pre-fix behavior) after
// passing through resolvePayment's single generic error return.
type nowPaymentsCreateError struct{ err error }

func (e *nowPaymentsCreateError) Error() string { return e.err.Error() }
func (e *nowPaymentsCreateError) Unwrap() error { return e.err }

// Server exposes HTTP endpoints for fiat-to-token flows.
type Server struct {
	store            *SQLiteStore
	oracle           *Oracle
	nowPayments      NowPaymentsClient
	node             NodeClient
	signer           Signer
	quoteTTL         time.Duration
	quoteCurrency    string
	defaultMintAsset string
	serviceFeeBps    int
	hmacSecret       []byte
	nowFn            func() time.Time
	ipnCallbackURL   string
	adminToken       []byte
}

// SetAdminToken configures the bearer token required by GET
// /admin/webhook-events. Kept as a setter (rather than a NewServer
// parameter) so it stays optional and doesn't disturb NewServer's existing
// call sites; an unset token leaves the route permanently unauthorized
// (fail closed) rather than open.
func (s *Server) SetAdminToken(token string) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		s.adminToken = nil
		return
	}
	s.adminToken = []byte(trimmed)
}

// QuoteRequest is the payload accepted by POST /quotes.
type QuoteRequest struct {
	Fiat        string `json:"fiat"`
	Token       string `json:"token"`
	MintAsset   string `json:"mintAsset"`
	PayCurrency string `json:"payCurrency"`
	AmountFiat  string `json:"amountFiat"`
	AmountMint  string `json:"amountMint"`
}

// QuoteResponse is returned to the caller when requesting a quote.
type QuoteResponse struct {
	QuoteID            string `json:"quoteId"`
	Fiat               string `json:"fiat"`
	Token              string `json:"token"`
	MintAsset          string `json:"mintAsset"`
	PayCurrency        string `json:"payCurrency"`
	AmountFiat         string `json:"amountFiat"`
	ServiceFeeFiat     string `json:"serviceFeeFiat"`
	TotalFiat          string `json:"totalFiat"`
	AmountToken        string `json:"amountToken"`
	EstimatedPayAmount string `json:"estimatedPayAmount,omitempty"`
	Expiry             string `json:"expiry"`
}

// InvoiceCreateRequest is accepted by POST /invoices.
type InvoiceCreateRequest struct {
	QuoteID   string `json:"quoteId"`
	Recipient string `json:"recipient"`
}

// PaymentCreateRequest is accepted by POST /payments -- the headless
// (deposit-address) sibling of InvoiceCreateRequest. PayCurrency is
// required (unlike the invoice flow, there is no checkout page for the
// payer to pick a currency on, so the caller must supply one up front).
type PaymentCreateRequest struct {
	QuoteID     string `json:"quoteId"`
	PayCurrency string `json:"payCurrency"`
	Recipient   string `json:"recipient"`
}

// NowPaymentsWebhookPayload models the minimal webhook structure shared by
// both NOWPayments products this service integrates with: invoice IPNs key
// off invoice_id, headless payment IPNs key off payment_id (with order_id
// echoing back whatever we sent at creation time). PaymentID is typed as
// flexibleAmount because NOWPayments' payment product commonly sends this
// field as a bare JSON number -- see flexibleAmount's doc comment.
type NowPaymentsWebhookPayload struct {
	InvoiceID     string         `json:"invoice_id"`
	PaymentID     flexibleAmount `json:"payment_id"`
	OrderID       string         `json:"order_id"`
	PaymentStatus string         `json:"payment_status"`
	Status        string         `json:"status"`
}

// NewServer constructs a payments gateway server.
func NewServer(store *SQLiteStore, oracle *Oracle, nowClient NowPaymentsClient, node NodeClient, signer Signer, quoteTTL time.Duration, quoteCurrency, defaultMintAsset string, serviceFeeBps int, hmacSecret string, ipnCallbackURL string) *Server {
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
	if strings.TrimSpace(defaultMintAsset) == "" {
		defaultMintAsset = "NHB"
	}
	return &Server{
		store:            store,
		oracle:           oracle,
		nowPayments:      nowClient,
		node:             node,
		signer:           signer,
		quoteTTL:         quoteTTL,
		quoteCurrency:    strings.ToUpper(strings.TrimSpace(quoteCurrency)),
		defaultMintAsset: strings.ToUpper(strings.TrimSpace(defaultMintAsset)),
		serviceFeeBps:    serviceFeeBps,
		hmacSecret:       secret,
		nowFn:            time.Now,
		ipnCallbackURL:   strings.TrimSpace(ipnCallbackURL),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && (r.URL.Path == "/quotes" || r.URL.Path == "/swap/quotes"):
		s.handleQuote(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/invoices" || r.URL.Path == "/swap/invoices"):
		s.handleInvoiceCreate(w, r)
	case r.Method == http.MethodGet && (r.URL.Path == "/invoices" || r.URL.Path == "/swap/invoices"):
		s.handleInvoiceList(w, r)
	case r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/invoices/") || strings.HasPrefix(r.URL.Path, "/swap/invoices/")):
		s.handleInvoiceGet(w, r)
	case r.Method == http.MethodGet && (r.URL.Path == "/currencies" || r.URL.Path == "/swap/currencies"):
		s.handleCurrencies(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/payments" || r.URL.Path == "/swap/payments"):
		s.handlePaymentCreate(w, r)
	case r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/payments/") || strings.HasPrefix(r.URL.Path, "/swap/payments/")):
		s.handlePaymentGet(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/reconciliation/summary":
		s.handleReconciliationSummary(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/reconciliation/export":
		s.handleReconciliationExport(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/webhooks/nowpayments":
		s.handleNowPaymentsWebhook(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/webhook-events":
		s.handleAdminWebhookEvents(w, r)
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
	normalised, err := s.normaliseQuoteRequest(req)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, body, nil)
		return
	}
	now := s.nowFn().UTC()
	amountToken, err := s.computeMintAmount(normalised.MintAsset, normalised.AmountFiat, now)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrPriceUnavailable) {
			status = http.StatusServiceUnavailable
		}
		s.writeError(w, r, status, err, body, nil)
		return
	}
	serviceFeeFiat, totalFiat, err := applyFeeToAmount(normalised.AmountFiat, s.serviceFeeBps)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	estimatedPayAmount, err := s.estimatePayAmount(r.Context(), totalFiat, normalised.PayCurrency)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	quoteID := uuid.NewString()
	expiry := now.Add(s.quoteTTL)
	record := QuoteRecord{
		ID:                 quoteID,
		FiatCurrency:       s.quoteCurrency,
		Token:              normalised.MintAsset,
		MintAsset:          normalised.MintAsset,
		PayCurrency:        normalised.PayCurrency,
		AmountFiat:         normalised.AmountFiat,
		ServiceFeeFiat:     serviceFeeFiat,
		TotalFiat:          totalFiat,
		AmountToken:        amountToken,
		EstimatedPayAmount: estimatedPayAmount,
		Expiry:             expiry,
		CreatedAt:          now,
	}
	if err := s.store.InsertQuote(r.Context(), record); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	resp := QuoteResponse{
		QuoteID:            quoteID,
		Fiat:               record.FiatCurrency,
		Token:              record.Token,
		MintAsset:          record.MintAsset,
		PayCurrency:        record.PayCurrency,
		AmountFiat:         record.AmountFiat,
		ServiceFeeFiat:     record.ServiceFeeFiat,
		TotalFiat:          record.TotalFiat,
		AmountToken:        record.AmountToken,
		EstimatedPayAmount: record.EstimatedPayAmount,
		Expiry:             expiry.Format(time.RFC3339),
	}
	s.writeJSON(w, r, http.StatusOK, resp, body)
}

type normalisedQuoteRequest struct {
	Fiat        string
	MintAsset   string
	PayCurrency string
	AmountFiat  string
}

func validateQuoteRequest(req QuoteRequest, expectedFiat string) error {
	if strings.TrimSpace(req.Fiat) == "" {
		return errors.New("fiat currency required")
	}
	if expectedFiat != "" && !strings.EqualFold(strings.TrimSpace(req.Fiat), expectedFiat) {
		return fmt.Errorf("unsupported fiat currency: %s", req.Fiat)
	}
	if strings.TrimSpace(req.Token) == "" {
		if strings.TrimSpace(req.MintAsset) == "" {
			return errors.New("token or mintAsset required")
		}
	}
	if strings.TrimSpace(req.AmountFiat) == "" && strings.TrimSpace(req.AmountMint) == "" {
		return errors.New("amountFiat or amountMint required")
	}
	if strings.TrimSpace(req.AmountFiat) != "" {
		if _, ok := new(big.Rat).SetString(req.AmountFiat); !ok {
			return fmt.Errorf("invalid amountFiat: %s", req.AmountFiat)
		}
	}
	if strings.TrimSpace(req.AmountMint) != "" {
		if _, ok := new(big.Rat).SetString(req.AmountMint); !ok {
			return fmt.Errorf("invalid amountMint: %s", req.AmountMint)
		}
	}
	return nil
}

func (s *Server) normaliseQuoteRequest(req QuoteRequest) (normalisedQuoteRequest, error) {
	if err := validateQuoteRequest(req, s.quoteCurrency); err != nil {
		return normalisedQuoteRequest{}, err
	}
	mintAsset := strings.ToUpper(strings.TrimSpace(firstNonEmpty(req.MintAsset, req.Token, s.defaultMintAsset)))
	// ZNHB is fixed supply and can never be minted -- the chain rejects any
	// TxTypeMint for it unconditionally (core/mint.go's ErrMintZNHBNotMintable),
	// regardless of role grants. Reject here too, before a real NOWPayments
	// invoice is created: without this, a customer could pay real fiat/crypto
	// for a ZNHB quote and only discover the mint is impossible when the
	// webhook fires post-payment, with no refund path (see mintWithVoucher).
	if mintAsset == "ZNHB" {
		return normalisedQuoteRequest{}, errors.New("ZNHB cannot be purchased -- it is fixed supply; buy NHB and swap to ZNHB on-chain instead")
	}
	payCurrency := strings.ToUpper(strings.TrimSpace(firstNonEmpty(req.PayCurrency, mintAsset)))
	amountFiat := strings.TrimSpace(req.AmountFiat)
	amountMint := strings.TrimSpace(req.AmountMint)
	if amountFiat == "" {
		if mintAsset == "NHB" && s.quoteCurrency == "USD" {
			amountFiat = amountMint
		} else {
			return normalisedQuoteRequest{}, errors.New("amountFiat is required for non-NHB mint assets")
		}
	}
	return normalisedQuoteRequest{
		Fiat:        s.quoteCurrency,
		MintAsset:   mintAsset,
		PayCurrency: payCurrency,
		AmountFiat:  amountFiat,
	}, nil
}

func (s *Server) computeMintAmount(mintAsset, amountFiat string, now time.Time) (string, error) {
	if strings.EqualFold(strings.TrimSpace(mintAsset), "NHB") && strings.EqualFold(s.quoteCurrency, "USD") {
		rat, ok := new(big.Rat).SetString(strings.TrimSpace(amountFiat))
		if !ok {
			return "", fmt.Errorf("invalid amountFiat: %s", amountFiat)
		}
		return formatRat(rat, 8), nil
	}
	price, err := s.oracle.Price(strings.ToUpper(strings.TrimSpace(mintAsset)), now)
	if err != nil {
		return "", err
	}
	return convertQuote(price, amountFiat)
}

func applyFeeToAmount(amountFiat string, feeBps int) (string, string, error) {
	base, ok := new(big.Rat).SetString(strings.TrimSpace(amountFiat))
	if !ok {
		return "", "", fmt.Errorf("invalid amountFiat: %s", amountFiat)
	}
	if feeBps <= 0 {
		text := formatRat(base, 8)
		return "0", text, nil
	}
	fee := new(big.Rat).Mul(base, new(big.Rat).SetFrac64(int64(feeBps), 10_000))
	total := new(big.Rat).Add(base, fee)
	return formatRat(fee, 8), formatRat(total, 8), nil
}

// deductFeeFromAmount is applyFeeToAmount's inverse: given an amount that
// has already landed (e.g. NOWPayments' own outcome_amount, already net of
// its conversion/network fees), it subtracts our own service fee to get
// what actually nets to us. Used for settlement-time minting -- unlike
// quote-time gross-up, where the buyer is asked to pay more than the mint
// amount, here the fee comes out of what already arrived. Clamped at zero:
// an amount smaller than our fee (a near-dust payment) nets to nothing
// rather than a negative mint.
func deductFeeFromAmount(amount string, feeBps int) (fee, net string, err error) {
	base, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	if !ok {
		return "", "", fmt.Errorf("invalid amount: %s", amount)
	}
	if feeBps <= 0 {
		text := formatRat(base, 8)
		return "0", text, nil
	}
	feeRat := new(big.Rat).Mul(base, new(big.Rat).SetFrac64(int64(feeBps), 10_000))
	netRat := new(big.Rat).Sub(base, feeRat)
	if netRat.Sign() < 0 {
		netRat = new(big.Rat)
	}
	return formatRat(feeRat, 8), formatRat(netRat, 8), nil
}

// mintDecimals is NHB and ZNHB's on-chain fixed-point precision -- see
// mintWithVoucher's doc comment for why every mint amount gets scaled by
// this many decimals at exactly one point before going on-chain.
const mintDecimals = 18

// decimalToWeiString converts a human-readable decimal amount (e.g.
// "14.523456") into the base-10 integer string representing that many
// smallest units at `decimals` precision (e.g. "14523456000000000000" at
// 18 decimals) -- what MintVoucher.Amount/AmountBig() actually require.
// Rejects an amount with more fractional digits than `decimals` supports
// rather than silently truncating or rounding them away: every amount this
// package produces is already capped at 8 decimal places (formatRat), well
// within 18, so this should never trigger in practice -- it exists as a
// defensive check against a future caller supplying more precision than
// the chain can represent, not a case this package's own callers hit.
func decimalToWeiString(amount string, decimals int) (string, error) {
	rat, ok := new(big.Rat).SetString(strings.TrimSpace(amount))
	if !ok {
		return "", fmt.Errorf("invalid amount: %s", amount)
	}
	if rat.Sign() <= 0 {
		return "", fmt.Errorf("amount must be positive: %s", amount)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	scaled := new(big.Rat).Mul(rat, new(big.Rat).SetInt(scale))
	if !scaled.IsInt() {
		return "", fmt.Errorf("amount %s has more than %d fractional digits", amount, decimals)
	}
	return scaled.Num().String(), nil
}

func (s *Server) estimatePayAmount(ctx context.Context, totalFiat, payCurrency string) (string, error) {
	payCurrency = strings.ToUpper(strings.TrimSpace(payCurrency))
	if payCurrency == "" || strings.EqualFold(payCurrency, s.quoteCurrency) {
		return totalFiat, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	estimate, err := s.nowPayments.Estimate(ctx, &NowPaymentsEstimateRequest{
		Amount:       totalFiat,
		CurrencyFrom: s.quoteCurrency,
		CurrencyTo:   payCurrency,
	})
	if err != nil {
		return "", err
	}
	amount := strings.TrimSpace(firstNonEmpty(string(estimate.EstimatedAmount), string(estimate.AmountTo)))
	if amount == "" {
		return "", fmt.Errorf("nowpayments estimate returned empty amount")
	}
	return amount, nil
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
		PriceAmount:   quote.TotalFiat,
		PriceCurrency: quote.FiatCurrency,
		PayCurrency:   quote.PayCurrency,
		OrderID:       invoiceID,
		OrderDesc:     fmt.Sprintf("Mint %s %s via %s", quote.AmountToken, quote.MintAsset, quote.PayCurrency),
		FixedRate:     true,
		// The swapper bears NOWPayments' processing cost, not the NHBCoin
		// treasury: NOWPayments grosses up what the payer is asked to send
		// so the merchant account still receives TotalFiat in full.
		IsFeePaidByUser: true,
		IpnCallbackURL:  s.ipnCallbackURL,
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
		"mintAsset":      quote.MintAsset,
		"payCurrency":    quote.PayCurrency,
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

// preferredCurrencyOrder gives a stable, sensible display order for the
// headless-payment currency picker: the currencies swappers reach for most
// come first, everything else NOWPayments has enabled for this merchant
// account follows in whatever order the API returned them.
var preferredCurrencyOrder = []string{
	"btc", "eth", "usdttrc20", "usdtbsc", "usdtsol", "ltc", "sol", "trx", "bnb", "doge",
}

// maxListedCurrencies caps the size of the GET /swap/currencies response.
const maxListedCurrencies = 40

func orderPreferredCurrencies(enabled []string, preferred []string, max int) []string {
	seen := make(map[string]bool, len(enabled))
	normalized := make([]string, 0, len(enabled))
	for _, c := range enabled {
		lc := strings.ToLower(strings.TrimSpace(c))
		if lc == "" || seen[lc] {
			continue
		}
		seen[lc] = true
		normalized = append(normalized, lc)
	}
	ordered := make([]string, 0, len(normalized))
	used := make(map[string]bool, len(normalized))
	for _, p := range preferred {
		if seen[p] && !used[p] {
			ordered = append(ordered, p)
			used[p] = true
		}
	}
	for _, c := range normalized {
		if !used[c] {
			ordered = append(ordered, c)
			used[c] = true
		}
	}
	if max > 0 && len(ordered) > max {
		ordered = ordered[:max]
	}
	return ordered
}

func currencyEnabled(coins []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, c := range coins {
		if strings.EqualFold(strings.TrimSpace(c), target) {
			return true
		}
	}
	return false
}

func (s *Server) handleCurrencies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	coins, err := s.nowPayments.ListMerchantCoins(ctx)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, nil, nil)
		return
	}
	ordered := orderPreferredCurrencies(coins, preferredCurrencyOrder, maxListedCurrencies)
	s.writeJSON(w, r, http.StatusOK, map[string]interface{}{"currencies": ordered}, nil)
}

func validatePaymentCreate(req PaymentCreateRequest) error {
	if strings.TrimSpace(req.QuoteID) == "" {
		return errors.New("quoteId required")
	}
	if strings.TrimSpace(req.PayCurrency) == "" {
		return errors.New("payCurrency required")
	}
	if strings.TrimSpace(req.Recipient) == "" {
		return errors.New("recipient required")
	}
	return nil
}

// isTerminalPaymentStatus reports whether a payment (tracked via our own
// PaymentRecord.Status) is done changing state on its own and therefore
// safe to leave alone (never silently reused as if still awaiting funds).
// This covers NOWPayments' own terminal statuses -- finished (success) and
// failed/expired/refunded (failure) -- plus the two statuses this service
// layers on top after a "finished" payment is processed by the webhook
// handler: minted (mint succeeded) and error (mint call failed). The latter
// two matter here because by the time a row reaches either, NOWPayments has
// already recorded the payment as finished and funds have already changed
// hands -- treating them as non-terminal would let handlePaymentCreate spin
// up a second NOWPayments payment for the same quote+currency and risk
// double-charging a swapper who already paid.
func isTerminalPaymentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished", "failed", "expired", "refunded", "minted", "settled_zero", "error":
		return true
	}
	return false
}

// resolvePayment is the atomic-claim half of handlePaymentCreate's
// idempotent-reuse policy. Unlike a SELECT-then-CreatePayment-then-INSERT
// sequence (which leaves a window for two concurrent requests to both pass
// the SELECT before either INSERT commits), this always attempts to INSERT
// a placeholder payment row for (quote.ID, payCurrency) FIRST:
// idx_payments_active_quote_currency is a partial UNIQUE index scoped to
// non-terminal statuses, so that INSERT can only ever succeed for one
// caller at a time per (quote_id, pay_currency), no matter how tightly two
// requests race -- SQLite enforces it at the storage layer, not this
// function's own control flow.
//
// The claim winner calls NOWPayments and fills the row in via
// fillClaimedPayment. Every other caller gets ErrPaymentSlotClaimed back
// immediately and either:
//   - reuses the winner's row once it's filled in (the common case: a
//     genuinely outstanding payment, or another request's claim that just
//     finished), or
//   - loops back and tries to claim the slot itself, if it has gone free
//     (the row is gone, or has reached a terminal status -- e.g. the
//     winner's NOWPayments call failed and fillClaimedPayment deleted the
//     placeholder), or
//   - pauses briefly and re-checks, if the row is still mid-creation
//     (status == paymentClaimingStatus) by whoever holds it.
//
// At most one goroutine/process ever holds the claim for a given
// (quote_id, pay_currency) pair at any instant, so at most one real
// NOWPayments CreatePayment call is ever in flight for it.
func (s *Server) resolvePayment(ctx context.Context, quote *QuoteRecord, payCurrency, recipient string, now time.Time) (PaymentRecord, error) {
	deadline := time.Now().Add(claimPaymentPollTimeout)
	for {
		placeholder := PaymentRecord{
			ID:          uuid.NewString(),
			QuoteID:     quote.ID,
			Recipient:   recipient,
			Status:      paymentClaimingStatus,
			PayCurrency: payCurrency,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.store.InsertPayment(ctx, placeholder); err == nil {
			// We hold the claim: it is now impossible for any other request
			// to win this same (quote_id, pay_currency) slot until this row
			// reaches a terminal status or is deleted (see
			// fillClaimedPayment).
			return s.fillClaimedPayment(ctx, quote, payCurrency, placeholder)
		} else if !errors.Is(err, ErrPaymentSlotClaimed) {
			return PaymentRecord{}, err
		}

		row, selErr := s.store.GetLatestPaymentForQuoteCurrency(ctx, quote.ID, payCurrency)
		if selErr != nil {
			return PaymentRecord{}, selErr
		}
		switch {
		case row != nil && !isTerminalPaymentStatus(row.Status) && row.Status != paymentClaimingStatus:
			// A completed, still-outstanding payment -- genuine reuse.
			return *row, nil
		case row != nil && row.Status == paymentClaimingStatus:
			// Still being filled in by whoever holds the claim -- nothing
			// to reuse yet, just wait and check again below.
		default:
			// row == nil, or row is terminal: the slot is free (the other
			// claimant's attempt failed and its placeholder was deleted, or
			// -- defensively -- it reached a terminal status some other
			// way). Loop back and try to claim it ourselves.
		}

		if ctx.Err() != nil {
			return PaymentRecord{}, ctx.Err()
		}
		if time.Now().After(deadline) {
			return PaymentRecord{}, errClaimPaymentTimedOut
		}
		select {
		case <-ctx.Done():
			return PaymentRecord{}, ctx.Err()
		case <-time.After(claimPaymentPollInterval):
		}
	}
}

// fillClaimedPayment calls NOWPayments to complete a placeholder row that
// InsertPayment just claimed on this goroutine's behalf. On failure it
// deletes the placeholder so the (quote_id, pay_currency) slot doesn't stay
// wedged behind a dead claim -- a transient NOWPayments error must not
// permanently block that quote+currency pair from ever getting a payment.
func (s *Server) fillClaimedPayment(ctx context.Context, quote *QuoteRecord, payCurrency string, placeholder PaymentRecord) (PaymentRecord, error) {
	npReq := &NowPaymentsPaymentRequest{
		PriceAmount:   quote.TotalFiat,
		PriceCurrency: quote.FiatCurrency,
		PayCurrency:   strings.ToLower(payCurrency),
		OrderID:       placeholder.ID,
		OrderDesc:     fmt.Sprintf("Mint %s %s via %s", quote.AmountToken, quote.MintAsset, payCurrency),
		FixedRate:     true,
		// Same policy as invoice creation: NOWPayments grosses up what the
		// payer is asked to send so the merchant account still receives
		// TotalFiat in full; the treasury never absorbs NOWPayments' own
		// processing fee.
		IsFeePaidByUser: true,
		IpnCallbackURL:  s.ipnCallbackURL,
	}
	payment, err := s.nowPayments.CreatePayment(ctx, npReq)
	if err != nil {
		// Detach from ctx for the cleanup delete -- ctx may already be the
		// reason this call failed (e.g. its deadline), but the slot still
		// needs to be freed regardless of that.
		if delErr := s.store.DeletePayment(context.WithoutCancel(ctx), placeholder.ID); delErr != nil {
			return PaymentRecord{}, fmt.Errorf("create nowpayments payment: %w (also failed to release claimed slot %s: %v)", err, placeholder.ID, delErr)
		}
		return PaymentRecord{}, &nowPaymentsCreateError{err: err}
	}
	status := strings.ToLower(strings.TrimSpace(payment.PaymentStatus))
	if status == "" {
		status = "waiting"
	}
	filled := placeholder
	filled.Status = status
	filled.NowID = payment.NowPaymentsID()
	filled.PayAddress = payment.PayAddress
	filled.PayAmount = string(payment.PayAmount)
	filled.PayinExtraID = payment.PayinExtraID
	// NOWPayments already created a real payment intent at this point (the
	// CreatePayment call above succeeded) -- unlike the create-failure path,
	// there is nothing safe to roll back here: deleting the placeholder
	// would free the (quote_id, pay_currency) slot for a fresh claim while a
	// real, un-cancellable NOWPayments payment still exists unlinked to any
	// local row, which is worse than a stuck slot. A local DB write failing
	// immediately after a successful network call is normally transient, so
	// retry a few times before giving up; if it still fails, still hand the
	// correct address/amount back to this caller (their money isn't
	// stranded) and log loudly -- the pre-existing reconciliation endpoints
	// (handleReconciliationSummary/Export) are the intended way to catch and
	// fix a payment that never made it past "claiming" locally.
	var updateErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
		}
		if updateErr = s.store.UpdatePayment(ctx, filled); updateErr == nil {
			return filled, nil
		}
	}
	log.Printf("payments-gateway: payment %s (nowpayments id %s) created successfully but failed to persist after retries: %v -- requires manual reconciliation", filled.ID, filled.NowID, updateErr)
	return filled, nil
}

func (s *Server) handlePaymentCreate(w http.ResponseWriter, r *http.Request) {
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
	var req PaymentCreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid JSON payload: %w", err), body, nil)
		return
	}
	if err := validatePaymentCreate(req); err != nil {
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
	payCurrency := strings.ToUpper(strings.TrimSpace(req.PayCurrency))

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Server-side re-validation against the merchant's enabled currencies --
	// defense in depth even though the caller is expected to have already
	// checked GET /swap/currencies; never trust client input alone here.
	coins, err := s.nowPayments.ListMerchantCoins(ctx)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	if !currencyEnabled(coins, payCurrency) {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("pay currency %s is not enabled", payCurrency), body, nil)
		return
	}

	// Idempotent reuse + atomic claim: resolvePayment always claims
	// (quote.ID, payCurrency) via a database-level uniqueness guarantee
	// (idx_payments_active_quote_currency) BEFORE calling out to
	// NOWPayments, so a non-terminal payment already outstanding for this
	// exact quote+currency pair -- or another request's in-flight claim for
	// it -- is reused/waited-on instead of a second live NOWPayments
	// payment ever being created for the same intent. A different
	// currency, or a terminal prior attempt, gets a fresh payment. See
	// resolvePayment's own comment for the full mechanism this closes the
	// TOCTOU race with.
	record, err := s.resolvePayment(ctx, quote, payCurrency, req.Recipient, now)
	if err != nil {
		var npErr *nowPaymentsCreateError
		switch {
		case errors.As(err, &npErr):
			// Surface NOWPayments' own rejection reason (e.g. "amountTo is
			// too small") as the user-facing error instead of the
			// log-oriented wrapper string -- but only for NOWPayments' own
			// 400 responses, which is the class it uses for client-input
			// validation failures. A 401/403/429/5xx is an operational
			// problem on our side (bad/expired API key, rate limiting,
			// their outage), not something the customer did wrong --
			// showing NOWPayments' own wording for those (e.g. "Invalid API
			// key") would misattribute our failure to them. Always log the
			// full upstream detail regardless of what's shown to the user,
			// since the simplified body written below is also what gets
			// audited -- an operator diagnosing an incident later still
			// needs the real status/code, even when the customer-facing
			// message is deliberately shortened.
			var apiErr *NowPaymentsAPIError
			if errors.As(npErr.err, &apiErr) {
				log.Printf("payments-gateway: nowpayments payment create rejected: status=%d code=%s message=%s raw=%s", apiErr.HTTPStatus, apiErr.Code, apiErr.Message, apiErr.Raw)
			}
			if errors.As(npErr.err, &apiErr) && apiErr.HTTPStatus == http.StatusBadRequest && apiErr.Message != "" {
				extra := map[string]interface{}{}
				if apiErr.Code != "" {
					extra["providerCode"] = apiErr.Code
				}
				s.writeError(w, r, http.StatusBadGateway, errors.New(apiErr.Message), body, extra)
			} else {
				s.writeError(w, r, http.StatusBadGateway, npErr.err, body, nil)
			}
		case errors.Is(err, errClaimPaymentTimedOut),
			errors.Is(err, context.DeadlineExceeded),
			errors.Is(err, context.Canceled):
			s.writeError(w, r, http.StatusServiceUnavailable, err, body, nil)
		default:
			s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		}
		return
	}
	resp := map[string]string{
		"paymentId":    record.ID,
		"payAddress":   record.PayAddress,
		"payAmount":    record.PayAmount,
		"payCurrency":  record.PayCurrency,
		"payinExtraId": record.PayinExtraID,
	}
	respBody, _ := json.Marshal(resp)
	if err := s.store.SaveIdempotency(r.Context(), key, requestHash, http.StatusOK, respBody); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	s.writeJSONBytes(w, r, http.StatusOK, respBody, body)
}

func (s *Server) handlePaymentGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/payments/")
	if id == r.URL.Path {
		id = strings.TrimPrefix(r.URL.Path, "/swap/payments/")
	}
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, errors.New("payment id required"), nil, nil)
		return
	}
	payment, err := s.store.GetPayment(r.Context(), id)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	if payment == nil {
		s.writeError(w, r, http.StatusNotFound, errors.New("payment not found"), nil, nil)
		return
	}
	resp, err := MarshalPayment(payment)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	s.writeJSONBytes(w, r, http.StatusOK, resp, nil)
}

func (s *Server) handleInvoiceGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/invoices/")
	if id == r.URL.Path {
		id = strings.TrimPrefix(r.URL.Path, "/swap/invoices/")
	}
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

func (s *Server) handleInvoiceList(w http.ResponseWriter, r *http.Request) {
	filter, err := parseInvoiceListFilter(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, nil, nil)
		return
	}
	items, err := s.store.ListInvoiceViews(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	total, err := s.store.CountInvoices(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	rows := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		rows = append(rows, FormatInvoiceView(item))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]interface{}{
		"total": total,
		"items": rows,
	}, nil)
}

func (s *Server) handleReconciliationSummary(w http.ResponseWriter, r *http.Request) {
	filter, err := parseInvoiceListFilter(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, nil, nil)
		return
	}
	filter.Limit = 0
	items, err := s.store.ListInvoiceViews(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	summary, err := SummarizeInvoiceViews(items)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	s.writeJSON(w, r, http.StatusOK, summary, nil)
}

func (s *Server) handleReconciliationExport(w http.ResponseWriter, r *http.Request) {
	filter, err := parseInvoiceListFilter(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, nil, nil)
		return
	}
	filter.Limit = 0
	items, err := s.store.ListInvoiceViews(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		body, err := MarshalInvoiceViews(items)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
			return
		}
		s.writeJSONBytes(w, r, http.StatusOK, body, nil)
	case "csv":
		body, err := MarshalInvoiceViewCSV(items)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="payments-reconciliation.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		s.audit(r.Context(), r, nil, body, http.StatusOK)
	default:
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("unsupported export format %q", format), nil, nil)
	}
}

// authorizeAdmin checks the Authorization header against s.adminToken using
// a constant-time comparison. Deliberately inlined only at the top of
// handleAdminWebhookEvents -- no blanket middleware, no change to any other
// route's (lack of) auth. An unset token fails closed: the route is always
// unauthorized rather than open.
func (s *Server) authorizeAdmin(r *http.Request) bool {
	if len(s.adminToken) == 0 {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := []byte(strings.TrimSpace(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(provided, s.adminToken) == 1
}

func (s *Server) handleAdminWebhookEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeAdmin(r) {
		s.writeError(w, r, http.StatusUnauthorized, errors.New("admin authorization required"), nil, nil)
		return
	}
	filter, err := parseWebhookEventFilter(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err, nil, nil)
		return
	}
	// Fetch one extra row beyond the page size so nextCursor can reflect
	// whether a further page actually has rows, rather than guessing from
	// "this page happened to be full" (which would send the client on one
	// extra round trip to an empty final page every time).
	lookaheadFilter := filter
	if lookaheadFilter.Limit > 0 {
		lookaheadFilter.Limit++
	}
	items, err := s.store.ListWebhookEvents(r.Context(), lookaheadFilter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	countFilter := filter
	countFilter.BeforeID = 0
	total, err := s.store.CountWebhookEvents(r.Context(), countFilter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, nil, nil)
		return
	}
	var nextCursor interface{}
	if filter.Limit > 0 && len(items) > filter.Limit {
		nextCursor = items[filter.Limit-1].ID
		items = items[:filter.Limit]
	}
	rows := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		rows = append(rows, FormatWebhookEvent(item))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]interface{}{
		"total":      total,
		"items":      rows,
		"nextCursor": nextCursor,
	}, nil)
}

func parseWebhookEventFilter(r *http.Request) (WebhookEventFilter, error) {
	query := r.URL.Query()
	filter := WebhookEventFilter{
		EventType:          strings.TrimSpace(query.Get("eventType")),
		InvoiceOrPaymentID: strings.TrimSpace(query.Get("id")),
	}
	if rawVerified := strings.TrimSpace(query.Get("signatureVerified")); rawVerified != "" {
		verified, err := strconv.ParseBool(rawVerified)
		if err != nil {
			return WebhookEventFilter{}, fmt.Errorf("invalid signatureVerified")
		}
		filter.SignatureVerified = &verified
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return WebhookEventFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	} else {
		filter.Limit = 100
	}
	if rawBeforeID := strings.TrimSpace(query.Get("beforeId")); rawBeforeID != "" {
		beforeID, err := strconv.ParseInt(rawBeforeID, 10, 64)
		if err != nil || beforeID < 0 {
			return WebhookEventFilter{}, fmt.Errorf("invalid beforeId")
		}
		filter.BeforeID = beforeID
	}
	parseTime := func(key string) (*time.Time, error) {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			return nil, nil
		}
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s", key)
		}
		utc := ts.UTC()
		return &utc, nil
	}
	var err error
	if filter.ReceivedFrom, err = parseTime("receivedFrom"); err != nil {
		return WebhookEventFilter{}, err
	}
	if filter.ReceivedTo, err = parseTime("receivedTo"); err != nil {
		return WebhookEventFilter{}, err
	}
	return filter, nil
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
	verified := s.verifyHMAC(body, sig)
	s.recordWebhookEvent(r.Context(), body, verified)
	if !verified {
		s.writeError(w, r, http.StatusUnauthorized, errors.New("invalid webhook signature"), body, nil)
		return
	}
	var payload NowPaymentsWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Errorf("invalid webhook payload: %w", err), body, nil)
		return
	}
	// invoice_id and payment_id key two different NOWPayments products
	// (checkout-page invoices vs. headless deposit-address payments); a
	// given IPN body carries exactly one of them depending on which product
	// generated it. Both flows end at the exact same mintWithVoucher call.
	if invoiceNowID := strings.TrimSpace(payload.InvoiceID); invoiceNowID != "" {
		s.handleInvoiceWebhook(w, r, body, invoiceNowID)
		return
	}
	if paymentNowID := strings.TrimSpace(string(payload.PaymentID)); paymentNowID != "" {
		s.handlePaymentWebhook(w, r, body, paymentNowID)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "ignored"}, body)
}

func (s *Server) handleInvoiceWebhook(w http.ResponseWriter, r *http.Request, body []byte, nowID string) {
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
	txHash, voucherHash, err := s.mintWithVoucher(ctx, invoice.ID, invoice.Recipient, quote.Token, quote.AmountToken)
	if err != nil {
		_ = s.store.UpdateInvoiceStatus(r.Context(), invoice.ID, "error", nil)
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	_ = s.store.UpdateInvoiceStatus(r.Context(), invoice.ID, "minted", &txHash)
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "minted", "txHash": txHash, "voucherHash": voucherHash}, body)
}

// handlePaymentWebhook is the headless-payment sibling of
// handleInvoiceWebhook: same signature-verified-by-the-caller entry point,
// same re-fetch-by-ID-before-trusting-the-body defensive pattern, same
// mintWithVoucher call. The only real difference is the settlement check --
// NowPaymentsPayment.Finished() rather than NowPaymentsInvoice.Paid() -- and
// that a "not yet finished" payment records NOWPayments' own granular
// status (waiting/confirming/sending/partially_paid) instead of a single
// generic "processing", since the payment product's status vocabulary is
// richer and the /swap/payments/{id} status route is meant to surface it.
func (s *Server) handlePaymentWebhook(w http.ResponseWriter, r *http.Request, body []byte, nowID string) {
	payment, err := s.store.GetPaymentByNowID(r.Context(), nowID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	if payment == nil {
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "unknown"}, body)
		return
	}
	if strings.EqualFold(payment.Status, "minted") || strings.EqualFold(payment.Status, "settled_zero") {
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "already minted"}, body)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	latest, err := s.nowPayments.GetPayment(ctx, nowID)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	if !latest.Finished() {
		pendingStatus := strings.ToLower(strings.TrimSpace(latest.PaymentStatus))
		if pendingStatus == "" {
			pendingStatus = "pending"
		}
		_ = s.store.UpdatePaymentStatus(r.Context(), payment.ID, pendingStatus, nil)
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "pending"}, body)
		return
	}
	quote, err := s.store.GetQuote(r.Context(), payment.QuoteID)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, body, nil)
		return
	}
	if quote == nil {
		s.writeError(w, r, http.StatusInternalServerError, fmt.Errorf("quote %s missing", payment.QuoteID), body, nil)
		return
	}
	// We mint whatever actually nets to us, never the originally quoted
	// amountToken: QR codes for these currencies can't carry an amount
	// (see cryptoPaymentUri.ts) and most wallets don't support prefilling
	// one for a token transfer even when a scheme could, so buyers
	// routinely type an amount by hand and senders often deduct their own
	// withdrawal fee from it -- exact-match settlement made this the
	// common case, not an edge case. outcome_amount is NOWPayments' own
	// converted/forwarded amount, already net of its conversion and
	// network fees; falling back to actually_paid only covers the
	// unexpected case where NOWPayments omits outcome_amount entirely.
	outcomeAmount := strings.TrimSpace(string(latest.OutcomeAmount))
	if outcomeAmount == "" {
		outcomeAmount = strings.TrimSpace(string(latest.ActuallyPaid))
	}
	txHash, mintAmount, err := s.settlePayment(ctx, payment, quote, outcomeAmount)
	if err != nil {
		if errors.Is(err, ErrMintDuplicate) {
			// The reconciler (see reconciler.go) already settled this exact
			// payment concurrently -- our own attempt was rejected by the
			// chain's replay protection, not a real failure. Leave status
			// alone rather than overwriting whatever it already wrote.
			s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "already minted"}, body)
			return
		}
		_ = s.store.UpdatePaymentStatus(r.Context(), payment.ID, "error", nil)
		s.writeError(w, r, http.StatusBadGateway, err, body, nil)
		return
	}
	var txHashPtr *string
	if txHash != "" {
		txHashPtr = &txHash
	}
	finalStatus := finalPaymentStatus(mintAmount)
	_ = s.store.UpdatePaymentStatus(r.Context(), payment.ID, finalStatus, txHashPtr)
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": finalStatus, "txHash": txHash, "mintAmount": mintAmount}, body)
}

// finalPaymentStatus picks the terminal status a settled payment should
// land on: "settled_zero" when settlePayment determined nothing nets to us
// after fees (a near-dust deposit) -- no on-chain mint occurred, and a
// status of "minted" would misrepresent that to anyone reading the payment
// record later (an operator, a reconciliation script, or the buyer's own
// checkout UI) as a real, successful token transfer. "minted" otherwise.
func finalPaymentStatus(mintAmount string) string {
	if strings.TrimSpace(mintAmount) == "0" {
		return "settled_zero"
	}
	return "minted"
}

// mintWithVoucher signs and submits a mint voucher for an explicit
// token+amount. It is shared verbatim by both the invoice flow (which
// always mints its originally quoted amountToken) and the headless-payment
// flow (which mints whatever the payment actually settles for, via
// settlePayment): onChainID just needs a globally-unique string to key
// on-chain invoice-replay protection (see core.ErrMintInvoiceUsed), and
// both InvoiceRecord.ID and PaymentRecord.ID are drawn from the same
// uuid.NewString() pool, so there is no collision risk between the two
// flows sharing this one field. Note that replay protection is keyed on
// onChainID alone, not amount -- a given onChainID can still only ever
// mint once, so a payment settling in two separate installments needs two
// distinct onChainID values, not two calls with the same one.
//
// amount is a human-readable decimal NHB/ZNHB string (e.g. "14.523456"),
// matching every value this package computes it from (quote.AmountToken,
// settlePayment's mintAmount, both produced by formatRat). This is the one
// place that converts to the on-chain integer representation
// MintVoucher.Amount/core.MintVoucher.AmountBig() actually require ("amount
// in wei" -- docs/otc/voucher.md, matching the wallet's own
// parseUnits(amount, 18)/formatUnits(balance, 18)) -- centralizing it here
// means every caller gets correct scaling for free, and there is exactly
// one place that could get it wrong instead of one per caller.
func (s *Server) mintWithVoucher(ctx context.Context, onChainID, recipient, token, amount string) (string, string, error) {
	weiAmount, err := decimalToWeiString(amount, mintDecimals)
	if err != nil {
		return "", "", err
	}
	voucher := core.MintVoucher{
		InvoiceID: onChainID,
		Recipient: recipient,
		Token:     token,
		Amount:    weiAmount,
		ChainID:   core.MintChainID,
		Expiry:    s.nowFn().Add(mintVoucherTTL).Unix(),
	}
	payload, err := voucher.CanonicalJSON()
	if err != nil {
		return "", "", err
	}
	sig, err := s.signer.Sign(ctx, payload)
	if err != nil {
		return "", "", err
	}
	txHash, err := s.node.MintWithSig(ctx, voucher, hex.EncodeToString(sig))
	if err != nil {
		return "", "", err
	}
	voucherHash, hashErr := core.MintVoucherHash(&voucher, sig)
	if hashErr != nil {
		return "", "", hashErr
	}
	return txHash, voucherHash, nil
}

// settlePayment computes what actually nets to us from a NOWPayments
// outcome amount -- already net of NOWPayments' own conversion/network
// fees -- minus our own service fee (PAY_GATEWAY_SERVICE_FEE_BPS), and
// mints that. Used uniformly by both the webhook path (a payment that
// reached NOWPayments' own "finished" status) and the reconciler (a
// partially_paid payment whose grace window has elapsed with nothing
// further arriving) -- either way we mint whatever landed, never the
// originally quoted amount. A net amount that rounds to zero or below
// (a near-dust deposit once our fee comes out) settles with no on-chain
// mint at all -- returns an empty txHash and "0", not an error, since
// there is genuinely nothing to mint.
func (s *Server) settlePayment(ctx context.Context, payment *PaymentRecord, quote *QuoteRecord, outcomeAmount string) (txHash string, mintAmount string, err error) {
	_, netAmountFiat, err := deductFeeFromAmount(outcomeAmount, s.serviceFeeBps)
	if err != nil {
		return "", "", err
	}
	netRat, ok := new(big.Rat).SetString(netAmountFiat)
	if !ok {
		return "", "", fmt.Errorf("invalid net settlement amount: %s", netAmountFiat)
	}
	if netRat.Sign() <= 0 {
		return "", "0", nil
	}
	mintAmount, err = s.computeMintAmount(quote.MintAsset, netAmountFiat, s.nowFn())
	if err != nil {
		return "", "", err
	}
	txHash, _, err = s.mintWithVoucher(ctx, payment.ID, payment.Recipient, quote.Token, mintAmount)
	if err != nil {
		return "", "", err
	}
	return txHash, mintAmount, nil
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

// recordWebhookEvent persists a fire-and-forget audit row for every inbound
// NOWPayments webhook attempt, valid or not. It does its own independent,
// best-effort JSON parse (separate from the "real" json.Unmarshal used for
// business logic in handleNowPaymentsWebhook) so a garbage or
// signature-failed body still yields a row with blank IDs rather than
// blocking. Errors are swallowed, mirroring s.audit()'s existing
// "_ = s.store.InsertAudit(...)" convention -- this must never fail or
// delay the actual webhook response.
func (s *Server) recordWebhookEvent(ctx context.Context, body []byte, verified bool) {
	if s.store == nil {
		return
	}
	var payload NowPaymentsWebhookPayload
	_ = json.Unmarshal(body, &payload)
	invoiceID := strings.TrimSpace(payload.InvoiceID)
	paymentID := strings.TrimSpace(string(payload.PaymentID))
	eventType := "unrecognized"
	switch {
	case invoiceID != "":
		eventType = "invoice"
	case paymentID != "":
		eventType = "payment"
	}
	now := s.nowFn().UTC()
	_ = s.store.InsertWebhookEvent(ctx, WebhookEventRecord{
		ReceivedAt:        now,
		EventType:         eventType,
		InvoiceID:         invoiceID,
		PaymentID:         paymentID,
		OrderID:           strings.TrimSpace(payload.OrderID),
		PaymentStatus:     strings.TrimSpace(firstNonEmpty(payload.PaymentStatus, payload.Status)),
		SignatureVerified: verified,
		RawPayload:        body,
		CreatedAt:         now,
	})
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

func parseInvoiceListFilter(r *http.Request) (InvoiceListFilter, error) {
	query := r.URL.Query()
	filter := InvoiceListFilter{
		Status:    strings.TrimSpace(query.Get("status")),
		Recipient: strings.TrimSpace(query.Get("recipient")),
	}
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return InvoiceListFilter{}, fmt.Errorf("invalid limit")
		}
		filter.Limit = limit
	} else {
		filter.Limit = 100
	}
	parseTime := func(key string) (*time.Time, error) {
		raw := strings.TrimSpace(query.Get(key))
		if raw == "" {
			return nil, nil
		}
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s", key)
		}
		utc := ts.UTC()
		return &utc, nil
	}
	var err error
	if filter.CreatedFrom, err = parseTime("created_from"); err != nil {
		return InvoiceListFilter{}, err
	}
	if filter.CreatedTo, err = parseTime("created_to"); err != nil {
		return InvoiceListFilter{}, err
	}
	if filter.UpdatedFrom, err = parseTime("updated_from"); err != nil {
		return InvoiceListFilter{}, err
	}
	if filter.UpdatedTo, err = parseTime("updated_to"); err != nil {
		return InvoiceListFilter{}, err
	}
	return filter, nil
}
