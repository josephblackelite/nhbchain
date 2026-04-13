package emissions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadScheduleJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	payload := `{
  "entries": [
    {"startEpoch": 1, "amount": "1000000000000000000"},
    {"startEpoch": 11, "amount": "500000000000000000", "decay": {"mode": "geometric", "ratioBps": 9000, "durationEpochs": 5, "floor": "10000000000000000"}}
  ]
}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write schedule: %v", err)
	}
	sched, err := LoadSchedule(path)
	if err != nil {
		t.Fatalf("load schedule: %v", err)
	}
	if got := sched.AmountForEpoch(0); got.Sign() != 0 {
		t.Fatalf("epoch 0 expected zero, got %s", got)
	}
	if got := sched.AmountForEpoch(1); got.String() != "1000000000000000000" {
		t.Fatalf("epoch 1 amount mismatch: %s", got)
	}
	if got := sched.AmountForEpoch(12); got.String() != "450000000000000000" {
		t.Fatalf("epoch 12 amount mismatch: %s", got)
	}
	// Duration clamps decay to 5 steps, so epoch 30 should equal epoch 16.
	if got := sched.AmountForEpoch(30); got.String() != "295245000000000000" {
		t.Fatalf("epoch 30 amount mismatch: %s", got)
	}
}

func TestLoadScheduleTOMLRejectUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.toml")
	payload := `[[entries]]
startEpoch = 1
amount = "1"
extra = 1
`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write schedule: %v", err)
	}
	if _, err := LoadSchedule(path); err == nil {
		t.Fatalf("expected unknown field error")
	}
}
