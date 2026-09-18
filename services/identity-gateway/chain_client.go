package identitygateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	nhbcrypto "nhbchain/crypto"
)

// AliasOwnerLookup resolves the address currently controlling an alias
// on-chain (core/identity.AliasRecord's Owner/Primary address -- the
// existing on-chain alias-ownership primitive; see core/identity/alias.go
// and rpc/identity_handlers.go's identity_resolve). handleBindAlias uses
// this, and only this, as the ground truth an alias-bind signature must
// recover to (NHB-AUDIT-S6b) -- never a client-supplied address.
type AliasOwnerLookup interface {
	ResolveOwner(ctx context.Context, alias string) ([20]byte, error)
}

// ErrAliasOwnerNotFound is returned by an AliasOwnerLookup when the alias
// has no on-chain registration at all -- a bind request for such an alias
// can never carry a valid ownership signature, so handleBindAlias rejects
// it the same way it rejects a signature that recovers to the wrong
// address.
var ErrAliasOwnerNotFound = errors.New("identity-gateway: alias has no on-chain owner")

// RPCAliasOwnerLookup implements AliasOwnerLookup against this chain's own
// existing, already-public identity_resolve JSON-RPC method
// (rpc/identity_handlers.go's handleIdentityResolve). No new on-chain
// primitive is introduced here -- core/identity.AliasRecord (Owner/Primary
// address, DeriveAliasID) already IS the on-chain alias-ownership
// primitive; identity-gateway was simply never wired up to consult it.
// This client is the minimal piece that was missing, deliberately mirroring
// services/escrow-gateway/node_client.go's own call()/jsonRPCRequest/
// jsonRPCResponse plumbing rather than inventing a second convention for
// talking to the same RPC server -- trimmed down to the one read-only
// method this gateway needs (no relayer key, no nonce/transaction
// submission, unlike escrow-gateway's mutating calls).
type RPCAliasOwnerLookup struct {
	baseURL string
	http    *http.Client
	nextID  atomic.Int64
}

// NewRPCAliasOwnerLookup constructs a lookup client against the node's
// JSON-RPC endpoint at baseURL (e.g. "http://127.0.0.1:8080/rpc").
func NewRPCAliasOwnerLookup(baseURL string) *RPCAliasOwnerLookup {
	return &RPCAliasOwnerLookup{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// identityResolveRPCResult mirrors rpc/identity_handlers.go's
// identityResolveResult field-for-field -- only the fields this lookup
// actually needs are decoded.
type identityResolveRPCResult struct {
	Alias   string `json:"alias"`
	AliasID string `json:"aliasId"`
	Primary string `json:"primary"`
}

// ResolveOwner calls identity_resolve(alias) and decodes the returned
// Primary bech32 address. Any RPC-level error (including "alias not
// found") is surfaced as an error -- callers must fail closed, never
// treat a lookup failure as "no owner to check against".
func (c *RPCAliasOwnerLookup) ResolveOwner(ctx context.Context, alias string) ([20]byte, error) {
	var zero [20]byte
	var result identityResolveRPCResult
	if err := c.call(ctx, "identity_resolve", []interface{}{alias}, &result); err != nil {
		return zero, fmt.Errorf("identity-gateway: resolve alias owner: %w", err)
	}
	primary := strings.TrimSpace(result.Primary)
	if primary == "" {
		return zero, ErrAliasOwnerNotFound
	}
	addr, err := nhbcrypto.DecodeAddress(primary)
	if err != nil {
		return zero, fmt.Errorf("identity-gateway: decode alias owner address %q: %w", primary, err)
	}
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out, nil
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

func (c *RPCAliasOwnerLookup) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.nextID.Add(1)
	buf, err := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", Method: method, Params: params, ID: id})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node rpc %s failed: status=%d body=%s", method, resp.StatusCode, string(body))
	}
	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
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
