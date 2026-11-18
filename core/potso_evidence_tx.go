package core

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/consensus/potso/penalty"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	statebank "nhbchain/state/bank"
	statepotso "nhbchain/state/potso"
)

var (
	// ErrEvidenceInvalidPayload means the transaction's Data did not decode as
	// an evidence report -- a pure function of the transaction's own bytes, so
	// classifyProposalError prunes it.
	ErrEvidenceInvalidPayload = errors.New("potsoSubmitEvidence: invalid payload")
	// ErrEvidenceReporterMismatch means Evidence.Reporter is not the account
	// that signed the transaction. Also a pure function of the transaction.
	ErrEvidenceReporterMismatch = errors.New("potsoSubmitEvidence: reporter must be the transaction signer")
	// ErrEvidenceAlreadyRecorded means this report, or another report of the
	// same offense (see evidence.Record.Offense: for an equivocation the double
	// sign at one offender, height, round and vote type, however the proof is
	// written and whichever other heights the report lists), is already in
	// state. It can never become new again: a record only leaves state once the
	// height of its offense is too old for any report of it to be submitted, so
	// the transaction is permanently dead.
	ErrEvidenceAlreadyRecorded = errors.New("potsoSubmitEvidence: evidence already recorded")
	// ErrEvidenceReporterNotBonded means the reporter holds less than the
	// minimum validator stake bonded. The reporter can bond more later, so
	// classifyProposalError skips rather than prunes it.
	ErrEvidenceReporterNotBonded = errors.New("potsoSubmitEvidence: reporter must have at least the minimum validator stake bonded")
	// ErrEvidenceTypeNotAccepted means the report is of a kind the intake no longer
	// takes (DOWNTIME, INVALID_BLOCK_PROPOSAL: nothing proves them and processing them
	// does nothing). Decided by the report alone, so classifyProposalError prunes it.
	// Only from EvidenceIntakeGuardActivationHeight (PL-R5T-09).
	ErrEvidenceTypeNotAccepted = errors.New("potsoSubmitEvidence: only equivocation reports are accepted")
	// ErrEvidenceOffenderNotBonded means the offender of an equivocation report holds
	// less than the minimum validator stake in staking stake (plus its own pending
	// unbonds), so a proof made against a throwaway key is refused. The offender can
	// bond more later, so classifyProposalError skips rather than prunes it. Only from
	// EvidenceIntakeGuardActivationHeight (PL-R5T-09).
	ErrEvidenceOffenderNotBonded = errors.New("potsoSubmitEvidence: offender must hold at least the minimum validator stake")
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
		return ev, fmt.Errorf("%w: payload required", ErrEvidenceInvalidPayload)
	}
	if err := rlp.DecodeBytes(data, &ev); err != nil {
		return ev, fmt.Errorf("%w: decode: %v", ErrEvidenceInvalidPayload, err)
	}
	return ev, nil
}

// evidenceWindowStart is the oldest height evidence may reference at
// currentHeight: evidence.ValidateEvidence refuses anything older as expired,
// and the evidence index drops records whose offense is older (see
// evidence.Offense.Height).
func evidenceWindowStart(currentHeight uint64) uint64 {
	if currentHeight > evidence.DefaultMaxAgeBlocks {
		return currentHeight - evidence.DefaultMaxAgeBlocks
	}
	return 0
}

