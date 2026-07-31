package main

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

// printAddressForKeyFile derives and prints the public address for an
// existing local key file, without ever printing the key itself. Useful for
// scripts that need to know a previously-generated validator's address
// again (e.g. re-running the bootstrap script after the first time).
func printAddressForKeyFile(keyFile string) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return
	}
	fmt.Println(privKey.PubKey().Address().String())
}

// setRewardBeneficiary lets a validator redirect its own epoch reward
// payouts to a wallet it actually uses. This is deliberately a local,
// signed-transaction operation (not a bare RPC call) -- only the validator's
// own key can authorize where its rewards go, and that key should never
// leave the validator server, so this command is meant to be run there.
// An empty beneficiary clears the redirect.
func setRewardBeneficiary(beneficiary string, keyFile string) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return
	}
	pubAddr := privKey.PubKey().Address().String()

	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return
	}

	payload := struct {
		Beneficiary string `json:"beneficiary"`
	}{Beneficiary: strings.TrimSpace(beneficiary)}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSetRewardBeneficiary,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		fmt.Printf("Error signing transaction: %v\n", err)
		return
	}

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending transaction: %v\n", err)
		return
	}

	if payload.Beneficiary == "" {
		fmt.Printf("Cleared reward beneficiary for %s. Future rewards will be credited to this address directly.\n", pubAddr)
		return
	}
	fmt.Printf("Set reward beneficiary for %s to %s.\n", pubAddr, payload.Beneficiary)
	fmt.Println("Future validator-selection rewards will be credited to that wallet instead of this address.")
}
