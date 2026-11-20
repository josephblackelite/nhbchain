package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// The chain id inside a signed escrow payload (ledger PL-EXT-07).
//
// A delegated escrow create, release, refund or dispute is authorized by a signature
// over the payload bytes: {escrowId, action, reason} for the three actions, the create
// fields for a create, and {escrowId, outcome, policyNonce, metadata} for the decision
// of an arbitration quorum. None of them names a network, and an escrow id is a hash of
// payer, payee, meta hash and nonce, so the same escrow held on two networks (a mainnet
// and a testnet, or the abandoned chains that run this code) has the same id on both
// and a signature made for one verifies on the other: a release signed on a test
// network releases the real escrow, and a create signed there creates one.
//
// From EscrowEnvelopeChainBindingActivationHeight (zero = off, the live value) each of
// these payloads must carry the network's own chain id as "chainId", a decimal string
// (the id nhb_getNetworkStats reports as networkChainId, StateProcessor.swapVoucherChainID),
// and one that does not, or that carries another network's, is refused before the
// escrow engine looks at it. The signature covers the payload bytes, so the chain id is
// signed with the rest and cannot be added by a relayer. Below the height the payloads
// are accepted exactly as before, which is what every committed block holds. An unset
// network id (zero, as in a test harness that never wired one) accepts nothing from
// the height on: failing closed is the safe side.
//
// The milestone project signatures (native/escrow/engine_milestone_signature.go) are
// not covered: nothing in this tree reaches them but tests, no transaction type and no
// RPC method.

var errEscrowEnvelopeChain = errors.New("escrow: signed payload does not carry this network's chain id")

// requireEscrowEnvelopeChain refuses a signed escrow payload that does not carry the
// network's chain id, from the activation height on. Below it, and with the rule off,
// it accepts every payload.
func (sp *StateProcessor) requireEscrowEnvelopeChain(payload []byte) error {
	if sp == nil || !ruleActiveAt(sp.validatorRules.EscrowEnvelopeChainBindingActivationHeight, sp.blockHeight()) {
		return nil
	}
	if sp.swapVoucherChainID == 0 {
		return fmt.Errorf("%w: the network's chain id is not configured", errEscrowEnvelopeChain)
	}
	var envelope struct {
		ChainID *string `json:"chainId"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.ChainID == nil {
		return fmt.Errorf("%w: the signed payload must carry chainId as a decimal string", errEscrowEnvelopeChain)
	}
	if want := strconv.FormatUint(sp.swapVoucherChainID, 10); *envelope.ChainID != want {
		return fmt.Errorf("%w: chainId is %q, this network's is %q", errEscrowEnvelopeChain, *envelope.ChainID, want)
	}
	return nil
}
