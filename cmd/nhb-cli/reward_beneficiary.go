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
//
// Returns 0 on success and 1 on any failure, matching the exit-code
// convention used by the other transaction-sending subcommands in this CLI
// (see runSendNHBCommand/runSendZNHBCommand in send.go). Callers such as
// scripts/deployvalidator.sh rely on a non-zero process exit to detect a
// failed beneficiary update -- returning only a printed message here (the
// previous behavior) makes that failure invisible to `if ! nhb-cli
// set-reward-beneficiary ...; then` checks.
func setRewardBeneficiary(beneficiary string, keyFile string) int {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return 1
	}
	pubAddr := privKey.PubKey().Address().String()

	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return 1
	}

	payload := struct {
		Beneficiary string `json:"beneficiary"`
	}{Beneficiary: strings.TrimSpace(beneficiary)}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
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
		return 1
	}

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending transaction: %v\n", err)
		return 1
	}

	if payload.Beneficiary == "" {
		fmt.Printf("Cleared reward beneficiary for %s. Future rewards will be credited to this address directly.\n", pubAddr)
		return 0
	}
	fmt.Printf("Set reward beneficiary for %s to %s.\n", pubAddr, payload.Beneficiary)
	fmt.Println("Future validator-selection rewards will be credited to that wallet instead of this address.")
	return 0
}
