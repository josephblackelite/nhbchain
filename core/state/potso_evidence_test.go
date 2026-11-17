package state

import (
	"encoding/json"
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
		// A report that carries no proof is its own offense: recorded, penalised
		// and expired by its own hash and the oldest height it lists.
		want := PotsoEvidenceIndexEntry{
			Hash:         record.Hash,
			Offense:      record.Hash,
			Offender:     record.Evidence.Offender,
			Reporter:     record.Evidence.Reporter,
			AnchorHeight: record.MinHeight(),
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
		if err := manager.PotsoPenaltyMarkApplied(record.Offense().Key, record.Evidence.Offender); err != nil {
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
	if applied, _ := manager.PotsoPenaltyApplied(old.Offense().Key, old.Evidence.Offender); applied {
		t.Fatalf("expected the expired record's applied-penalty record deleted")
	}
	if applied, _ := manager.PotsoPenaltyApplied(edge.Offense().Key, edge.Evidence.Offender); !applied {
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

// equivocationRecord builds the record of an EQUIVOCATION report against offender
// whose proof names the decision point (height, round, vote type) and whose report
// lists the given heights. Only the shape of the proof matters at this level -- the
// signatures are verified before a report is ever recorded -- so none are attached,
// and the details differ by salt, standing in for the different ways one accusation
// can be written down (and so giving each report its own hash).
func equivocationRecord(t *testing.T, offender byte, height uint64, round int, voteType evidence.EquivocationVoteType, heights []uint64, salt string) *evidence.Record {
	t.Helper()
	details, err := json.Marshal(struct {
		evidence.EquivocationProof
		Salt string `json:"salt"`
	}{
		EquivocationProof: evidence.EquivocationProof{Height: height, Round: round, VoteType: voteType},
		Salt:              salt,
	})
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}
	ev := evidence.Evidence{
		Type:     evidence.TypeEquivocation,
		Offender: [20]byte{0xC0, offender},
		Heights:  heights,
		Details:  details,
		Reporter: [20]byte{0xD0, offender},
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	return &evidence.Record{Hash: hash, Evidence: ev, ReceivedAt: 1}
}

// TestPotsoEvidenceIndexRecordsAnEquivocationByItsOffense: the entry of an
// equivocation report carries the identity of the double-sign it accuses the
// offender of and the height the double-sign happened at -- not the report's own
// hash and the oldest height the report happens to list.
func TestPotsoEvidenceIndexRecordsAnEquivocationByItsOffense(t *testing.T) {
	manager := newTestManager(t)
	record := equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{50, 300}, "a")
	if record.Offense().Key == record.Hash {
		t.Fatalf("an equivocation report's offense must not be its hash")
	}
	if got := record.Offense().Height; got != 300 {
		t.Fatalf("expected the offense at the proof's height 300, got %d", got)
	}
	if err := manager.PotsoEvidencePutRecord(record); err != nil {
		t.Fatalf("put record: %v", err)
	}
	entries, err := manager.PotsoEvidenceIndex()
	if err != nil || len(entries) != 1 {
		t.Fatalf("index: %v (%d entries)", err, len(entries))
	}
	want := PotsoEvidenceIndexEntry{
		Hash:         record.Hash,
		Offense:      record.Offense().Key,
		Offender:     record.Evidence.Offender,
		Reporter:     record.Evidence.Reporter,
		AnchorHeight: 300,
	}
	if entries[0] != want {
		t.Fatalf("got %+v, want %+v", entries[0], want)
	}
	if record.MinHeight() != 50 {
		t.Fatalf("test premise: the report lists an older height than its offense, got min %d", record.MinHeight())
	}
}

// TestPotsoEvidencePutRecordRefusesASecondReportOfOneOffense: a report with another
// hash -- other bytes in the details, other heights listed beside the proof's --
// but the same offender, height, round and vote type is the same offense, and is
// refused rather than indexed beside the first. A different round, vote type,
// height or offender is a different offense.
func TestPotsoEvidencePutRecordRefusesASecondReportOfOneOffense(t *testing.T) {
	manager := newTestManager(t)
	first := equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{300}, "a")
	if err := manager.PotsoEvidencePutRecord(first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	sameOffense := []*evidence.Record{
		equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{300}, "b"),
		equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{299, 300}, "a"),
		equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{300, 301}, "c"),
	}
	for i, record := range sameOffense {
		if record.Hash == first.Hash {
			t.Fatalf("variant %d: test premise: the hashes must differ", i)
		}
		before := manager.trie.Hash()
		if err := manager.PotsoEvidencePutRecord(record); err == nil {
			t.Fatalf("variant %d: expected a second report of the same offense to be refused", i)
		}
		if manager.trie.Hash() != before {
			t.Fatalf("variant %d: a refused record must leave state untouched", i)
		}
	}

	otherOffenses := []*evidence.Record{
		equivocationRecord(t, 1, 300, 3, evidence.EquivocationVotePrevote, []uint64{300}, "a"),   // another round
		equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrecommit, []uint64{300}, "a"), // another vote type
		equivocationRecord(t, 1, 301, 2, evidence.EquivocationVotePrevote, []uint64{301}, "a"),   // another height
		equivocationRecord(t, 2, 300, 2, evidence.EquivocationVotePrevote, []uint64{300}, "a"),   // another offender
	}
	for i, record := range otherOffenses {
		if err := manager.PotsoEvidencePutRecord(record); err != nil {
			t.Fatalf("offense %d differs from the first in one respect and must be recorded: %v", i, err)
		}
	}
	if hashes, _ := manager.PotsoEvidencePendingHashes(); len(hashes) != 1+len(otherOffenses) {
		t.Fatalf("expected the first report and the %d other offenses indexed, got %d", len(otherOffenses), len(hashes))
	}
}

