package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
)

// NodeClient is a thin JSON-RPC client used by the gateway.
//
// EscrowCreate/Release/Refund/Dispute take an already-verified participant
// signature (payload, signature) rather than a plain caller string --
// escrow_create/release/refund/dispute/resolve were disabled chain-side
// (see rpc/escrow_handlers.go's escrowRPCDisabledMessage) because they
// mutated validator state directly outside the block pipeline, guaranteeing
// a consensus fork on this 2-validator chain. The replacement is a real
// signed transaction (TxTypeDelegated{Create,Release,Refund,Dispute}Escrow,
// core/types/transaction.go) that this gateway submits using its own
// relayer key -- but authorization for the underlying action still comes
// entirely from the participant's own signature, embedded in the
// transaction's payload and verified on-chain
// (native/escrow/engine.go's *WithSignature methods), never from the
// gateway's identity. See docs/escrow/nhbchain-escrow-gateway.md for the
// full signing contract client integrators now need to follow.
type NodeClient interface {
	EscrowCreate(ctx context.Context, payload, signature []byte) (*EscrowCreateResponse, error)
	EscrowGet(ctx context.Context, id string) (*EscrowState, error)
	EscrowGetRealm(ctx context.Context, id string) (*EscrowRealm, error)
	EscrowRelease(ctx context.Context, payload, signature []byte) error
	EscrowRefund(ctx context.Context, payload, signature []byte) error
	EscrowDispute(ctx context.Context, payload, signature []byte) error
	// EscrowResolve relays a realm arbitration-committee decision on-chain
	// via TxTypeArbitrateRelease/Refund (native/escrow/engine.go's
	// ResolveWithSignatures, core/state_transition.go's applyArbitrate) --
	// mirrors EscrowRelease/Refund/Dispute's relayed-signature model
	// exactly, except the embedded authorization is a quorum of arbiter
	// signatures (verified against the escrow's FrozenArb committee) rather
	// than a single participant's. decision is the exact raw JSON bytes an
	// arbiter signed (`{escrowId, outcome, policyNonce, metadata?}`,
	// verbatim -- never re-marshaled, since even a whitespace/key-order
	// difference would hash differently than what was actually signed);
	// signatures is each committee member's 65-byte secp256k1 signature
	// over keccak256(decision). The gateway does not pre-verify the quorum
	// itself (native/escrow's decision-envelope/quorum helpers are
	// unexported, and duplicating that logic here risks a subtle
	// divergence from the chain's own authoritative check) -- it relays
	// as-is and surfaces whatever the chain returns, exactly like the
	// other delegated actions. Requires the escrow to have been created
	// against a registered arbitration realm (TxTypeEscrowCreateRealm,
	// RoleEscrowRealmAdmin-gated) with a matching PolicyNonce -- an escrow
	// with no realm attached fails with a clear on-chain error ("missing
	// frozen arbitrator policy"), not a gateway-side placeholder.
	EscrowResolve(ctx context.Context, escrowID string, decision []byte, signatures [][]byte) error
	// P2PCreateTrade is permanently retired -- the bilateral OTC trade flow
	// it fronted (p2p_createTrade et al.) has no signed-transaction
	// replacement and is superseded by the P2P ZNHB market (native/market,
	// live since 2026-08-24), which already does atomic seller-escrows/
	// buyer-pays swaps through real signed transactions. Always returns
	// ErrP2PTradeRetired.
	P2PCreateTrade(ctx context.Context, req P2PAcceptRequest) (*P2PAcceptResponse, error)
	P2PGetTrade(ctx context.Context, tradeID string) (*P2PTradeState, error)
	FetchEvents(ctx context.Context, afterSeq int64, limit int) ([]NodeEvent, error)
	// RelayerBalance returns the configured relayer's current NHB balance
	// (wei), so main.go's periodic low-balance check has something to poll --
	// this relayer pays gas for every transaction the gateway submits, and
	// nothing else previously monitored whether it was running low. Returns
	// ErrRelayerNotConfigured if InitRelayer has not been called yet.
	RelayerBalance(ctx context.Context) (*big.Int, error)
}

