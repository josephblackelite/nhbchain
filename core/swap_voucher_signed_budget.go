package core

import (
	"fmt"
	"math/big"
	"strings"

	swap "nhbchain/native/swap"
)

// PL-EXT-35: the budget the treasury's curve-price guard holds a voucher mint to comes
// only from what the mint authority signed, from a configured height.
//
// A voucher mint compares the Treasury Distribution Curve's cost of the voucher's ZNHB
// amount with a USD budget. The budget was the submission's own usdAmount, and only when
// that was empty, for a voucher in USD, the signed fiat amount. The submission's usdAmount
// is covered by neither signed digest and the transaction is senderless, so whoever
// submitted the voucher (or edited the transaction on its way) could write the budget that
// made the guard pass: a voucher the issuer sold below the curve price could be minted at
// the issuer's price, and for a voucher in another currency the submitter supplied the
// whole budget.
//
// From the height the budget is the signed fiat amount of a voucher in USD, a submission
// whose usdAmount is set and differs from it is refused, and a voucher in any other
// currency is refused too: nothing the issuer signs gives its value in USD, so no budget
// can be derived from signed data. A signed USD figure for those needs a new voucher
// version; until one exists the rule is switched on only once the issuer sells in USD.

// SetSwapVoucherSignedBudget sets the first block height from which a voucher mint's USD
// budget is read only from signed fields. Zero (the default) keeps the submission's
// usdAmount at every height. A chain rule: the same value on every validator, set before
// the chain reaches the height.
func (sp *StateProcessor) SetSwapVoucherSignedBudget(fromHeight uint64) {
	if sp == nil {
		return
	}
	sp.swapVoucherSignedBudgetFromHeight = fromHeight
}

// swapVoucherSignedBudgetActive reports whether the block being executed takes the
// voucher's USD budget from signed fields only.
func (sp *StateProcessor) swapVoucherSignedBudgetActive() bool {
	return sp != nil && sp.swapVoucherSignedBudgetFromHeight != 0 && sp.blockHeight() >= sp.swapVoucherSignedBudgetFromHeight
}

// swapVoucherUSDBudget is the USD figure the curve guard compares the curve cost with, and
// the value stored as the voucher's USD amount.
func (sp *StateProcessor) swapVoucherUSDBudget(voucher *swap.VoucherV1, submitted string) (string, error) {
	submitted = strings.TrimSpace(submitted)
	if !sp.swapVoucherSignedBudgetActive() {
		if submitted == "" && strings.EqualFold(voucher.Fiat, "USD") {
			submitted = strings.TrimSpace(voucher.FiatAmount)
		}
		return submitted, nil
	}
	if !strings.EqualFold(strings.TrimSpace(voucher.Fiat), "USD") {
		return "", fmt.Errorf("%w: a %s voucher carries no signed USD value to hold the treasury to its curve price",
			ErrSwapUnsupportedFiat, strings.ToUpper(strings.TrimSpace(voucher.Fiat)))
	}
	signed := strings.TrimSpace(voucher.FiatAmount)
	if submitted != "" {
		given, okGiven := new(big.Rat).SetString(submitted)
		want, okWant := new(big.Rat).SetString(signed)
		if !okGiven || !okWant || given.Cmp(want) != 0 {
			return "", fmt.Errorf("%w: usdAmount differs from the signed fiat amount", ErrSwapVoucherInvalidPayload)
		}
	}
	return signed, nil
}
