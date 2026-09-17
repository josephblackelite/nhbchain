package determinism

import (
	"testing"
	"time"

	"nhbchain/core/engagement"
)

func TestHeartbeatSequenceDeterministic(t *testing.T) {
	cfg := engagement.DefaultConfig()
	manager := engagement.NewManager(cfg)

	now := time.Unix(1700000000, 0)
	manager.SetNow(func() time.Time {
		return now
	})

	var validator [20]byte
	copy(validator[:], []byte("deterministic-validator"))

	token, err := manager.RegisterDevice(validator, "validator-device")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}

	stamp1, err := manager.SubmitHeartbeat("validator-device", token, 0)
	if err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	if stamp1 != now.Unix() {
		t.Fatalf("expected timestamp %d, got %d", now.Unix(), stamp1)
	}

	now = now.Add(cfg.HeartbeatInterval)
	stamp2, err := manager.SubmitHeartbeat("validator-device", token, 0)
	if err != nil {
		t.Fatalf("second heartbeat: %v", err)
	}
	if want := int64(cfg.HeartbeatInterval.Seconds()); (stamp2 - stamp1) != want {
		t.Fatalf("expected interval %d seconds, got %d", want, stamp2-stamp1)
	}

	t.Run("replay detection", func(t *testing.T) {
		if _, err := manager.SubmitHeartbeat("validator-device", token, stamp2); err == nil {
			t.Fatal("expected replay detection error")
		}
	})

	// Recreate the manager to ensure the same explicit timestamps are accepted.
	manager2 := engagement.NewManager(cfg)
	token2, err := manager2.RegisterDevice(validator, "validator-device")
	if err != nil {
		t.Fatalf("register device on new manager: %v", err)
	}
	for _, ts := range []int64{stamp1, stamp2 + int64(cfg.HeartbeatInterval.Seconds())} {
		got, err := manager2.SubmitHeartbeat("validator-device", token2, ts)
		if err != nil {
			t.Fatalf("submit heartbeat %d on new manager: %v", ts, err)
		}
		if got != ts {
			t.Fatalf("expected timestamp %d, got %d", ts, got)
		}
	}
}