// applySubmitEvidenceTransaction deterministically executes a
// TxTypeSubmitEvidence transaction. It is the consensus-side counterpart of
// the retired direct-write half of Node.PotsoSubmitEvidence (NHB-AUDIT-C10
// follow-up) -- see that TxType's doc comment in core/types/transaction.go
// for the full rationale.
//
// The transaction is an ordinary signed native transaction: sender is the
// recovered signer, its nonce is checked before this runs and consumed here,
// and the sender must be the report's own Reporter and have at least the
// minimum validator stake bonded. Reporting is therefore never free or
// anonymous, and the bounds below (evidence.MaxPendingRecords,
// evidence.MaxRecordsPerReporter) are what keep the evidence set from growing
// without limit.
//
// Beyond that this function does exactly two things: validate the evidence
// via evidence.ValidateEvidence (the signature-over-conflicting-votes check
// consensus/potso/penalty relies on, the binding of that proof to the heights
// the report lists, plus the payload size bounds), and -- if valid and not
// already recorded -- persist it into the state trie via nhbstate.Manager, so
// every validator that applies this transaction ends up with byte-identical
// evidence state. "Already recorded" is decided by the report's offense (see
// evidence.Record.Offense), not by its hash: the hash covers the heights and
// the exact bytes the reporter chose to write, so one double-sign written a
// second way (the two votes swapped, the JSON laid out differently, another
// height listed beside it) has another hash and would otherwise be a fresh
// report, slashed again for stake the offender bonded since. It does NOT
// compute or apply any slashing/weight penalty itself; that is
// processPendingEvidence below, run from the real block-lifecycle sites
// (CreateBlock/ValidateBlock/CommitBlock). Keeping penalty application out of
// this function matters because ApplyTransaction (and therefore this function)
// is also invoked by ordinary mempool-admission simulation
// (Node.validateTransaction), whose result is thrown away with its state copy.
func (sp *StateProcessor) applySubmitEvidenceTransaction(tx *types.Transaction, sender []byte) error {
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
	if !bytes.Equal(ev.Reporter[:], sender) {
		return ErrEvidenceReporterMismatch
	}
	// From the activation height only an equivocation report is taken: the other kinds
	// carry no proof and do nothing when processed, yet each fills one of the 256 slots
	// (PL-R5T-09). Decided by the report alone.
	guard := ruleActiveAt(sp.validatorRules.EvidenceIntakeGuardActivationHeight, sp.blockHeight())
	if guard && ev.Type != evidence.TypeEquivocation {
		return ErrEvidenceTypeNotAccepted
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
	//
	// For an equivocation the same call also requires the proof's own height to
	// be one of the heights listed and not above currentHeight. That is what
	// makes the window checks apply to the offense itself: a proof for an old
	// double-sign cannot be carried by a report that lists a recent height.
	currentHeight := sp.blockHeight()
	if verr := evidence.ValidateEvidenceBound(&ev, hash, currentHeight, evidence.DefaultMaxAgeBlocks, nil, sp.voteBinding()); verr != nil {
		return verr
	}

	if guard {
		if err := sp.requireEvidenceOffenderBond(ev.Offender); err != nil {
			return err
		}
	}
	if err := sp.requireEvidenceReporterBond(sender, guard); err != nil {
		return err
	}

	manager := nhbstate.NewManager(sp.Trie)
	// Records that have aged out of the window no longer count against the
	// bounds below. Pruning here (it writes nothing when nothing expired)
	// rather than only at the end of the block means a full index frees its
	// room in the very block its records expire.
	entries, err := manager.PotsoEvidencePrune(evidenceWindowStart(currentHeight))
	if err != nil {
		return fmt.Errorf("potsoSubmitEvidence: prune expired records: %w", err)
	}
	if _, exists, err := manager.PotsoEvidenceGetRecord(hash); err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load existing record: %w", err)
	} else if exists {
		// A duplicate report can never become a first report again, so it is
		// refused rather than accepted as an empty transaction that would
		// still occupy block space and burn a nonce.
		return ErrEvidenceAlreadyRecorded
	}
	record := &evidence.Record{Hash: hash, Evidence: ev.Clone(), ReceivedAt: sp.blockTimestamp().Unix()}
	// The same offense reported another way is a duplicate too, and equally
	// dead for good: the record of an offense is kept until the height it was
	// committed at leaves the window, and a report can be submitted only while
	// that height (which it must list) is inside it. This is checked before the
	// reporter's quota, so a duplicate is refused as a duplicate.
	offense := record.Offense()
	held := 0
	for _, entry := range entries {
		if entry.Offense == offense.Key {
			return fmt.Errorf("%w: the same offense is already held as report %x", ErrEvidenceAlreadyRecorded, entry.Hash[:8])
		}
		if entry.Reporter == ev.Reporter {
			held++
		}
	}
	if held >= evidence.MaxRecordsPerReporter {
		return evidence.ErrReporterQuota
	}

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
	return sp.incrementNativeAccountNonce(sender)
}

