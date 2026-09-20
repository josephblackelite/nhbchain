package rpc

import "net/http"

// reputationRPCDisabledMessage: reputation_verifySkill recorded a skill
// attestation in the live state trie of the one validator that handled the
// call, outside block execution, and it took the verifier's address from the
// request body: the bearer token authenticated the caller to the node, but
// nothing proved the caller held the verifier key. No other validator saw the
// write, so it made that validator's pending state differ from theirs and left
// it to fork the next block it proposed. Disabled, like the other RPC mutators
// that wrote validator-local state (see escrowRPCDisabledMessage and
// stakeRPCDisabledMessage). Node.ReputationVerifySkill is left in place for its
// tests; no RPC calls it.
const reputationRPCDisabledMessage = "this method is disabled -- it wrote validator-local state outside the block pipeline on the strength of an unsigned verifier address, so one validator could disagree with the others about the next block; a signed-transaction replacement is pending"

func (s *Server) handleReputationVerifySkill(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, reputationRPCDisabledMessage, nil)
}
