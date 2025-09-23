package loyalty

import "nhbchain/core/types"

// Engine represents the native loyalty and rewards module.
type Engine struct {
	// In a real implementation, this could hold configurable reward rates.
}

// NewEngine creates a new loyalty engine.
func NewEngine() *Engine {
	return &Engine{}
}

// OnTransactionSuccess is called by the state processor after a successful
// transaction. Loyalty rewards are not yet distributed at this stage; the
// registry focuses on program discovery and governance first.
func (e *Engine) OnTransactionSuccess(fromAccount *types.Account, toAccount *types.Account) {
	_ = fromAccount
	_ = toAccount
}
