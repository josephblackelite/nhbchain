package engagement

import (
	"testing"
	"time"
)

func TestManagerRateLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HeartbeatInterval = time.Minute
	mgr := NewManager(cfg)
	mgr.SetNow(func() time.Time { return time.Unix(1000, 0).UTC() })

	var addr [20]byte
	token, err := mgr.RegisterDevice(addr, "device-1")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}

	ts, err := mgr.SubmitHeartbeat("device-1", token, 0)
	if err != nil {
		t.Fatalf("first heartbeat failed: %v", err)
	}
	if _, err := mgr.SubmitHeartbeat("device-1", token, ts+int64(time.Second*30/time.Second)); err == nil {
		t.Fatalf("expected rate limit error")
	}
}

func TestManagerReplay(t *testing.T) {
	cfg := DefaultConfig()
	mgr := NewManager(cfg)
	mgr.SetNow(func() time.Time { return time.Unix(2000, 0).UTC() })

	var addr [20]byte
	token, err := mgr.RegisterDevice(addr, "device-2")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}

	ts, err := mgr.SubmitHeartbeat("device-2", token, 0)
	if err != nil {
		t.Fatalf("first heartbeat failed: %v", err)
	}
	if _, err := mgr.SubmitHeartbeat("device-2", token, ts); err == nil {
		t.Fatalf("expected replay detection")
	}
}
