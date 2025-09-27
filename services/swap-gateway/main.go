package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
)

type orderStatus string

const (
	orderStatusPending       orderStatus = "PENDING"
	orderStatusPaid          orderStatus = "PAID"
	orderStatusMintSubmitted orderStatus = "MINT_SUBMITTED"
	orderStatusMinted        orderStatus = "MINTED"
)

type order struct {
	OrderID    string      `json:"orderId"`
	Reference  string      `json:"reference"`
	Fiat       string      `json:"fiat"`
	AmountFiat string      `json:"amountFiat"`
	Recipient  string      `json:"recipient"`
	Rate       string      `json:"rate"`
	AmountWei  string      `json:"amountWei"`
	PayURL     string      `json:"payUrl"`
	Status     orderStatus `json:"status"`
	MintedWei  string      `json:"minted"`
	TxRef      string      `json:"txRef"`
}

type orderStore struct {
	mu     sync.RWMutex
	orders map[string]*order
}

func newOrderStore() *orderStore {
	return &orderStore{orders: make(map[string]*order)}
}

func (s *orderStore) get(id string) (*order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ord, ok := s.orders[id]
	if !ok {
		return nil, false
	}
	dup := *ord
	return &dup, true
}

func (s *orderStore) createOrGet(ord *order) (*order, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.orders[ord.Reference]
	if ok {
		if existing.AmountFiat != ord.AmountFiat || existing.Fiat != ord.Fiat || existing.Recipient != ord.Recipient {
			return nil, false, fmt.Errorf("order reference %s already exists with different details", ord.Reference)
		}
		dup := *existing
		return &dup, true, nil
	}
	s.orders[ord.Reference] = ord
	dup := *ord
	return &dup, false, nil
}

func (s *orderStore) update(id string, fn func(*order) error) (*order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.orders[id]
	if !ok {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if err := fn(existing); err != nil {
		return nil, err
	}
	dup := *existing
	return &dup, nil
}

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("swap-gateway", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "swap-gateway",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		log.Fatalf("init telemetry: %v", err)
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	if err := run(); err != nil {
		log.Fatalf("swap gateway failed: %v", err)
	}
}

func run() error {
	port := getEnv("SWAP_PORT", "8090")
	nodeURL := getEnv("SWAP_NODE_RPC_URL", "http://127.0.0.1:8545")
	chainIDStr := getEnv("SWAP_CHAIN_ID", "187001")
	hmacSecret := os.Getenv("SWAP_PAYMENT_HMAC_SECRET")
	priceSource := getEnv("SWAP_PRICE_SOURCE", "fixed:0.10")
	minterAddr := getEnv("MINTER_ZNHB_ADDRESS", "")
	minterPriv := getEnv("MINTER_ZNHB_PRIVKEY", "")

	if minterAddr == "" || minterPriv == "" {
		return errors.New("MINTER_ZNHB_ADDRESS and MINTER_ZNHB_PRIVKEY must be set")
	}

	chainID, err := strconv.ParseInt(chainIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid SWAP_CHAIN_ID: %w", err)
	}

	quoter, err := NewQuoter(priceSource)
	if err != nil {
		return fmt.Errorf("init quoter: %w", err)
	}

	store := newOrderStore()
	client := NewNodeClient(nodeURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/swap/quote", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleQuote(w, r, quoter)
	})
	mux.HandleFunc("/swap/order", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleOrder(w, r, quoter, store)
	})
	mux.HandleFunc("/webhooks/payment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handlePaymentWebhook(w, r, store, quoter, client, chainID, minterAddr, minterPriv, hmacSecret)
	})
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		orderID := strings.TrimPrefix(r.URL.Path, "/orders/")
		if orderID == "" {
			http.Error(w, "orderId required", http.StatusBadRequest)
			return
		}
		handleGetOrder(w, r, store, orderID)
	})

	handler := otelhttp.NewHandler(loggingMiddleware(mux), "swap-gateway")
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
	}

	log.Printf("swap gateway listening on %s", srv.Addr)
	return srv.ListenAndServe()
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

type quoteRequest struct {
	Fiat       string `json:"fiat"`
	AmountFiat string `json:"amountFiat"`
}

type quoteResponse struct {
	Fiat       string `json:"fiat"`
	AmountFiat string `json:"amountFiat"`
	Rate       string `json:"rate"`
	AmountWei  string `json:"znHB"`
}

func handleQuote(w http.ResponseWriter, r *http.Request, quoter *Quoter) {
	var req quoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Fiat == "" || req.AmountFiat == "" {
		http.Error(w, "fiat and amountFiat required", http.StatusBadRequest)
		return
	}
	rate, amountWei, err := quoter.Quote(r.Context(), req.Fiat, req.AmountFiat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, quoteResponse{Fiat: req.Fiat, AmountFiat: req.AmountFiat, Rate: rate, AmountWei: amountWei})
}

type orderRequest struct {
	Fiat       string `json:"fiat"`
	AmountFiat string `json:"amountFiat"`
	Recipient  string `json:"recipient"`
	Reference  string `json:"reference"`
}

type orderResponse struct {
	OrderID   string `json:"orderId"`
	PayURL    string `json:"payUrl"`
	Expected  string `json:"expected"`
	Recipient string `json:"recipient"`
	Fiat      string `json:"fiat"`
	AmountWei string `json:"amountWei"`
	Rate      string `json:"rate"`
}

