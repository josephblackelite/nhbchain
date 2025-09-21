package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const rpcEndpoint = "http://localhost:8080" // Assumes the CLI is run on the same machine as the node

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	command := os.Args[1]
	switch command {
	case "generate-key":
		generateKey()
	case "balance":
		if len(os.Args) < 3 {
			fmt.Println("Error: Please provide an address.")
			printUsage()
			return
		}
		getBalance(os.Args[2])
	case "claim-username": // NEW: Handle the new command
		if len(os.Args) < 4 {
			fmt.Println("Error: Please provide a username and a key file.")
			printUsage()
			return
		}
		claimUsername(os.Args[2], os.Args[3])
	case "stake": // NEW: Handle the stake command
		if len(os.Args) < 4 {
			fmt.Println("Error: Please provide an amount and a key file.")
			printUsage()
			return
		}
		amount, err := strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil {
			fmt.Println("Error: Invalid amount.")
			return
		}
		stake(amount, os.Args[3])
	case "un-stake": // NEW: Handle the un-stake command
		if len(os.Args) < 4 {
			fmt.Println("Error: Please provide an amount and a key file.")
			printUsage()
			return
		}
		amount, err := strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil {
			fmt.Println("Error: Invalid amount.")
			return
		}
		unStake(amount, os.Args[3])
	case "heartbeat": // NEW: Handle the heartbeat command
		if len(os.Args) < 3 {
			fmt.Println("Error: Please provide a key file.")
			printUsage()
			return
		}
		heartbeat(os.Args[2])
	case "deploy": // NEW: Handle the deploy command
		if len(os.Args) < 4 {
			fmt.Println("Error: Please provide a bytecode file and a key file.")
			printUsage()
			return
		}
		deploy(os.Args[2], os.Args[3])
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

// NEW: stake creates and sends a transaction to stake ZapNHB.
func stake(amount int64, keyFile string) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return
	}
	pubAddr := privKey.PubKey().Address().String()

	// Get the latest account info (especially the nonce) before creating the transaction.
	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return
	}

	tx := types.Transaction{
		Type:  types.TxTypeStake,
		Nonce: account.Nonce,
		Value: big.NewInt(amount), // For a stake tx, Value is the amount of ZapNHB
	}
	tx.Sign(privKey.PrivateKey)

	if err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending stake transaction: %v\n", err)
		return
	}

	fmt.Printf("Successfully sent stake transaction for %d ZapNHB.\n", amount)
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
}

// NEW: unStake creates and sends a transaction to un-stake ZapNHB.
func unStake(amount int64, keyFile string) {
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

	tx := types.Transaction{
		Type:  types.TxTypeUnstake,
		Nonce: account.Nonce,
		Value: big.NewInt(amount),
	}
	tx.Sign(privKey.PrivateKey)

	if err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending un-stake transaction: %v\n", err)
		return
	}

	fmt.Printf("Successfully sent un-stake transaction for %d ZapNHB.\n", amount)
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
}

// NEW: heartbeat sends a transaction to increase Engagement Score.
func heartbeat(keyFile string) {
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

	tx := types.Transaction{
		Type:  types.TxTypeHeartbeat,
		Nonce: account.Nonce,
		Value: big.NewInt(0), // Heartbeats transfer no value
	}
	tx.Sign(privKey.PrivateKey)

	if err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending heartbeat transaction: %v\n", err)
		return
	}

	fmt.Println("Successfully sent heartbeat transaction.")
}
func generateKey() {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		panic(err)
	}

	fileName := "wallet.key"
	if err := os.WriteFile(fileName, key.Bytes(), 0600); err != nil {
		panic(fmt.Sprintf("Failed to save key to %s: %v", fileName, err))
	}

	fmt.Printf("Generated new key and saved to %s\n", fileName)
	fmt.Printf("Your public address is: %s\n", key.PubKey().Address().String())
}

