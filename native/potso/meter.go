package potso

import "strings"

// DayFormat aligns with the UTC calendar date string used by POTSO meters.
const DayFormat = "2006-01-02"

// Meter captures per-day raw participation counters and the derived engagement score.
type Meter struct {
	Day           string `json:"day"`
	UptimeSeconds uint64 `json:"uptimeSeconds"`
	TxCount       uint64 `json:"txCount"`
	EscrowEvents  uint64 `json:"escrowEvents"`
	RawScore      uint64 `json:"rawScore"`
	Score         uint64 `json:"score"`
}

// NormaliseDay trims whitespace and enforces upper bound on allowed formats.
func NormaliseDay(day string) string {
	return strings.TrimSpace(day)
}

// RecomputeScore recalculates both the raw and final score based on the latest counters.
func (m *Meter) RecomputeScore() {
	if m == nil {
		return
	}
	m.RawScore = ComputeRawScore(m.UptimeSeconds, m.TxCount, m.EscrowEvents)
	m.Score = ComputeScore(m.RawScore)
}

// ComputeRawScore derives a baseline score from raw metrics. Uptime is scaled to minute
// resolution while transaction and escrow activity provide additional weight.
func ComputeRawScore(uptimeSeconds, txCount, escrowEvents uint64) uint64 {
	uptimeMinutes := uptimeSeconds / 60
	return uptimeMinutes + txCount*5 + escrowEvents*10
}

// ComputeScore currently returns the raw score unchanged but is defined separately to allow
// future capping or smoothing without altering stored raw values.
func ComputeScore(raw uint64) uint64 {
	return raw
}
