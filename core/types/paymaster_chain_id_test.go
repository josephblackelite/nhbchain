package types

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// A sponsor signs a digest built from the transaction hash, and the hash covers the chain id (ledger PL-R5T-29). So whatever
// signs sponsorship must sign the transaction as it will be submitted, with the id the node reports in nhb_getNetworkStats: a
// sponsor signature made for the legacy constant is not a signature for the network's own id, and the other way round. No
// separate chain id lives in the sponsor signer; it takes the one the transaction carries.
func TestSponsorSignatureIsForTheChainIDTheTransactionCarries(t *testing.T) {
	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sponsorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(senderKey.PublicKey).Bytes()
	sponsor := crypto.PubkeyToAddress(sponsorKey.PublicKey).Bytes()
	network, _ := new(big.Int).SetString("18346390202490284624", 10)

	build := func(chainID *big.Int) *Transaction {
		return &Transaction{
			ChainID:   new(big.Int).Set(chainID),
			Type:      TxTypeTransfer,
			Nonce:     4,
			To:        sender,
			Value:     big.NewInt(1),
			GasLimit:  25000,
			GasPrice:  big.NewInt(1),
			Paymaster: sponsor,
		}
	}

	for name, id := range map[string]*big.Int{"legacy constant": NHBChainID(), "network id": network} {
		tx := build(id)
		if err := tx.SignPaymasterBound(sponsorKey, sender); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := tx.PaymasterSponsorBound(sender)
		if err != nil || string(got) != string(sponsor) {
			t.Fatalf("%s: the sponsor does not verify for the id it signed: %v", name, err)
		}
	}

	// Signed for the legacy constant, submitted with the network id: not the sponsor's signature any more.
	signed := build(NHBChainID())
	if err := signed.SignPaymasterBound(sponsorKey, sender); err != nil {
		t.Fatal(err)
	}
	moved := build(network)
	moved.PaymasterR, moved.PaymasterS, moved.PaymasterV = signed.PaymasterR, signed.PaymasterS, signed.PaymasterV
	if got, err := moved.PaymasterSponsorBound(sender); err == nil && string(got) == string(sponsor) {
		t.Fatal("a sponsor signature made for the legacy chain id still verifies for the network id")
	} else if err != nil && !errors.Is(err, ErrPaymasterSignatureInvalid) {
		t.Fatalf("unexpected error: %v", err)
	}
	// And the digests differ, which is why.
	a, _ := signed.PaymasterSponsorDigest(sender)
	b, _ := moved.PaymasterSponsorDigest(sender)
	if string(a) == string(b) {
		t.Fatal("the sponsor digest does not depend on the chain id")
	}
}
