package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

// The values a config gets for the four timers and the minimum block interval when
// it leaves them out (config.defaultConsensusConfig).
func defaultTimingConfig() config.Consensus {
	return config.Consensus{
		ProposalTimeout:  2 * time.Second,
		PrevoteTimeout:   2 * time.Second,
		PrecommitTimeout: 2 * time.Second,
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: time.Second,
	}
}

func capturedAdvisoryLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, nil)), buf
}

// A commit timeout overridden below 2s leaves the default one-second interval longer
// than half of it. The engine lowers the interval, so consensusd must start on these
// settings and say so, not stop on a value the operator never wrote.
func TestShortCommitTimeoutOverrideStartsWithTheDefaultInterval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   durationFlag
		env    map[string]string
		commit time.Duration
	}{
		{"flag 1s", durationFlag{value: time.Second, set: true}, nil, time.Second},
		{"flag 500ms", durationFlag{value: 500 * time.Millisecond, set: true}, nil, 500 * time.Millisecond},
		{"environment 1500ms", durationFlag{}, map[string]string{commitTimeoutEnv: "1500ms"}, 1500 * time.Millisecond},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				v, ok := tc.env[key]
				return v, ok
			}
			got, err := applyConsensusTimeoutOverrides(defaultTimingConfig(), durationFlag{}, durationFlag{}, durationFlag{}, tc.flag, lookup)
			if err != nil {
				t.Fatalf("applyConsensusTimeoutOverrides: %v", err)
			}
			if got.CommitTimeout != tc.commit {
				t.Fatalf("commit timeout %v, want %v", got.CommitTimeout, tc.commit)
			}
			if err := config.ValidateConsensus(got); err != nil {
				t.Fatalf("ValidateConsensus after the override: %v", err)
			}

			logger, buf := capturedAdvisoryLogger()
			if n := warnConsensusAdvisories(logger, got); n != 1 {
				t.Fatalf("%d advisories logged, want 1: %s", n, buf.String())
			}
			out := buf.String()
			if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "minimum block interval") {
				t.Fatalf("advisory log %q is not a WARN about the minimum block interval", out)
			}
		})
	}
}

// Nothing is logged when the interval is within half the commit timeout, or off.
func TestNoAdvisoryWhenTheIntervalFits(t *testing.T) {
	off := defaultTimingConfig()
	off.MinBlockInterval = 0
	for name, c := range map[string]config.Consensus{"defaults": defaultTimingConfig(), "interval off": off} {
		c := c
		t.Run(name, func(t *testing.T) {
			logger, buf := capturedAdvisoryLogger()
			if n := warnConsensusAdvisories(logger, c); n != 0 || buf.Len() != 0 {
				t.Fatalf("%d advisories, log %q, want none", n, buf.String())
			}
		})
	}
}

// A timer that is not positive is still refused, whatever the interval.
func TestAdvisoryDoesNotRelaxTheTimerChecks(t *testing.T) {
	c := defaultTimingConfig()
	c.CommitTimeout = time.Second
	c.PrevoteTimeout = 0
	if err := config.ValidateConsensus(c); err == nil {
		t.Fatalf("ValidateConsensus accepted a prevote timeout of zero")
	}
}