// ErrP2PTradeRetired is returned by P2PCreateTrade -- see the NodeClient
// interface doc comment.
var ErrP2PTradeRetired = errors.New("escrow-gateway: p2p trade creation is permanently retired -- use the P2P ZNHB market instead")

// ErrRelayerNotConfigured is returned by every mutating escrow call when
// InitRelayer has not been called (or failed) -- read endpoints (EscrowGet,
// EscrowGetRealm, P2PGetTrade, FetchEvents) remain unaffected.
var ErrRelayerNotConfigured = errors.New("escrow-gateway: relayer key not configured -- mutating escrow endpoints are unavailable")

// escrowRelayerGasLimit/GasPrice are fixed, matching
// services/payments-gateway/node_client.go's attestRedemptionGasLimit/Price
// convention for a service-operated relayer key.
var (
	escrowRelayerGasLimit = uint64(30_000)
	escrowRelayerGasPrice = big.NewInt(1)
)

// RPCNodeClient implements NodeClient against the nhb JSON-RPC server.
type RPCNodeClient struct {
	baseURL   string
	authToken string
	http      *http.Client
	nextID    atomic.Int64

	// relayerMu serializes every transaction this client submits --
	// unlike payments-gateway's redemption watcher (single-writer by
	// design), escrow-gateway's mutating endpoints can be hit by multiple
	// concurrent HTTP requests, and a blockchain account's nonce sequence
	// is inherently sequential, so submission itself must be serialized
	// regardless of how many requests arrive concurrently.
	relayerMu      sync.Mutex
	relayerKey     *nhbcrypto.PrivateKey
	relayerAddress string
	relayerNonce   uint64
	relayerReady   bool
}

