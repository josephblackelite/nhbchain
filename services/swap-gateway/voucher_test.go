package main

import (
	"bytes"
	"testing"

	repoCrypto "nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func TestVoucherHashDeterministic(t *testing.T) {
	recipientBytes := bytes.Repeat([]byte{0x11}, 20)
	recipient := repoCrypto.NewAddress(repoCrypto.NHBPrefix, recipientBytes).String()
	voucher := VoucherV1{
		Domain:     "NHB_SWAP_VOUCHER_V1",
		ChainID:    187001,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     "1000000000000000000",
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       "0.10",
		OrderID:    "SWP_1",
		Nonce:      "abcd",
		Expiry:     1700000000,
	}

	hash1, err := voucher.Hash()
	if err != nil {
		t.Fatalf("hash1: %v", err)
	}
	hash2, err := voucher.Hash()
	if err != nil {
		t.Fatalf("hash2: %v", err)
	}

	if !bytes.Equal(hash1, hash2) {
		t.Fatalf("hashes differ")
	}
}

func TestSignVoucherRecoverAddress(t *testing.T) {
	key, err := ethcrypto.HexToECDSA("4f3edf983ac636a65a842ce7c78d9aa706d3b113b37e2b8c3c6d53295d85f81b")
	if err != nil {
		t.Fatalf("hex to ecdsa: %v", err)
	}
	minterAddr := repoCrypto.NewAddress(repoCrypto.NHBPrefix, ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()).String()

	recipientBytes := bytes.Repeat([]byte{0x22}, 20)
	recipient := repoCrypto.NewAddress(repoCrypto.NHBPrefix, recipientBytes).String()

	voucher := VoucherV1{
		Domain:     "NHB_SWAP_VOUCHER_V1",
		ChainID:    187001,
		Token:      "ZNHB",
		Recipient:  recipient,
		Amount:     "1000000000000000000",
		Fiat:       "USD",
		FiatAmount: "100.00",
		Rate:       "0.10",
		OrderID:    "SWP_TEST",
		Nonce:      "0011ee",
		Expiry:     1800000000,
	}

	sig, err := SignVoucher(voucher, "0x1234")
	if err == nil {
		t.Fatalf("expected signing to fail with wrong key input")
	}

	sig, err = SignVoucher(voucher, "0x4f3edf983ac636a65a842ce7c78d9aa706d3b113b37e2b8c3c6d53295d85f81b")
	if err != nil {
		t.Fatalf("SignVoucher: %v", err)
	}

	recovered, err := RecoverVoucherSignerAddress(voucher, sig)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered != minterAddr {
		t.Fatalf("expected %s recovered, got %s", minterAddr, recovered)
	}
}
