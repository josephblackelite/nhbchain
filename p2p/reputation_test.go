package p2p

import (
	"testing"
	"time"
)

func TestReputationEvents(t *testing.T) {
	cfg := ReputationConfig{
		GreyScore:        10,
		BanScore:         20,
		GreylistDuration: time.Minute,
		BanDuration:      time.Minute,
		DecayHalfLife:    time.Hour,
	}
	rep := NewReputationManager(cfg)
	now := time.Now()

	status := rep.MarkHeartbeat("peer", now)
	if status.Score != heartbeatRewardDelta {
		t.Fatalf("expected heartbeat score %d, got %d", heartbeatRewardDelta, status.Score)
	}

	status = rep.MarkUptime("peer", 24*time.Hour, now)
	if status.Score != heartbeatRewardDelta+uptimeRewardDelta {
		t.Fatalf("expected uptime bonus applied, got %d", status.Score)
	}

	status = rep.MarkUseful("peer", now)
	if status.Useful != 1 {
		t.Fatalf("expected useful counter to increment, got %d", status.Useful)
	}

	status = rep.PenalizeMalformed("peer", now, false)
	if status.Score != heartbeatRewardDelta+uptimeRewardDelta+malformedMessagePenaltyDelta {
		t.Fatalf("malformed penalty not applied, got %d", status.Score)
	}

	status = rep.PenalizeSpam("peer", now, false)
	if !status.Greylisted {
		t.Fatalf("expected spam to trigger greylist, score=%d", status.Score)
	}

	mis := rep.MarkMisbehavior("peer", now)
	if mis.Misbehavior == 0 {
		t.Fatalf("expected misbehavior counter to increment")
	}

	latencyStatus := rep.ObserveLatency("peer", 50*time.Millisecond, now)
	if latencyStatus.LatencyMS <= 0 {
		t.Fatalf("expected latency to be recorded, got %f", latencyStatus.LatencyMS)
	}

	status = rep.PenalizeInvalidBlock("peer", now, false)
	if !status.Banned {
		t.Fatalf("expected invalid block to trigger ban, score=%d", status.Score)
	}

	persistent := rep.PenalizeInvalidBlock("persistent", now, true)
	if persistent.Banned {
		t.Fatalf("persistent peers should not be banned")
	}
}

func TestReputationDecayToZero(t *testing.T) {
	cfg := ReputationConfig{
		GreyScore:        100,
		BanScore:         200,
		GreylistDuration: time.Minute,
		BanDuration:      time.Minute,
		DecayHalfLife:    time.Second,
	}
	rep := NewReputationManager(cfg)
	now := time.Now()

	rep.MarkHeartbeat("peer", now)
	rep.MarkHeartbeat("peer", now)

	later := now.Add(5 * cfg.DecayHalfLife)
	score := rep.Score("peer", later)
	if score != 0 {
		t.Fatalf("expected score to decay to zero, got %d", score)
	}
}
