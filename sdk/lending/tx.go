package lending

import (
	"fmt"
	"math/big"
	"strings"

	lendingv1 "nhbchain/proto/lending/v1"
)

// ensurePositiveAmount parses and validates that the provided string represents a
// strictly positive integer. Amounts are encoded as base-10 strings to match protobuf
// payload expectations.
func ensurePositiveAmount(label, amount string) (string, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return "", fmt.Errorf("%s amount required", label)
	}
	parsed, ok := new(big.Int).SetString(trimmed, 10)
	if !ok || parsed.Sign() <= 0 {
		return "", fmt.Errorf("%s amount must be a positive integer", label)
	}
	return parsed.String(), nil
}

// NewMsgSupply constructs a lending supply message ensuring the caller has provided
// the minimum required fields.
func NewMsgSupply(supplier, poolID, amount string) (*lendingv1.MsgSupply, error) {
	trimmedSupplier := strings.TrimSpace(supplier)
	if trimmedSupplier == "" {
		return nil, fmt.Errorf("supplier address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	normalizedAmount, err := ensurePositiveAmount("supply", amount)
	if err != nil {
		return nil, err
	}
	return &lendingv1.MsgSupply{
		Supplier: trimmedSupplier,
		PoolId:   trimmedPool,
		Amount:   normalizedAmount,
	}, nil
}

// NewMsgBorrow constructs a borrowing request with validation around the borrower
// identity, pool identifier and amount. The recipient field is optional but will be
// normalised if provided.
func NewMsgBorrow(borrower, poolID, amount, recipient string) (*lendingv1.MsgBorrow, error) {
	trimmedBorrower := strings.TrimSpace(borrower)
	if trimmedBorrower == "" {
		return nil, fmt.Errorf("borrower address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	normalizedAmount, err := ensurePositiveAmount("borrow", amount)
	if err != nil {
		return nil, err
	}
	msg := &lendingv1.MsgBorrow{
		Borrower:  trimmedBorrower,
		PoolId:    trimmedPool,
		Amount:    normalizedAmount,
		Recipient: strings.TrimSpace(recipient),
	}
	return msg, nil
}

// NewMsgRepay validates the inputs for a repay instruction.
func NewMsgRepay(payer, poolID, amount string) (*lendingv1.MsgRepay, error) {
	trimmedPayer := strings.TrimSpace(payer)
	if trimmedPayer == "" {
		return nil, fmt.Errorf("payer address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	normalizedAmount, err := ensurePositiveAmount("repay", amount)
	if err != nil {
		return nil, err
	}
	return &lendingv1.MsgRepay{
		Payer:  trimmedPayer,
		PoolId: trimmedPool,
		Amount: normalizedAmount,
	}, nil
}

// NewMsgLiquidate builds a liquidation request validating actor addresses and the
// repay amount.
func NewMsgLiquidate(liquidator, poolID, borrower, repayAmount string) (*lendingv1.MsgLiquidate, error) {
	trimmedLiquidator := strings.TrimSpace(liquidator)
	if trimmedLiquidator == "" {
		return nil, fmt.Errorf("liquidator address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		return nil, fmt.Errorf("pool id required")
	}
	trimmedBorrower := strings.TrimSpace(borrower)
	if trimmedBorrower == "" {
		return nil, fmt.Errorf("borrower address required")
	}
	normalizedAmount, err := ensurePositiveAmount("repay", repayAmount)
	if err != nil {
		return nil, err
	}
	return &lendingv1.MsgLiquidate{
		Liquidator:  trimmedLiquidator,
		PoolId:      trimmedPool,
		Borrower:    trimmedBorrower,
		RepayAmount: normalizedAmount,
	}, nil
}
