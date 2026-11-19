package core

import (
	"bytes"
	"errors"
	"fmt"

	"nhbchain/core/types"
)

// This file holds the mempool admission rules for the transaction types that
// carry no recoverable sender (types.RequiresSignature reports false). Such a
// transaction authenticates only its Data payload: the rest of the envelope
// (nonce, gas fields, expiry fields, intent reference, ...) is hashed but not
// covered by any signature, and the per-sender mempool limits cannot apply to
// it because there is no sender to count against.
//
// Everything here is node-local admission policy. It is evaluated only when a
// transaction is offered to this node's mempool (RPC, gRPC or peer gossip). It
// is never consulted when a block is validated or applied, so it cannot change
// how any block executes, and a block that carries a transaction this node
// would not have admitted still validates and applies exactly as before.

// ErrMempoolSenderlessTypeLimit is returned when too many transactions of one
// sender-less type are already pending. It wraps ErrMempoolFull so callers
// that already treat a full mempool as back-pressure (the RPC layer answers
// 503) handle it the same way.
var ErrMempoolSenderlessTypeLimit = fmt.Errorf("%w: pending limit for this transaction type reached", ErrMempoolFull)

// maxPendingSenderlessPerType bounds how many transactions of a single
// sender-less type may be pending in the mempool at once. Honest traffic for
// these types is a handful per block interval; the bound only has to leave
// room for that while keeping copies of one payload that differ only inside
// Data (which no envelope rule can see) from filling the shared mempool.
const maxPendingSenderlessPerType = 64

// senderlessTypePendingLimit returns the per-type pending bound for t, or 0
// when t is not bounded per type. The two mint types are not: both already
// admit at most one pending transaction per voucher identity (invoice id, or
// provider transaction id and order id; see addTransaction), so their
// pending count follows the number of distinct signed vouchers rather than
// the number of copies of one voucher.
func senderlessTypePendingLimit(t types.TxType) int {
	switch t {
	case types.TxTypeMint, types.TxTypeSwapVoucherMint:
		return 0
	default:
		return maxPendingSenderlessPerType
	}
}

// senderlessEnvelopeCarriesIntent reports whether t may carry IntentRef and
// IntentExpiry on its envelope. On this line no sender-less type does: the
// lending fee withdrawals, which needed them, are not part of it, so both
// fields must be empty. A type added later that must carry them is listed here
// on purpose, and senderlessReviewedTypes in the tests forces that decision.
func senderlessEnvelopeCarriesIntent(t types.TxType) bool {
	return false
}

// senderlessEnvelopeError reports why the envelope of a sender-less
// transaction is not the canonical shape this node's own submitters build:
// every field other than ChainID, Type and Data left at its zero value, with
// IntentRef and IntentExpiry additionally allowed for the types selected by
// senderlessEnvelopeCarriesIntent. It returns nil for a canonical envelope.
func senderlessEnvelopeError(tx *types.Transaction) error {
	switch {
	case tx.Nonce != 0:
		return errors.New("nonce must be zero")
	case tx.GasLimit != 0:
		return errors.New("gasLimit must be zero")
	case tx.GasPrice != nil && tx.GasPrice.Sign() != 0:
		return errors.New("gasPrice must be zero")
	case tx.Value != nil && tx.Value.Sign() != 0:
		return errors.New("value must be zero")
	case tx.MaxBlockHeight != 0:
		return errors.New("maxBlockHeight must be zero")
	case len(tx.To) != 0:
		return errors.New("to must be empty")
	case len(tx.Paymaster) != 0:
		return errors.New("paymaster must be empty")
	case tx.MerchantAddress != "" || tx.DeviceID != "" || tx.RefundOf != "":
		return errors.New("merchantAddr, deviceId and refundOf must be empty")
	case tx.R != nil && tx.R.Sign() != 0,
		tx.S != nil && tx.S.Sign() != 0,
		tx.V != nil && tx.V.Sign() != 0,
		tx.PaymasterR != nil && tx.PaymasterR.Sign() != 0,
		tx.PaymasterS != nil && tx.PaymasterS.Sign() != 0,
		tx.PaymasterV != nil && tx.PaymasterV.Sign() != 0:
		return errors.New("envelope signatures must be empty")
	}
	if !senderlessEnvelopeCarriesIntent(tx.Type) && (len(tx.IntentRef) != 0 || tx.IntentExpiry != 0) {
		return errors.New("intentRef and intentExpiry must be empty for this transaction type")
	}
	return nil
}

// senderlessPendingCheckLocked applies the sender-less duplicate and per-type
// bounds against the resident mempool. It reports duplicate=true when a
// pending transaction of the same type already carries byte-identical Data;
// the caller treats that as an already-accepted submission. Callers must hold
// n.mempoolMu.
func (n *Node) senderlessPendingCheckLocked(tx *types.Transaction) (duplicate bool, err error) {
	pending := 0
	for _, existing := range n.mempool {
		if existing == nil || existing.Type != tx.Type {
			continue
		}
		if bytes.Equal(existing.Data, tx.Data) {
			return true, nil
		}
		pending++
	}
	if limit := senderlessTypePendingLimit(tx.Type); limit > 0 && pending >= limit {
		return false, ErrMempoolSenderlessTypeLimit
	}
	return false, nil
}
