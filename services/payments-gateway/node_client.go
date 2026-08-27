package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core"
	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
)

// NodeClient exposes the minimal RPC surface required by the payments gateway.
type NodeClient interface {
	MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error)

	// ListPendingRedemptions returns every currently-pending NHB redemption
	// (swap-out) request via the swap_listPendingRedemptions RPC method --
	// see core/node.go's Node.ListPendingRedemptions and
	// rpc/swap_redemption_handlers.go for the chain-side implementation this
	// mirrors.
	ListPendingRedemptions(ctx context.Context) ([]RedemptionRequest, error)

	// SendAttestRedemption builds, signs (using the dedicated redemption
	// attestor key configured via InitAttestor -- never the mint signer),
	// and submits a standard V3 TxTypeAttestRedemption transaction reporting
	// a redemption's off-chain payout outcome back on-chain. status must be
	// "paid" or "failed"; payoutReference is required (and failureReason
	// ignored) when status is "paid", and vice versa for "failed" -- see
	// core/state_transition.go's applyAttestRedemption for the authoritative
	// validation this mirrors.
	SendAttestRedemption(ctx context.Context, requestID, status, payoutReference, failureReason string) (txHash string, err error)

	// GetTransactionReceipt reports whether txHash has landed in a block yet
	// (via nhb_getTransactionReceipt). Used to confirm an attestation
	// transaction actually committed before the local redemption row is
	// marked fully done.
	GetTransactionReceipt(ctx context.Context, txHash string) (confirmed bool, err error)
}

// RedemptionRequest mirrors the JSON shape
// rpc/swap_redemption_handlers.go's formatRedemptionRequest produces for
// swap_listPendingRedemptions (which itself formats
// nhbstate.StoredRedemptionRequest). Account is the redeemer's NHB address
// in its normal bech32 string form; DestinationAddress is the raw payout
// address the redeemer supplied on-chain, validated by this service (see
// isValidTRC20Address in redeem_watcher.go), never by the chain.
type RedemptionRequest struct {
	RequestID          string `json:"requestId"`
	Account            string `json:"account"`
	NHBAmountWei       string `json:"nhbAmountWei"`
	DestinationAsset   string `json:"destinationAsset"`
	DestinationAddress string `json:"destinationAddress"`
	Status             string `json:"status"`
	CreatedAt          int64  `json:"createdAt"`
}

// attestRedemptionGasLimit/attestRedemptionGasPrice match
// core/redeem_nhb_test.go's attestRedemptionTx helper exactly -- the
// chain-side test fixture for what a valid TxTypeAttestRedemption
// transaction looks like.
var (
	attestRedemptionGasLimit = uint64(25_000)
	attestRedemptionGasPrice = big.NewInt(1)
)

// ErrMintDuplicate indicates the node rejected a mint voucher because its
// InvoiceID (the payments-gateway's onChainID) already minted successfully
// -- the on-chain replay protection the whole system relies on for
// idempotency, surfaced as JSON-RPC error code -32010 (rpc.codeDuplicateTx
// / core.ErrMintInvoiceUsed on the node side). Callers that might race a
// second settlement attempt against the same payment (e.g. the
// reconciler sweeping a payment a webhook is concurrently minting) should
// treat this as "already handled", not a real failure -- see
// errors.Is(err, ErrMintDuplicate).
var ErrMintDuplicate = errors.New("payments-gateway: mint voucher already settled")

// rpcCodeDuplicateTx mirrors rpc.codeDuplicateTx (rpc/http.go) -- the two
// packages don't share an import here, so the numeric value is duplicated
// deliberately rather than pulling in the rpc package's much larger surface
// for one constant.
const rpcCodeDuplicateTx = -32010

// RPCNodeClient is a lightweight JSON-RPC client.
type RPCNodeClient struct {
	baseURL   string
	authToken string
	http      *http.Client
	nextID    atomic.Int64

	// attestorMu guards the fields below, all set once by InitAttestor and
	// then read/mutated only by SendAttestRedemption. A mutex here is
	// defensive belt-and-braces, not a substitute for RedeemWatcher's own
	// single-writer design (see redeem_watcher.go's RedeemWatcher doc
	// comment) -- concurrent SendAttestRedemption calls would still each
	// consume a distinct nonce without racing, but the watcher must still
	// never actually call this concurrently, since two attestations for the
	// same request submitted out of order could confuse (though never
	// corrupt) on-chain state.
	attestorMu      sync.Mutex
	attestorKey     *nhbcrypto.PrivateKey
	attestorAddress string
	attestorNonce   uint64
	attestorReady   bool
}

// NewRPCNodeClient constructs a new RPC client.
func NewRPCNodeClient(baseURL, authToken string) *RPCNodeClient {
	return &RPCNodeClient{
		baseURL:   baseURL,
		authToken: authToken,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *RPCNodeClient) MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error) {
	params := []interface{}{voucher, signature}
	var result struct {
		TxHash string `json:"txHash"`
	}
	if err := c.call(ctx, "mint_with_sig", params, &result); err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == rpcCodeDuplicateTx {
			return "", fmt.Errorf("%w: %s", ErrMintDuplicate, rpcErr.Message)
		}
		return "", err
	}
	return result.TxHash, nil
}

