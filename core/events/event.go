package events

// Event represents a structured state change emitted by the chain.
type Event interface {
	EventType() string
}

// Emitter broadcasts events to downstream subscribers (e.g. RPC, indexers).
type Emitter interface {
	Emit(Event)
}

// NoopEmitter is a helper that satisfies the Emitter interface while discarding
// all events. It is useful when a component wants to optionally expose events.
type NoopEmitter struct{}

// Emit implements the Emitter interface.
func (NoopEmitter) Emit(Event) {}