func NewRPCNodeClient(baseURL, authToken string) *RPCNodeClient {
	return &RPCNodeClient{
		baseURL:   baseURL,
		authToken: authToken,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// InitRelayer configures the gateway's own signing key and fetches its
// current on-chain nonce once via nhb_getBalance. Must be called before any
// mutating escrow endpoint is served -- see RPCNodeClient.relayerMu's doc
// comment for why the nonce is then only ever advanced under that mutex.
func (c *RPCNodeClient) InitRelayer(ctx context.Context, key *nhbcrypto.PrivateKey) error {
	if key == nil {
		return fmt.Errorf("relayer key required")
	}
	address := key.PubKey().Address().String()
	var resp struct {
		Nonce uint64 `json:"nonce"`
	}
	if err := c.call(ctx, "nhb_getBalance", []interface{}{address}, &resp); err != nil {
		return fmt.Errorf("fetch relayer nonce: %w", err)
	}
	c.relayerMu.Lock()
	defer c.relayerMu.Unlock()
	c.relayerKey = key
	c.relayerAddress = address
	c.relayerNonce = resp.Nonce
	c.relayerReady = true
	return nil
}

// RelayerAddress returns the configured relayer's NHB address, or "" if
// InitRelayer has not been called yet. Used for startup logging/funding
// checks -- like any transaction sender, the relayer needs its own NHB gas
// balance.
func (c *RPCNodeClient) RelayerAddress() string {
	c.relayerMu.Lock()
	defer c.relayerMu.Unlock()
	return c.relayerAddress
}

// RelayerBalance queries the relayer's current NHB balance via
// nhb_getBalance -- the same RPC call InitRelayer already uses for the
// nonce, just read for its balanceNHB field instead. Does not hold
// relayerMu across the network call (only to read the address), matching
// every other read-only method on this client.
func (c *RPCNodeClient) RelayerBalance(ctx context.Context) (*big.Int, error) {
	c.relayerMu.Lock()
	address := c.relayerAddress
	ready := c.relayerReady
	c.relayerMu.Unlock()
	if !ready || address == "" {
		return nil, ErrRelayerNotConfigured
	}
	var resp struct {
		BalanceNHB *big.Int `json:"balanceNHB"`
	}
	if err := c.call(ctx, "nhb_getBalance", []interface{}{address}, &resp); err != nil {
		return nil, fmt.Errorf("fetch relayer balance: %w", err)
	}
	if resp.BalanceNHB == nil {
		return big.NewInt(0), nil
	}
	return resp.BalanceNHB, nil
}

// submitEscrowTx builds, signs (with the relayer key), and submits a
// transaction of the given type carrying data as its payload. Every
// TxTypeDelegated*Escrow call funnels through this one method so nonce
// allocation stays correctly serialized under relayerMu.
func (c *RPCNodeClient) submitEscrowTx(ctx context.Context, txType types.TxType, data []byte) (string, error) {
	c.relayerMu.Lock()
	defer c.relayerMu.Unlock()
	if !c.relayerReady || c.relayerKey == nil {
		return "", ErrRelayerNotConfigured
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    c.relayerNonce,
		GasLimit: escrowRelayerGasLimit,
		GasPrice: new(big.Int).Set(escrowRelayerGasPrice),
		Data:     data,
	}
	if err := tx.Sign(c.relayerKey.PrivateKey); err != nil {
		return "", fmt.Errorf("sign transaction: %w", err)
	}
	var txHash string
	if err := c.call(ctx, "nhb_sendTransaction", []interface{}{tx}, &txHash); err != nil {
		return "", err
	}
	// Only advance the cached nonce after a successful submission -- a
	// failed call (rejected by the node before it ever reached the
	// mempool) must not burn a nonce slot, mirroring
	// SendAttestRedemption's exact same reasoning.
	c.relayerNonce++
	return txHash, nil
}

type delegatedCreateEscrowPayload struct {
	Payload   []byte
	Signature []byte
}

type delegatedEscrowActionPayload struct {
	EscrowID  string
	Payload   []byte
	Signature []byte
}

// escrowCreateEnvelopeFields is the subset of a signed create envelope
// (native/escrow/engine.go's escrowCreateEnvelope) this client needs to
// decode back out of payload in order to compute the resulting escrow ID
// itself -- nhb_sendTransaction only ever returns a bare txHash, never the
// derived escrow ID, so the gateway must derive it the same deterministic
// way the engine does (see native/escrow/engine.go's Create: id =
// keccak256(payer || payee || metaHash || nonce-as-8-byte-big-endian)).
type escrowCreateEnvelopeFields struct {
	Payer string `json:"payer"`
	Payee string `json:"payee"`
	Nonce uint64 `json:"nonce"`
	Meta  string `json:"meta,omitempty"`
}

func (c *RPCNodeClient) EscrowCreate(ctx context.Context, payload, signature []byte) (*EscrowCreateResponse, error) {
	var fields escrowCreateEnvelopeFields
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("decode create envelope: %w", err)
	}
	// Payer/Payee are hex in the envelope, not bech32 -- matching
	// native/escrow's escrowCreateEnvelope convention (see
	// server.go's escrowCreateEnvelopeWire, which is what actually
	// produces this payload: hex.EncodeToString(payerAddr.Bytes())).
	payerBytes, err := decodeHex(strings.TrimSpace(fields.Payer))
	if err != nil {
		return nil, fmt.Errorf("decode create envelope payer: %w", err)
	}
	payeeBytes, err := decodeHex(strings.TrimSpace(fields.Payee))
	if err != nil {
		return nil, fmt.Errorf("decode create envelope payee: %w", err)
	}
	if len(payerBytes) != 20 {
		return nil, fmt.Errorf("create envelope payer must be 20 bytes")
	}
	if len(payeeBytes) != 20 {
		return nil, fmt.Errorf("create envelope payee must be 20 bytes")
	}
	var meta [32]byte
	if trimmed := strings.TrimSpace(fields.Meta); trimmed != "" {
		metaBytes, err := decodeHex(trimmed)
		if err != nil {
			return nil, fmt.Errorf("decode create envelope meta: %w", err)
		}
		if len(metaBytes) > len(meta) {
			return nil, fmt.Errorf("create envelope meta must be <= 32 bytes")
		}
		copy(meta[:], metaBytes)
	}
	if fields.Nonce == 0 {
		return nil, fmt.Errorf("create envelope nonce must be positive")
	}
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], fields.Nonce)
	id := ethcrypto.Keccak256Hash(payerBytes, payeeBytes, meta[:], nonceBuf[:])

	data, err := rlp.EncodeToBytes(delegatedCreateEscrowPayload{Payload: payload, Signature: signature})
	if err != nil {
		return nil, fmt.Errorf("encode delegated create payload: %w", err)
	}
	if _, err := c.submitEscrowTx(ctx, types.TxTypeDelegatedCreateEscrow, data); err != nil {
		return nil, err
	}
	return &EscrowCreateResponse{ID: "0x" + hex.EncodeToString(id[:])}, nil
}

