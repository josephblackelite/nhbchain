package oracleattesterd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	bbolt "go.etcd.io/bbolt"
	"gopkg.in/yaml.v3"

	nhbcrypto "nhbchain/crypto"
	"nhbchain/observability"
	swapv1 "nhbchain/proto/swap/v1"
	cons "nhbchain/sdk/consensus"
)

const (
	headerNowPaymentsSignature    = "X-Nowpayments-Signature"
	headerNowPaymentsSignatureAlt = "x-nowpayments-sig"
	providerNowPayments           = "NOWPAYMENTS"
	maxWebhookBodyBytes           = 1 << 20
)

// Config captures the runtime options for the attestation service.
type Config struct {
	ListenAddress     string        `yaml:"listen"`
	ConsensusEndpoint string        `yaml:"consensus"`
	ChainID           string        `yaml:"chain_id"`
	SignerKey         string        `yaml:"signer_key"`
	SignerKeyFile     string        `yaml:"signer_key_file"`
	SignerKeyEnv      string        `yaml:"signer_key_env"`
	NonceStart        uint64        `yaml:"nonce_start"`
	Authority         string        `yaml:"authority"`
	TreasuryAccount   string        `yaml:"treasury_account"`
	CollectorAddress  string        `yaml:"collector"`
	Confirmations     uint64        `yaml:"confirmations"`
	NowPaymentsSecret string        `yaml:"nowpayments_secret"`
	DatabasePath      string        `yaml:"database"`
	Provider          string        `yaml:"provider"`
	RequestTimeout    time.Duration `yaml:"-"`
	RequestTimeoutSec int           `yaml:"request_timeout_seconds"`
	Fee               FeeConfig     `yaml:"fee"`
	Assets            []AssetConfig `yaml:"assets"`
	EVM               EVMConfig     `yaml:"evm"`
}

// FeeConfig describes optional fee metadata attached to consensus submissions.
type FeeConfig struct {
	Amount string `yaml:"amount"`
	Denom  string `yaml:"denom"`
	Payer  string `yaml:"payer"`
}

// AssetConfig binds a stable asset symbol to its ERC-20 representation.
type AssetConfig struct {
	Symbol   string `yaml:"symbol"`
	Address  string `yaml:"address"`
	Decimals int    `yaml:"decimals"`
}

// EVMConfig describes the RPC endpoint used for settlement verification.
type EVMConfig struct {
	RPCURL string `yaml:"rpc_url"`
}

