// Package txchainid reads the chain id an off-chain signer must put in a
// transaction (ledger PL-R5T-29).
//
// Until a chain reaches its TxNetworkChainIDActivationHeight a node accepts only
// the legacy constant every NHB network shares; from it, only the network's own
// id. The node reports the one to use as txChainId in the public
// nhb_getNetworkStats call (net_info is operator-only), for the next block. A
// signer therefore asks the node instead of hard-coding either value, so the same
// signer keeps working across the activation height.
//
// The rules every signer that uses this package follows:
//
//   - the id is read from the node, once, and kept for a short time (DefaultTTL),
//     because the answer changes once, at the activation height;
//   - if the call fails, or the node reports nothing, zero, or a value that is not
//     a uint64, the signer does not sign: there is no fallback to a constant, which
//     would produce a transaction that replays on another network.
package txchainid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Method is the public read call that reports the id (field txChainId).
const Method = "nhb_getNetworkStats"

// DefaultTTL is how long a read answer is reused. It is short on purpose: the
// answer flips once, at the activation height, and a signer that lives across
// that height must pick the new id up within seconds.
const DefaultTTL = 15 * time.Second

// maxAnswerBytes bounds what is read from a node: the real answer is a few
// hundred bytes.
const maxAnswerBytes = 1 << 20

// ErrUnavailable is wrapped by every error that means "no usable chain id": the
// node could not be asked, or its answer carried none.
var ErrUnavailable = errors.New("txchainid: the node did not report a usable transaction chain id")

// Fetcher performs one read of the id from a node.
type Fetcher func(ctx context.Context) (*big.Int, error)

// Parse extracts txChainId from the result of nhb_getNetworkStats. The node serves
// it as a decimal string; a JSON number is also read exactly (never through a
// float). Anything that is not a positive uint64 is refused.
func Parse(result []byte) (*big.Int, error) { return parseField(result, "txChainId") }

// ParseNetwork extracts networkChainId from the result of nhb_getNetworkStats: the running chain's own, genesis-derived id (the
// value net_info calls chainId, which is operator-only). It is NOT the id a transaction must carry (that is txChainId, which
// is the legacy constant until the activation height): a signer that names the chain itself, such as a mint voucher signer,
// reads this one. Same strictness as Parse: a positive uint64 or a refusal.
func ParseNetwork(result []byte) (*big.Int, error) { return parseField(result, "networkChainId") }

func parseField(result []byte, field string) (*big.Int, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(result, &fields); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	raw := bytes.TrimSpace(fields[field])
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("%w: %s is missing (a node older than the release that reports it)", ErrUnavailable, field)
	}
	var text string
	switch {
	case raw[0] == '"':
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	case raw[0] >= '0' && raw[0] <= '9':
		text = string(raw)
	default:
		return nil, fmt.Errorf("%w: %s is not a number", ErrUnavailable, field)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("%w: %s is empty", ErrUnavailable, field)
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return nil, fmt.Errorf("%w: %s %q is not a decimal number", ErrUnavailable, field, text)
		}
	}
	id, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, fmt.Errorf("%w: %s %q is not a decimal number", ErrUnavailable, field, text)
	}
	if err := validate(id); err != nil {
		return nil, err
	}
	return id, nil
}

// validate accepts only a positive value that fits the uint64 a transaction
// hash encodes the chain id as.
func validate(id *big.Int) error {
	switch {
	case id == nil:
		return fmt.Errorf("%w: no value", ErrUnavailable)
	case id.Sign() <= 0:
		return fmt.Errorf("%w: %s is not positive", ErrUnavailable, id)
	case !id.IsUint64():
		return fmt.Errorf("%w: %s does not fit a uint64", ErrUnavailable, id)
	}
	return nil
}

// Reader reads the id through a Fetcher and keeps the answer for a short time.
// It is safe for concurrent use; concurrent callers share one read.
type Reader struct {
	fetch Fetcher
	ttl   time.Duration
	now   func() time.Time

	mu    sync.Mutex
	value *big.Int
	at    time.Time
}

// NewReader returns a Reader that asks fetch and reuses the answer for ttl (zero
// or negative means DefaultTTL).
func NewReader(fetch Fetcher, ttl time.Duration) *Reader {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Reader{fetch: fetch, ttl: ttl, now: time.Now}
}

// ChainID returns the id a transaction signed now must carry, as a copy the
// caller may modify. It fails closed: when the node cannot be asked or reports
// no usable id it returns an error that wraps nothing but the cause, never an
// expired or default value.
func (r *Reader) ChainID(ctx context.Context) (*big.Int, error) {
	if r == nil || r.fetch == nil {
		return nil, fmt.Errorf("%w: no node to ask", ErrUnavailable)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	started := r.now()
	if r.value != nil && started.Sub(r.at) < r.ttl {
		return new(big.Int).Set(r.value), nil
	}
	r.value = nil
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("txchainid: %w", err)
	}
	id, err := r.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("txchainid: read %s: %w", Method, err)
	}
	if err := validate(id); err != nil {
		return nil, err
	}
	r.value = new(big.Int).Set(id)
	r.at = started
	return new(big.Int).Set(id), nil
}

// Expiry reports how long an answer is reused (zero for a nil reader).
func (r *Reader) Expiry() time.Duration {
	if r == nil {
		return 0
	}
	return r.ttl
}

// Invalidate drops the kept answer, so the next ChainID asks the node again (a
// signer calls it when the node refuses a transaction for its chain id).
func (r *Reader) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.value = nil
	r.mu.Unlock()
}

// HTTPFetcher returns a Fetcher that calls nhb_getNetworkStats on a node's
// JSON-RPC endpoint. token, when not empty, is sent as a bearer token (the call
// is public; a deployment that fronts the node with authentication may need it).
// A nil client gets one with a 10 second timeout.
func HTTPFetcher(client *http.Client, endpoint, token string) Fetcher {
	return httpFetcher(client, endpoint, token, Parse)
}

// NetworkHTTPFetcher is HTTPFetcher for the running chain's own id (networkChainId, see ParseNetwork) instead of the id a
// transaction must carry. Same call, same refusals.
func NetworkHTTPFetcher(client *http.Client, endpoint, token string) Fetcher {
	return httpFetcher(client, endpoint, token, ParseNetwork)
}

func httpFetcher(client *http.Client, endpoint, token string, parse func([]byte) (*big.Int, error)) Fetcher {
	endpoint = strings.TrimSpace(endpoint)
	token = strings.TrimSpace(token)
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context) (*big.Int, error) {
		if endpoint == "" {
			return nil, fmt.Errorf("%w: no node endpoint configured", ErrUnavailable)
		}
		body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": Method, "params": []interface{}{}})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("node answered HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxAnswerBytes {
			return nil, fmt.Errorf("%w: the answer is larger than %d bytes", ErrUnavailable, maxAnswerBytes)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("%w: undecodable answer: %v", ErrUnavailable, err)
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("node error %d: %s", envelope.Error.Code, envelope.Error.Message)
		}
		if len(bytes.TrimSpace(envelope.Result)) == 0 || string(bytes.TrimSpace(envelope.Result)) == "null" {
			return nil, fmt.Errorf("%w: the answer has no result", ErrUnavailable)
		}
		return parse(envelope.Result)
	}
}
