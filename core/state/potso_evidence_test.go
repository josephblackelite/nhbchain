package state

import (
	"errors"
	"testing"

	"nhbchain/consensus/potso/evidence"
)

// indexedRecord builds the record for one DOWNTIME report that references the
// single height given, distinct per n.
func indexedRecord(t *testing.T, n int, height uint64) *evidence.Record {
	t.Helper()
	ev := evidence.Evidence{
		Type:     evidence.TypeDowntime,
		Offender: [20]byte{0xA0, byte(n >> 8), byte(n)},
		Heights:  []uint64{height},
		Reporter: [20]byte{0xB0, byte(n >> 8), byte(n)},
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	return &evidence.Record{Hash: hash, Evidence: ev, ReceivedAt: 1}
}

func TestPotsoEvidenceIndexSummarisesEachRecord(t *testing.T) {
	manager := newTestManager(t)
	first, second := indexedRecord(t, 1, 40), indexedRecord(t, 2, 30)
	for _, record := range []*evidence.Record{first, second} {
		if err := manager.PotsoEvidencePutRecord(record); err != nil {
			t.Fatalf("put record: %v", err)
		}
	}
	entries, err := manager.PotsoEvidenceIndex()
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for i, record := range []*evidence.Record{first, second} {
		want := PotsoEvidenceIndexEntry{
			Hash:      record.Hash,
			Offender:  record.Evidence.Offender,
			Reporter:  record.Evidence.Reporter,
			MinHeight: record.MinHeight(),
		}
		if entries[i] != want {
			t.Fatalf("entry %d: got %+v, want %+v", i, entries[i], want)
		}
	}
	hashes, err := manager.PotsoEvidencePendingHashes()
	if err != nil {
		t.Fatalf("pending hashes: %v", err)
	}
	if len(hashes) != 2 || hashes[0] != first.Hash || hashes[1] != second.Hash {
		t.Fatalf("expected the hashes oldest-recorded first, got %x", hashes)
	}
	if _, ok, err := manager.PotsoEvidenceGetRecord(second.Hash); err != nil || !ok {
		t.Fatalf("expected the record to be retrievable: ok=%v err=%v", ok, err)
	}
}

func TestPotsoEvidencePutRecordIsHardBounded(t *testing.T) {
	manager := newTestManager(t)
	for i := 0; i < evidence.MaxPendingRecords; i++ {
		if err := manager.PotsoEvidencePutRecord(indexedRecord(t, i, 10)); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	over := indexedRecord(t, evidence.MaxPendingRecords, 10)
	before := manager.trie.Hash()
	if err := manager.PotsoEvidencePutRecord(over); !errors.Is(err, evidence.ErrIndexFull) {
		t.Fatalf("expected evidence.ErrIndexFull past the bound, got %v", err)
	}
	if manager.trie.Hash() != before {
		t.Fatalf("a refused record must leave state untouched")
	}
	if _, ok, _ := manager.PotsoEvidenceGetRecord(over.Hash); ok {
		t.Fatalf("a refused record must not be stored")
	}
}

func TestPotsoEvidencePutRecordRefusesADuplicate(t *testing.T) {
	manager := newTestManager(t)
	record := indexedRecord(t, 1, 10)
	if err := manager.PotsoEvidencePutRecord(record); err != nil {
		t.Fatalf("put record: %v", err)
	}
	if err := manager.PotsoEvidencePutRecord(record); err == nil {
		t.Fatalf("expected a second put of the same record to be refused")
	}
	if hashes, _ := manager.PotsoEvidencePendingHashes(); len(hashes) != 1 {
		t.Fatalf("expected the index to hold the record once, got %d", len(hashes))
	}
}

// TestPotsoEvidencePruneDropsExpiredRecordsAndTheirPenaltyMarks: a record whose
// oldest height is below the window start goes, with its applied-penalty record;
// one exactly at the start stays; and once everything has gone no trace is left
// in state.
func TestPotsoEvidencePruneDropsExpiredRecordsAndTheirPenaltyMarks(t *testing.T) {
	manager := newTestManager(t)
	empty := manager.trie.Hash()

	old, edge, recent := indexedRecord(t, 1, 100), indexedRecord(t, 2, 101), indexedRecord(t, 3, 500)
	for _, record := range []*evidence.Record{old, edge, recent} {
		if err := manager.PotsoEvidencePutRecord(record); err != nil {
			t.Fatalf("put record: %v", err)
		}
		if err := manager.PotsoPenaltyMarkApplied(record.Hash, record.Evidence.Offender); err != nil {
			t.Fatalf("mark applied: %v", err)
		}
	}

	kept, err := manager.PotsoEvidencePrune(101)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(kept) != 2 || kept[0].Hash != edge.Hash || kept[1].Hash != recent.Hash {
		t.Fatalf("expected the record at the window start and the newer one kept, in order; got %+v", kept)
	}
	if _, ok, _ := manager.PotsoEvidenceGetRecord(old.Hash); ok {
		t.Fatalf("expected the expired record deleted")
	}
	if applied, _ := manager.PotsoPenaltyApplied(old.Hash, old.Evidence.Offender); applied {
		t.Fatalf("expected the expired record's applied-penalty record deleted")
	}
	if applied, _ := manager.PotsoPenaltyApplied(edge.Hash, edge.Evidence.Offender); !applied {
		t.Fatalf("expected the kept record's applied-penalty record retained")
	}

	if _, err := manager.PotsoEvidencePrune(1_000); err != nil {
		t.Fatalf("prune everything: %v", err)
	}
	if manager.trie.Hash() != empty {
		t.Fatalf("expected no trace of the evidence left in state once every record is pruned")
	}
}

func TestPotsoEvidencePruneWritesNothingWhenNothingExpired(t *testing.T) {
	manager := newTestManager(t)
	if _, err := manager.PotsoEvidencePrune(1_000); err != nil {
		t.Fatalf("prune empty: %v", err)
	}
	if err := manager.PotsoEvidencePutRecord(indexedRecord(t, 1, 500)); err != nil {
		t.Fatalf("put record: %v", err)
	}
	before := manager.trie.Hash()
	kept, err := manager.PotsoEvidencePrune(500)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(kept) != 1 || manager.trie.Hash() != before {
		t.Fatalf("expected a prune that removes nothing to leave state untouched")
	}
}

// TestPotsoPenaltyAppliedIsVersionedWithState: the applied-penalty record is
// ordinary trie state, so a state copy that is thrown away takes its records
// with it and the state it was copied from never sees them.
func TestPotsoPenaltyAppliedIsVersionedWithState(t *testing.T) {
	manager := newTestManager(t)
	hash, offender := [32]byte{1}, [20]byte{2}

	copied, err := manager.trie.Copy()
	if err != nil {
		t.Fatalf("copy trie: %v", err)
	}
	scratch := NewManager(copied)
	if err := scratch.PotsoPenaltyMarkApplied(hash, offender); err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	if applied, _ := scratch.PotsoPenaltyApplied(hash, offender); !applied {
		t.Fatalf("expected the record visible in the copy that wrote it")
	}
	if applied, _ := manager.PotsoPenaltyApplied(hash, offender); applied {
		t.Fatalf("a record written in a discarded copy must not be visible in the original state")
	}
	if applied, _ := manager.PotsoPenaltyApplied(hash, [20]byte{3}); applied {
		t.Fatalf("a record is per (hash, offender)")
	}
}
