package state

import (
	"fmt"

	"nhbchain/consensus/potso/evidence"
)

var (
	// potsoEvidenceIndexKey holds the bounded index of live evidence records
	// (see PotsoEvidenceIndexEntry). It is a new key, not a re-use of the
	// earlier hash-only list, so the two layouts can never be confused.
	potsoEvidenceIndexKey = []byte("potso/evidence/index")
	// potsoPenaltyAppliedPrefix keys the record that one evidence report's
	// penalty has already been applied (see PotsoPenaltyApplied).
	potsoPenaltyAppliedPrefix = []byte("potso/penalty/applied/")
)

// PotsoEvidenceIndexEntry summarises one live evidence record. The per-block
// evidence pass reads only this list to prune expired records, skip ones whose
// penalty is already applied and count a reporter's records, so it never has to
// load and decode every stored record just to learn there is nothing to do.
type PotsoEvidenceIndexEntry struct {
	Hash      [32]byte
	Offender  [20]byte
	Reporter  [20]byte
	MinHeight uint64
}

func potsoPenaltyAppliedKey(hash [32]byte, offender [20]byte) []byte {
	buf := make([]byte, 0, len(potsoPenaltyAppliedPrefix)+len(hash)+len(offender))
	buf = append(buf, potsoPenaltyAppliedPrefix...)
	buf = append(buf, hash[:]...)
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
// via PotsoEvidenceGetRecord that the hash is not already recorded before
// calling this -- it refuses a duplicate rather than deciding what an
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
	for _, existing := range entries {
		if existing.Hash == record.Hash {
			return fmt.Errorf("potso: evidence %x is already indexed", record.Hash)
		}
	}
	if err := m.KVPut(potsoEvidenceRecordKey(record.Hash), record); err != nil {
		return err
	}
	entries = append(entries, PotsoEvidenceIndexEntry{
		Hash:      record.Hash,
		Offender:  record.Evidence.Offender,
		Reporter:  record.Evidence.Reporter,
		MinHeight: record.MinHeight(),
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

// PotsoEvidencePrune removes every record whose oldest referenced height is
// below fromHeight, together with its applied-penalty record, and returns the
// entries that remain. fromHeight is the start of the evidence window: the
// same predicate evidence.ValidateEvidence uses to refuse a report as expired,
// so a pruned record can never be submitted again and its penalty can never be
// applied twice. It writes nothing when no record has expired.
func (m *Manager) PotsoEvidencePrune(fromHeight uint64) ([]PotsoEvidenceIndexEntry, error) {
	entries, err := m.PotsoEvidenceIndex()
	if err != nil {
		return nil, err
	}
	kept := make([]PotsoEvidenceIndexEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.MinHeight >= fromHeight {
			kept = append(kept, entry)
			continue
		}
		if err := m.KVDelete(potsoEvidenceRecordKey(entry.Hash)); err != nil {
			return nil, err
		}
		if err := m.KVDelete(potsoPenaltyAppliedKey(entry.Hash, entry.Offender)); err != nil {
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

// PotsoPenaltyApplied reports whether the penalty for the evidence report with
// this canonical hash has already been applied to offender. It lives in the
// state trie next to the evidence set itself, so it is versioned with the block
// that applied it: an execution whose state copy is discarded leaves no record
// behind, and every node that reaches a block sees the same answer.
func (m *Manager) PotsoPenaltyApplied(hash [32]byte, offender [20]byte) (bool, error) {
	return m.KVGet(potsoPenaltyAppliedKey(hash, offender), nil)
}

// PotsoPenaltyMarkApplied records that the penalty for hash has been applied to
// offender. It is removed with the evidence record when that ages out.
func (m *Manager) PotsoPenaltyMarkApplied(hash [32]byte, offender [20]byte) error {
	return m.KVPut(potsoPenaltyAppliedKey(hash, offender), true)
}
