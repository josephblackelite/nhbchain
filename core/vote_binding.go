package core

import "nhbchain/core/types"

// Consensus votes bound to the network (ledger PL-R5T-31).
//
// A vote's signed digest was the sha256 of {blockHash, round, type, height}, with no
// chain or genesis identity: two honest votes by one key on two networks at the same
// height, round and type formed a valid equivocation proof, and a vote or quorum
// certificate of one network verified on another. From voteChainBindingFromHeight
// (zero = off, the live value) a vote at or above the height is signed and checked over
// the genesis hash too (types.VoteBinding), by the BFT engine, by the quorum certificate
// check of a synced block (Node.voteBinding) and by the equivocation proof of a report
// (StateProcessor.voteBinding). The rule reads the height of the vote itself, so history
// and a fresh node's replay are unchanged.

// SetVoteChainBindingActivationHeight sets the first vote height signed over the genesis
// hash. Zero keeps the legacy digest at every height. A chain rule: the same value on
// every validator, chosen above the chain tip, all restarted together.
func (sp *StateProcessor) SetVoteChainBindingActivationHeight(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.voteChainBindingFromHeight = fromHeight
}

// SetGenesisHash installs the network's genesis hash, which the vote binding signs over.
func (sp *StateProcessor) SetGenesisHash(hash []byte) {
	if sp == nil {
		return
	}
	sp.genesisHash = append([]byte(nil), hash...)
}

// voteBinding is the binding the processor judges equivocation proofs by.
func (sp *StateProcessor) voteBinding() types.VoteBinding {
	if sp == nil {
		return types.VoteBinding{}
	}
	return types.VoteBinding{Genesis: sp.genesisHash, FromHeight: sp.voteChainBindingFromHeight}
}

// SetVoteChainBindingActivationHeight sets the height on the node and on its state
// processor (see StateProcessor.SetVoteChainBindingActivationHeight).
func (n *Node) SetVoteChainBindingActivationHeight(height uint64) {
	if n == nil {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	n.voteChainBindingHeight = height
	if n.state != nil {
		n.state.SetVoteChainBindingActivationHeight(height)
	}
}

// VoteBinding is the binding every vote, quorum certificate and equivocation proof is held
// to: the BFT engine is built with it (bft.WithVoteBinding) so that what it signs and what
// the node checks always agree.
func (n *Node) VoteBinding() types.VoteBinding {
	if n == nil {
		return types.VoteBinding{}
	}
	var genesis []byte
	if n.chain != nil {
		genesis = n.chain.GenesisHash()
	}
	return types.VoteBinding{Genesis: genesis, FromHeight: n.voteChainBindingHeight}
}
