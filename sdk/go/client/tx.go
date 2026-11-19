package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
)

const (
	jsonRPCVersion     = "2.0"
	defaultRPCID       = 1
	defaultGasLimit    = uint64(25_000)
	defaultGasPriceStr = "1"
)

var defaultGasPrice = func() *big.Int {
	v, _ := new(big.Int).SetString(defaultGasPriceStr, 10)
	return v
}()

// Client wraps a JSON-RPC endpoint and exposes helpers for crafting transfer transactions.
type Client struct {
	endpoint   string
	httpClient *http.Client
	authToken  string
	// chainID is set only by WithChainID: an id the caller chose. Otherwise a transaction carries the id the node
	// reports (nhb_getNetworkStats, field txChainId), read through chainIDs; there is no built-in default (PL-R5T-29).
	chainID    *big.Int
	chainIDs   *txchainid.Reader
	chainIDTTL time.Duration
	gasLimit   uint64
	gasPrice   *big.Int
}

// Option configures the high-level client defaults.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client used for RPC calls.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// WithAuthToken sets the bearer token attached to privileged RPC requests.
func WithAuthToken(token string) Option {
	return func(c *Client) {
		c.authToken = strings.TrimSpace(token)
	}
}

// WithChainID makes every constructed transaction carry chainID and stops the client asking the node. It is an
// explicit choice by the caller (for a node older than the release that reports the id, or a test): without it the
// client reads the id the node reports and refuses to sign when the node does not report one. A nil value is ignored.
func WithChainID(chainID *big.Int) Option {
	return func(c *Client) {
		if chainID != nil {
			c.chainID = new(big.Int).Set(chainID)
		}
	}
}

// WithChainIDExpiry sets how long an id read from the node is reused (default txchainid.DefaultTTL, 15 seconds). The
// node reports the legacy constant until the chain's activation height and the network's own id from it, so the
// answer must not be kept for long.
func WithChainIDExpiry(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.chainIDTTL = d
		}
	}
}

// WithGasLimit configures the default gas limit applied to constructed transfers.
func WithGasLimit(limit uint64) Option {
	return func(c *Client) {
		c.gasLimit = limit
	}
}

// WithGasPrice configures the default gas price attached to constructed transfers.
func WithGasPrice(price *big.Int) Option {
	return func(c *Client) {
		if price != nil {
			c.gasPrice = new(big.Int).Set(price)
		}
	}
}

// New initialises a client bound to the provided JSON-RPC endpoint.
func New(endpoint string, opts ...Option) (*Client, error) {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil, fmt.Errorf("client: endpoint required")
	}
	c := &Client{
		endpoint:   trimmed,
		httpClient: http.DefaultClient,
		gasLimit:   defaultGasLimit,
		gasPrice:   new(big.Int).Set(defaultGasPrice),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.httpClient == nil {
		c.httpClient = http.DefaultClient
	}
	c.chainIDs = txchainid.NewReader(c.fetchTxChainID, c.chainIDTTL)
	if c.gasLimit == 0 {
		c.gasLimit = defaultGasLimit
	}
	if c.gasPrice == nil || c.gasPrice.Sign() <= 0 {
		c.gasPrice = new(big.Int).Set(defaultGasPrice)
	}
	return c, nil
}

// TxOption customises individual transactions created by the helper methods.
type TxOption func(*types.Transaction)

// TxWithGasLimit overrides the gas limit for a single transfer request.
func TxWithGasLimit(limit uint64) TxOption {
	return func(tx *types.Transaction) {
		if tx != nil {
			tx.GasLimit = limit
		}
	}
}

// TxWithGasPrice overrides the gas price for a single transfer request.
func TxWithGasPrice(price *big.Int) TxOption {
	return func(tx *types.Transaction) {
		if tx != nil && price != nil {
			tx.GasPrice = new(big.Int).Set(price)
		}
	}
}

// SendZNHBTransfer builds, signs, and submits a ZapNHB transfer derived from the provided
// private key and recipient address. The helper automatically fetches the latest account
// nonce, applies the configured gas settings, signs the payload, and broadcasts it through
// the configured JSON-RPC endpoint.
func (c *Client) SendZNHBTransfer(ctx context.Context, key *crypto.PrivateKey, recipient string, amount *big.Int, opts ...TxOption) (*types.Transaction, string, error) {
	if c == nil {
		return nil, "", fmt.Errorf("client: instance required")
	}
	tx, err := c.prepareTransferTx(ctx, key, recipient, amount, types.TxTypeTransferZNHB, opts...)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		return nil, "", fmt.Errorf("client: sign transaction: %w", err)
	}
	result, err := c.submit(ctx, tx)
	if err != nil {
		return nil, "", err
	}
	return tx, result, nil
}

