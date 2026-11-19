package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// RoleLendingProtocolAdmin is the role that may withdraw a pool's protocol fees
// (TxTypeLendingWithdrawProtocolFees). It is granted by a role.allowlist proposal,
// and its name must first be in [governance] AllowedRoles on every validator.
const RoleLendingProtocolAdmin = "ROLE_LENDING_PROTOCOL_ADMIN"

var (
	// ErrLendingFeeWithdrawInvalidPayload: the payload does not decode, names no
	// recipient or an unusable one, or an amount that is not positive. A pure function
	// of the transaction's own bytes; pruned.
	ErrLendingFeeWithdrawInvalidPayload = errors.New("lending: invalid fee withdrawal payload")
	// ErrLendingFeeWithdrawUnauthorized: the sender does not hold
	// RoleLendingProtocolAdmin. The role can be granted later, so it is skipped.
	ErrLendingFeeWithdrawUnauthorized = errors.New("lending: the sender may not withdraw protocol fees")
	// ErrLendingFeeWithdrawExceedsAvailable: the amount is more than the pool's fee
	// counter or its free liquidity. Fees accrue and loans are repaid, so it is skipped.
	ErrLendingFeeWithdrawExceedsAvailable = errors.New("lending: the amount is more than the pool can pay out as protocol fees")
)

// lendingFeeWithdrawPayload is the Data of a TxTypeLendingWithdrawProtocolFees
// transaction. The amount is a decimal string, the way every other amount in a
// payload is, so no precision is lost in transit.
type lendingFeeWithdrawPayload struct {
	PoolID    string `json:"poolId,omitempty"`
	Recipient string `json:"recipient"`
	AmountWei string `json:"amountWei"`
}

// applyLendingWithdrawProtocolFeesTransaction pays part of a pool's protocol-fee
// counter to a recipient the sender names (ledger PL-DC-19). The sender must hold
// RoleLendingProtocolAdmin; replay is the sender's nonce like any signed transaction;
// the pool must exist (a withdrawal never creates one); the amount is bounded by the
// counter and by the pool's own free liquidity, so a pool can never be paid out of the
// cash of another pool that shares the module balance. Everything is checked before
// anything is written.
func (sp *StateProcessor) applyLendingWithdrawProtocolFeesTransaction(tx *types.Transaction, sender []byte) error {
	if tx == nil {
		return fmt.Errorf("%w: transaction required", ErrLendingFeeWithdrawInvalidPayload)
	}
	var payload lendingFeeWithdrawPayload
	if err := json.Unmarshal(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrLendingFeeWithdrawInvalidPayload, err)
	}
	poolID := normalizeLendingPoolID(payload.PoolID)
	amount, ok := new(big.Int).SetString(strings.TrimSpace(payload.AmountWei), 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("%w: amount must be a positive whole number of wei", ErrLendingFeeWithdrawInvalidPayload)
	}
	recipient, err := crypto.DecodeAddress(strings.TrimSpace(payload.Recipient))
	if err != nil || recipient.Prefix() != crypto.NHBPrefix {
		return fmt.Errorf("%w: recipient must be an NHB address", ErrLendingFeeWithdrawInvalidPayload)
	}

	manager := nhbstate.NewManager(sp.Trie)
	if !manager.HasRole(RoleLendingProtocolAdmin, sender) {
		return ErrLendingFeeWithdrawUnauthorized
	}
	if _, found, err := manager.LendingGetMarket(poolID); err != nil {
		return err
	} else if !found {
		return fmt.Errorf("%w: %q", ErrLendingPoolNotFound, poolID)
	}

	engine, _, err := sp.lendingEngine(poolID)
	if err != nil {
		return err
	}
	withdrawn, err := engine.WithdrawProtocolFees(recipient, amount)
	if err != nil {
		if errors.Is(err, lending.ErrInsufficientLiquidity) {
			return fmt.Errorf("%w: %v", ErrLendingFeeWithdrawExceedsAvailable, err)
		}
		return err
	}
	if evt := (events.LendingProtocolFeesWithdrawn{
		PoolID:    poolID,
		Admin:     crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...)),
		Recipient: recipient,
		Amount:    withdrawn,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return sp.incrementNativeAccountNonce(sender)
}
