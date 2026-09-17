package potso

import (
	"testing"
	"time"
)

func TestApplyHeartbeatSequence(t *testing.T) {
	state := &HeartbeatState{}
	delta, accepted, err := state.ApplyHeartbeat(1000, 1, []byte{0x01})
	if err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	if !accepted {
		t.Fatalf("first heartbeat not accepted")
	}
	if delta != HeartbeatIntervalSeconds {
		t.Fatalf("expected default delta, got %d", delta)
	}

	_, accepted, err = state.ApplyHeartbeat(1050, 1, []byte{0x01})
	if err != ErrHeartbeatTooSoon {
		t.Fatalf("expected too soon error, got %v", err)
	}
	if accepted {
		t.Fatalf("duplicate heartbeat should not be accepted")
	}

	delta, accepted, err = state.ApplyHeartbeat(1120, 2, []byte{0x02})
	if err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	if !accepted {
		t.Fatalf("second heartbeat not accepted")
	}
	if delta != 120 {
		t.Fatalf("unexpected delta: %d", delta)
	}
}

func TestWithinTolerance(t *testing.T) {
	now := time.Unix(2000, 0)
	if !WithinTolerance(2000, now) {
		t.Fatalf("expected equal timestamp to be tolerated")
	}
	if !WithinTolerance(1881, now) {
		t.Fatalf("expected -119s skew to be tolerated")
	}
	if WithinTolerance(1879, now) {
		t.Fatalf("expected -121s skew to be rejected")
	}
}
