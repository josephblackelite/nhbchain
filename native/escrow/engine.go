package escrow

import "nhbchain/core/events"

// Engine wires the escrow business logic with external state and event
// emitters. The full transition logic will arrive in CODEx 1.2; for now the
// engine provides deterministic event emission helpers.
type Engine struct {
	emitter events.Emitter
}

// NewEngine creates an escrow engine with a no-op emitter. Callers can override
// the emitter via SetEmitter.
func NewEngine() *Engine {
	return &Engine{emitter: events.NoopEmitter{}}
}

// SetEmitter configures the event emitter used by the engine. Passing nil resets
// the emitter to a no-op implementation.
func (e *Engine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

func (e *Engine) emit(event events.Event) {
	if e == nil || e.emitter == nil {
		return
	}
	e.emitter.Emit(event)
}
