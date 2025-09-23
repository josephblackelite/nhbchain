Set-Content -Path .\native\loyalty\engine.go -Encoding UTF8 -Value @'
package loyalty

import "nhbchain/core/types"

// Engine represents the native loyalty and rewards module.
// Behavior is driven by global/program configuration stored in state.
type Engine struct{}

// NewEngine creates a new loyalty engine.
func NewEngine() *Engine { return &Engine{} }

// OnTransactionSuccess is kept for compatibility with the current StateProcessor call-site.
// Rewards are applied via the new context-driven paths (ApplyBaseReward / ApplyProgramReward).
func (e *Engine) OnTransactionSuccess(fromAccount *types.Account, toAccount *types.Account) {
	// no-op: rewards handled by context-driven engines
	_ = fromAccount
	_ = toAccount
}
'@
