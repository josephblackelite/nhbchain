package core

import (
	"math/big"
	"strings"
	"sync"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/native/fees"
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
// maxRetainedEvents. The events some readers count over the whole run -- POTSO
// penalties and the escrow events -- are kept in a second log of up to
// maxPinnedEvents, so busy events of other kinds do not push them out. Fees applied
// are not among them (PL-R5T-42): one is emitted for every transfer tagged with a fee
// domain, free-tier ones included, so they filled the second log and pushed the
// penalty and escrow events out of it; the owner wallet's fee total is read from the
// durable per-domain totals (Node.OwnerWalletFeeAccrual) instead. Both limits are
// variables so a test can shrink them.
var (
	maxRetainedEvents = 20_000
	maxPinnedEvents   = 100_000
)

// isPinnedEvent reports whether an event of this type is kept in the pinned log.
func isPinnedEvent(eventType string) bool {
	return eventType == events.TypePotsoPenaltyApplied ||
		strings.HasPrefix(strings.ToLower(eventType), "escrow.")
}

// OwnerWalletFeeAccrual sums, by asset, the fees that the durable per-domain fee totals
// credit to wallet over the given domains (PL-R5T-42). Unlike a sum over the retained
// fee events it does not shrink when the event log wraps or the node restarts.
func (n *Node) OwnerWalletFeeAccrual(wallet [20]byte, domains []string) (map[string]*big.Int, error) {
	out := make(map[string]*big.Int)
	seen := make(map[string]bool, len(domains))
	for _, domain := range domains {
		key := fees.NormalizeDomain(domain)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		totals, err := n.FeesTotals(key)
		if err != nil {
			return nil, err
		}
		for _, entry := range totals {
			if entry.Wallet != wallet || entry.Fee == nil {
				continue
			}
			asset := strings.ToUpper(strings.TrimSpace(entry.Asset))
			if asset == "" {
				asset = "UNKNOWN"
			}
			if out[asset] == nil {
				out[asset] = big.NewInt(0)
			}
			out[asset].Add(out[asset], entry.Fee)
		}
	}
	return out, nil
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

// view returns the retained events, oldest first, in a slice of its own but
// without copying their attribute maps: the log never changes an event once it
// holds it, so the maps can be read after the log has moved on. It costs a
// fixed-size copy per event, which is why a reader that must hold a lock to
// see a consistent log takes a view under the lock and copies the events
// (copyEvents) after letting go of it. The events returned must not be handed to
// anyone who may write to their maps.
func (l *eventLog) view() []types.Event {
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
		out = append(out, src.event)
	}
	return out
}

// snapshot returns deep copies of the retained events, oldest first.
func (l *eventLog) snapshot() []types.Event {
	return copyEvents(l.view())
}

// copyEvents returns deep copies of list, with an attribute map of its own for
// each event. A nil slice stays nil.
func copyEvents(list []types.Event) []types.Event {
	if list == nil {
		return nil
	}
	out := make([]types.Event, len(list))
	for i := range list {
		attrs := make(map[string]string, len(list[i].Attributes))
		for k, v := range list[i].Attributes {
			attrs[k] = v
		}
		out[i] = types.Event{Type: list[i].Type, Attributes: attrs}
	}
	return out
}

// pendingEventsView returns the events logged on this processor since the last
// commit in a slice of its own, without copying their attribute maps: the
// processor never changes an event it has logged, but it does cut the slice
// back when a transaction is rolled back and then reuses the space, so the
// slice itself must not be shared. See eventLog.view for the use.
func (sp *StateProcessor) pendingEventsView() []types.Event {
	if sp == nil {
		return nil
	}
	return append([]types.Event{}, sp.events...)
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
