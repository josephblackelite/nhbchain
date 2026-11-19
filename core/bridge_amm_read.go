package core

import (
	"fmt"
	"math/big"
	"time"
)

// Read-only views of the bridge and the native pool for the RPC server (ledger PL-BC-C, PL-BC-B). Each reads a copy of
// the state at the next block's height, so "swaps open" and "bridge active" are what the next block would see, and none
// of them writes anything or takes part in consensus.

func (n *Node) readProcessor() (*StateProcessor, error) {
	if n == nil {
		return nil, fmt.Errorf("node unavailable")
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.state == nil || n.state.Trie == nil {
		return nil, fmt.Errorf("state unavailable")
	}
	copyOf, err := n.state.Copy()
	if err != nil {
		return nil, err
	}
	// With no block yet the read looks at height 1 at the epoch start: a read never needs the machine's clock.
	height, stamp := uint64(1), time.Unix(0, 0).UTC()
	if n.chain != nil {
		if head := n.chain.CurrentHeader(); head != nil {
			height, stamp = head.Height+1, time.Unix(head.Timestamp, 0).UTC()
		}
	}
	copyOf.BeginBlock(height, stamp)
	return copyOf, nil
}

// BridgeStatus reports the bridge's totals and escrow balance.
func (n *Node) BridgeStatus() (*BridgeStatusView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	view, err := sp.BridgeStatus()
	if err != nil {
		return nil, err
	}
	view.Active = sp.bridgeActive()
	return view, nil
}

// AmmPool reports the pool's reserves, shares and settings.
func (n *Node) AmmPool() (*AmmPoolView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	view, err := sp.AmmPool()
	if err != nil {
		return nil, err
	}
	view.Active = sp.ammActive()
	return view, nil
}

// AmmQuote reports what a swap of amountIn would pay out now.
func (n *Node) AmmQuote(dir uint8, amountIn *big.Int) (*big.Int, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	return sp.AmmQuote(dir, amountIn)
}

// AmmTrades (the newest swaps first, each with its block's time) is in market_data.go.