func getBalance(addr string) {
	account, err := fetchAccount(addr)
	if err != nil {
		fmt.Printf("Error fetching balance: %v\n", err)
		return
	}

	fmt.Printf("State for: %s\n", addr)
	fmt.Printf("  Username: %s\n", account.Username)
	fmt.Printf("  NHBCoin:  %s\n", account.BalanceNHB.String())
	fmt.Printf("  ZapNHB:   %s\n", account.BalanceZNHB.String())
	fmt.Printf("  Staked:   %s ZapNHB\n", account.Stake.String()) // Display the staked amount
	fmt.Printf("  Nonce:    %d\n", account.Nonce)
}

func claimUsername(username string, keyFile string) {
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

	// Construct the native TxTypeRegisterIdentity transaction
	tx := types.Transaction{
		Type:  types.TxTypeRegisterIdentity, // Type 2
		Nonce: account.Nonce,
		// The username is passed in the Data field as a byte slice
		Data:  []byte(username),
		Value: big.NewInt(0), // This transaction transfers no NHBCoin
		// Gas can be added here if needed by your L1's fee model for native txs
	}
	tx.Sign(privKey.PrivateKey)

	if err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending claim-username transaction: %v\n", err)
		return
	}

	fmt.Printf("Successfully sent transaction to claim username '%s'.\n", username)
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
}

// --- RPC HELPER FUNCTIONS ---

func fetchAccount(addr string) (*types.Account, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"id": 1, "method": "nhb_getBalance", "params": []string{addr},
	})

	resp, err := http.Post(rpcEndpoint, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the node at %s. Is it running?", rpcEndpoint)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result types.Account `json:"result"`
		Error  string        `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != "" {
		return nil, fmt.Errorf("error from node: %s", rpcResp.Error)
	}
	return &rpcResp.Result, nil
}

func sendTransaction(tx *types.Transaction) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"id": 1, "method": "nhb_sendTransaction", "params": []interface{}{tx},
	})
	resp, err := http.Post(rpcEndpoint, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("failed to connect to the node")
	}
	defer resp.Body.Close()
	// A full implementation would check the response for errors here.
	return nil
}

func loadPrivateKey(path string) (*crypto.PrivateKey, error) {
	keyBytes, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return crypto.PrivateKeyFromBytes(keyBytes)
}

// NEW: deploy reads contract bytecode and sends a creation transaction.
func deploy(bytecodeFile string, keyFile string) {
	bytecodeHex, err := ioutil.ReadFile(bytecodeFile)
	if err != nil {
		fmt.Printf("Error reading bytecode file: %v\n", err)
		return
	}
	bytecode, err := hex.DecodeString(string(bytecodeHex))
	if err != nil {
		fmt.Printf("Error decoding bytecode from hex: %v\n", err)
		return
	}

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

	tx := types.Transaction{
		Type:     types.TxTypeTransfer, // EVM transactions use the standard transfer type
		Nonce:    account.Nonce,
		To:       nil, // To is nil for contract creation
		Value:    big.NewInt(0),
		Data:     bytecode,
		GasLimit: 1000000, // High gas limit for deployment
		GasPrice: big.NewInt(1),
	}
	tx.Sign(privKey.PrivateKey)

	if err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending deploy transaction: %v\n", err)
		return
	}

	fmt.Println("Successfully sent contract deployment transaction.")
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
}

func printUsage() {
	fmt.Println("Usage: nhb-cli <command> [arguments]")
	fmt.Println("Commands:")
	fmt.Println("  generate-key                      - Generates a new key and saves to wallet.key")
	fmt.Println("  balance <address>                 - Checks the balance and stake of an address")
	fmt.Println("  claim-username <username> <key_file>     - Claims a unique username for your wallet") // NEW LINE
	fmt.Println("  stake <amount> <path_to_key_file> - Stakes a specified amount of ZapNHB")
	fmt.Println("  un-stake <amount> <path_to_key_file> - Un-stakes a specified amount of ZapNHB")
	fmt.Println("  heartbeat <path_to_key_file>        - Sends a heartbeat to increase engagement score")
	fmt.Println("  deploy <bytecode_file> <key_file>    - Deploys a smart contract")
}
