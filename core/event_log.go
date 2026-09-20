package core

import (
	"strings"
	"sync"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

// The events a node reports through Node.Events (the explorer's slashing and fee
// views, the escrow event list) live in memory only and are not part of any
// state root. They used to be carried by every state processor and deep-copied
// whole each time the node copied its state -- for every block it built,
// validated or committed, and for every transaction it simulated -- and they
// were never cleared, so the cost of a block and the size of the process grew for
// as long as the node ran, and a replay from genesis was quadratic. Committed
// blocks now hand their events to a log with a fixed size, and the state keeps
// none between blocks, so copying the state costs the same on the millionth block
// as on the first.
//
// Most events are kept only while they are among the most recent
// maxRetainedEvents. The events some readers add up or count over the whole run
// -- fees applied, POTSO penalties, and the escrow events -- are kept in a second
// log of up to maxPinnedEvents, so busy events of other kinds do not push them
// out. Both are variables so a test can shrink them.
var (
	maxRetainedEvents = 20_000
	maxPinnedEvents   = 100_000
)

// isPinnedEvent reports whether an event of this type is kept in the pinned log.
func isPinnedEvent(eventType string) bool {
	return eventType == events.TypeFeeApplied ||
		eventType == events.TypePotsoPenaltyApplied ||
		strings.HasPrefix(strings.ToLower(eventType), "escrow.")
}

// retainedEvent is an event with its position in the order the node saw them.
type retainedEvent struct {
	seq   uint64
	event types.Event
}

// eventRing keeps the last limit events added to it, oldest dropped first.
type eventRing struct {
	limit int
	// buf holds up to limit events; once it is full, start is where the oldest
	// one sits and the next event replaces it.
	buf   []retainedEvent
	start int
}

func (r *eventRing) add(evt retainedEvent) {
	if len(r.buf) < r.limit {
		r.buf = append(r.buf, evt)
		return
	}
	r.buf[r.start] = evt
	r.start = (r.start + 1) % r.limit
}

// at returns the i-th oldest event held, for i below len(r.buf).
func (r *eventRing) at(i int) retainedEvent {
	return r.buf[(r.start+i)%len(r.buf)]
}

// eventLog is the node's bounded record of the events committed blocks produced.
// It is safe for concurrent use.
type eventLog struct {
	mu     sync.Mutex
	next   uint64
	recent eventRing
	pinned eventRing
}

// newEventLog returns a log that keeps the last recent events and the last
// pinned events of the kinds isPinnedEvent selects. A limit below one is one.
func newEventLog(recent, pinned int) *eventLog {
	if recent < 1 {
		recent = 1
	}
	if pinned < 1 {
		pinned = 1
	}
	return &eventLog{recent: eventRing{limit: recent}, pinned: eventRing{limit: pinned}}
}

// append adds events in order. The events are taken over, not copied: callers
// hand in events nothing else holds.
func (l *eventLog) append(batch []types.Event) {
	if l == nil || len(batch) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, evt := range batch {
		entry := retainedEvent{seq: l.next, event: evt}
		l.next++
		if isPinnedEvent(evt.Type) {
			l.pinned.add(entry)
		} else {
			l.recent.add(entry)
		}
	}
}

// snapshot returns deep copies of the retained events, oldest first.
func (l *eventLog) snapshot() []types.Event {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]types.Event, 0, len(l.recent.buf)+len(l.pinned.buf))
	i, j := 0, 0
	for i < len(l.recent.buf) || j < len(l.pinned.buf) {
		var src retainedEvent
		if j >= len(l.pinned.buf) || (i < len(l.recent.buf) && l.recent.at(i).seq < l.pinned.at(j).seq) {
			src = l.recent.at(i)
			i++
		} else {
			src = l.pinned.at(j)
			j++
		}
		attrs := make(map[string]string, len(src.event.Attributes))
		for k, v := range src.event.Attributes {
			attrs[k] = v
		}
		out = append(out, types.Event{Type: src.event.Type, Attributes: attrs})
	}
	return out
}

// takeEvents hands back the events logged on this processor so far and empties
// its log.
func (sp *StateProcessor) takeEvents() []types.Event {
	if sp == nil {
		return nil
	}
	taken := sp.events
	sp.events = nil
	return taken
}
