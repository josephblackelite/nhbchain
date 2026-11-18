package swap

// ListWhere and ExportCSVWhere read the voucher ledger through a predicate, so
// that a caller who may see only some providers' records is handed only those:
// not the other providers' records, not their ids in a cursor, not their number
// in a count or a total. With no predicate they must behave exactly as List and
// ExportCSV do.

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

type scopeSeed struct {
	id       string
	provider string
	amount   int64
}

// scopeFixture holds five vouchers in creation order:
// a-1 (alpha), b-1 (beta), a-2 ("Alpha " in mixed case), b-2 (beta), a-3 (alpha).
func scopeFixture(t *testing.T, seeds []scopeSeed) *Ledger {
	t.Helper()
	ledger := NewLedger(newMockStorage())
	for i, seed := range seeds {
		rec := &VoucherRecord{
			Provider:        seed.provider,
			ProviderTxID:    seed.id,
			FiatCurrency:    "USD",
			FiatAmount:      "50.00",
			Rate:            "0.25",
			Token:           "ZNHB",
			MintAmountWei:   big.NewInt(seed.amount),
			QuoteTimestamp:  1700000000 + int64(i),
			OracleSource:    "manual",
			MinterSignature: "0xsig",
			CreatedAt:       1700000100 + int64(i),
		}
		if err := ledger.Put(rec); err != nil {
			t.Fatalf("put %s: %v", seed.id, err)
		}
	}
	return ledger
}

func mixedSeeds() []scopeSeed {
	return []scopeSeed{
		{"a-1", "alpha", 1},
		{"b-1", "beta", 10},
		{"a-2", "Alpha ", 100},
		{"b-2", "beta", 1000},
		{"a-3", "alpha", 10000},
	}
}

func keepProvider(provider string) func(*VoucherRecord) bool {
	return func(r *VoucherRecord) bool { return ProviderKey(r.Provider) == provider }
}

func ids(records []*VoucherRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.ProviderTxID)
	}
	return out
}

func TestProviderKeyMatchesTheAllowListNormalisation(t *testing.T) {
	for _, in := range []string{"alpha", " Alpha ", "ALPHA", "\talpha\n"} {
		if got := ProviderKey(in); got != "alpha" {
			t.Fatalf("ProviderKey(%q) = %q, want alpha", in, got)
		}
	}
	// ProviderConfig.IsAllowed folds case and trims space; the scope must agree
	// with it on every spelling, or a provider the node lets mint could be one a
	// partner cannot read back (or the other way round).
	cfg := ProviderConfig{Allow: []string{"alpha"}}
	for _, in := range []string{"alpha", " Alpha ", "ALPHA", "beta", ""} {
		if cfg.IsAllowed(in) != (ProviderKey(in) == "alpha") {
			t.Fatalf("ProviderKey and IsAllowed disagree on %q", in)
		}
	}
}

