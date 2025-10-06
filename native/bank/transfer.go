package bank

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	nhbstate "nhbchain/core/state"
)

const txHashHexLength = 64

// ParseTxHash normalises and validates a transaction hash expressed as a hex
// string. The returned array always contains the raw 32-byte hash.
func ParseTxHash(ref string) ([32]byte, error) {
	var hash [32]byte
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return hash, fmt.Errorf("bank: tx hash required")
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		trimmed = trimmed[2:]
	}
	if len(trimmed) != txHashHexLength {
		return hash, fmt.Errorf("bank: tx hash must be 32 bytes (got %d hex chars)", len(trimmed))
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return hash, fmt.Errorf("bank: decode tx hash: %w", err)
	}
	copy(hash[:], decoded)
	return hash, nil
}

// ValidateRefund ensures the refund against the supplied origin hash does not
// exceed the tracked origin amount.
func ValidateRefund(manager *nhbstate.Manager, origin [32]byte, amount *big.Int) error {
	if manager == nil {
		return fmt.Errorf("bank: state manager required")
	}
	ledger := manager.RefundLedger()
	if ledger == nil {
		return fmt.Errorf("bank: refund ledger unavailable")
	}
	return ledger.ValidateRefund(origin, amount)
}

// RecordOrigin stores the initial transfer amount for later refund tracking.
func RecordOrigin(manager *nhbstate.Manager, txHash [32]byte, amount *big.Int, timestamp uint64) error {
	if manager == nil {
		return fmt.Errorf("bank: state manager required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil
	}
	ledger := manager.RefundLedger()
	if ledger == nil {
		return fmt.Errorf("bank: refund ledger unavailable")
	}
	_, err := ledger.RecordOrigin(txHash, amount, timestamp)
	return err
}

// RecordRefund appends a refund entry to the ledger and updates the cumulative
// refunded tally.
func RecordRefund(manager *nhbstate.Manager, origin [32]byte, refund [32]byte, amount *big.Int, timestamp uint64) error {
	if manager == nil {
		return fmt.Errorf("bank: state manager required")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil
	}
	ledger := manager.RefundLedger()
	if ledger == nil {
		return fmt.Errorf("bank: refund ledger unavailable")
	}
	_, err := ledger.ApplyRefund(origin, refund, amount, timestamp)
	return err
}
