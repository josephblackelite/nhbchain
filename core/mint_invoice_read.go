package core

import (
	"errors"
	"fmt"
	"strings"

	nhbstate "nhbchain/core/state"
)

// ErrMintInvoiceIDRequired is returned when a mint invoice is asked about without an identifier.
var ErrMintInvoiceIDRequired = errors.New("mint: invoice id required")

// MintInvoiceStatus is what the committed state says about one mint invoice identifier.
type MintInvoiceStatus struct {
	// InvoiceID is the identifier that was looked up: the one asked about, trimmed as the mint trims it.
	InvoiceID string
	// Settled is true when a mint carrying this identifier has been executed in a committed block.
	Settled bool
	// Height is the height of the next block. The answer was read from the state that block will be built
	// on, which is the state left by the block before it.
	Height uint64
}

// MintInvoiceSettled reports whether a mint carrying this invoice identifier has been executed in a
// committed block.
//
// A mint voucher's identifier is executed once. A second submission of the same identifier is refused with
// ErrMintInvoiceUsed in two different situations, and the refusal does not say which: the first mint has
// been executed (applyMintTransaction found the identifier recorded in the state), or the first mint is
// only waiting in this node's mempool (addTransaction found it there). A mint that is only waiting can
// still be dropped, for example when its voucher expires or when the node restarts, and then nothing was
// minted. This read is the way to tell the two apart.
//
// It reads the committed state only, the same key applyMintTransaction writes (nhbstate.MintInvoiceKey).
// It never looks at the mempool or at a block that was built and not committed, so a mint that is only
// waiting reads as not settled. It writes nothing.
func (n *Node) MintInvoiceSettled(invoiceID string) (MintInvoiceStatus, error) {
	trimmed := strings.TrimSpace(invoiceID)
	if trimmed == "" {
		return MintInvoiceStatus{}, ErrMintInvoiceIDRequired
	}
	if n == nil {
		return MintInvoiceStatus{}, fmt.Errorf("node unavailable")
	}
	status := MintInvoiceStatus{InvoiceID: trimmed}
	err := n.WithState(func(manager *nhbstate.Manager) error {
		var used bool
		found, err := manager.KVGet(nhbstate.MintInvoiceKey(trimmed), &used)
		if err != nil {
			return err
		}
		status.Settled = found && used
		// Read while the state lock is held, so the height and the answer belong to the same state.
		if n.chain != nil {
			status.Height = n.chain.GetHeight() + 1
		}
		return nil
	})
	if err != nil {
		return MintInvoiceStatus{}, err
	}
	return status, nil
}
