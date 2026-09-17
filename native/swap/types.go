package swap

import "math/big"

// StableAsset enumerates supported fiat-backed stablecoins handled by the swap module.
type StableAsset string

const (
	// StableAssetUSDC represents USD Coin deposits and payouts.
	StableAssetUSDC StableAsset = "USDC"
	// StableAssetUSDT represents Tether USD deposits and payouts.
	StableAssetUSDT StableAsset = "USDT"
)

// DepositVoucher captures a fiat on-ramp event that credits NHB in exchange for
// an off-chain USDC/USDT transfer.
type DepositVoucher struct {
	InvoiceID    string
	Provider     string
	StableAsset  StableAsset
	StableAmount *big.Int
	NhbAmount    *big.Int
	Account      string
	Memo         string
	CreatedAt    int64
}

// CashOutStatus captures the lifecycle state for a pending stable payout.
type CashOutStatus string

const (
	// CashOutStatusPending identifies intents that are escrowed but not yet settled.
	CashOutStatusPending CashOutStatus = "pending"
	// CashOutStatusSettled marks intents that have been paid out and burned on-chain.
	CashOutStatusSettled CashOutStatus = "settled"
	// CashOutStatusAborted marks intents that have been refunded to the submitter.
	CashOutStatusAborted CashOutStatus = "aborted"
)

// CashOutIntent records a user's desire to convert NHB back into a stablecoin.
type CashOutIntent struct {
	IntentID     string
	InvoiceID    string
	Account      string
	StableAsset  StableAsset
	StableAmount *big.Int
	NhbAmount    *big.Int
	CreatedAt    int64
	Status       CashOutStatus
	EscrowLockID string
	SettledAt    int64
}

// EscrowLock tracks NHB that has been isolated pending a cash-out receipt.
type EscrowLock struct {
	LockID    string
	IntentID  string
	Account   string
	NhbAmount *big.Int
	LockedAt  int64
	Burned    bool
	BurnedAt  int64
}

// PayoutReceipt proves that an off-chain redemption has occurred.
type PayoutReceipt struct {
	ReceiptID    string
	IntentID     string
	StableAsset  StableAsset
	StableAmount *big.Int
	NhbAmount    *big.Int
	TxHash       string
	EvidenceURI  string
	SettledAt    int64
}

// TreasurySoftInventory aggregates stable credits that remain available for redemptions.
type TreasurySoftInventory struct {
	StableAsset StableAsset
	Deposits    *big.Int
	Payouts     *big.Int
	Balance     *big.Int
	UpdatedAt   int64
}

// Clone returns a deep copy of the inventory record.
func (i *TreasurySoftInventory) Clone() *TreasurySoftInventory {
	if i == nil {
		return nil
	}
	clone := *i
	if i.Deposits != nil {
		clone.Deposits = new(big.Int).Set(i.Deposits)
	}
	if i.Payouts != nil {
		clone.Payouts = new(big.Int).Set(i.Payouts)
	}
	if i.Balance != nil {
		clone.Balance = new(big.Int).Set(i.Balance)
	}
	return &clone
}
