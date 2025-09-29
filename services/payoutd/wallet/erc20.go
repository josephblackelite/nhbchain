package wallet

import (
	"context"
	"math/big"
	"time"
)

// ERC20Wallet captures the functionality payoutd requires from the treasury hot wallet.
type ERC20Wallet interface {
	Transfer(ctx context.Context, asset, destination string, amount *big.Int) (string, error)
	WaitForConfirmations(ctx context.Context, txHash string, confirmations int, pollInterval time.Duration) error
}

// FuncWallet adapts callback functions to the ERC20Wallet interface.
type FuncWallet struct {
	TransferFunc func(ctx context.Context, asset, destination string, amount *big.Int) (string, error)
	ConfirmFunc  func(ctx context.Context, txHash string, confirmations int, pollInterval time.Duration) error
}

// Transfer delegates to the configured callback.
func (w FuncWallet) Transfer(ctx context.Context, asset, destination string, amount *big.Int) (string, error) {
	if w.TransferFunc == nil {
		return "", nil
	}
	return w.TransferFunc(ctx, asset, destination, amount)
}

// WaitForConfirmations delegates to the configured callback.
func (w FuncWallet) WaitForConfirmations(ctx context.Context, txHash string, confirmations int, pollInterval time.Duration) error {
	if w.ConfirmFunc == nil {
		return nil
	}
	return w.ConfirmFunc(ctx, txHash, confirmations, pollInterval)
}