// LoadConfig reads configuration from disk and applies defaults.
func LoadConfig(path string) (Config, error) {
	cfg := Config{
		ListenAddress:     ":8085",
		NonceStart:        1,
		Confirmations:     6,
		Provider:          providerNowPayments,
		DatabasePath:      filepath.Join(os.TempDir(), "oracle-attestations.db"),
		RequestTimeoutSec: 15,
	}
	if strings.TrimSpace(path) == "" {
		return cfg, fmt.Errorf("config path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.ListenAddress = strings.TrimSpace(cfg.ListenAddress)
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":8085"
	}
	cfg.ConsensusEndpoint = strings.TrimSpace(cfg.ConsensusEndpoint)
	if cfg.ConsensusEndpoint == "" {
		return Config{}, fmt.Errorf("consensus endpoint required")
	}
	cfg.ChainID = strings.TrimSpace(cfg.ChainID)
	if cfg.ChainID == "" {
		return Config{}, fmt.Errorf("chain_id required")
	}
	cfg.SignerKey = strings.TrimSpace(cfg.SignerKey)
	cfg.SignerKeyEnv = strings.TrimSpace(cfg.SignerKeyEnv)
	cfg.SignerKeyFile = strings.TrimSpace(cfg.SignerKeyFile)
	if cfg.SignerKey == "" {
		switch {
		case cfg.SignerKeyEnv != "":
			value := strings.TrimSpace(os.Getenv(cfg.SignerKeyEnv))
			if value == "" {
				return Config{}, fmt.Errorf("signer_key_env %s is empty", cfg.SignerKeyEnv)
			}
			cfg.SignerKey = value
		case cfg.SignerKeyFile != "":
			contents, err := os.ReadFile(cfg.SignerKeyFile)
			if err != nil {
				return Config{}, fmt.Errorf("read signer_key_file: %w", err)
			}
			cfg.SignerKey = strings.TrimSpace(string(contents))
		default:
			return Config{}, fmt.Errorf("signer_key required")
		}
	}
	cfg.Authority = strings.TrimSpace(cfg.Authority)
	if cfg.Authority == "" {
		return Config{}, fmt.Errorf("authority required")
	}
	cfg.TreasuryAccount = strings.TrimSpace(cfg.TreasuryAccount)
	if cfg.TreasuryAccount == "" {
		return Config{}, fmt.Errorf("treasury_account required")
	}
	cfg.CollectorAddress = strings.TrimSpace(cfg.CollectorAddress)
	if cfg.CollectorAddress == "" {
		return Config{}, fmt.Errorf("collector required")
	}
	cfg.NowPaymentsSecret = strings.TrimSpace(cfg.NowPaymentsSecret)
	if cfg.NowPaymentsSecret == "" {
		return Config{}, fmt.Errorf("nowpayments_secret required")
	}
	cfg.DatabasePath = strings.TrimSpace(cfg.DatabasePath)
	if cfg.DatabasePath == "" {
		return Config{}, fmt.Errorf("database path required")
	}
	if cfg.NonceStart == 0 {
		cfg.NonceStart = 1
	}
	if cfg.Confirmations > 128 {
		cfg.Confirmations = 128
	}
	if cfg.Provider = strings.TrimSpace(cfg.Provider); cfg.Provider == "" {
		cfg.Provider = providerNowPayments
	}
	if cfg.RequestTimeoutSec <= 0 {
		cfg.RequestTimeoutSec = 15
	}
	cfg.RequestTimeout = time.Duration(cfg.RequestTimeoutSec) * time.Second
	if cfg.EVM.RPCURL = strings.TrimSpace(cfg.EVM.RPCURL); cfg.EVM.RPCURL == "" {
		return Config{}, fmt.Errorf("evm.rpc_url required")
	}
	if len(cfg.Assets) == 0 {
		return Config{}, fmt.Errorf("at least one asset must be configured")
	}
	return cfg, nil
}

// Asset describes a supported settlement asset.
type Asset struct {
	Symbol   string
	Address  common.Address
	Decimals int
}

// Server handles webhook ingestion and attestation submission.
type Server struct {
	secret        string
	provider      string
	authority     string
	treasury      string
	collector     common.Address
	confirmations uint64
	nonceStart    uint64
	timeout       time.Duration
	assets        map[string]Asset
	store         *InvoiceStore
	verifier      SettlementVerifier
	submitter     VoucherSubmitter
	fee           FeeConfig
	clock         func() time.Time
	metrics       *observability.OracleAttesterdMetrics
}

// NewServer wires the HTTP handler with its dependencies.
func NewServer(cfg Config, store *InvoiceStore, verifier SettlementVerifier, submitter VoucherSubmitter) (*Server, error) {
	if store == nil {
		return nil, fmt.Errorf("invoice store required")
	}
	if verifier == nil {
		return nil, fmt.Errorf("settlement verifier required")
	}
	if submitter == nil {
		return nil, fmt.Errorf("consensus submitter required")
	}
	collector := common.HexToAddress(cfg.CollectorAddress)
	if collector == (common.Address{}) {
		return nil, fmt.Errorf("collector address invalid")
	}
	assets := make(map[string]Asset, len(cfg.Assets))
	for _, raw := range cfg.Assets {
		symbol := strings.ToUpper(strings.TrimSpace(raw.Symbol))
		if symbol == "" {
			return nil, fmt.Errorf("asset symbol required")
		}
		addr := common.HexToAddress(strings.TrimSpace(raw.Address))
		if addr == (common.Address{}) {
			return nil, fmt.Errorf("asset %s address invalid", symbol)
		}
		if raw.Decimals <= 0 || raw.Decimals > 36 {
			return nil, fmt.Errorf("asset %s decimals invalid", symbol)
		}
		assets[symbol] = Asset{Symbol: symbol, Address: addr, Decimals: raw.Decimals}
	}
	server := &Server{
		secret:        cfg.NowPaymentsSecret,
		provider:      cfg.Provider,
		authority:     cfg.Authority,
		treasury:      cfg.TreasuryAccount,
		collector:     collector,
		confirmations: cfg.Confirmations,
		nonceStart:    cfg.NonceStart,
		timeout:       cfg.RequestTimeout,
		assets:        assets,
		store:         store,
		verifier:      verifier,
		submitter:     submitter,
		fee:           cfg.Fee,
		clock:         time.Now,
		metrics:       observability.OracleAttesterd(),
	}
	if server.timeout <= 0 {
		server.timeout = 15 * time.Second
	}
	return server, nil
}

// ServeHTTP dispatches supported endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	case "/np/webhook":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleNowPayments(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleNowPayments(w http.ResponseWriter, r *http.Request) {
	reader := http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	body, err := io.ReadAll(reader)
	_ = r.Body.Close()
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("read webhook: %w", err))
		return
	}
	signature := strings.TrimSpace(r.Header.Get(headerNowPaymentsSignature))
	if signature == "" {
		signature = strings.TrimSpace(r.Header.Get(headerNowPaymentsSignatureAlt))
	}
	if !VerifyIPNHMAC(s.secret, body, signature) {
		s.writeError(w, http.StatusUnauthorized, errors.New("invalid webhook signature"))
		return
	}
	event, err := parseNowPaymentsPayload(body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	assetCfg, ok := s.assets[event.Asset]
	if !ok {
		s.writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("unsupported asset %s", event.Asset))
		return
	}
	if event.TokenAddress != (common.Address{}) && event.TokenAddress != assetCfg.Address {
		s.writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("token address mismatch for %s", event.Asset))
		return
	}
	amountWei, err := convertStableAmount(event.Amount, assetCfg.Decimals)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err)
		return
	}
	if amountWei.Sign() <= 0 {
		s.writeError(w, http.StatusBadRequest, fmt.Errorf("amount must be positive"))
		return
	}
	state, err := s.store.Reserve(event.InvoiceID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err)
		return
	}
	switch state {
	case InvoiceStateMinted:
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "minted"})
		return
	case InvoiceStatePending:
		s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	var processErr error
	defer func() {
		if processErr != nil {
			if err := s.store.Release(event.InvoiceID); err != nil {
				log.Printf("release invoice %s: %v", event.InvoiceID, err)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(r.Context(), s.timeout)
	defer cancel()
	if err := s.verifier.Confirm(ctx, event.TxHash, assetCfg, s.collector, amountWei, s.confirmations); err != nil {
		processErr = err
		s.writeError(w, http.StatusConflict, fmt.Errorf("settlement verification failed: %w", err))
		return
	}
	nonce, err := s.store.ReserveNonce(s.nonceStart)
	if err != nil {
		processErr = err
		s.writeError(w, http.StatusInternalServerError, fmt.Errorf("reserve nonce: %w", err))
		return
	}
	createdAt := event.CreatedAt.Unix()
	if createdAt < 0 {
		createdAt = 0
	}
	voucher := &swapv1.DepositVoucher{
		InvoiceId:    event.InvoiceID,
		Provider:     s.provider,
		StableAsset:  assetCfg.Symbol,
		StableAmount: amountWei.String(),
		NhbAmount:    amountWei.String(),
		Account:      s.treasury,
		Memo:         event.TxHash.Hex(),
		CreatedAt:    createdAt,
	}
	msg := &swapv1.MsgMintDepositVoucher{Authority: s.authority, Voucher: voucher}
	if err := s.submitter.Submit(ctx, msg, nonce); err != nil {
		processErr = err
		s.writeError(w, http.StatusBadGateway, fmt.Errorf("submit voucher: %w", err))
		return
	}
	if err := s.store.MarkMinted(event.InvoiceID); err != nil {
		processErr = err
		s.writeError(w, http.StatusInternalServerError, fmt.Errorf("mark minted: %w", err))
		return
	}
	if s.metrics != nil {
		s.metrics.RecordVoucherMint(assetCfg.Symbol)
		age := s.clock().Sub(event.CreatedAt)
		if age < 0 {
			age = 0
		}
		s.metrics.RecordFreshness(assetCfg.Symbol, age)
	}
	slog.InfoContext(r.Context(), "voucher minted",
		slog.String("invoice_id", event.InvoiceID),
		slog.String("asset", assetCfg.Symbol),
		slog.String("tx_hash", event.TxHash.Hex()),
	)
	processErr = nil
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "minted", "invoiceId": event.InvoiceID})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, err error) {
	if err == nil {
		w.WriteHeader(status)
		return
	}
	log.Printf("webhook error: %v", err)
	s.writeJSON(w, status, map[string]string{"error": err.Error()})
}

