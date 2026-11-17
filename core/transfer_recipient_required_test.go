package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
)

// PL-DC-31: there is no contract-creation transaction type. The geth EVM tail of
// applyEvmTransaction is only reachable through a native transfer, and a
// transfer must name a real recipient before anything else runs, so a "create
// contract" shaped transfer (no recipient) is refused outright instead of falling
// into code that was never meant to be reached.
func TestNativeTransferWithoutARealRecipientIsRefused(t *testing.T) {
	cases := []struct {
		name string
		to   []byte
		want string
	}{
		{"no recipient (the contract-creation shape)", nil, "recipient address required"},
		{"empty recipient", []byte{}, "recipient address invalid"},
		{"short recipient", make([]byte, 19), "recipient address invalid"},
		// (A recipient longer than 20 bytes cannot even be signed: Sign refuses it.)
		{"the zero address", make([]byte, 20), "recipient address invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.fund(0, 1_000_000)
			tx := f.signed(0, &types.Transaction{
				Type:  types.TxTypeTransfer,
				To:    tc.to,
				Value: big.NewInt(1),
				// A contract-creation transaction would carry code as its data.
				Data: []byte{0x60, 0x80, 0x60, 0x40, 0x52},
			})
			err := f.apply(tc.name, tx)
			if err == nil {
				t.Fatalf("a native transfer with %s was accepted", tc.name)
			}
			if !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("expected ErrInvalidTransaction, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected the refusal to say %q, got %q", tc.want, err.Error())
			}
		})
	}
}
