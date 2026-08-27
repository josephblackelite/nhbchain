package events

import (
	"encoding/hex"
	"math/big"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeLendingAutoDebitSucceeded is emitted once per successful
	// fixed-term interest installment collected by
	// settleLendingAutoDebits (core/lending_autodebit_settlement.go).
	TypeLendingAutoDebitSucceeded = "lending.autodebit.succeeded"
	// TypeLendingAutoDebitFailed is emitted once per failed installment
	// attempt that has NOT yet reached the delinquency threshold (still
	// scheduled for retry).
	TypeLendingAutoDebitFailed = "lending.autodebit.failed"
	// TypeLendingFixedTermLoanDelinquent is emitted the moment a loan's
	// consecutive missed installments reaches
	// lending.AutoDebitMaxConsecutiveMisses -- see
	// lending.FixedTermLoanStatusDelinquent's doc comment for what this
	// does and deliberately does not do (no automatic collateral seizure).
	TypeLendingFixedTermLoanDelinquent = "lending.fixedterm.delinquent"
)

// LendingAutoDebitSucceeded records one successful auto-debited interest
// installment against a fixed-term loan.
type LendingAutoDebitSucceeded struct {
	LoanID         [32]byte
	Borrower       [20]byte
	PoolID         string
	Cycle          uint32
	InstallmentWei *big.Int
	NextCycle      uint32
}

func (LendingAutoDebitSucceeded) EventType() string { return TypeLendingAutoDebitSucceeded }

func (e LendingAutoDebitSucceeded) Event() *types.Event {
	installment := big.NewInt(0)
	if e.InstallmentWei != nil {
		installment = new(big.Int).Set(e.InstallmentWei)
	}
	attrs := map[string]string{
		"loanId":         hex.EncodeToString(e.LoanID[:]),
		"poolId":         e.PoolID,
		"cycle":          strconv.FormatUint(uint64(e.Cycle), 10),
		"installmentWei": installment.String(),
		"nextCycle":      strconv.FormatUint(uint64(e.NextCycle), 10),
	}
	if e.Borrower != ([20]byte{}) {
		attrs["borrower"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Borrower[:]).String()
	}
	return &types.Event{Type: TypeLendingAutoDebitSucceeded, Attributes: attrs}
}

// LendingAutoDebitFailed records one failed installment attempt that is
// still within the retry budget (not yet delinquent).
type LendingAutoDebitFailed struct {
	LoanID            [32]byte
	Borrower          [20]byte
	PoolID            string
	Cycle             uint32
	InstallmentWei    *big.Int
	ConsecutiveMissed uint32
	NextAttemptAt     uint64
}

func (LendingAutoDebitFailed) EventType() string { return TypeLendingAutoDebitFailed }

func (e LendingAutoDebitFailed) Event() *types.Event {
	installment := big.NewInt(0)
	if e.InstallmentWei != nil {
		installment = new(big.Int).Set(e.InstallmentWei)
	}
	attrs := map[string]string{
		"loanId":            hex.EncodeToString(e.LoanID[:]),
		"poolId":            e.PoolID,
		"cycle":             strconv.FormatUint(uint64(e.Cycle), 10),
		"installmentWei":    installment.String(),
		"consecutiveMissed": strconv.FormatUint(uint64(e.ConsecutiveMissed), 10),
		"nextAttemptAt":     strconv.FormatUint(e.NextAttemptAt, 10),
	}
	if e.Borrower != ([20]byte{}) {
		attrs["borrower"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Borrower[:]).String()
	}
	return &types.Event{Type: TypeLendingAutoDebitFailed, Attributes: attrs}
}

// LendingFixedTermLoanDelinquent records a loan crossing the
// AutoDebitMaxConsecutiveMisses threshold -- the signal an operator/portal
// reconcile job should watch for, since no automatic collateral recovery
// happens yet (see FixedTermLoanStatusDelinquent's doc comment).
type LendingFixedTermLoanDelinquent struct {
	LoanID         [32]byte
	Borrower       [20]byte
	PoolID         string
	OutstandingWei *big.Int
}

func (LendingFixedTermLoanDelinquent) EventType() string { return TypeLendingFixedTermLoanDelinquent }

func (e LendingFixedTermLoanDelinquent) Event() *types.Event {
	outstanding := big.NewInt(0)
	if e.OutstandingWei != nil {
		outstanding = new(big.Int).Set(e.OutstandingWei)
	}
	attrs := map[string]string{
		"loanId":         hex.EncodeToString(e.LoanID[:]),
		"poolId":         e.PoolID,
		"outstandingWei": outstanding.String(),
	}
	if e.Borrower != ([20]byte{}) {
		attrs["borrower"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Borrower[:]).String()
	}
	return &types.Event{Type: TypeLendingFixedTermLoanDelinquent, Attributes: attrs}
}
