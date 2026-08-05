package lending

import (
	"bytes"
	"encoding/json"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestSignAndEncodeRoundTrips proves the exact passthrough contract
// SupplyAsset/BorrowAsset/etc rely on: a transaction built with the New*Tx
// helpers and signed via SignAndEncode JSON-decodes back into a
// types.Transaction whose recovered signer matches the signing key -- the
// same round trip the node's nhb_sendTransaction handler performs server
// side (rpc/http.go's txDTO).
func TestSignAndEncodeRoundTrips(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	account, err := SenderAddress(key)
	if err != nil {
		t.Fatalf("derive sender address: %v", err)
	}

	tx, err := NewSupplyTx(types.NHBChainID(), 7, "default", "1000")
	if err != nil {
		t.Fatalf("build supply tx: %v", err)
	}
	if tx.Type != types.TxTypeLendingSupplyNHB {
		t.Fatalf("unexpected tx type: %v", tx.Type)
	}
	if tx.Data != nil {
		t.Fatalf("expected no payload for the default pool, got %q", tx.Data)
	}

	encoded, err := SignAndEncode(tx, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign and encode: %v", err)
	}

	var decoded types.Transaction
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("decode signed tx json: %v", err)
	}
	if err := decoded.ValidateBasic(); err != nil {
		t.Fatalf("decoded tx failed validation: %v", err)
	}
	from, err := decoded.From()
	if err != nil {
		t.Fatalf("recover signer: %v", err)
	}
	recovered, err := crypto.NewAddress(crypto.NHBPrefix, from)
	if err != nil {
		t.Fatalf("encode recovered address: %v", err)
	}
	if recovered.String() != account {
		t.Fatalf("recovered signer %q does not match expected account %q", recovered.String(), account)
	}
	if decoded.Value.String() != "1000" {
		t.Fatalf("unexpected value: %s", decoded.Value.String())
	}
}

// TestNewLiquidateTxEncodesBorrower proves the liquidate payload carries the
// borrower being liquidated -- the one field applyLendingLiquidate requires
// beyond the signer itself (core/lending_native.go).
func TestNewLiquidateTxEncodesBorrower(t *testing.T) {
	tx, err := NewLiquidateTx(types.NHBChainID(), 1, "default", "nhb1borroweraddress")
	if err != nil {
		t.Fatalf("build liquidate tx: %v", err)
	}
	if tx.Type != types.TxTypeLendingLiquidate {
		t.Fatalf("unexpected tx type: %v", tx.Type)
	}
	if !bytes.Contains(tx.Data, []byte("nhb1borroweraddress")) {
		t.Fatalf("expected payload to carry the borrower address, got %q", tx.Data)
	}

	if _, err := NewLiquidateTx(types.NHBChainID(), 1, "default", ""); err == nil {
		t.Fatalf("expected error for empty borrower")
	}
}

// TestNewSupplyTxRejectsNonPositiveAmount matches the on-chain rule
// (core/lending_native.go's applyLendingSupplyNHB requires tx.Value > 0) --
// catching this client-side avoids submitting a transaction guaranteed to
// be rejected by the node.
func TestNewSupplyTxRejectsNonPositiveAmount(t *testing.T) {
	if _, err := NewSupplyTx(types.NHBChainID(), 1, "default", "0"); err == nil {
		t.Fatalf("expected error for zero amount")
	}
	if _, err := NewSupplyTx(types.NHBChainID(), 1, "default", "-5"); err == nil {
		t.Fatalf("expected error for negative amount")
	}
}