func handleOrder(w http.ResponseWriter, r *http.Request, quoter *Quoter, store *orderStore) {
	var req orderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Fiat == "" || req.AmountFiat == "" || req.Recipient == "" || req.Reference == "" {
		http.Error(w, "fiat, amountFiat, recipient, reference required", http.StatusBadRequest)
		return
	}
	if err := validateRecipient(req.Recipient); err != nil {
		http.Error(w, fmt.Sprintf("invalid recipient: %v", err), http.StatusBadRequest)
		return
	}

	rate, amountWei, err := quoter.Quote(r.Context(), req.Fiat, req.AmountFiat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ord := &order{
		OrderID:    req.Reference,
		Reference:  req.Reference,
		Fiat:       req.Fiat,
		AmountFiat: req.AmountFiat,
		Recipient:  req.Recipient,
		Rate:       rate,
		AmountWei:  amountWei,
		PayURL:     fmt.Sprintf("https://pay.dev/checkout/%s", req.Reference),
		Status:     orderStatusPending,
	}

	stored, existed, err := store.createOrGet(ord)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !existed {
		log.Printf("created order %s for %s %s", stored.OrderID, stored.Fiat, stored.AmountFiat)
	}

	resp := orderResponse{
		OrderID:   stored.OrderID,
		PayURL:    stored.PayURL,
		Expected:  stored.AmountFiat,
		Recipient: stored.Recipient,
		Fiat:      stored.Fiat,
		AmountWei: stored.AmountWei,
		Rate:      stored.Rate,
	}
	writeJSON(w, resp)
}

func validateRecipient(recipient string) error {
	if recipient == "" {
		return errors.New("recipient empty")
	}
	_, err := decodeRecipient(recipient)
	return err
}

type paymentWebhook struct {
	OrderID    string `json:"orderId"`
	Fiat       string `json:"fiat"`
	AmountFiat string `json:"amountFiat"`
	Paid       bool   `json:"paid"`
	TxRef      string `json:"txRef"`
}

type webhookResponse struct {
	OK        bool   `json:"ok"`
	Submitted bool   `json:"submitted"`
	Minted    string `json:"minted"`
}

func handlePaymentWebhook(w http.ResponseWriter, r *http.Request, store *orderStore, quoter *Quoter, client VoucherSubmitter, chainID int64, minterAddr, minterPriv, hmacSecret string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if hmacSecret != "" {
		sig := r.Header.Get("X-HMAC")
		if !VerifyIPNHMAC(hmacSecret, body, sig) {
			http.Error(w, "invalid HMAC", http.StatusUnauthorized)
			return
		}
	}

	var payload paymentWebhook
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if payload.OrderID == "" {
		http.Error(w, "orderId required", http.StatusBadRequest)
		return
	}

	if !payload.Paid {
		http.Error(w, "payment not completed", http.StatusBadRequest)
		return
	}

	ord, err := store.update(payload.OrderID, func(o *order) error {
		if o.Status != orderStatusPending && o.Status != orderStatusPaid {
			return fmt.Errorf("order %s already processed", o.OrderID)
		}
		if payload.Fiat != "" && !strings.EqualFold(payload.Fiat, o.Fiat) {
			return fmt.Errorf("fiat mismatch: %s != %s", payload.Fiat, o.Fiat)
		}
		if payload.AmountFiat != "" && payload.AmountFiat != o.AmountFiat {
			return fmt.Errorf("amount mismatch: %s != %s", payload.AmountFiat, o.AmountFiat)
		}
		o.Status = orderStatusPaid
		o.TxRef = payload.TxRef
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mintedWei := ord.AmountWei
	rate := ord.Rate

	nonce, err := randomHex(16)
	if err != nil {
		http.Error(w, "failed to create nonce", http.StatusInternalServerError)
		return
	}
	expiry := time.Now().Add(15 * time.Minute).Unix()

	voucher := VoucherV1{
		Domain:     "NHB_SWAP_VOUCHER_V1",
		ChainID:    chainID,
		Token:      "ZNHB",
		Recipient:  ord.Recipient,
		Amount:     mintedWei,
		Fiat:       ord.Fiat,
		FiatAmount: ord.AmountFiat,
		Rate:       rate,
		OrderID:    ord.OrderID,
		Nonce:      nonce,
		Expiry:     expiry,
	}

	sigBytes, err := SignVoucher(voucher, minterPriv)
	if err != nil {
		http.Error(w, fmt.Sprintf("sign voucher: %v", err), http.StatusInternalServerError)
		return
	}
	signerAddr, err := RecoverVoucherSignerAddress(voucher, sigBytes)
	if err != nil {
		http.Error(w, fmt.Sprintf("recover signer: %v", err), http.StatusInternalServerError)
		return
	}
	if !strings.EqualFold(signerAddr, minterAddr) {
		http.Error(w, "signer mismatch", http.StatusInternalServerError)
		return
	}
	sigHex := "0x" + hex.EncodeToString(sigBytes)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := client.SubmitVoucher(ctx, voucher, sigHex); err != nil {
		http.Error(w, fmt.Sprintf("submit voucher: %v", err), http.StatusBadGateway)
		return
	}

	if _, err := store.update(ord.OrderID, func(o *order) error {
		o.Status = orderStatusMintSubmitted
		o.MintedWei = mintedWei
		return nil
	}); err != nil {
		log.Printf("warn: failed to update order after submit: %v", err)
	}

	writeJSON(w, webhookResponse{OK: true, Submitted: true, Minted: mintedWei})
}

func handleGetOrder(w http.ResponseWriter, r *http.Request, store *orderStore, orderID string) {
	ord, ok := store.get(orderID)
	if !ok {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}
	writeJSON(w, ord)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response error: %v", err)
	}
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func decodeRecipient(recipient string) ([]byte, error) {
	addr, err := decodeBech32Address(recipient)
	if err != nil {
		return nil, err
	}
	return addr, nil
}

// decodeBech32Address is defined in voucher.go to avoid duplication.
