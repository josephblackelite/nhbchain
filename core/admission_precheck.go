package core

import (
	"fmt"
	"math/big"
	"time"

	"nhbchain/core/types"
)

// Admission pre-checks (ledger PL-EXT-91, a bug-bounty report, 2026-11-21).
//
// validateTransaction runs a full state simulation for every transaction it admits: a copy of the state, a
// block context and an execution, with the state lock held for all of it. That used to run BEFORE the strict
// nonce sequence check of addTransaction and before any mempool quota, so a validly signed transaction that
// could only fail late (a value above the sender's balance, a stale or repeated nonce) cost a simulation each
// time it was sent, and the nonce is never consumed by a failure, so the same transaction could be sent again
// with another recipient. The two checks below refuse the cases that need only one account read, before the
// copy. They refuse nothing the simulation would have admitted: each one is a condition under which the
// execution itself returns an error (the strict nonce sequence of Native V3 transactions, the legacy rule for
// the old envelope, and "insufficient funds for transfer+gas" / "znhb transfer: insufficient balance", which
// need at least the value to be there whatever the fee, the free tier or a sponsor does).
//
// This is admission only: no block, no consensus rule and no state root changes, and a transaction that is
// already in a block is judged exactly as before.

// admissionNonceError is the refusal for a nonce that does not follow the account's. addTransaction uses the
// same function, so the message does not depend on which check sees the transaction first.
func admissionNonceError(tx *types.Transaction, expectedNonce uint64) error {
	if tx.Type > 0 { // Strict sequencing for Native V3 Executions
		if tx.Nonce != expectedNonce {
			return fmt.Errorf("%w: strict nonce sequence breached: expected %d, got %d", ErrInvalidTransaction, expectedNonce, tx.Nonce)
		}
		return nil
	}
	// Legacy EVM
	if tx.Nonce < expectedNonce {
		return fmt.Errorf("%w: nonce %d has already been used; current account nonce is %d", ErrInvalidTransaction, tx.Nonce, expectedNonce)
	}
	return nil
}

// admissionPrecheck refuses, without copying the state, a transaction that the simulation is certain to
// refuse. It returns nil for anything it cannot judge from one account read, so the simulation decides.
func (n *Node) admissionPrecheck(tx *types.Transaction, sender []byte) error {
	account, err := n.GetAccount(sender)
	if err != nil || account == nil {
		return nil // the simulation reports what is wrong with the state
	}
	if err := admissionNonceError(tx, account.Nonce); err != nil {
		return err
	}
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return nil
	}
	switch tx.Type {
	case types.TxTypeTransfer:
		if balanceBelow(account.BalanceNHB, tx.Value) {
			return fmt.Errorf("%w: insufficient funds for transfer+gas", ErrInvalidTransaction)
		}
	case types.TxTypeTransferZNHB:
		if balanceBelow(account.BalanceZNHB, tx.Value) {
			return fmt.Errorf("%w: znhb transfer: insufficient balance", ErrInvalidTransaction)
		}
	}
	return nil
}

// heartbeatAdmissionWindowSeconds is how far from the node's own clock a heartbeat's timestamp may be, in
// either direction, for the node to ADMIT it (ledger PL-EXT-71, several bug-bounty reports, one of them with
// the cost measured, 2026-11-25).
//
// applyHeartbeat accepts any signed timestamp, and rolloverEngagement then walks ONE DAY AT A TIME from the
// account's stored day to the timestamp's day, keeping a score update (and emitting an event) for every day:
// a timestamp dated the year 9999 is 2.9 million days, which the reporter measured at 6.3 seconds, 7.7 GB
// allocated and 2.5 GB resident for ONE transaction, and a first heartbeat dated the year 1 does the same
// walk on the account's NEXT transaction. The account needs no balance and the heartbeat pays no fee. The
// admission simulation executes the transaction, so a node would do that work just to say yes or no.
//
// The consensus rule that bounds the timestamp (HeartbeatTimestampBoundActivationHeight, 120 seconds after the
// block time) stays off until a height is set; this check is admission only and changes no block, rule or
// state root. One day either way is generous on purpose: it turns the walk into at most a few iterations and
// refuses only a clock that is wrong by more than a day, which no heartbeat can honestly carry. A timestamp of
// 0 means the block time and is never refused here.
const heartbeatAdmissionWindowSeconds = 24 * 60 * 60

// heartbeatDeviceIDMaxBytes is the longest device id a heartbeat may carry for the node to ADMIT it (ledger
// PL-EXT-93, a bug-bounty report, 2026-11-25).
//
// applyHeartbeat copies the payload's device id, unchecked, into the engagement.heartbeat event, and the node
// keeps the last 20,000 events in memory counted by number, not by size. The heartbeat is free, the account needs
// no balance, and a block may carry about 600 KB, so a stream of heartbeats with a device id of several hundred
// kilobytes each fills that log with gigabytes: the reporter measured 200 events at 204,800 bytes each and
// projects 4.1 GB at the full ring, more than a small validator host holds in all. Real device
// ids (a uuid, a host name) are far under 256 bytes. This is admission only: it changes no block, rule or state
// root, and a heartbeat already in a block is executed as before.
const heartbeatDeviceIDMaxBytes = 256

// admissionHeartbeatError refuses a heartbeat whose timestamp is more than a day from now, or whose device id is
// longer than heartbeatDeviceIDMaxBytes.
func admissionHeartbeatError(tx *types.Transaction, now time.Time) error {
	if tx == nil || tx.Type != types.TxTypeHeartbeat || len(tx.Data) == 0 {
		return nil
	}
	var payload types.HeartbeatPayload
	if err := decodeHeartbeatPayload(tx.Data, &payload); err != nil {
		return nil // the execution reports an undecodable payload
	}
	if len(payload.DeviceID) > heartbeatDeviceIDMaxBytes {
		return fmt.Errorf("%w: heartbeat device id is %d bytes, over the %d byte limit",
			ErrInvalidTransaction, len(payload.DeviceID), heartbeatDeviceIDMaxBytes)
	}
	if payload.Timestamp == 0 {
		return nil
	}
	nowUnix := now.Unix()
	if payload.Timestamp > nowUnix+heartbeatAdmissionWindowSeconds || payload.Timestamp < nowUnix-heartbeatAdmissionWindowSeconds {
		return fmt.Errorf("%w: heartbeat timestamp %d is more than %d seconds from the node's clock %d",
			ErrInvalidTransaction, payload.Timestamp, heartbeatAdmissionWindowSeconds, nowUnix)
	}
	return nil
}

// balanceBelow reports whether the balance (nil counts as zero) is less than the value.
func balanceBelow(balance, value *big.Int) bool {
	if balance == nil {
		return value.Sign() > 0
	}
	return balance.Cmp(value) < 0
}
