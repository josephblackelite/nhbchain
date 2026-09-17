package potso

import "testing"

func TestMeterRecomputeScore(t *testing.T) {
	m := &Meter{Day: "2025-01-01", UptimeSeconds: 3600, TxCount: 3, EscrowEvents: 2}
	m.RecomputeScore()
	expectedRaw := uint64(60 + 3*5 + 2*10)
	if m.RawScore != expectedRaw {
		t.Fatalf("unexpected raw score: %d", m.RawScore)
	}
	if m.Score != expectedRaw {
		t.Fatalf("score mismatch: %d", m.Score)
	}
}
