package core

import (
	"math/big"

	"nhbchain/core/types"
)

// The chain id a transaction carries (ledger PL-R5T-29).
//
// A transaction's V3 signing hash covers its envelope ChainID, but the only id the
// node has ever accepted is the constant 0x4e4842 ("NHB", types.NHBChainID), the same on
// every network that runs this code: the live chain, the abandoned Phase E and 2026-08
// chains, devnets and testnets. A signed action on one network is therefore valid on
// every other network on which the same key holds the same nonce or role.
//
// From txNetworkChainIDFromHeight (zero = off, the live value) a transaction must carry
// the network's own chain id (the genesis-derived id the node reports through
// nhb_getNetworkStats, StateProcessor.swapVoucherChainID) and the legacy constant is refused, so a
// transaction signed for one network does not verify on another. Below the height only
// the legacy constant is accepted, which is what every committed block holds. An
// unset network id (zero, as in a test harness that never wired one) accepts nothing
// from the height on: the setting is misconfigured, and failing closed is the safe side.
//
// Transactions the node builds itself (heartbeats, the senderless submissions) take
// their id from txChainIDForNextBlock, and nhb_getNetworkStats reports the same value (txChainId) so
// that command-line tools, SDKs and the portal sign with the id the node will accept
// for the next block.

// SetTxNetworkChainIDActivationHeight sets the first block height from which a
// transaction must carry the network's own chain id. Zero keeps the legacy constant at
// every height. A chain rule: the same value on every validator, chosen above the chain
// tip, all restarted together.
func (sp *StateProcessor) SetTxNetworkChainIDActivationHeight(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.txNetworkChainIDFromHeight = fromHeight
}

// txNetworkChainIDActive reports whether the network's own chain id is the one a
// transaction must carry in the block at height.
func (sp *StateProcessor) txNetworkChainIDActive(height uint64) bool {
	return sp != nil && sp.txNetworkChainIDFromHeight != 0 && height >= sp.txNetworkChainIDFromHeight
}

// txChainIDAccepted reports whether a transaction carrying chainID is valid in the
// block at height. It is the one place the block executor, the node's admission check
// and the RPC pre-check ask.
func (sp *StateProcessor) txChainIDAccepted(chainID *big.Int, height uint64) bool {
	if chainID == nil {
		return false
	}
	if !sp.txNetworkChainIDActive(height) {
		return types.IsValidChainID(chainID)
	}
	if sp.swapVoucherChainID == 0 {
		return false
	}
	return chainID.IsUint64() && chainID.Uint64() == sp.swapVoucherChainID
}

// txChainIDAt is the chain id a transaction must carry to be valid in the block at height.
func (sp *StateProcessor) txChainIDAt(height uint64) *big.Int {
	if sp.txNetworkChainIDActive(height) && sp.swapVoucherChainID != 0 {
		return new(big.Int).SetUint64(sp.swapVoucherChainID)
	}
	return types.NHBChainID()
}

// SetTxNetworkChainIDActivationHeight sets the height on the node and on its state
// processor (see StateProcessor.SetTxNetworkChainIDActivationHeight).
func (n *Node) SetTxNetworkChainIDActivationHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.txNetworkChainIDHeight = height
	if n.state != nil {
		n.state.SetTxNetworkChainIDActivationHeight(height)
	}
}

// nextBlockHeightLocked is the height of the block that would be built next. The
// caller holds stateMu.
func (n *Node) nextBlockHeightLocked() uint64 {
	var next uint64 = 1
	if n.chain != nil {
		if header := n.chain.CurrentHeader(); header != nil {
			next = header.Height + 1
		}
	}
	return next
}

// TxChainIDForNextBlock is the chain id a transaction submitted now must carry to be
// valid in the next block: the legacy constant until the activation height, the
// network's own id from it.
func (n *Node) TxChainIDForNextBlock() *big.Int {
	if n == nil {
		return types.NHBChainID()
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.txChainIDAt(n.nextBlockHeightLocked())
}

// txChainIDForNextBlock is TxChainIDForNextBlock for the transactions the node builds.
func (n *Node) txChainIDForNextBlock() *big.Int { return n.TxChainIDForNextBlock() }

// TxChainIDAcceptedForNextBlock reports whether a transaction carrying chainID would
// pass the chain id check in the next block. The RPC pre-check asks it.
func (n *Node) TxChainIDAcceptedForNextBlock(chainID *big.Int) bool {
	if n == nil {
		return types.IsValidChainID(chainID)
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.state.txChainIDAccepted(chainID, n.nextBlockHeightLocked())
}