// InitAttestor configures the dedicated redemption-attestor signing key and
// fetches its current on-chain nonce once via nhb_getBalance. Must be called
// exactly once, before RedeemWatcher.Run's ticker loop starts --
// SendAttestRedemption increments the cached nonce locally on every
// successful submission rather than refetching it, which is only safe under
// the watcher's single-writer, never-concurrent design.
func (c *RPCNodeClient) InitAttestor(ctx context.Context, key *nhbcrypto.PrivateKey) error {
	if key == nil {
		return fmt.Errorf("attestor key required")
	}
	address := key.PubKey().Address().String()
	var resp struct {
		Nonce uint64 `json:"nonce"`
	}
	if err := c.call(ctx, "nhb_getBalance", []interface{}{address}, &resp); err != nil {
		return fmt.Errorf("fetch attestor nonce: %w", err)
	}
	c.attestorMu.Lock()
	defer c.attestorMu.Unlock()
	c.attestorKey = key
	c.attestorAddress = address
	c.attestorNonce = resp.Nonce
	c.attestorReady = true
	return nil
}

// AttestorAddress returns the configured attestor's NHB address, or "" if
// InitAttestor has not been called yet. Used for startup logging/funding
// checks (see the plan's "fund and monitor the attestor's gas balance"
// operational note).
func (c *RPCNodeClient) AttestorAddress() string {
	c.attestorMu.Lock()
	defer c.attestorMu.Unlock()
	return c.attestorAddress
}

// ListPendingRedemptions calls swap_listPendingRedemptions, gated by the
// same requireAuthInto bearer-JWT auth as nhb_sendTransaction/mint_with_sig
// (see rpc/http.go's dispatch switch) -- reusing c.authToken via call(), not
// a separate auth scheme.
func (c *RPCNodeClient) ListPendingRedemptions(ctx context.Context) ([]RedemptionRequest, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "swap_listPendingRedemptions", []interface{}{}, &raw); err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	// rpc/swap_redemption_handlers.go's handleSwapListPendingRedemptions
	// wraps its response as {"requests": [...]}; the bare-array branch below
	// is defensive only, in case that ever changes.
	if trimmed[0] == '[' {
		var arr []RedemptionRequest
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("decode swap_listPendingRedemptions array response: %w", err)
		}
		return arr, nil
	}
	var wrapper struct {
		Requests []RedemptionRequest `json:"requests"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, fmt.Errorf("decode swap_listPendingRedemptions response: %w", err)
	}
	return wrapper.Requests, nil
}

// attestRedemptionPayload is the RLP-encoded tx.Data payload
// TxTypeAttestRedemption expects. Field order is load-bearing: RLP encodes
// struct fields positionally, and this must match
// core/state_transition.go's applyAttestRedemption decode struct exactly
// (RequestID, Status, PayoutReference, FailureReason -- see
// core/redeem_nhb_test.go's attestRedemptionTx helper for the chain-side
// fixture this mirrors).
type attestRedemptionPayload struct {
	RequestID       string
	Status          string
	PayoutReference string
	FailureReason   string
}

// SendAttestRedemption builds, signs, and submits a standard V3
// TxTypeAttestRedemption transaction. See the NodeClient interface doc
// comment for status/payoutReference/failureReason semantics.
func (c *RPCNodeClient) SendAttestRedemption(ctx context.Context, requestID, status, payoutReference, failureReason string) (string, error) {
	c.attestorMu.Lock()
	defer c.attestorMu.Unlock()
	if !c.attestorReady || c.attestorKey == nil {
		return "", fmt.Errorf("redemption attestor not configured -- call InitAttestor first")
	}
	payload := attestRedemptionPayload{
		RequestID:       requestID,
		Status:          status,
		PayoutReference: payoutReference,
		FailureReason:   failureReason,
	}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return "", fmt.Errorf("encode attestation payload: %w", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeAttestRedemption,
		Nonce:    c.attestorNonce,
		GasLimit: attestRedemptionGasLimit,
		GasPrice: new(big.Int).Set(attestRedemptionGasPrice),
		Data:     data,
	}
	if err := tx.Sign(c.attestorKey.PrivateKey); err != nil {
		return "", fmt.Errorf("sign attestation: %w", err)
	}
	var txHash string
	if err := c.call(ctx, "nhb_sendTransaction", []interface{}{tx}, &txHash); err != nil {
		return "", err
	}
	// Only advance the cached nonce after a successful submission -- an
	// error above (including one caused by an unexpectedly stale nonce)
	// leaves it unchanged, so the next attempt reuses the same value rather
	// than skipping ahead over a transaction that never actually landed.
	c.attestorNonce++
	return txHash, nil
}

// GetTransactionReceipt reports whether txHash has a receipt yet (i.e. has
// landed in a block). nhb_getTransactionReceipt returns a JSON null result
// for a transaction it doesn't know about (see rpc/http.go's
// handleGetTransactionReceipt), which this treats as "not confirmed yet",
// not an error.
func (c *RPCNodeClient) GetTransactionReceipt(ctx context.Context, txHash string) (bool, error) {
	var raw json.RawMessage
	if err := c.call(ctx, "nhb_getTransactionReceipt", []interface{}{txHash}, &raw); err != nil {
		return false, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false, nil
	}
	return true, nil
}

// rpcError carries the JSON-RPC error's code alongside its message so
// callers that need to distinguish error classes (see ErrMintDuplicate)
// aren't reduced to string-matching call()'s flattened error text.
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("node rpc error: %s", e.Message)
}

func (c *RPCNodeClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.nextID.Add(1)
	bodyStruct := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	buf, err := json.Marshal(bodyStruct)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.authToken) != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node rpc %s failed: status=%d", method, resp.StatusCode)
	}
	var rpcResp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return &rpcError{Code: rpcResp.Error.Code, Message: rpcResp.Error.Message}
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return fmt.Errorf("node rpc returned empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}
