package core

import (
	"fmt"
	"log/slog"
	"time"

	"nhbchain/observability"
)

// The liveness watchdog is an independent goroutine that notices when the
// committed height stops advancing and says so, with enough diagnostics to
// tell the possible causes apart. It deliberately does not depend on the BFT
// engine: a stall is exactly when the engine cannot be relied on to report on
// itself, and the chain's last halt produced no signal at all beyond stdout
// lines nobody was watching.
//
// It observes the committed height by polling rather than by hooking commit, so
// it adds nothing to the commit path and sees every way a block can land
// (a live BFT commit or a synced block) uniformly.

const (
	// watchdogTick is how often the watchdog samples the committed height.
	watchdogTick = 5 * time.Second
	// stallReAlertEvery is how often a continuing stall is re-reported.
	stallReAlertEvery = time.Minute
)

// startLivenessWatchdog starts the watchdog once per node.
func (n *Node) startLivenessWatchdog() {
	if n == nil {
		return
	}
	n.watchdogOnce.Do(func() {
		if n.chain != nil {
			// Process start counts as a commit for the stall clock.
			n.observeCommitProgress(n.chain.GetHeight(), n.localNow())
		}
		go func() {
			ticker := time.NewTicker(watchdogTick)
			defer ticker.Stop()
			for range ticker.C {
				n.livenessCheck(n.localNow())
			}
		}()
	})
}

// livenessCheck is one watchdog tick at time now. It is separate from the
// goroutine so tests can drive it with a fake clock.
func (n *Node) livenessCheck(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("LIVENESS: watchdog panic", slog.String("panic", sanitizeLogText(fmt.Sprint(r))))
		}
	}()
	if n == nil || n.chain == nil {
		return
	}
	cfg := n.buildConfigSnapshot()
	height := n.chain.GetHeight()
	since := n.observeCommitProgress(height, now)
	stalled := cfg.StallAfter > 0 && since >= cfg.StallAfter

	var reportStall, recovered bool
	n.build.mu.Lock()
	if stalled {
		if !n.build.wdStalled || now.Sub(n.build.wdLastAlert) >= stallReAlertEvery {
			reportStall = true
			n.build.wdLastAlert = now
		}
		n.build.wdStalled = true
	} else if n.build.wdStalled {
		n.build.wdStalled = false
		recovered = true
	}
	n.build.mu.Unlock()

	metrics := observability.Consensus()
	metrics.SetSecondsSinceLastCommit(since.Seconds())
	metrics.SetLastCommitHeight(height)
	metrics.SetLivenessStalled(stalled)

	if reportStall {
		slog.Error(fmt.Sprintf("LIVENESS: no block committed for %ds", int(since.Seconds())),
			n.livenessFields("no_commit")...)
	}
	if recovered {
		slog.Info("LIVENESS: recovered", n.livenessFields("recovered")...)
	}
}
