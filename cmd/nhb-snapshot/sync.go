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
	"sort"
	"strings"
	"time"

	"nhbchain/core/types"
)

// Exit codes of wait-synced beyond the usual 0, 1 and 2.
const (
	exitTimeout = 3
	exitStalled = 4
	exitChain   = 5
	exitFork    = 6
	exitNoRPC   = 7
)

const (
	// defaultRPCTimeout is how long a node that is starting may take to answer
	// its first request. A node that starts normally answers within a minute or
	// two, so one that has not answered by then is not running or is
	// crash-looping, and waiting out the whole of the overall timeout for it
	// would tell the operator nothing for hours.
	defaultRPCTimeout = 3 * time.Minute
	// minPeers is how many peers a node must be connected to before it counts
	// as being at the network tip: a node that hears from nobody has not
	// followed the network to anywhere.
	minPeers = 1
	// blocksPerReply is the most blocks one nhb_getLatestBlocks call returns.
	blocksPerReply = 20
	// maxReferenceLag is the largest --max-lag-blocks that can be honoured
	// against a reference node: the two nodes' newest blocks are compared, so
	// the lists each returns must still overlap.
	maxReferenceLag = blocksPerReply - 5
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

// latestBlocks reads up to count of the newest blocks the node has, newest
// first, through nhb_getLatestBlocks.
func (c *rpcClient) latestBlocks(ctx context.Context, count int) ([]*nodeStatus, error) {
	var blocks []*types.Block
	if err := c.call(ctx, "nhb_getLatestBlocks", []any{count}, &blocks); err != nil {
		return nil, err
	}
	var out []*nodeStatus
	for _, b := range blocks {
		if b == nil || b.Header == nil {
			continue
		}
		hash, err := b.Header.Hash()
		if err != nil {
			return nil, err
		}
		out = append(out, &nodeStatus{Height: b.Header.Height, TipHash: hash, StateRoot: b.Header.StateRoot, Timestamp: b.Header.Timestamp})
	}
	if len(out) == 0 {
		return nil, errors.New("nhb_getLatestBlocks returned no block")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Height > out[j].Height })
	return out, nil
}

// latestBlock reads the newest block the node has, through nhb_getLatestBlocks.
func (c *rpcClient) latestBlock(ctx context.Context) (*nodeStatus, error) {
	blocks, err := c.latestBlocks(ctx, 1)
	if err != nil {
		return nil, err
	}
	return blocks[0], nil
}

type waitOptions struct {
	RPC           string
	TipRPC        string
	ExpectChainID *uint64
	ExpectGenesis []byte // optional; compared with net_info's genesisHash
	// MinHeight, when it is not zero, is the height of the snapshot the node
	// started from: the node counts as synced only once it has applied a block
	// above it, which a snapshot that is not part of the network's chain can
	// never do.
	MinHeight     uint64
	MaxLagBlocks  uint64
	MaxLagSeconds int64
	Interval      time.Duration
	Timeout       time.Duration
	// RPCTimeout bounds the wait for the node's RPC to answer for the first
	// time; Timeout still bounds the whole wait.
	RPCTimeout   time.Duration
	StallTimeout time.Duration
	Stable       int
	Out          io.Writer
	Now          func() time.Time
}

