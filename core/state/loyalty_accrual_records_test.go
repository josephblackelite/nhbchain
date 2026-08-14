package state

import (
	"math/big"
	"testing"

	"nhbchain/native/loyalty"
)

// TestAppendLoyaltyProgramAccrualRecordRoundTrip proves a single appended
// program-level accrual record survives RLP encode/decode through
// KVAppend/KVGetList with every field intact.
func TestAppendLoyaltyProgramAccrualRecordRoundTrip(t *testing.T) {
	manager := newLoyaltyTestManager(t)

	var programID loyalty.ProgramID
	programID[31] = 0x01
	var addr [20]byte
	addr[19] = 0x02
	var txHash [32]byte
	txHash[0] = 0xAA

	record := loyalty.AccrualRecord{
		ProgramID: programID,
		Address:   addr,
		Amount:    big.NewInt(1234),
		Kind:      loyalty.AccrualKindProgram,
		TxHash:    txHash,
		Timestamp: 1700000000,
	}
	if err := manager.AppendLoyaltyProgramAccrualRecord(programID, "2024-01-10", record); err != nil {
		t.Fatalf("append record: %v", err)
	}

	records, err := manager.LoyaltyProgramDailyAccrualRecords(programID, "2024-01-10")
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	got := records[0]
	if got.ProgramID != programID {
		t.Fatalf("unexpected programID: %x", got.ProgramID)
	}
	if got.Address != addr {
		t.Fatalf("unexpected address: %x", got.Address)
	}
	if got.Amount == nil || got.Amount.String() != "1234" {
		t.Fatalf("unexpected amount: %v", got.Amount)
	}
	if got.Kind != loyalty.AccrualKindProgram {
		t.Fatalf("unexpected kind: %s", got.Kind)
	}
	if got.TxHash != txHash {
		t.Fatalf("unexpected txHash: %x", got.TxHash)
	}
	if got.Timestamp != 1700000000 {
		t.Fatalf("unexpected timestamp: %d", got.Timestamp)
	}

	// A different day bucket for the same program must stay empty.
	empty, err := manager.LoyaltyProgramDailyAccrualRecords(programID, "2024-01-11")
	if err != nil {
		t.Fatalf("load empty day: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no records for unrelated day, got %d", len(empty))
	}
}

// TestAppendLoyaltyProgramAccrualRecordDedupRegression is the regression
// test AccrualRecord's doc comment warns about: core/state's KVAppend dedups
// an appended list by the exact serialized bytes of the appended value (see
// its bytes.Equal check). Two distinct accrual events for the same program,
// address, day, kind, and reward amount -- differing ONLY by transaction
// hash -- must both survive as two separate records. If TxHash were ever
// dropped from AccrualRecord (or two records legitimately shared a TxHash),
// this test would start failing with len(records)==1, catching the
// regression the doc comment warns about.
func TestAppendLoyaltyProgramAccrualRecordDedupRegression(t *testing.T) {
	manager := newLoyaltyTestManager(t)

	var programID loyalty.ProgramID
	programID[31] = 0x02
	var addr [20]byte
	addr[19] = 0x03

	var txHashA, txHashB [32]byte
	txHashA[0] = 0x01
	txHashB[0] = 0x02

	base := loyalty.AccrualRecord{
		ProgramID: programID,
		Address:   addr,
		Amount:    big.NewInt(500),
		Kind:      loyalty.AccrualKindProgram,
		Timestamp: 1700000000,
	}

	recordA := base
	recordA.Amount = new(big.Int).Set(base.Amount)
	recordA.TxHash = txHashA
	recordB := base
	recordB.Amount = new(big.Int).Set(base.Amount)
	recordB.TxHash = txHashB

	if err := manager.AppendLoyaltyProgramAccrualRecord(programID, "2024-02-01", recordA); err != nil {
		t.Fatalf("append record A: %v", err)
	}
	if err := manager.AppendLoyaltyProgramAccrualRecord(programID, "2024-02-01", recordB); err != nil {
		t.Fatalf("append record B: %v", err)
	}

	records, err := manager.LoyaltyProgramDailyAccrualRecords(programID, "2024-02-01")
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 distinct accrual records (not deduped), got %d: %#v", len(records), records)
	}
	seen := map[[32]byte]bool{}
	for _, r := range records {
		if r.Amount == nil || r.Amount.String() != "500" {
			t.Fatalf("unexpected amount on record: %v", r.Amount)
		}
		seen[r.TxHash] = true
	}
	if !seen[txHashA] || !seen[txHashB] {
		t.Fatalf("expected both tx hashes present, got %#v", records)
	}

	// Appending the exact same record (recordA, identical bytes) a second
	// time must be a genuine no-op via KVAppend's intended dedup -- this is
	// the behavior the gotcha is about, not a bug: a truly identical
	// re-append (same tx hash and all) should collapse, only a distinct
	// tx hash must not.
	if err := manager.AppendLoyaltyProgramAccrualRecord(programID, "2024-02-01", recordA); err != nil {
		t.Fatalf("re-append record A: %v", err)
	}
	records, err = manager.LoyaltyProgramDailyAccrualRecords(programID, "2024-02-01")
	if err != nil {
		t.Fatalf("reload records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected re-appending an identical record to no-op, got %d records", len(records))
	}
}

// TestAppendLoyaltyBaseAccrualRecordRoundTrip mirrors the program-level
// round trip test above for the base-reward index, which is keyed by
// address+day rather than programID+day since ApplyBaseReward has no
// program context (see BaseRewardState.AppendLoyaltyBaseAccrualRecord's doc
// comment in native/loyalty/engine_base.go).
func TestAppendLoyaltyBaseAccrualRecordRoundTrip(t *testing.T) {
	manager := newLoyaltyTestManager(t)

	var addr [20]byte
	addr[19] = 0x09
	var txHash [32]byte
	txHash[0] = 0xBB

	record := loyalty.AccrualRecord{
		Address:   addr,
		Amount:    big.NewInt(77),
		Kind:      loyalty.AccrualKindBase,
		TxHash:    txHash,
		Timestamp: 1700000001,
	}
	if err := manager.AppendLoyaltyBaseAccrualRecord(addr[:], "2024-03-05", record); err != nil {
		t.Fatalf("append record: %v", err)
	}

	records, err := manager.LoyaltyBaseDailyAccrualRecords(addr[:], "2024-03-05")
	if err != nil {
		t.Fatalf("load records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	got := records[0]
	if got.Address != addr {
		t.Fatalf("unexpected address: %x", got.Address)
	}
	if got.Amount == nil || got.Amount.String() != "77" {
		t.Fatalf("unexpected amount: %v", got.Amount)
	}
	if got.Kind != loyalty.AccrualKindBase {
		t.Fatalf("unexpected kind: %s", got.Kind)
	}
	if got.TxHash != txHash {
		t.Fatalf("unexpected txHash: %x", got.TxHash)
	}
	// ProgramID is meaningless for a base-kind record and must decode back
	// as its zero value.
	if got.ProgramID != (loyalty.ProgramID{}) {
		t.Fatalf("expected zero-value programID for base record, got %x", got.ProgramID)
	}
}
