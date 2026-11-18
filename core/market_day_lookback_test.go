package core

import (
	"testing"

	nhbstate "nhbchain/core/state"
)

// PL-BC-B2: a read walks back at most a bounded number of calendar days, so a long quiet stretch cannot make it slow.
func TestMarketDayRowsLookbackIsBounded(t *testing.T) {
	node := newTestNode(t)
	seedFillAt(t, node, 1, 20_000*86_400+1, 10, 5)
	seedFillAt(t, node, 2, (20_000+399)*86_400+1, 10, 5) // 399 days later: inside the bound
	read := func() []*nhbstate.MarketDayRow {
		var rows []*nhbstate.MarketDayRow
		_ = node.WithStateView(func(m *nhbstate.Manager) error { rows, _ = m.ListMarketDayRows(10); return nil })
		return rows
	}
	if rows := read(); len(rows) != 2 {
		t.Fatalf("a day 399 days back is still reached, got %d rows", len(rows))
	}
	seedFillAt(t, node, 3, (20_000+800)*86_400+1, 10, 5) // 401 days after the second: beyond the bound
	rows := read()
	if len(rows) != 1 || rows[0].Day != 20_800 {
		t.Fatalf("a read does not walk back past the bound: got %d rows", len(rows))
	}
}
