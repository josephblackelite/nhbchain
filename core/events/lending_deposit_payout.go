package events

import (
	"encoding/hex"
	"math/big"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeLendingDepositPayoutSucceeded is emitted once per successful
	// fixed-term deposit payout step (interest installment, principal, or
	// both together) by settleLendingDepositPayouts
	// (core/lending_deposit_payout_settlement.go).
	TypeLendingDepositPayoutSucceeded = "lending.depositpayout.succeeded"
	// TypeLendingDepositPayoutDelayed is emitted when a payout attempt is
	// deferred for a genuine, expected timing mismatch (the fixed-term
	// deposit reserve or general pool liquidity hasn't caught up yet) --
	// NOT an error; the attempt is simply rescheduled shortly.
	TypeLendingDepositPayoutDelayed = "lending.depositpayout.delayed"
)

// LendingDepositPayoutSucceeded records one successful payout step against
// a fixed-term deposit.
type LendingDepositPayoutSucceeded struct {
	DepositID        [32]byte
	Depositor        [20]byte
	PoolID           string
	InterestPaidWei  *big.Int
	PrincipalPaidWei *big.Int
	Matured          bool
}

func (LendingDepositPayoutSucceeded) EventType() string { return TypeLendingDepositPayoutSucceeded }

func (e LendingDepositPayoutSucceeded) Event() *types.Event {
	interest := big.NewInt(0)
	if e.InterestPaidWei != nil {
		interest = new(big.Int).Set(e.InterestPaidWei)
	}
	principal := big.NewInt(0)
	if e.PrincipalPaidWei != nil {
		principal = new(big.Int).Set(e.PrincipalPaidWei)
	}
	attrs := map[string]string{
		"depositId":        hex.EncodeToString(e.DepositID[:]),
		"poolId":           e.PoolID,
		"interestPaidWei":  interest.String(),
		"principalPaidWei": principal.String(),
		"matured":          strconv.FormatBool(e.Matured),
	}
	if e.Depositor != ([20]byte{}) {
		attrs["depositor"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Depositor[:]).String()
	}
	return &types.Event{Type: TypeLendingDepositPayoutSucceeded, Attributes: attrs}
}

// LendingDepositPayoutDelayed records a payout attempt that could not be
// completed yet for an expected, retryable reason.
type LendingDepositPayoutDelayed struct {
	DepositID     [32]byte
	Depositor     [20]byte
	PoolID        string
	Reason        string
	NextAttemptAt uint64
}

func (LendingDepositPayoutDelayed) EventType() string { return TypeLendingDepositPayoutDelayed }

func (e LendingDepositPayoutDelayed) Event() *types.Event {
	attrs := map[string]string{
		"depositId":     hex.EncodeToString(e.DepositID[:]),
		"poolId":        e.PoolID,
		"reason":        e.Reason,
		"nextAttemptAt": strconv.FormatUint(e.NextAttemptAt, 10),
	}
	if e.Depositor != ([20]byte{}) {
		attrs["depositor"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Depositor[:]).String()
	}
	return &types.Event{Type: TypeLendingDepositPayoutDelayed, Attributes: attrs}
}