// SendNHBTransfer builds, signs, and submits a native NHB transfer derived from the provided
// private key and recipient address. It mirrors the ZapNHB helper but emits a TypeTransfer
// transaction for the native asset.
func (c *Client) SendNHBTransfer(ctx context.Context, key *crypto.PrivateKey, recipient string, amount *big.Int, opts ...TxOption) (*types.Transaction, string, error) {
	if c == nil {
		return nil, "", fmt.Errorf("client: instance required")
	}
	tx, err := c.prepareTransferTx(ctx, key, recipient, amount, types.TxTypeTransfer, opts...)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		return nil, "", fmt.Errorf("client: sign transaction: %w", err)
	}
	result, err := c.submit(ctx, tx)
	if err != nil {
		return nil, "", err
	}
	return tx, result, nil
}

func (c *Client) prepareTransferTx(ctx context.Context, key *crypto.PrivateKey, recipient string, amount *big.Int, txType types.TxType, opts ...TxOption) (*types.Transaction, error) {
	if key == nil || key.PrivateKey == nil {
		return nil, fmt.Errorf("client: signing key required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("client: amount must be greater than zero")
	}
	dest, err := crypto.DecodeAddress(recipient)
	if err != nil {
		return nil, fmt.Errorf("client: decode recipient: %w", err)
	}
	sender := key.PubKey()
	if sender == nil || sender.PublicKey == nil {
		return nil, fmt.Errorf("client: derive sender address: public key unavailable")
	}
	chainID, err := c.TxChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("client: transaction chain id: %w", err)
	}
	nonce, err := c.fetchNonce(ctx, sender.Address().String())
	if err != nil {
		return nil, err
	}
	gasPrice := c.gasPrice
	if gasPrice == nil {
		gasPrice = defaultGasPrice
	}
	tx := &types.Transaction{
		ChainID:  chainID,
		Type:     txType,
		Nonce:    nonce,
		To:       dest.Bytes(),
		Value:    new(big.Int).Set(amount),
		GasLimit: c.gasLimit,
		GasPrice: new(big.Int).Set(gasPrice),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(tx)
		}
	}
	if tx.GasLimit == 0 {
		return nil, fmt.Errorf("client: gas limit must be greater than zero")
	}
	if tx.GasPrice == nil || tx.GasPrice.Sign() <= 0 {
		return nil, fmt.Errorf("client: gas price must be greater than zero")
	}
	return tx, nil
}

// TxChainID returns the chain id a transaction signed now must carry: the id the caller chose with WithChainID, or
// else the one the node reports as txChainId in the public nhb_getNetworkStats call, reused for a short time. It
// returns an error, never a default, when the node cannot be asked or reports no usable id. Callers that build
// transactions with the builders in sdk/lending pass it as their chain id argument.
func (c *Client) TxChainID(ctx context.Context) (*big.Int, error) {
	if c == nil {
		return nil, fmt.Errorf("client: instance required: %w", txchainid.ErrUnavailable)
	}
	if c.chainID != nil {
		return new(big.Int).Set(c.chainID), nil
	}
	return c.chainIDs.ChainID(ctx)
}

// fetchTxChainID is the read behind TxChainID. The call is public, so it is made without the bearer token.
func (c *Client) fetchTxChainID(ctx context.Context) (*big.Int, error) {
	var result json.RawMessage
	if err := c.call(ctx, txchainid.Method, []interface{}{}, false, &result); err != nil {
		return nil, err
	}
	return txchainid.Parse(result)
}

func (c *Client) fetchNonce(ctx context.Context, address string) (uint64, error) {
	return c.AccountNonce(ctx, address)
}

// AccountNonce fetches the current nonce for address. Exported so callers
// building transactions for services other than this client's own transfer
// helpers (e.g. sdk/lending's signed-tx builders) can reuse the same lookup.
func (c *Client) AccountNonce(ctx context.Context, address string) (uint64, error) {
	var resp balanceResponse
	if err := c.call(ctx, "nhb_getBalance", []interface{}{address}, false, &resp); err != nil {
		return 0, err
	}
	return resp.Nonce, nil
}

func (c *Client) submit(ctx context.Context, tx *types.Transaction) (string, error) {
	var resp string
	if err := c.call(ctx, "nhb_sendTransaction", []interface{}{tx}, true, &resp); err != nil {
		return "", err
	}
	return resp, nil
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc,omitempty"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type balanceResponse struct {
	Nonce uint64 `json:"nonce"`
}

func (c *Client) call(ctx context.Context, method string, params []interface{}, requireAuth bool, out interface{}) error {
	if requireAuth && strings.TrimSpace(c.authToken) == "" {
		return fmt.Errorf("client: auth token required for %s", method)
	}
	payload := rpcRequest{
		JSONRPC: jsonRPCVersion,
		ID:      defaultRPCID,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("client: encode rpc payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("client: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if requireAuth {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("client: rpc call failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("client: rpc error status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("client: decode rpc response: %w", err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("client: rpc error %d: %s", decoded.Error.Code, decoded.Error.Message)
	}
	if out == nil || len(decoded.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(decoded.Result, out); err != nil {
		return fmt.Errorf("client: decode rpc result: %w", err)
	}
	return nil
}