func TestListWhereReturnsOnlyKeptRecordsInLedgerOrder(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	got, cursor, err := ledger.ListWhere(0, 0, "", 50, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"a-1", "a-2", "a-3"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty", cursor)
	}
	got, _, err = ledger.ListWhere(0, 0, "", 50, keepProvider("beta"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"b-1", "b-2"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
}

func TestListWhereFillsEachPageWithKeptRecordsOnly(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	page1, cursor, err := ledger.ListWhere(0, 0, "", 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if want := []string{"a-1", "a-2"}; !reflect.DeepEqual(ids(page1), want) {
		t.Fatalf("page 1 = %v, want %v", ids(page1), want)
	}
	// The cursor is the last record handed over, which is the caller's own: never
	// the id of a record that sits between them and belongs to someone else.
	if cursor != "a-2" {
		t.Fatalf("cursor = %q, want a-2", cursor)
	}
	page2, cursor, err := ledger.ListWhere(0, 0, cursor, 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if want := []string{"a-3"}; !reflect.DeepEqual(ids(page2), want) {
		t.Fatalf("page 2 = %v, want %v", ids(page2), want)
	}
	if cursor != "" {
		t.Fatalf("last page cursor = %q, want empty", cursor)
	}
}

// List sets a cursor whenever a full page leaves any entry behind. For a scoped
// read that would tell the caller that someone else has records after theirs;
// the cursor must be empty when nothing of the caller's own remains.
func TestListWhereCursorIsEmptyWhenOnlyOthersRecordsRemain(t *testing.T) {
	ledger := scopeFixture(t, []scopeSeed{
		{"a-1", "alpha", 1},
		{"a-2", "alpha", 1},
		{"b-1", "beta", 1},
		{"b-2", "beta", 1},
	})
	got, cursor, err := ledger.ListWhere(0, 0, "", 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"a-1", "a-2"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q: a full page that leaves only another provider's records behind must not point at them", cursor)
	}
}

// A cursor that names another provider's record must behave exactly like a
// cursor that names nothing: the list restarts. Otherwise the caller could ask
// "is b-1 a voucher, and where does it sit?" and be answered by where the page
// begins.
func TestListWhereTreatsAnotherProvidersCursorAsUnknown(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	foreign, foreignCursor, err := ledger.ListWhere(0, 0, "b-1", 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("foreign cursor: %v", err)
	}
	unknown, unknownCursor, err := ledger.ListWhere(0, 0, "no-such-id", 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("unknown cursor: %v", err)
	}
	if !reflect.DeepEqual(ids(foreign), ids(unknown)) || foreignCursor != unknownCursor {
		t.Fatalf("a foreign cursor answers %v/%q, an unknown one %v/%q: they must be identical", ids(foreign), foreignCursor, ids(unknown), unknownCursor)
	}
	if want := []string{"a-1", "a-2"}; !reflect.DeepEqual(ids(foreign), want) {
		t.Fatalf("got %v, want the first page %v", ids(foreign), want)
	}
	// The caller's own cursor still pages on.
	own, _, err := ledger.ListWhere(0, 0, "a-1", 2, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("own cursor: %v", err)
	}
	if want := []string{"a-2", "a-3"}; !reflect.DeepEqual(ids(own), want) {
		t.Fatalf("own cursor page = %v, want %v", ids(own), want)
	}
}

func TestListWhereKeepsTheTimeRange(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	// a-2 was created at 1700000102, a-3 at 1700000104.
	got, _, err := ledger.ListWhere(1700000102, 1700000103, "", 50, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"a-2"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
}

func TestListWhereWithNoPredicateIsList(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	for _, tc := range []struct {
		start, end int64
		cursor     string
		limit      int
	}{
		{0, 0, "", 50}, {0, 0, "", 2}, {0, 0, "a-1", 2}, {0, 0, "a-3", 2}, {0, 0, "nope", 3},
		{1700000101, 1700000103, "", 1}, {0, 0, "", 0}, {1800000000, 0, "", 5},
	} {
		wantRecs, wantCur, wantErr := ledger.List(tc.start, tc.end, tc.cursor, tc.limit)
		gotRecs, gotCur, gotErr := ledger.ListWhere(tc.start, tc.end, tc.cursor, tc.limit, nil)
		if (wantErr == nil) != (gotErr == nil) || wantCur != gotCur || !reflect.DeepEqual(wantRecs, gotRecs) {
			t.Fatalf("ListWhere(nil) differs from List for %+v: got %v/%q/%v, want %v/%q/%v", tc, ids(gotRecs), gotCur, gotErr, ids(wantRecs), wantCur, wantErr)
		}
	}
}

func TestExportCSVWhereCountsAndTotalsOnlyKeptRecords(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	encoded, count, total, err := ledger.ExportCSVWhere(0, 0, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	if want := big.NewInt(1 + 100 + 10000); total.Cmp(want) != 0 {
		t.Fatalf("total = %s, want %s", total, want)
	}
	csvText := decodeCSV(t, encoded)
	for _, id := range []string{"a-1", "a-2", "a-3"} {
		if !strings.Contains(csvText, id) {
			t.Fatalf("csv lacks %s:\n%s", id, csvText)
		}
	}
	for _, id := range []string{"b-1", "b-2", "beta"} {
		if strings.Contains(csvText, id) {
			t.Fatalf("csv leaks %q:\n%s", id, csvText)
		}
	}
}

func TestExportCSVWhereHasOnlyTheHeaderWhenNothingIsKept(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	encoded, count, total, err := ledger.ExportCSVWhere(0, 0, func(*VoucherRecord) bool { return false })
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 0 || total.Sign() != 0 {
		t.Fatalf("count = %d, total = %s, want 0 and 0", count, total)
	}
	lines := strings.Split(strings.TrimSpace(decodeCSV(t, encoded)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "providerTxId,provider,") {
		t.Fatalf("expected the header row only, got %q", lines)
	}
}

func TestExportCSVWhereWithNoPredicateIsExportCSV(t *testing.T) {
	ledger := scopeFixture(t, mixedSeeds())
	wantEnc, wantCount, wantTotal, err := ledger.ExportCSV(0, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	gotEnc, gotCount, gotTotal, err := ledger.ExportCSVWhere(0, 0, nil)
	if err != nil {
		t.Fatalf("export where: %v", err)
	}
	if gotEnc != wantEnc || gotCount != wantCount || gotTotal.Cmp(wantTotal) != 0 {
		t.Fatalf("ExportCSVWhere(nil) differs from ExportCSV: %d/%s vs %d/%s", gotCount, gotTotal, wantCount, wantTotal)
	}
}

// The scan reads one record at a time, so a ledger of a few thousand records
// with a caller who owns a handful must still come back correct.
func TestListWhereFindsASparseOwnerInALargeLedger(t *testing.T) {
	var seeds []scopeSeed
	for i := 0; i < 600; i++ {
		provider := "beta"
		if i%97 == 0 {
			provider = "alpha"
		}
		seeds = append(seeds, scopeSeed{fmt.Sprintf("id-%04d", i), provider, 1})
	}
	ledger := scopeFixture(t, seeds)
	got, cursor, err := ledger.ListWhere(0, 0, "", 3, keepProvider("alpha"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"id-0000", "id-0097", "id-0194"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("got %v, want %v", ids(got), want)
	}
	if cursor != "id-0194" {
		t.Fatalf("cursor = %q, want id-0194", cursor)
	}
}

func decodeCSV(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode csv: %v", err)
	}
	return string(raw)
}
