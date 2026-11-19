package core

// Mint vouchers and the chain id they name (ledger PL-R5T-58).
//
// core.MintChainID is the chain id of the retired Phase E genesis, hard-coded.
// MintWithSignature and applyMintTransaction require a voucher's chainId to equal it,
// not the genesis-derived id of the chain that is running (StateProcessor.
// swapVoucherChainID, which the swap voucher path checks against): on the relaunch
// chain a voucher that names the running chain is refused while one that names the
// dead chain is accepted, so a voucher signed for the dead chain by a key that holds
// the minting role on both can be replayed here.
//
// The constant is also the Phase E sentinel of two one-shot supply repairs
// (SeedGenesisNHBSupplyOnce, ReconcileNHBMintSupplyDriftOnce), so it must not simply
// be repointed. Those two guards now compare against phaseEChainID, a separate name
// for the same value, which keeps them no-ops on every other chain whatever happens to
// the voucher rule.
//
// Two chain rules, both off (zero) on the live chain:
//
//   - mintRunningChainIDFromHeight: from that height a voucher naming the running
//     chain's own id is accepted too. Every mint committed so far named the Phase E
//     id, so accepting both is replay-safe.
//   - mintPhaseERetireFromHeight: from that height the Phase E id is refused and only
//     the running id is accepted (it also switches the running id on, whatever the
//     first setting says). The voucher signer must have moved to the running id by
//     then; the cutover is the operator's decision.
//
// A voucher that names the running chain id while the running chain id is unset (zero,
// as in a test harness that never wired one) is never accepted by either rule.

// phaseEChainID is the genesis-derived chain id of the retired Phase E genesis. Same
// value as MintChainID; the supply-repair guards use this name so that the voucher
// rule can change without touching them.
const phaseEChainID uint64 = 430060579445266314

// SetMintRunningChainIDActivationHeight sets the first block height from which a mint
// voucher that names the running chain's id is accepted. Zero leaves the Phase E id
// as the only accepted one. A chain rule: the same value on every validator, chosen
// above the chain tip, all restarted together.
func (sp *StateProcessor) SetMintRunningChainIDActivationHeight(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.mintRunningChainIDFromHeight = fromHeight
}

// SetMintPhaseEChainIDRetireHeight sets the first block height from which a mint
// voucher that names the Phase E id is refused. Zero never retires it. A chain rule,
// like SetMintRunningChainIDActivationHeight.
func (sp *StateProcessor) SetMintPhaseEChainIDRetireHeight(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.mintPhaseERetireFromHeight = fromHeight
}

// mintVoucherChainIDAccepted reports whether a voucher naming chainID is valid in the
// block at height. It is the one place both the block executor and the admission
// check ask.
func (sp *StateProcessor) mintVoucherChainIDAccepted(chainID, height uint64) bool {
	if sp == nil {
		return chainID == MintChainID
	}
	retired := sp.mintPhaseERetireFromHeight != 0 && height >= sp.mintPhaseERetireFromHeight
	runningOn := retired || (sp.mintRunningChainIDFromHeight != 0 && height >= sp.mintRunningChainIDFromHeight)
	if runningOn && sp.swapVoucherChainID != 0 && chainID == sp.swapVoucherChainID {
		return true
	}
	return chainID == MintChainID && !retired
}

// SetMintRunningChainIDActivationHeight sets the height on the node and on its state
// processor (see StateProcessor.SetMintRunningChainIDActivationHeight).
func (n *Node) SetMintRunningChainIDActivationHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.mintRunningChainIDHeight = height
	if n.state != nil {
		n.state.SetMintRunningChainIDActivationHeight(height)
	}
}

// SetMintPhaseEChainIDRetireHeight sets the height on the node and on its state
// processor (see StateProcessor.SetMintPhaseEChainIDRetireHeight).
func (n *Node) SetMintPhaseEChainIDRetireHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.mintPhaseERetireHeight = height
	if n.state != nil {
		n.state.SetMintPhaseEChainIDRetireHeight(height)
	}
}

// mintVoucherChainIDAcceptedForNextBlock is the admission-time form of the rule: it
// asks as of the block that would be built next.
func (n *Node) mintVoucherChainIDAcceptedForNextBlock(chainID uint64) bool {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	var next uint64 = 1
	if n.chain != nil {
		if header := n.chain.CurrentHeader(); header != nil {
			next = header.Height + 1
		}
	}
	return n.state.mintVoucherChainIDAccepted(chainID, next)
}
