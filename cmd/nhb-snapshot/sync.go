package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nhbchain/core/types"
)

// Exit codes of wait-synced beyond the usual 0, 1 and 2.
const (
	exitTimeout = 3
	exitStalled = 4
	exitChain   = 5
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// rpcClient is a minimal JSON-RPC client for the two public, read-only calls a
// follower's progress can be read from.
type rpcClient struct {
	url    string
	client *http.Client
}

func newRPCClient(url string) *rpcClient {
	return &rpcClient{url: strings.TrimSpace(url), client: &http.Client{Timeout: 10 * time.Second}}
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *rpcClient) call(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s: %s", method, resp.Status, strings.TrimSpace(string(raw)))
	}
	var envelope rpcResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("%s: undecodable answer: %w", method, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s: error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

type netInfo struct {
	NodeID      string `json:"nodeId"`
	ChainID     uint64 `json:"chainId"`
	GenesisHash string `json:"genesisHash"`
	PeerCounts  struct {
		Total int `json:"total"`
	} `json:"peerCounts"`
}

func (c *rpcClient) netInfo(ctx context.Context) (*netInfo, error) {
	var info netInfo
	if err := c.call(ctx, "net_info", []any{}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

type nodeStatus struct {
	Height    uint64
	TipHash   []byte
	StateRoot []byte
	Timestamp int64
}

// latestBlock reads the newest block the node has, through nhb_getLatestBlocks.
func (c *rpcClient) latestBlock(ctx context.Context) (*nodeStatus, error) {
	var blocks []*types.Block
	if err := c.call(ctx, "nhb_getLatestBlocks", []any{1}, &blocks); err != nil {
		return nil, err
	}
	if len(blocks) == 0 || blocks[0] == nil || blocks[0].Header == nil {
		return nil, errors.New("nhb_getLatestBlocks returned no block")
	}
	h := blocks[0].Header
	hash, err := h.Hash()
	if err != nil {
		return nil, err
	}
	return &nodeStatus{Height: h.Height, TipHash: hash, StateRoot: h.StateRoot, Timestamp: h.Timestamp}, nil
}

type waitOptions struct {
	RPC           string
	TipRPC        string
	ExpectChainID *uint64
	ExpectGenesis []byte // optional; compared with net_info's genesisHash
	MaxLagBlocks  uint64
	MaxLagSeconds int64
	Interval      time.Duration
	Timeout       time.Duration
	StallTimeout  time.Duration
	Stable        int
	Out           io.Writer
	Now           func() time.Time
}

type syncResult struct {
	Height   uint64
	TipHash  []byte
	Root     []byte
	TipRPC   uint64 // height of the reference node at the last poll, zero when none is used
	LagBlock uint64
	LagSecs  int64
	Duration time.Duration
}

func (o *waitOptions) defaults() {
	if o.MaxLagBlocks == 0 {
		o.MaxLagBlocks = 3
	}
	if o.MaxLagSeconds <= 0 {
		o.MaxLagSeconds = 60
	}
	if o.Interval <= 0 {
		o.Interval = 5 * time.Second
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Hour
	}
	if o.StallTimeout <= 0 {
		o.StallTimeout = 15 * time.Minute
	}
	if o.Stable <= 0 {
		o.Stable = 2
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

func checkIdentity(info *netInfo, o *waitOptions, who string) error {
	if o.ExpectChainID != nil && info.ChainID != *o.ExpectChainID {
		return &exitError{exitChain, fmt.Errorf("%s reports chain id %d, expected %d: it is not on the pinned network", who, info.ChainID, *o.ExpectChainID)}
	}
	if o.ExpectGenesis != nil && !strings.EqualFold(strings.TrimPrefix(info.GenesisHash, "0x"), hex.EncodeToString(o.ExpectGenesis)) {
		return &exitError{exitChain, fmt.Errorf("%s reports genesis hash %s, expected %s", who, info.GenesisHash, hex.EncodeToString(o.ExpectGenesis))}
	}
	return nil
}

// waitSynced polls a node until it is within a few blocks of the network tip.
//
// With a reference RPC the distance is measured in blocks against that node's
// newest block. Without one the newest block's own timestamp is compared with
// this host's clock: a block committed within MaxLagSeconds of now means the
// node has reached the tip of a chain that is still producing blocks. The
// reference is used only to decide when to stop waiting; it authenticates
// nothing, the node validates every block it applies itself.
func waitSynced(ctx context.Context, o waitOptions) (*syncResult, error) {
	o.defaults()
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	local := newRPCClient(o.RPC)
	var ref *rpcClient
	if strings.TrimSpace(o.TipRPC) != "" {
		ref = newRPCClient(o.TipRPC)
	}
	start := o.Now()

	// The node may still be starting: wait for its RPC, but stop at once when
	// it answers with the wrong chain.
	for {
		info, err := local.netInfo(ctx)
		if err == nil {
			if err := checkIdentity(info, &o, "the node"); err != nil {
				return nil, err
			}
			fmt.Fprintf(o.Out, "node is up: chain id %d, %d peers\n", info.ChainID, info.PeerCounts.Total)
			break
		}
		fmt.Fprintf(o.Out, "waiting for the node's RPC: %v\n", err)
		if err := sleepCtx(ctx, o.Interval); err != nil {
			return nil, &exitError{exitTimeout, fmt.Errorf("the node's RPC did not come up: %v", lastErr(err, ctx))}
		}
	}
	if ref != nil {
		info, err := ref.netInfo(ctx)
		if err != nil {
			return nil, fmt.Errorf("the reference RPC %s does not answer net_info: %w", o.TipRPC, err)
		}
		if err := checkIdentity(info, &o, "the reference RPC"); err != nil {
			return nil, err
		}
	}

	var (
		stableRuns  int
		lastHeight  uint64
		lastAdvance = o.Now()
		firstSeen   bool
		firstHeight uint64
		firstAt     time.Time
	)
	for {
		status, err := local.latestBlock(ctx)
		if err != nil {
			fmt.Fprintf(o.Out, "could not read the node's newest block: %v\n", err)
		} else {
			now := o.Now()
			if !firstSeen {
				firstSeen, firstHeight, firstAt = true, status.Height, now
				lastHeight = status.Height
				lastAdvance = now
			}
			if status.Height > lastHeight {
				lastHeight = status.Height
				lastAdvance = now
			}
			res := &syncResult{Height: status.Height, TipHash: status.TipHash, Root: status.StateRoot, Duration: now.Sub(start)}
			synced := false
			var line string
			if ref != nil {
				refStatus, refErr := ref.latestBlock(ctx)
				if refErr != nil {
					fmt.Fprintf(o.Out, "could not read the reference node's newest block: %v\n", refErr)
				} else {
					res.TipRPC = refStatus.Height
					if refStatus.Height > status.Height {
						res.LagBlock = refStatus.Height - status.Height
					}
					synced = res.LagBlock <= o.MaxLagBlocks
					line = fmt.Sprintf("height %d, network tip %d, %d blocks behind", status.Height, refStatus.Height, res.LagBlock)
				}
			} else {
				res.LagSecs = now.Unix() - status.Timestamp
				synced = res.LagSecs <= o.MaxLagSeconds
				line = fmt.Sprintf("height %d, newest block is %d s old", status.Height, res.LagSecs)
			}
			if line != "" {
				if rate := progressRate(firstHeight, status.Height, firstAt, now); rate != "" {
					line += ", " + rate
				}
				fmt.Fprintln(o.Out, line)
				if synced {
					stableRuns++
					if stableRuns >= o.Stable {
						return res, nil
					}
				} else {
					stableRuns = 0
				}
			}
			if !synced && now.Sub(lastAdvance) > o.StallTimeout {
				return nil, &exitError{exitStalled, fmt.Errorf("the node has not advanced past height %d for %s: it is not syncing (check the peer list and the node log)", lastHeight, o.StallTimeout)}
			}
		}
		if err := sleepCtx(ctx, o.Interval); err != nil {
			return nil, &exitError{exitTimeout, fmt.Errorf("the node did not reach the network tip within %s (last height %d)", o.Timeout, lastHeight)}
		}
	}
}

func progressRate(h0, h1 uint64, t0, t1 time.Time) string {
	secs := t1.Sub(t0).Seconds()
	if secs < 1 || h1 <= h0 {
		return ""
	}
	return fmt.Sprintf("%.1f blocks/s", float64(h1-h0)/secs)
}

func lastErr(err error, ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