// Store exposes the backing invoice store for testing and diagnostics.
func (s *Server) Store() *InvoiceStore {
	if s == nil {
		return nil
	}
	return s.store
}

// parseNowPaymentsPayload validates and normalises the incoming webhook payload.
func parseNowPaymentsPayload(body []byte) (*npEvent, error) {
	var payload npWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	invoice := strings.TrimSpace(firstNonEmpty(payload.InvoiceID, payload.OrderID))
	if invoice == "" {
		return nil, fmt.Errorf("invoice_id required")
	}
	status := strings.ToLower(strings.TrimSpace(payload.PaymentStatus))
	switch status {
	case "finished", "confirmed", "confirming", "completed":
	default:
		return nil, fmt.Errorf("payment status %q not settled", payload.PaymentStatus)
	}
	amount := strings.TrimSpace(firstNonEmpty(payload.ActuallyPaid, payload.PayAmount))
	if amount == "" {
		return nil, fmt.Errorf("amount missing")
	}
	asset := strings.ToUpper(strings.TrimSpace(payload.PayCurrency))
	if asset == "" {
		return nil, fmt.Errorf("pay_currency required")
	}
	txHashStr := strings.TrimSpace(firstNonEmpty(
		payload.TransactionHash,
		payload.PaymentDetails.TxHash,
		payload.PaymentDetails.PayoutTxID,
	))
	if txHashStr == "" {
		return nil, fmt.Errorf("transaction hash missing")
	}
	txHash := common.HexToHash(txHashStr)
	if txHash == (common.Hash{}) {
		return nil, fmt.Errorf("invalid transaction hash")
	}
	createdAt := parseTimestamp(firstNonEmpty(payload.UpdatedAt, payload.CreatedAt))
	event := &npEvent{
		InvoiceID:    invoice,
		Asset:        asset,
		Amount:       amount,
		TxHash:       txHash,
		CreatedAt:    createdAt,
		TokenAddress: common.HexToAddress(strings.TrimSpace(payload.PaymentDetails.TokenAddress)),
	}
	return event, nil
}

