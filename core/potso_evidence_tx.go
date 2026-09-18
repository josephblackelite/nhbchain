package core

import (
	"fmt"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
)

// encodeSubmitEvidenceTransaction serialises ev into the canonical Data
// payload for a TxTypeSubmitEvidence transaction. evidence.Evidence is
// already RLP round-trippable -- consensus/potso/evidence/store.go's own
// Record (which embeds an Evidence value) has always been persisted this
// way -- so no separate wire struct is introduced here.
func encodeSubmitEvidenceTransaction(ev evidence.Evidence) ([]byte, error) {
	return rlp.EncodeToBytes(&ev)
}

// decodeSubmitEvidenceTransaction reconstructs the Evidence payload from a
// TxTypeSubmitEvidence transaction's Data field.
func decodeSubmitEvidenceTransaction(data []byte) (evidence.Evidence, error) {
	var ev evidence.Evidence
	if len(data) == 0 {
		return ev, fmt.Errorf("potsoSubmitEvidence: payload required")
	}
	if err := rlp.DecodeBytes(data, &ev); err != nil {
		return ev, fmt.Errorf("potsoSubmitEvidence: decode payload: %w", err)
	}
	return ev, nil
}

// applySubmitEvidenceTransaction deterministically executes a
// TxTypeSubmitEvidence transaction. It is the consensus-side counterpart of
// the retired direct-write half of Node.PotsoSubmitEvidence (NHB-AUDIT-C10
// follow-up) -- see that TxType's doc comment in core/types/transaction.go
// for the full rationale.
//
// This function deliberately does exactly two things: validate the evidence
// via the existing, unmodified evidence.ValidateEvidence gate (the same
// signature-over-conflicting-votes check consensus/potso/penalty relies on),
// and -- if valid and not already recorded -- persist it into the state
// trie via nhbstate.Manager, so every validator that applies this
// transaction ends up with byte-identical evidence state. It does NOT
// compute or apply any slashing/weight penalty itself; that remains
// core/node.go's processPendingEvidenceForState, called only from the real
// block-lifecycle sites (CreateBlock/ValidateBlock/CommitBlock), which now
// reads its pending set from this trie-backed record instead of the old
// node-local evidence.Store. Keeping penalty application out of this
// function matters because ApplyTransaction (and therefore this function)
// is also invoked by ordinary mempool-admission simulation
// (Node.validateTransaction), which must never have an observable,
// non-rollback-able side effect for a transaction that may never actually
// be included in any committed block -- unlike this function's trie writes
// (safely scoped to whichever state copy is executing, discarded if that
// copy is abandoned), state/potso.Ledger's weight and idempotency
// bookkeeping is a single Node-wide in-memory structure with no such
// scoping.
func (sp *StateProcessor) applySubmitEvidenceTransaction(tx *types.Transaction) error {
	if tx == nil {
		return fmt.Errorf("potsoSubmitEvidence: transaction required")
	}
	if err := nativecommon.Guard(sp.pauses, modulePotso); err != nil {
		return err
	}
	ev, err := decodeSubmitEvidenceTransaction(tx.Data)
	if err != nil {
		return err
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		return &evidence.ValidationError{Reason: evidence.RejectReasonInvalidType, Message: err.Error()}
	}

	// heightLookup is intentionally nil here: evidence.ValidateEvidence's
	// future-height and max-age checks already bound Heights deterministically
	// against currentHeight (the block this transaction is being applied
	// into -- identical on every validator that applies this same block),
	// with no need to consult this node's own chain/block index. The old RPC
	// path's heightLookup (n.chain.GetBlockByHeight) offered an extra, purely
	// defensive "does this height actually exist" check; height existence is
	// already implied by height <= currentHeight on a single, height-indexed
	// chain with no forks, so omitting it here trades a redundant check for
	// keeping this function free of any dependency on node-local chain state.
	currentHeight := sp.blockHeight()
	if verr := evidence.ValidateEvidence(&ev, hash, currentHeight, evidence.DefaultMaxAgeBlocks, nil); verr != nil {
		return verr
	}

	manager := nhbstate.NewManager(sp.Trie)
	if _, exists, err := manager.PotsoEvidenceGetRecord(hash); err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load existing record: %w", err)
	} else if exists {
		// Idempotent resubmission of already-recorded evidence -- a safe
		// no-op, matching the old evidence.Store.Put's exact "already
		// exists" contract.
		return nil
	}

	record := &evidence.Record{Hash: hash, Evidence: ev.Clone(), ReceivedAt: sp.blockTimestamp().Unix()}
	if err := manager.PotsoEvidencePutRecord(record); err != nil {
		return fmt.Errorf("potsoSubmitEvidence: persist record: %w", err)
	}

	minHeight := record.MinHeight()
	if evt := (events.PotsoEvidenceAccepted{
		Hash:         hash,
		EvidenceType: string(ev.Type),
		Offender:     ev.Offender,
		Height:       minHeight,
		Reporter:     ev.Reporter,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}
