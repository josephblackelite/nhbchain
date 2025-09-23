package loyalty

// Engine represents the native loyalty and rewards module. The behaviour of the
// engine is fully driven by the stored global configuration.
type Engine struct{}

// NewEngine creates a new loyalty engine.
func NewEngine() *Engine {
	return &Engine{}
}

// OnTransactionSuccess is called by the state processor after a successful
// transaction. The engine evaluates the configured base rewards and updates the
// supplied state accordingly.
func (e *Engine) OnTransactionSuccess(st BaseRewardState, ctx *BaseRewardContext) {
	if e == nil || st == nil || ctx == nil {
		return
	}
	e.ApplyBaseReward(st, ctx)
}