// npWebhookPayload mirrors the NowPayments webhook schema subset we consume.
type npWebhookPayload struct {
	InvoiceID        string            `json:"invoice_id"`
	OrderID          string            `json:"order_id"`
	PaymentStatus    string            `json:"payment_status"`
	PayAmount        string            `json:"pay_amount"`
	ActuallyPaid     string            `json:"actually_paid"`
	PayCurrency      string            `json:"pay_currency"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	TransactionHash  string            `json:"transaction_hash"`
	PaymentDetails   npPaymentDetails  `json:"payment_details"`
	AdditionalFields map[string]string `json:"additional_data"`
}

type npPaymentDetails struct {
	TxHash       string `json:"tx_hash"`
	PayoutTxID   string `json:"payout_txid"`
	TokenAddress string `json:"token_address"`
}

type npEvent struct {
	InvoiceID    string
	Asset        string
	Amount       string
	TxHash       common.Hash
	TokenAddress common.Address
	CreatedAt    time.Time
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseTimestamp(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Now().UTC()
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return ts.UTC()
	}
	return time.Now().UTC()
}

func convertStableAmount(amount string, decimals int) (*big.Int, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required")
	}
	rat := new(big.Rat)
	if _, ok := rat.SetString(trimmed); !ok {
		return nil, fmt.Errorf("invalid decimal amount %q", amount)
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	rat.Mul(rat, scale)
	if !rat.IsInt() {
		return nil, fmt.Errorf("amount requires precision beyond %d decimals", decimals)
	}
	return rat.Num(), nil
}

// InvoiceState represents the reservation status stored for an invoice.
type InvoiceState int

const (
	// InvoiceStateNew indicates the invoice was newly reserved in this request.
	InvoiceStateNew InvoiceState = iota
	// InvoiceStatePending indicates the invoice is already being processed.
	InvoiceStatePending
	// InvoiceStateMinted indicates the invoice has been finalised.
	InvoiceStateMinted
)

// InvoiceStore persists invoice processing state and the next nonce counter.
type InvoiceStore struct {
	db *bbolt.DB
}

var (
	bucketInvoices = []byte("invoices")
	bucketMeta     = []byte("meta")
	keyNonce       = []byte("next_nonce")
)

// NewInvoiceStore opens (or creates) the persistence database.
func NewInvoiceStore(path string) (*InvoiceStore, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketInvoices); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketMeta); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &InvoiceStore{db: db}, nil
}

// Close releases the underlying database handle.
func (s *InvoiceStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Reserve marks an invoice as pending and returns its prior state.
func (s *InvoiceStore) Reserve(invoiceID string) (InvoiceState, error) {
	if s == nil || s.db == nil {
		return InvoiceStatePending, fmt.Errorf("invoice store not initialised")
	}
	trimmed := strings.TrimSpace(invoiceID)
	if trimmed == "" {
		return InvoiceStatePending, fmt.Errorf("invoice id required")
	}
	var state InvoiceState
	err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketInvoices)
		key := []byte(trimmed)
		existing := bucket.Get(key)
		if existing == nil {
			if err := bucket.Put(key, []byte("pending")); err != nil {
				return err
			}
			state = InvoiceStateNew
			return nil
		}
		switch string(existing) {
		case "pending":
			state = InvoiceStatePending
		case "minted":
			state = InvoiceStateMinted
		default:
			state = InvoiceStatePending
		}
		return nil
	})
	if err != nil {
		return InvoiceStatePending, err
	}
	return state, nil
}

// MarkMinted records the invoice as completed.
func (s *InvoiceStore) MarkMinted(invoiceID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("invoice store not initialised")
	}
	trimmed := strings.TrimSpace(invoiceID)
	if trimmed == "" {
		return fmt.Errorf("invoice id required")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketInvoices)
		key := []byte(trimmed)
		if bucket.Get(key) == nil {
			return fmt.Errorf("invoice %s not reserved", trimmed)
		}
		return bucket.Put(key, []byte("minted"))
	})
}

// Release removes the reservation (allowing a retry).
func (s *InvoiceStore) Release(invoiceID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("invoice store not initialised")
	}
	trimmed := strings.TrimSpace(invoiceID)
	if trimmed == "" {
		return fmt.Errorf("invoice id required")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketInvoices)
		key := []byte(trimmed)
		if val := bucket.Get(key); val != nil && string(val) == "pending" {
			return bucket.Delete(key)
		}
		return nil
	})
}

// ReserveNonce returns the next nonce and increments the persisted counter.
func (s *InvoiceStore) ReserveNonce(start uint64) (uint64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("invoice store not initialised")
	}
	if start == 0 {
		start = 1
	}
	var nonce uint64
	err := s.db.Update(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketMeta)
		raw := bucket.Get(keyNonce)
		if raw == nil {
			nonce = start
		} else {
			nonce = bytesToUint64(raw)
		}
		next := make([]byte, 8)
		putUint64(next, nonce+1)
		return bucket.Put(keyNonce, next)
	})
	if err != nil {
		return 0, err
	}
	return nonce, nil
}

func bytesToUint64(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

func putUint64(buf []byte, value uint64) {
	if len(buf) < 8 {
		return
	}
	buf[0] = byte(value >> 56)
	buf[1] = byte(value >> 48)
	buf[2] = byte(value >> 40)
	buf[3] = byte(value >> 32)
	buf[4] = byte(value >> 24)
	buf[5] = byte(value >> 16)
	buf[6] = byte(value >> 8)
	buf[7] = byte(value)
}

// VoucherSubmitter submits consensus transactions for minting vouchers.
type VoucherSubmitter interface {
	Submit(ctx context.Context, msg *swapv1.MsgMintDepositVoucher, nonce uint64) error
}

// ConsensusSubmitter signs and submits transactions via the consensus SDK.
type ConsensusSubmitter struct {
	Client  *cons.Client
	Signer  *nhbcrypto.PrivateKey
	ChainID string
	Fee     FeeConfig
}

func (c *ConsensusSubmitter) Submit(ctx context.Context, msg *swapv1.MsgMintDepositVoucher, nonce uint64) error {
	if c == nil {
		return fmt.Errorf("consensus submitter not initialised")
	}
	if msg == nil {
		return fmt.Errorf("message required")
	}
	envelope, err := cons.NewTx(msg, nonce, c.ChainID, c.Fee.Amount, c.Fee.Denom, c.Fee.Payer, "")
	if err != nil {
		return fmt.Errorf("build envelope: %w", err)
	}
	_, err = cons.Submit(ctx, c.Client, envelope, nil, c.Signer)
	return err
}

// VerifyIPNHMAC mirrors the swap-gateway implementation to validate webhook integrity.
func VerifyIPNHMAC(secret string, body []byte, provided string) bool {
	if strings.TrimSpace(secret) == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	cleaned := strings.TrimSpace(strings.ToLower(provided))
	cleaned = strings.TrimPrefix(cleaned, "0x")
	if cleaned == "" {
		return false
	}
	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, decoded)
}