type syncResult struct {
	Height   uint64
	TipHash  []byte
	Root     []byte
	Peers    int    // peers the node was connected to at the last poll
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
	if o.RPCTimeout <= 0 {
		o.RPCTimeout = defaultRPCTimeout
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

// waitSynced polls a node until it is at the network tip. All of these must
// hold on Stable polls in a row:
//
//   - the node is connected to at least one peer;
//   - when MinHeight is set, the node has applied a block above it. A node that
//     started from a snapshot that is not part of the network's chain can never
//     apply the network's next block, so this is what a forged snapshot cannot
//     fake;
//   - with a reference RPC, the two nodes' newest blocks are at most
//     MaxLagBlocks apart and are the same blocks (a different block hash at a
//     height both hold ends the wait: the node is on another chain);
//   - without one, the newest block's own timestamp is within MaxLagSeconds of
//     this host's clock, on either side: a block dated in the future is no
//     evidence of being at the tip of a chain that is still producing blocks.
//
// The reference is used to decide when to stop waiting and to catch a node on
// another chain; the node validates every block it applies itself.
//
// No stage of the wait may run for the whole of Timeout without saying so: the
// node's RPC has RPCTimeout (or StallTimeout, when that is shorter) to answer for
// the first time (a service that is not running, or that crash-loops, never
// does), and a node that answers nothing or does not advance is given up on after
// StallTimeout.
func waitSynced(ctx context.Context, o waitOptions) (*syncResult, error) {
	o.defaults()
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	local := newRPCClient(o.RPC)
	var ref *rpcClient
	window := 1
	if strings.TrimSpace(o.TipRPC) != "" {
		ref = newRPCClient(o.TipRPC)
		window = referenceWindow(o.MaxLagBlocks)
	}
	start := o.Now()

	// The node may still be starting: wait for its RPC, but only as long as a
	// node that starts normally takes (and never longer than a node may go without
	// advancing), and stop at once when it answers with the wrong chain.
	rpcBound := o.RPCTimeout
	if o.StallTimeout < rpcBound {
		rpcBound = o.StallTimeout
	}
	rpcCtx, cancelRPC := context.WithTimeout(ctx, rpcBound)
	defer cancelRPC()
	var lastRPCErr error
	for {
		info, err := local.netInfo(rpcCtx)
		if err == nil {
			if err := checkIdentity(info, &o, "the node"); err != nil {
				return nil, err
			}
			fmt.Fprintf(o.Out, "node is up: chain id %d, %d peers\n", info.ChainID, info.PeerCounts.Total)
			break
		}
		if rpcCtx.Err() == nil {
			lastRPCErr = err
		}
		fmt.Fprintf(o.Out, "waiting for the node's RPC: %v\n", err)
		if err := sleepCtx(rpcCtx, o.Interval); err != nil {
			if ctx.Err() != nil {
				return nil, &exitError{exitTimeout, fmt.Errorf("the node's RPC did not come up: %v", lastErr(err, ctx))}
			}
			answer := "no answer"
			if lastRPCErr != nil {
				answer = lastRPCErr.Error()
			}
			return nil, &exitError{exitNoRPC, fmt.Errorf("the node's RPC did not answer within %s (last: %s): the node is not running, or it is crash-looping (check the service and its log)", rpcBound, answer)}
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
		blocks, err := local.latestBlocks(ctx, window)
		if err != nil {
			fmt.Fprintf(o.Out, "could not read the node's newest block: %v\n", err)
			// A node whose RPC answered once and then answers nothing has not
			// advanced either: that ends the wait after StallTimeout, not Timeout.
			if o.Now().Sub(lastAdvance) > o.StallTimeout {
				return nil, &exitError{exitStalled, fmt.Errorf("the node has not answered a request for its newest block, and has not advanced past height %d, for %s: it is not running or not syncing (check the service and its log)", lastHeight, o.StallTimeout)}
			}
		} else {
			status := blocks[0]
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
			synced := true
			var notes []string
			if info, infoErr := local.netInfo(ctx); infoErr != nil {
				synced = false
				notes = append(notes, fmt.Sprintf("its peer count is unreadable: %v", infoErr))
			} else {
				res.Peers = info.PeerCounts.Total
				if res.Peers < minPeers {
					synced = false
					notes = append(notes, "connected to no peer")
				}
			}
			if o.MinHeight > 0 && status.Height <= o.MinHeight {
				synced = false
				notes = append(notes, fmt.Sprintf("no block above the snapshot's height %d has been applied yet", o.MinHeight))
			}
			var line string
			if ref != nil {
				refBlocks, refErr := ref.latestBlocks(ctx, window)
				if refErr != nil {
					fmt.Fprintf(o.Out, "could not read the reference node's newest block: %v\n", refErr)
					synced = false
				} else {
					refStatus := refBlocks[0]
					res.TipRPC = refStatus.Height
					side := "behind"
					if refStatus.Height >= status.Height {
						res.LagBlock = refStatus.Height - status.Height
					} else {
						res.LagBlock = status.Height - refStatus.Height
						side = "ahead"
					}
					if res.LagBlock > o.MaxLagBlocks {
						synced = false
					}
					common, err := compareWindows(blocks, refBlocks)
					if err != nil {
						return nil, err
					}
					if common == 0 {
						synced = false
						notes = append(notes, "its newest blocks and the reference node's have no height in common")
					}
					line = fmt.Sprintf("height %d, network tip %d, %d blocks %s", status.Height, refStatus.Height, res.LagBlock, side)
				}
			} else {
				res.LagSecs = now.Unix() - status.Timestamp
				if res.LagSecs > o.MaxLagSeconds {
					synced = false
				}
				if res.LagSecs < -o.MaxLagSeconds {
					synced = false
					notes = append(notes, "its newest block is dated in the future")
				}
				line = fmt.Sprintf("height %d, newest block is %d s old", status.Height, res.LagSecs)
			}
			if line != "" {
				line += fmt.Sprintf(", %d peers", res.Peers)
				if rate := progressRate(firstHeight, status.Height, firstAt, now); rate != "" {
					line += ", " + rate
				}
				if len(notes) > 0 {
					line += " (" + strings.Join(notes, "; ") + ")"
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

// referenceWindow is how many of the newest blocks each node is asked for when
// its blocks are compared with the reference node's: enough that the two lists
// still overlap while the nodes are as far apart as the caller accepts, and few
// enough to keep the replies small.
func referenceWindow(maxLag uint64) int {
	n := maxLag + 5
	if n > blocksPerReply {
		n = blocksPerReply
	}
	return int(n)
}

// compareWindows checks that two nodes' newest blocks agree at every height
// both list, and returns how many heights that is. Blocks are final once
// committed, so a different block at one height is not a race between the two
// reads: the nodes are on different chains.
func compareWindows(local, ref []*nodeStatus) (int, error) {
	refHash := make(map[uint64][]byte, len(ref))
	for _, b := range ref {
		refHash[b.Height] = b.TipHash
	}
	common := 0
	for _, b := range local {
		want, ok := refHash[b.Height]
		if !ok {
			continue
		}
		common++
		if !bytes.Equal(want, b.TipHash) {
			return common, &exitError{exitFork, fmt.Errorf("the node's block %d is %x but the reference node's is %x: the two are not on the same chain, so the snapshot this node started from is not part of the network's chain", b.Height, b.TipHash, want)}
		}
	}
	return common, nil
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
