package rpc

import (
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/types"
)

// TransactionResult summarises an executed transaction for RPC consumers.
type TransactionResult struct {
	Hash        string `json:"hash"`
	Type        string `json:"type"`
	Asset       string `json:"asset,omitempty"`
	BlockHash   string `json:"blockHash,omitempty"`
	BlockNumber string `json:"blockNumber,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Value       string `json:"value,omitempty"`
	Nonce       string `json:"nonce,omitempty"`
	GasLimit    string `json:"gasLimit,omitempty"`
	GasPrice    string `json:"gasPrice,omitempty"`
	Input       string `json:"input,omitempty"`
}

// ReceiptResult reflects the final state of a confirmed transaction.
type ReceiptResult struct {
	TransactionHash string       `json:"transactionHash"`
	BlockHash       string       `json:"blockHash,omitempty"`
	BlockNumber     string       `json:"blockNumber,omitempty"`
	Status          string       `json:"status"`
	GasUsed         string       `json:"gasUsed"`
	Logs            []ReceiptLog `json:"logs"`
}

// ReceiptLog captures a structured event emitted during transaction execution.
type ReceiptLog map[string]string

// hexString formats a uint64 as a 0x-prefixed hexadecimal string.
func hexString(v uint64) string {
	return fmt.Sprintf("0x%x", v)
}

// hexBig formats a big integer as a 0x-prefixed hexadecimal string.
func hexBig(v *big.Int) string {
	if v == nil {
		return "0x0"
	}
	if v.Sign() == 0 {
		return "0x0"
	}
	return fmt.Sprintf("0x%x", v)
}

// formatTxType converts a TxType into a human readable label.
func formatTxType(t types.TxType) string {
	switch t {
	case types.TxTypeTransfer:
		return "Transfer"
	case types.TxTypeTransferZNHB:
		return "TransferZNHB"
	case types.TxTypeRegisterIdentity:
		return "RegisterIdentity"
	case types.TxTypeCreateEscrow:
		return "CreateEscrow"
	case types.TxTypeReleaseEscrow:
		return "ReleaseEscrow"
	case types.TxTypeRefundEscrow:
		return "RefundEscrow"
	case types.TxTypeStake:
		return "Stake"
	case types.TxTypeUnstake:
		return "Unstake"
	case types.TxTypeHeartbeat:
		return "Heartbeat"
	case types.TxTypeLockEscrow:
		return "LockEscrow"
	case types.TxTypeDisputeEscrow:
		return "DisputeEscrow"
	case types.TxTypeArbitrateRelease:
		return "ArbitrateRelease"
	case types.TxTypeArbitrateRefund:
		return "ArbitrateRefund"
	case types.TxTypeStakeClaim:
		return "StakeClaim"
	case types.TxTypeMint:
		return "Mint"
	case types.TxTypeSwapPayoutReceipt:
		return "SwapPayoutReceipt"
	default:
		return fmt.Sprintf("0x%02x", byte(t))
	}
}

// assetLabel returns the canonical asset for transfer-style transactions.
func assetLabel(t types.TxType) string {
	switch t {
	case types.TxTypeTransfer:
		return "NHB"
	case types.TxTypeTransferZNHB:
		return "ZNHB"
	default:
		return ""
	}
}

// ensureHexPrefix normalises hash-like values to use a 0x prefix.
func ensureHexPrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		return trimmed
	}
	return "0x" + trimmed
}
