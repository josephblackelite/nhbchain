package swap

import (
	"fmt"
	"math/big"
	"strings"

	swapv1 "nhbchain/proto/swap/v1"
)

func ensurePositiveDecimal(label, amount string) (string, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return "", fmt.Errorf("%s amount required", label)
	}
	parsed, ok := new(big.Float).SetString(trimmed)
	if !ok {
		return "", fmt.Errorf("%s amount must be numeric", label)
	}
	if parsed.Sign() <= 0 {
		return "", fmt.Errorf("%s amount must be positive", label)
	}
	return parsed.Text('f', -1), nil
}

// NewMsgSwapExactIn creates a swap instruction exchanging an exact input amount of a
// token for a minimum output amount.
func NewMsgSwapExactIn(trader, poolID, tokenIn, amountIn, minAmountOut, recipient string) (*swapv1.MsgSwapExactIn, error) {
	trimmedTrader := strings.TrimSpace(trader)
	if trimmedTrader == "" {
		return nil, fmt.Errorf("trader address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	trimmedToken := strings.TrimSpace(tokenIn)
	if trimmedToken == "" {
		return nil, fmt.Errorf("input token required")
	}
	normalizedIn, err := ensurePositiveDecimal("input", amountIn)
	if err != nil {
		return nil, err
	}
	normalizedOut, err := ensurePositiveDecimal("minimum output", minAmountOut)
	if err != nil {
		return nil, err
	}
	return &swapv1.MsgSwapExactIn{
		Trader:       trimmedTrader,
		PoolId:       trimmedPool,
		TokenIn:      trimmedToken,
		AmountIn:     normalizedIn,
		MinAmountOut: normalizedOut,
		Recipient:    strings.TrimSpace(recipient),
	}, nil
}

// NewMsgSwapExactOut creates a swap instruction targeting an exact output amount while
// capping the maximum tokens the trader is willing to supply.
func NewMsgSwapExactOut(trader, poolID, tokenOut, amountOut, maxAmountIn, recipient string) (*swapv1.MsgSwapExactOut, error) {
	trimmedTrader := strings.TrimSpace(trader)
	if trimmedTrader == "" {
		return nil, fmt.Errorf("trader address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	trimmedToken := strings.TrimSpace(tokenOut)
	if trimmedToken == "" {
		return nil, fmt.Errorf("output token required")
	}
	normalizedOut, err := ensurePositiveDecimal("output", amountOut)
	if err != nil {
		return nil, err
	}
	normalizedIn, err := ensurePositiveDecimal("max input", maxAmountIn)
	if err != nil {
		return nil, err
	}
	return &swapv1.MsgSwapExactOut{
		Trader:      trimmedTrader,
		PoolId:      trimmedPool,
		TokenOut:    trimmedToken,
		AmountOut:   normalizedOut,
		MaxAmountIn: normalizedIn,
		Recipient:   strings.TrimSpace(recipient),
	}, nil
}