func (c *RPCNodeClient) EscrowGet(ctx context.Context, id string) (*EscrowState, error) {
	var result EscrowState
	err := c.call(ctx, "escrow_get", []interface{}{map[string]string{"id": id}}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) EscrowGetRealm(ctx context.Context, id string) (*EscrowRealm, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, errors.New("realm id required")
	}
	var result EscrowRealm
	if err := c.call(ctx, "escrow_getRealm", []interface{}{map[string]string{"id": trimmed}}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) escrowDelegatedAction(ctx context.Context, txType types.TxType, escrowID string, payload, signature []byte) error {
	data, err := rlp.EncodeToBytes(delegatedEscrowActionPayload{
		EscrowID:  escrowID,
		Payload:   payload,
		Signature: signature,
	})
	if err != nil {
		return fmt.Errorf("encode delegated action payload: %w", err)
	}
	_, err = c.submitEscrowTx(ctx, txType, data)
	return err
}

func (c *RPCNodeClient) EscrowRelease(ctx context.Context, payload, signature []byte) error {
	escrowID, err := escrowIDFromActionEnvelope(payload)
	if err != nil {
		return err
	}
	return c.escrowDelegatedAction(ctx, types.TxTypeDelegatedReleaseEscrow, escrowID, payload, signature)
}

func (c *RPCNodeClient) EscrowRefund(ctx context.Context, payload, signature []byte) error {
	escrowID, err := escrowIDFromActionEnvelope(payload)
	if err != nil {
		return err
	}
	return c.escrowDelegatedAction(ctx, types.TxTypeDelegatedRefundEscrow, escrowID, payload, signature)
}

func (c *RPCNodeClient) EscrowDispute(ctx context.Context, payload, signature []byte) error {
	escrowID, err := escrowIDFromActionEnvelope(payload)
	if err != nil {
		return err
	}
	return c.escrowDelegatedAction(ctx, types.TxTypeDelegatedDisputeEscrow, escrowID, payload, signature)
}

func escrowIDFromActionEnvelope(payload []byte) (string, error) {
	var fields struct {
		EscrowID string `json:"escrowId"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return "", fmt.Errorf("decode action envelope: %w", err)
	}
	trimmed := strings.TrimSpace(fields.EscrowID)
	if trimmed == "" {
		return "", fmt.Errorf("action envelope escrowId required")
	}
	return trimmed, nil
}

// arbitrateEscrowPayload mirrors native/escrow's decode target inside
// applyArbitrate (core/state_transition.go) field-for-field -- that struct
// is RLP-decoded there (positional, not by its json tags), so field order
// and type here must match exactly. Signatures are hex strings, matching
// applyArbitrate's own hex.DecodeString(strings.TrimPrefix(sig, "0x")) loop.
type arbitrateEscrowPayload struct {
	EscrowID   string
	Decision   []byte
	Signatures []string
}

// arbitrateTxTypeForOutcome picks TxTypeArbitrateRelease/Refund to match the
// decision's own outcome field. applyArbitrate's dispatch actually ignores
// which of the two tx types was used (both call the same
// ResolveWithSignatures, which derives the real outcome from the signed
// decision payload itself) -- this is purely so the tx type visible on
// explorers/audit logs stays meaningful rather than defaulting to one
// constant regardless of what happened.
func arbitrateTxTypeForOutcome(decision []byte) (types.TxType, error) {
	var envelope struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(decision, &envelope); err != nil {
		return 0, fmt.Errorf("decode decision outcome: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Outcome)) {
	case "release":
		return types.TxTypeArbitrateRelease, nil
	case "refund":
		return types.TxTypeArbitrateRefund, nil
	default:
		return 0, fmt.Errorf("escrow resolve: decision outcome must be release or refund, got %q", envelope.Outcome)
	}
}

func (c *RPCNodeClient) EscrowResolve(ctx context.Context, escrowID string, decision []byte, signatures [][]byte) error {
	trimmedID := strings.TrimSpace(escrowID)
	if trimmedID == "" {
		return errors.New("escrow resolve: escrowId required")
	}
	if len(decision) == 0 {
		return errors.New("escrow resolve: decision payload required")
	}
	if len(signatures) == 0 {
		return errors.New("escrow resolve: signature bundle required")
	}
	txType, err := arbitrateTxTypeForOutcome(decision)
	if err != nil {
		return err
	}
	sigHex := make([]string, len(signatures))
	for i, sig := range signatures {
		sigHex[i] = "0x" + hex.EncodeToString(sig)
	}
	data, err := rlp.EncodeToBytes(arbitrateEscrowPayload{
		EscrowID:   trimmedID,
		Decision:   decision,
		Signatures: sigHex,
	})
	if err != nil {
		return fmt.Errorf("encode arbitrate payload: %w", err)
	}
	_, err = c.submitEscrowTx(ctx, txType, data)
	return err
}

func (c *RPCNodeClient) P2PCreateTrade(ctx context.Context, req P2PAcceptRequest) (*P2PAcceptResponse, error) {
	return nil, ErrP2PTradeRetired
}

func (c *RPCNodeClient) P2PGetTrade(ctx context.Context, tradeID string) (*P2PTradeState, error) {
	var result P2PTradeState
	if err := c.call(ctx, "p2p_getTrade", []interface{}{map[string]string{"tradeId": tradeID}}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) FetchEvents(ctx context.Context, afterSeq int64, limit int) ([]NodeEvent, error) {
	params := map[string]interface{}{
		"after": afterSeq,
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var result []NodeEvent
	if err := c.call(ctx, "events_since", []interface{}{params}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeHex(value string) ([]byte, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	return hex.DecodeString(trimmed)
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int64       `json:"id"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int64            `json:"id"`
	Result  json.RawMessage  `json:"result"`
	Error   *jsonRPCErrorObj `json:"error"`
}

type jsonRPCErrorObj struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *RPCNodeClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.nextID.Add(1)
	bodyStruct := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
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
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("node rpc %s failed: status=%d body=%s", method, resp.StatusCode, string(body))
	}
	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("node rpc error: %s", rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return errors.New("node rpc returned empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}

// EscrowCreateRequest is the request payload accepted by the gateway.
type EscrowCreateRequest struct {
	Payer    string `json:"payer"`
	Payee    string `json:"payee"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	FeeBps   uint32 `json:"feeBps"`
	Deadline int64  `json:"deadline"`
	Nonce    uint64 `json:"nonce"`
	Mediator string `json:"mediator,omitempty"`
	Realm    string `json:"realm,omitempty"`
	Meta     string `json:"meta,omitempty"`
}

// EscrowCreateResponse mirrors the node RPC result.
type EscrowCreateResponse struct {
	ID string `json:"id"`
}

// EscrowState mirrors the JSON returned by the node for escrow_get.
type EscrowState struct {
	ID                string   `json:"id"`
	Payer             string   `json:"payer"`
	Payee             string   `json:"payee"`
	Mediator          *string  `json:"mediator,omitempty"`
	Token             string   `json:"token"`
	Amount            string   `json:"amount"`
	FeeBps            uint32   `json:"feeBps"`
	Deadline          int64    `json:"deadline"`
	CreatedAt         int64    `json:"createdAt"`
	Nonce             uint64   `json:"nonce"`
	Status            string   `json:"status"`
	Meta              string   `json:"meta"`
	Realm             *string  `json:"realm,omitempty"`
	FrozenAt          *int64   `json:"frozenAt,omitempty"`
	Scheme            *uint8   `json:"arbScheme,omitempty"`
	Threshold         *uint32  `json:"arbThreshold,omitempty"`
	PolicyNonce       *uint64  `json:"policyNonce,omitempty"`
	Version           *uint64  `json:"realmVersion,omitempty"`
	Members           []string `json:"arbitrators,omitempty"`
	RealmScope        *string  `json:"realmScope,omitempty"`
	RealmType         *string  `json:"realmType,omitempty"`
	RealmProfile      *string  `json:"realmProfile,omitempty"`
	RealmFeeBps       *uint32  `json:"realmFeeBps,omitempty"`
	RealmFeeRecipient *string  `json:"realmFeeRecipient,omitempty"`
}

// EscrowRealm mirrors the JSON returned by escrow_getRealm.
type EscrowRealm struct {
	ID              string               `json:"id"`
	Version         uint64               `json:"version"`
	NextPolicyNonce uint64               `json:"nextPolicyNonce"`
	CreatedAt       int64                `json:"createdAt"`
	UpdatedAt       int64                `json:"updatedAt"`
	Arbitrators     *EscrowRealmPolicy   `json:"arbitrators,omitempty"`
	Metadata        *EscrowRealmMetadata `json:"metadata,omitempty"`
}

// EscrowRealmPolicy captures the arbitrator scheme for a realm.
type EscrowRealmPolicy struct {
	Scheme    string   `json:"scheme"`
	Threshold uint32   `json:"threshold"`
	Members   []string `json:"members,omitempty"`
}

// EscrowRealmMetadata exposes provider context for a realm.
type EscrowRealmMetadata struct {
	Scope             string `json:"scope"`
	Type              string `json:"type,omitempty"`
	ProviderProfile   string `json:"providerProfile,omitempty"`
	ArbitrationFeeBps uint32 `json:"arbitrationFeeBps"`
	FeeRecipient      string `json:"feeRecipient,omitempty"`
}

// P2PAcceptRequest captures the gateway request forwarded to the node RPC when
// creating a dual-escrow trade.
type P2PAcceptRequest struct {
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

// P2PAcceptResponse mirrors the node RPC response for trade creation.
type P2PAcceptResponse struct {
	TradeID       string                         `json:"tradeId"`
	EscrowBaseID  string                         `json:"escrowBaseId"`
	EscrowQuoteID string                         `json:"escrowQuoteId"`
	PayIntents    map[string]P2PPayIntentPayload `json:"payIntents"`
}

// P2PPayIntentPayload mirrors a pay intent object returned by the node.
type P2PPayIntentPayload struct {
	To     string `json:"to"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
}

// P2PTradeState mirrors the node RPC response for fetching trade details.
type P2PTradeState struct {
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
	Status      string `json:"status"`
}

// NodeEvent represents an emitted escrow or trade event returned by the node.
type NodeEvent struct {
	Sequence   int64             `json:"sequence"`
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
	Height     uint64            `json:"height"`
	TxHash     string            `json:"txHash"`
	Timestamp  int64             `json:"timestamp"`
}