// requireEvidenceReporterBond refuses a reporter that does not have at least
// the governed minimum validator stake of its own ZNHB bonded (LockedZNHB: the
// reporter's own locked stake, not stake other accounts delegated to it).
//
// With stakeBond (from EvidenceIntakeGuardActivationHeight, PL-R5T-09) the bond is the
// reporter's staking Stake instead: LockedZNHB is also raised by a point-of-sale hold,
// which can be voided at once, so one balance could bond many reporters in turn.
func (sp *StateProcessor) requireEvidenceReporterBond(reporter []byte, stakeBond bool) error {
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load minimum stake: %w", err)
	}
	account, err := sp.getAccount(reporter)
	if err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load reporter account: %w", err)
	}
	bond := account.LockedZNHB
	if stakeBond {
		bond = account.Stake
	}
	if bond == nil || bond.Cmp(minStake) < 0 {
		return ErrEvidenceReporterNotBonded
	}
	return nil
}

// requireEvidenceOffenderBond refuses an equivocation report whose offender holds less
// than the governed minimum validator stake as staking Stake plus its own pending
// unbonds (the part of its bond a slash can still reach, see
// SlashPendingUnbondsActivationHeight). The proof is only a signature check, so without
// this a throwaway key can be "caught" as often as anyone likes.
func (sp *StateProcessor) requireEvidenceOffenderBond(offender [20]byte) error {
	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load minimum stake: %w", err)
	}
	account, err := sp.getAccount(offender[:])
	if err != nil {
		return fmt.Errorf("potsoSubmitEvidence: load offender account: %w", err)
	}
	bond := new(big.Int)
	if account.Stake != nil {
		bond.Set(account.Stake)
	}
	bond.Add(bond, ownPendingUnbonds(account, offender))
	if bond.Cmp(minStake) < 0 {
		return ErrEvidenceOffenderNotBonded
	}
	return nil
}

