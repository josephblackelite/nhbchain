package penalty

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/consensus/potso/evidence"
	statepotso "nhbchain/state/potso"
)

// memRecords stands in for the block's own state: the durable applied-penalty
// record penalty.Engine reads and writes when it is given one.
type memRecords struct {
	applied  map[[52]byte]struct{}
	loadErr  error
	storeErr error
}

func newMemRecords() *memRecords { return &memRecords{applied: make(map[[52]byte]struct{})} }

func recordsKey(hash [32]byte, offender [20]byte) [52]byte {
	var key [52]byte
	copy(key[:32], hash[:])
	copy(key[32:], offender[:])
	return key
}

func (m *memRecords) PotsoPenaltyApplied(hash [32]byte, offender [20]byte) (bool, error) {
	if m.loadErr != nil {
		return false, m.loadErr
	}
	_, ok := m.applied[recordsKey(hash, offender)]
	return ok, nil
}

func (m *memRecords) PotsoPenaltyMarkApplied(hash [32]byte, offender [20]byte) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.applied[recordsKey(hash, offender)] = struct{}{}
	return nil
}

type countingSlasher struct{ calls int }

func (s *countingSlasher) Slash(addr [20]byte, amount *big.Int) error {
	s.calls++
	return nil
}

// slashingEngine builds an engine whose equivocation rule slashes 100% of the
// offender's base weight, against a fresh ledger seeded with weight 800.
func slashingEngine(t *testing.T, offender [20]byte, records Records, slasher *countingSlasher) (*Engine, *statepotso.Ledger) {
	t.Helper()
	ledger, err := statepotso.NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	if _, err := ledger.EnsureBaseline(offender, big.NewInt(800)); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cfg := DefaultConfig()
	cfg.SlashEnabled = true
	cfg.EquivocationSlashBps = 10000
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	engine := NewEngine(catalog, ledger, slasher)
	if records != nil {
		engine.WithRecords(records)
	}
	return engine, ledger
}

func equivocationRecord(offender [20]byte) *evidence.Record {
	return &evidence.Record{
		Hash:     [32]byte{0xE1},
		Evidence: evidence.Evidence{Type: evidence.TypeEquivocation, Offender: offender},
	}
}

// TestEngineWithRecordsKeepsTheAppliedRecordOutOfProcessMemory: given a
// durable record, the engine reads and writes only that -- the weight ledger's
// in-memory applied map, which no block execution may depend on, is untouched --
// and a repeat application against the same record is idempotent.
func TestEngineWithRecordsKeepsTheAppliedRecordOutOfProcessMemory(t *testing.T) {
	offender := [20]byte{1}
	records := newMemRecords()
	slasher := &countingSlasher{}
	engine, ledger := slashingEngine(t, offender, records, slasher)
	record := equivocationRecord(offender)

	first, err := engine.Apply(record, Context{BlockHeight: 10})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if first.Idempotent || slasher.calls != 1 || first.SlashApplied.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("expected one real slash of 800, got idempotent=%v calls=%d slash=%s", first.Idempotent, slasher.calls, first.SlashApplied)
	}
	if ledger.WasPenaltyApplied(record.Hash, offender) {
		t.Fatalf("the ledger's in-memory applied record must not be used when a durable one is supplied")
	}
	if applied, _ := records.PotsoPenaltyApplied(record.Hash, offender); !applied {
		t.Fatalf("expected the durable record written")
	}

	again, err := engine.Apply(record, Context{BlockHeight: 11})
	if err != nil {
		t.Fatalf("apply again: %v", err)
	}
	if !again.Idempotent || slasher.calls != 1 {
		t.Fatalf("expected a repeat against the same record to be idempotent, got idempotent=%v calls=%d", again.Idempotent, slasher.calls)
	}
}

// TestEngineWithRecordsAppliesAgainInAFreshState: two executions each with their
// own state -- a discarded trial build, then the real block -- both apply the
// penalty. Neither leaves anything behind for the other, which is what lets
// every node reach the same result from the same starting state.
func TestEngineWithRecordsAppliesAgainInAFreshState(t *testing.T) {
	offender := [20]byte{1}
	record := equivocationRecord(offender)

	trialSlasher := &countingSlasher{}
	trial, _ := slashingEngine(t, offender, newMemRecords(), trialSlasher)
	if _, err := trial.Apply(record, Context{BlockHeight: 10}); err != nil {
		t.Fatalf("trial apply: %v", err)
	}

	committedSlasher := &countingSlasher{}
	committed, _ := slashingEngine(t, offender, newMemRecords(), committedSlasher)
	res, err := committed.Apply(record, Context{BlockHeight: 10})
	if err != nil {
		t.Fatalf("committed apply: %v", err)
	}
	if res.Idempotent || trialSlasher.calls != 1 || committedSlasher.calls != 1 {
		t.Fatalf("expected each execution to apply once against its own state: trial=%d committed=%d idempotent=%v",
			trialSlasher.calls, committedSlasher.calls, res.Idempotent)
	}
}

// TestEngineWithoutRecordsStillUsesTheLedger keeps the standalone behaviour
// tools and tests rely on.
func TestEngineWithoutRecordsStillUsesTheLedger(t *testing.T) {
	offender := [20]byte{1}
	slasher := &countingSlasher{}
	engine, ledger := slashingEngine(t, offender, nil, slasher)
	record := equivocationRecord(offender)
	if _, err := engine.Apply(record, Context{BlockHeight: 10}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !ledger.WasPenaltyApplied(record.Hash, offender) {
		t.Fatalf("expected the ledger's own record written when no durable one is supplied")
	}
}

// TestEngineWithRecordsSurfacesStateErrors: a failure to read or write the
// durable record fails the application -- a penalty is never applied on a guess,
// and never applied without being recorded.
func TestEngineWithRecordsSurfacesStateErrors(t *testing.T) {
	offender := [20]byte{1}
	record := equivocationRecord(offender)

	loadFails := newMemRecords()
	loadFails.loadErr = errors.New("read failed")
	slasher := &countingSlasher{}
	engine, _ := slashingEngine(t, offender, loadFails, slasher)
	if _, err := engine.Apply(record, Context{}); !errors.Is(err, loadFails.loadErr) {
		t.Fatalf("expected the read failure to surface, got %v", err)
	}
	if slasher.calls != 0 {
		t.Fatalf("nothing may be slashed when the applied record cannot be read")
	}

	storeFails := newMemRecords()
	storeFails.storeErr = errors.New("write failed")
	engine, _ = slashingEngine(t, offender, storeFails, &countingSlasher{})
	if _, err := engine.Apply(record, Context{}); !errors.Is(err, storeFails.storeErr) {
		t.Fatalf("expected the write failure to surface, got %v", err)
	}
}
