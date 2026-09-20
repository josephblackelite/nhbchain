package state

import (
	"fmt"

	"nhbchain/consensus/potso/evidence"
)

var (
	// potsoEvidenceIndexKey holds the bounded index of live evidence records
	// (see PotsoEvidenceIndexEntry). It is a new key, not a re-use of the
	// earlier lists, so that a layout that identifies a report by its offense
	// can never be read as one that identifies it by its hash alone.
	potsoEvidenceIndexKey = []byte("potso/evidence/index/v2")
	// potsoPenaltyAppliedPrefix keys the record that one offense's penalty has
	// already been applied (see PotsoPenaltyApplied).
	potsoPenaltyAppliedPrefix = []byte("potso/penalty/applied/")
)

// PotsoEvidenceIndexEntry summarises one live evidence record. The per-block
// evidence pass reads only this list to prune expired records, skip ones whose
// penalty is already applied, count a reporter's records and recognise an offense
// that is already recorded, so it never has to load and decode every stored
// record just to learn there is nothing to do.
type PotsoEvidenceIndexEntry struct {
	// Hash is the report's canonical hash: the key its record is stored under.
	Hash [32]byte
	// Offense is the identity of what the report accuses the offender of (see
	// evidence.Offense). One offense has one entry, whatever its reporters wrote
	// into their reports, and its penalty is recorded under this key.
	Offense  [32]byte
	Offender [20]byte
	Reporter [20]byte
	// AnchorHeight is the height whose age decides when the entry expires: the
	// height the offense was committed at (see evidence.Offense.Height). The
	// entry, and the record of the penalty applied for it, are kept while that
	// height is inside the evidence window, which is exactly as long as a report
	// of the offense can still be submitted.
	AnchorHeight uint64
}

func potsoPenaltyAppliedKey(offense [32]byte, offender [20]byte) []byte {
	buf := make([]byte, 0, len(potsoPenaltyAppliedPrefix)+len(offense)+len(offender))
	buf = append(buf, potsoPenaltyAppliedPrefix...)
	buf = append(buf, offense[:]...)
	buf = append(buf, offender[:]...)
	return buf
}

// PotsoEvidenceIndex returns the live evidence records, oldest-recorded first.
// The list is bounded by evidence.MaxPendingRecords: PotsoEvidencePutRecord
// refuses to grow it past that and PotsoEvidencePrune drops records that have
// aged out of the evidence window.
func (m *Manager) PotsoEvidenceIndex() ([]PotsoEvidenceIndexEntry, error) {
	var entries []PotsoEvidenceIndexEntry
	if _, err := m.KVGet(potsoEvidenceIndexKey, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// putPotsoEvidenceIndex stores entries, or removes the key entirely when the
// list is empty so an emptied index leaves no residue in state.
func (m *Manager) putPotsoEvidenceIndex(entries []PotsoEvidenceIndexEntry) error {
	if len(entries) == 0 {
		return m.KVDelete(potsoEvidenceIndexKey)
	}
	return m.KVPut(potsoEvidenceIndexKey, entries)
}

// PotsoEvidencePutRecord persists a newly accepted misbehaviour report and
// adds it to the evidence index (see PotsoEvidenceIndex). Callers must confirm
// via PotsoEvidenceGetRecord that the hash is not already recorded, and via the
// index that the report's offense (evidence.Record.Offense) is not, before
// calling this -- it refuses a duplicate of either rather than deciding what an
// already-recorded report means, which stays with the caller. It returns
// evidence.ErrIndexFull rather than growing the index past
// evidence.MaxPendingRecords.
func (m *Manager) PotsoEvidencePutRecord(record *evidence.Record) error {
	if record == nil {
		return fmt.Errorf("potso: evidence record must not be nil")
	}
	entries, err := m.PotsoEvidenceIndex()
	if err != nil {
		return err
	}
	if len(entries) >= evidence.MaxPendingRecords {
		return evidence.ErrIndexFull
	}
	offense := record.Offense()
	for _, existing := range entries {
		if existing.Hash == record.Hash {
			return fmt.Errorf("potso: evidence %x is already indexed", record.Hash)
		}
		if existing.Offense == offense.Key {
			return fmt.Errorf("potso: the offense of evidence %x is already indexed as evidence %x", record.Hash, existing.Hash)
		}
	}
	if err := m.KVPut(potsoEvidenceRecordKey(record.Hash), record); err != nil {
		return err
	}
	entries = append(entries, PotsoEvidenceIndexEntry{
		Hash:         record.Hash,
		Offense:      offense.Key,
		Offender:     record.Evidence.Offender,
		Reporter:     record.Evidence.Reporter,
		AnchorHeight: offense.Height,
	})
	return m.putPotsoEvidenceIndex(entries)
}

// PotsoEvidencePendingHashes returns the hash of every live evidence record,
// oldest-recorded first. Records are removed once they age out of the evidence
// window (see PotsoEvidencePrune), not merely once their penalty is applied:
// PotsoPenaltyApplied is what makes re-processing an applied record a no-op.
func (m *Manager) PotsoEvidencePendingHashes() ([][32]byte, error) {
	entries, err := m.PotsoEvidenceIndex()
	if err != nil {
		return nil, err
	}
	hashes := make([][32]byte, len(entries))
	for i, entry := range entries {
		hashes[i] = entry.Hash
	}
	return hashes, nil
}

// PotsoEvidencePrune removes every record whose anchor height (the height of the
// offense it reports; the oldest referenced height for a report of any other
// kind) is below fromHeight, together with its applied-penalty record, and
// returns the entries that remain. fromHeight is the start of the evidence
// window: the same predicate evidence.ValidateEvidence uses to refuse a report
// as expired, so once a record is pruned nothing that reports its offense can be
// submitted again and its penalty can never be applied twice. It writes nothing
// when no record has expired.
func (m *Manager) PotsoEvidencePrune(fromHeight uint64) ([]PotsoEvidenceIndexEntry, error) {
	entries, err := m.PotsoEvidenceIndex()
	if err != nil {
		return nil, err
	}
	kept := make([]PotsoEvidenceIndexEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.AnchorHeight >= fromHeight {
			kept = append(kept, entry)
			continue
		}
		if err := m.KVDelete(potsoEvidenceRecordKey(entry.Hash)); err != nil {
			return nil, err
		}
		if err := m.KVDelete(potsoPenaltyAppliedKey(entry.Offense, entry.Offender)); err != nil {
			return nil, err
		}
	}
	if len(kept) == len(entries) {
		return entries, nil
	}
	if err := m.putPotsoEvidenceIndex(kept); err != nil {
		return nil, err
	}
	return kept, nil
}

// PotsoPenaltyApplied reports whether the penalty for an offense -- the identity
// evidence.Record.Offense gives a report -- has already been applied to offender.
// It is keyed by the offense and not by the hash of whichever report brought it
// to light, so no other way of writing the same accusation can be penalised
// again. It lives in the state trie next to the evidence set itself, so it is
// versioned with the block that applied it: an execution whose state copy is
// discarded leaves no record behind, and every node that reaches a block sees
// the same answer.
func (m *Manager) PotsoPenaltyApplied(offense [32]byte, offender [20]byte) (bool, error) {
	return m.KVGet(potsoPenaltyAppliedKey(offense, offender), nil)
}

// PotsoPenaltyMarkApplied records that the penalty for offense has been applied
// to offender. It is removed with the evidence record when that ages out.
func (m *Manager) PotsoPenaltyMarkApplied(offense [32]byte, offender [20]byte) error {
	return m.KVPut(potsoPenaltyAppliedKey(offense, offender), true)
}
