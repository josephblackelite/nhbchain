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

// BalanceProvider exposes on-chain balance inspection for treasury controls.
type BalanceProvider interface {
	Balance(ctx context.Context, asset string) (*big.Int, error)
}

// AssetStatus describes how a treasury wallet routes a specific payout asset.
type AssetStatus struct {
	Native       bool   `json:"native"`
	TokenAddress string `json:"token_address,omitempty"`
}

// Status summarises the active treasury wallet wiring exposed through admin APIs.
type Status struct {
	Mode        string                 `json:"mode"`
	RPCURL      string                 `json:"rpc_url,omitempty"`
	ChainID     string                 `json:"chain_id,omitempty"`
	FromAddress string                 `json:"from_address,omitempty"`
	Assets      map[string]AssetStatus `json:"assets,omitempty"`
}

// StatusProvider exposes wallet configuration/status for administrative reporting.
type StatusProvider interface {
	Status() Status
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
