package state

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/native/loyalty"
)

var (
	// loyaltyProgramAccrualRecordsPrefix + a program's 32-byte ID + ':' + a
	// UTC day string ("YYYY-MM-DD") is the raw (un-hashed) KVAppend/
	// KVGetList key for that program's day-bucketed list of individual
	// accrual records (kind loyalty.AccrualKindProgram) -- mirrors
	// stakeDelegatorIndexPrefix's pattern (see prefixes.go) and
	// LoyaltyProgramDailyTotalKey's programID+day shape (see manager.go),
	// just with its own prefix so it never collides with either.
	loyaltyProgramAccrualRecordsPrefix = []byte("loyalty-index:program-accrual-records:")

	// loyaltyBaseAccrualRecordsPrefix + a UTC day string + ':' + an address is
	// the raw (un-hashed) KVAppend/KVGetList key for that address's
	// day-bucketed list of individual base-reward accrual records (kind
	// loyalty.AccrualKindBase). The base spend reward has no program
	// context (see BaseRewardState.AppendLoyaltyBaseAccrualRecord's doc
	// comment), so this index is keyed by address+day instead of
	// programID+day, mirroring LoyaltyBaseDailyMeterKey's own addr+day
	// shape rather than the program-level index above.
	loyaltyBaseAccrualRecordsPrefix = []byte("loyalty-index:base-accrual-records:")
)

// loyaltyProgramAccrualRecordsKey returns the raw (un-hashed) KVAppend/
// KVGetList key for a program's accrual-record list on the given UTC day.
func loyaltyProgramAccrualRecordsKey(id loyalty.ProgramID, day string) []byte {
	key := make([]byte, 0, len(loyaltyProgramAccrualRecordsPrefix)+len(id)+1+len(day))
	key = append(key, loyaltyProgramAccrualRecordsPrefix...)
	key = append(key, id[:]...)
	key = append(key, ':')
	key = append(key, day...)
	return key
}

// loyaltyBaseAccrualRecordsKey returns the raw (un-hashed) KVAppend/
// KVGetList key for an address's base-reward accrual-record list on the
// given UTC day.
func loyaltyBaseAccrualRecordsKey(addr []byte, day string) []byte {
	key := make([]byte, 0, len(loyaltyBaseAccrualRecordsPrefix)+len(day)+1+len(addr))
	key = append(key, loyaltyBaseAccrualRecordsPrefix...)
	key = append(key, day...)
	key = append(key, ':')
	key = append(key, addr...)
	return key
}

// AppendLoyaltyProgramAccrualRecord appends a single individual accrual
// record to the given program's day-bucketed index, mirroring
// merchantIdxKey's KVAppend-based pattern (native/loyalty/registry.go)
// exactly: the record is RLP-encoded and appended as raw bytes, so callers
// must ensure record.TxHash is populated -- KVAppend dedups an appended list
// by the exact serialized bytes of the appended value, and without a
// per-transaction-unique field a second, genuinely distinct accrual that
// happened to serialize identically to an earlier one would be silently
// dropped (see AccrualRecord's doc comment).
func (m *Manager) AppendLoyaltyProgramAccrualRecord(id loyalty.ProgramID, day string, record loyalty.AccrualRecord) error {
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		return fmt.Errorf("day must not be empty")
	}
	encoded, err := rlp.EncodeToBytes(&record)
	if err != nil {
		return fmt.Errorf("loyalty: encode program accrual record: %w", err)
	}
	return m.KVAppend(loyaltyProgramAccrualRecordsKey(id, trimmed), encoded)
}

// LoyaltyProgramDailyAccrualRecords returns every individual accrual record
// appended for the given program on the given UTC day, in insertion order.
// Returns an empty, non-nil slice if none exist.
func (m *Manager) LoyaltyProgramDailyAccrualRecords(id loyalty.ProgramID, day string) ([]loyalty.AccrualRecord, error) {
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	var raw [][]byte
	if err := m.KVGetList(loyaltyProgramAccrualRecordsKey(id, trimmed), &raw); err != nil {
		return nil, fmt.Errorf("loyalty: load program accrual records: %w", err)
	}
	records := make([]loyalty.AccrualRecord, 0, len(raw))
	for _, entry := range raw {
		var record loyalty.AccrualRecord
		if err := rlp.DecodeBytes(entry, &record); err != nil {
			return nil, fmt.Errorf("loyalty: decode program accrual record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

// AppendLoyaltyBaseAccrualRecord appends a single individual base-reward
// accrual record to the given address's day-bucketed index. Same KVAppend
// dedup-by-value caveat as AppendLoyaltyProgramAccrualRecord above.
func (m *Manager) AppendLoyaltyBaseAccrualRecord(addr []byte, day string, record loyalty.AccrualRecord) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		return fmt.Errorf("day must not be empty")
	}
	encoded, err := rlp.EncodeToBytes(&record)
	if err != nil {
		return fmt.Errorf("loyalty: encode base accrual record: %w", err)
	}
	return m.KVAppend(loyaltyBaseAccrualRecordsKey(addr, trimmed), encoded)
}

// LoyaltyBaseDailyAccrualRecords returns every individual base-reward
// accrual record appended for the given address on the given UTC day, in
// insertion order. Returns an empty, non-nil slice if none exist. Not
// currently exposed over RPC (loyalty_listAccruals is scoped to a program),
// kept for symmetry with the program-level index and for tests.
func (m *Manager) LoyaltyBaseDailyAccrualRecords(addr []byte, day string) ([]loyalty.AccrualRecord, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	trimmed := strings.TrimSpace(day)
	if trimmed == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	var raw [][]byte
	if err := m.KVGetList(loyaltyBaseAccrualRecordsKey(addr, trimmed), &raw); err != nil {
		return nil, fmt.Errorf("loyalty: load base accrual records: %w", err)
	}
	records := make([]loyalty.AccrualRecord, 0, len(raw))
	for _, entry := range raw {
		var record loyalty.AccrualRecord
		if err := rlp.DecodeBytes(entry, &record); err != nil {
			return nil, fmt.Errorf("loyalty: decode base accrual record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}
