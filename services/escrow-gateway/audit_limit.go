package main

import (
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"
)

// What the audit log keeps of a request that failed authentication. Such a
// request has no principal, so every byte of it, the path, the query and the
// body, is whatever the sender chose, and anyone who can reach the port can send
// as many as they like. The row is therefore kept small and the rate at which
// rows are written is bounded; a request that authenticated is recorded in full,
// as before.
const (
	// maxUnauthenticatedAuditBytes caps the stored path and the stored
	// response of a failed-authentication row. The response is the error text,
	// which can quote a header value the sender supplied.
	maxUnauthenticatedAuditBytes = 512

	// unauthenticatedAuditPerSecond and unauthenticatedAuditBurst are the
	// refill rate and the size of the bucket that rows for failed
	// authentications are drawn from. Rows beyond that are not written.
	unauthenticatedAuditPerSecond = 1.0
	unauthenticatedAuditBurst     = 50

	// unauthenticatedAuditReportEvery is how often the number of rows not
	// written is logged, when there are any.
	unauthenticatedAuditReportEvery = time.Minute

	truncatedMarker = "...(truncated)"

	// defaultUnauthenticatedAuditRetention is how long those rows are kept
	// unless the operator says otherwise (30 days).
	defaultUnauthenticatedAuditRetention = 30 * 24 * time.Hour

	// unauthenticatedAuditPruneEvery is how often the retention is applied.
	unauthenticatedAuditPruneEvery = time.Hour
)

// auditLimiter is a token bucket for the audit rows of failed authentications.
type auditLimiter struct {
	mu         sync.Mutex
	perSecond  float64
	burst      float64
	tokens     float64
	last       time.Time
	dropped    int
	lastReport time.Time
}

func newAuditLimiter(perSecond float64, burst int) *auditLimiter {
	return &auditLimiter{perSecond: perSecond, burst: float64(burst), tokens: float64(burst)}
}

// allow reports whether one more row may be written at now. When it may not, the
// refusal is counted and, at most once per unauthenticatedAuditReportEvery, the
// count is logged and reset.
func (l *auditLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.last = now
		l.lastReport = now
	}
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.perSecond
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	l.dropped++
	if now.Sub(l.lastReport) >= unauthenticatedAuditReportEvery {
		slog.Warn("escrow gateway did not write audit rows for failed authentications; the rate is limited",
			"notWritten", l.dropped, "since", l.lastReport.UTC().Format(time.RFC3339))
		l.dropped = 0
		l.lastReport = now
	}
	return false
}

// truncateAudit returns s unchanged when it is at most max bytes, and otherwise
// a prefix of it ending in a marker, at most max bytes in all and cut on a
// character boundary.
func truncateAudit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(truncatedMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedMarker
}