// processPendingEvidence applies the penalty for every evidence record held in
// state whose penalty has not been applied yet, and drops the records that have
// aged out of the evidence window. It reads and writes only sp's own state:
// which penalties are applied is a trie record (see Manager.PotsoPenaltyApplied)
// and the weight ledger it needs is built fresh for each call from the
// offender's on-chain stake, so a build, a validation, a commit and a later
// replay of the same block all compute the same result, and an execution whose
// state copy is discarded leaves nothing behind. With no evidence recorded it
// reads one key and writes nothing.
//
// Each report's penalty is applied in the block that records it (the evidence
// transactions run first, this runs after them), so a record is never left
// waiting; the per-block cost is bounded by evidence.MaxPendingRecords.
func (sp *StateProcessor) processPendingEvidence(currentHeight uint64) error {
	manager := nhbstate.NewManager(sp.Trie)
	entries, err := manager.PotsoEvidencePrune(evidenceWindowStart(currentHeight))
	if err != nil {
		return fmt.Errorf("prune evidence: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	cfg := penalty.DefaultConfig()
	cfg.SlashEnabled = true
	cfg.EquivocationSlashBps = 10000 // 100% slashing on equivocation

	catalog, err := penalty.BuildCatalog(cfg)
	if err != nil {
		return fmt.Errorf("build penalty catalog: %w", err)
	}
	// A scratch ledger, private to this call: the tracked weight is derived
	// from the offender's stake in this state, not carried between blocks in
	// memory where a discarded execution could leak into the next one.
	ledger, err := statepotso.NewLedger(nil, nil)
	if err != nil {
		return fmt.Errorf("potso: weight ledger: %w", err)
	}
	// The forfeited stake lands on the treasury's liquid balance. When the
	// treasury is the admin wallet that is ZNHB newly counted by
	// CheckZNHBSupplyInvariant, so bookedSlasher books the same amount into the
	// Reward Pool in the same transition. It measures the change to the wallet's
	// tracked total across the call rather than predicting what the slasher
	// does -- the slasher clamps to the offender's locked balance, and the
	// offender can itself be the admin wallet, in which case the ZNHB only moves
	// within the wallet's own total. Before the pools are bootstrapped there is
	// nothing to mirror into: EnsureZNHBPoolsBootstrapped splits whatever the
	// wallet holds at that point.
	validatorSlasher := statebank.NewValidatorSlasher(manager, sp.evidenceSlashTreasury())
	// From the activation height the slash continues into the offender's own pending
	// unbonds after its locked balance (PL-R5T-26), and is sized on the stake plus
	// those unbonds (below).
	reachUnbonds := ruleActiveAt(sp.validatorRules.SlashPendingUnbondsActivationHeight, currentHeight)
	if reachUnbonds {
		validatorSlasher = validatorSlasher.ReachingPendingUnbonds()
	}
	slasher := sp.bookedSlasher(validatorSlasher)
	engine := penalty.NewEngine(catalog, ledger, slasher).WithRecords(manager)

	for _, entry := range entries {
		// Applied penalties are recorded against the offense, not the report
		// (see evidence.Record.Offense), so this report is skipped whenever its
		// offense was penalised, whichever report did it.
		applied, err := manager.PotsoPenaltyApplied(entry.Offense, entry.Offender)
		if err != nil {
			return fmt.Errorf("load penalty record %x: %w", entry.Hash, err)
		}
		if applied {
			continue
		}
		rec, ok, err := manager.PotsoEvidenceGetRecord(entry.Hash)
		if err != nil {
			return fmt.Errorf("load evidence %x: %w", entry.Hash, err)
		}
		if !ok || rec == nil {
			continue
		}

		// NHB-AUDIT-C10: refresh this offender's tracked Base weight
		// from their REAL, current on-chain stake immediately before
		// computing any penalty against them -- without this, Base
		// silently defaults to the ledger's floor (nil -> zero in
		// production), making every slash/decay percentage compute
		// against zero regardless of actual stake or misconduct. See
		// EnsureBaseline's doc comment. A missing/unreadable account
		// is not fatal here -- it just leaves this offender's weight
		// untouched for this pass, same as before this fix existed.
		if account, acctErr := manager.GetAccount(rec.Evidence.Offender[:]); acctErr == nil && account != nil && account.Stake != nil {
			base := account.Stake
			if reachUnbonds {
				base = new(big.Int).Add(account.Stake, ownPendingUnbonds(account, rec.Evidence.Offender))
			}
			if _, err := ledger.EnsureBaseline(rec.Evidence.Offender, base); err != nil {
				return fmt.Errorf("potso: ensure baseline weight for %x: %w", rec.Evidence.Offender, err)
			}
		}
		ctx := penalty.Context{
			BlockHeight:  currentHeight,
			MissedEpochs: 0,
		}
		res, err := engine.Apply(rec, ctx)
		if err != nil {
			return fmt.Errorf("apply penalty for %x: %w", rec.Hash, err)
		}
		if !res.Idempotent && res.Event != nil {
			sp.AppendEvent(res.Event)
		}
		// The slasher debited the offender's stake without going through setAccount,
		// the one place the eligible-validator map and the validator set are
		// recomputed, so both still hold the stake it had before (PL-R5T-25). From the
		// activation height they are re-evaluated here, in the block that slashes.
		if !res.Idempotent && ruleActiveAt(sp.validatorRules.SlashEligibilityRefreshActivationHeight, currentHeight) {
			if err := sp.refreshValidatorEligibility(rec.Evidence.Offender[:]); err != nil {
				return fmt.Errorf("refresh eligibility of slashed validator %x: %w", rec.Evidence.Offender, err)
			}
		}
	}
	return nil
}

// evidenceSlashTreasury is where a slash's forfeited stake is credited. It has
// to be the same address on every validator, so it is the chain's admin wallet
// (fixed by genesis) or, on a chain that has none, a fixed module address --
// never the executing validator's own fallback treasury, which differs from one
// validator to the next.
func (sp *StateProcessor) evidenceSlashTreasury() [20]byte {
	if sp.hasAdminWallet {
		return sp.adminWallet
	}
	var addr [20]byte
	copy(addr[:], deriveModuleAddress("module/potso/slashed", crypto.ZNHBPrefix).Bytes())
	return addr
}