// TestPotsoEvidencePruneMeasuresAnEquivocationFromItsOffenseHeight: the record of an
// equivocation, and the record that its penalty was applied, live exactly as long
// as a report of the offense can still be submitted -- while the height of the
// double-sign is inside the window -- however old the other heights the report
// lists. Dropping them when the oldest listed height expires would let the offense
// be reported afresh, and penalised again, in between.
func TestPotsoEvidencePruneMeasuresAnEquivocationFromItsOffenseHeight(t *testing.T) {
	manager := newTestManager(t)
	empty := manager.trie.Hash()
	record := equivocationRecord(t, 1, 300, 2, evidence.EquivocationVotePrevote, []uint64{50, 300}, "a")
	if err := manager.PotsoEvidencePutRecord(record); err != nil {
		t.Fatalf("put record: %v", err)
	}
	offense := record.Offense()
	if err := manager.PotsoPenaltyMarkApplied(offense.Key, record.Evidence.Offender); err != nil {
		t.Fatalf("mark applied: %v", err)
	}

	// The oldest listed height (50) has left a window that starts at 100; the
	// offense (300) has not.
	kept, err := manager.PotsoEvidencePrune(100)
	if err != nil || len(kept) != 1 {
		t.Fatalf("expected the record kept while its offense is inside the window: %v (%d kept)", err, len(kept))
	}
	if applied, _ := manager.PotsoPenaltyApplied(offense.Key, record.Evidence.Offender); !applied {
		t.Fatalf("expected the applied-penalty record kept with the report")
	}
	if kept, _ = manager.PotsoEvidencePrune(300); len(kept) != 1 {
		t.Fatalf("expected the record kept on the boundary")
	}

	if kept, err = manager.PotsoEvidencePrune(301); err != nil || len(kept) != 0 {
		t.Fatalf("expected the record pruned once its offense leaves the window: %v (%d kept)", err, len(kept))
	}
	if _, ok, _ := manager.PotsoEvidenceGetRecord(record.Hash); ok {
		t.Fatalf("expected the record deleted")
	}
	if applied, _ := manager.PotsoPenaltyApplied(offense.Key, record.Evidence.Offender); applied {
		t.Fatalf("expected the applied-penalty record deleted with the report")
	}
	if manager.trie.Hash() != empty {
		t.Fatalf("expected no trace of the evidence left in state once it is pruned")
	}
}
