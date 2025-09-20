package loyalty

import (
	"math/big"
	"nhbchain/core/types"
)

// Engine represents the native loyalty and rewards module.
type Engine struct {
	// In a real implementation, this could hold configurable reward rates.
}

// NewEngine creates a new loyalty engine.
func NewEngine() *Engine {
	return &Engine{}
}

// OnTransactionSuccess is called by the state processor after a successful transaction.
// It updates the accounts with their ZapNHB rewards.
func (e *Engine) OnTransactionSuccess(fromAccount *types.Account, toAccount *types.Account) {
	// Define the reward amount (1 ZapNHB)
	rewardAmount := big.NewInt(1)

	// Add the reward to the sender's ZapNHB balance
	fromAccount.BalanceZNHB.Add(fromAccount.BalanceZNHB, rewardAmount)

	// Add the reward to the recipient's ZapNHB balance
	toAccount.BalanceZNHB.Add(toAccount.BalanceZNHB, rewardAmount)
}
