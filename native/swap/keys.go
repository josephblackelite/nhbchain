package swap

import "strings"

var (
	stableDepositVoucherPrefix   = []byte("swap/stable/voucher/")
	stableDepositVoucherIndexKey = []byte("swap/stable/voucher/index")
	stableCashOutIntentPrefix    = []byte("swap/stable/intent/")
	stableEscrowLockPrefix       = []byte("swap/stable/escrow/")
	stablePayoutReceiptPrefix    = []byte("swap/stable/receipt/")
	stableInventoryPrefix        = []byte("swap/stable/inventory/")
)

func stableDepositVoucherKey(invoiceID string) []byte {
	trimmed := strings.TrimSpace(invoiceID)
	buf := make([]byte, len(stableDepositVoucherPrefix)+len(trimmed))
	copy(buf, stableDepositVoucherPrefix)
	copy(buf[len(stableDepositVoucherPrefix):], trimmed)
	return buf
}

func stableCashOutIntentKey(intentID string) []byte {
	trimmed := strings.TrimSpace(intentID)
	buf := make([]byte, len(stableCashOutIntentPrefix)+len(trimmed))
	copy(buf, stableCashOutIntentPrefix)
	copy(buf[len(stableCashOutIntentPrefix):], trimmed)
	return buf
}

func stableEscrowLockKey(intentID string) []byte {
	trimmed := strings.TrimSpace(intentID)
	buf := make([]byte, len(stableEscrowLockPrefix)+len(trimmed))
	copy(buf, stableEscrowLockPrefix)
	copy(buf[len(stableEscrowLockPrefix):], trimmed)
	return buf
}

func stablePayoutReceiptKey(intentID string) []byte {
	trimmed := strings.TrimSpace(intentID)
	buf := make([]byte, len(stablePayoutReceiptPrefix)+len(trimmed))
	copy(buf, stablePayoutReceiptPrefix)
	copy(buf[len(stablePayoutReceiptPrefix):], trimmed)
	return buf
}

func stableInventoryKey(asset string) []byte {
	trimmed := strings.ToUpper(strings.TrimSpace(asset))
	buf := make([]byte, len(stableInventoryPrefix)+len(trimmed))
	copy(buf, stableInventoryPrefix)
	copy(buf[len(stableInventoryPrefix):], trimmed)
	return buf
}
