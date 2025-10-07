package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

var rpcEndpoint = defaultRPCEndpoint() // Defaults to localhost, can be overridden via RPC_URL or --rpc flag
var rpcAuthToken = os.Getenv("NHB_RPC_TOKEN")

// legacyWalletKeyMaterial represents the placeholder wallet key that previously
// lived in the repository. If we ever encounter it on disk we refuse to use it
// and instruct the operator to rotate immediately.
var legacyWalletKeyMaterial = []byte{
	0x19, 0x7e, 0xe8, 0x50, 0x90, 0xe7, 0xcd, 0x05,
	0xd7, 0xd6, 0xa7, 0xc2, 0x59, 0xff, 0x91, 0xf5,
	0x1e, 0x1c, 0x49, 0xe1, 0xe4, 0x74, 0xb8, 0x0e,
	0x8c, 0xf6, 0x5f, 0xf8, 0xa6, 0x3d, 0xb8, 0xf6,
}

func main() {
	args := os.Args[1:]
	var err error
	rpcEndpoint = defaultRPCEndpoint()
	args, err = applyGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if len(args) < 1 {
		printUsage()
		return
	}

	command := args[0]
	switch command {
	case "generate-key":
		generateKey()
	case "balance":
		if len(args) < 2 {
			fmt.Println("Error: Please provide an address.")
			printUsage()
			return
		}
		getBalance(args[1])
	case "claim-username": // NEW: Handle the new command
		if len(args) < 3 {
			fmt.Println("Error: Please provide a username and a key file.")
			printUsage()
			return
		}
		claimUsername(args[1], args[2])
	case "stake": // NEW: Handle the stake command
		if len(args) < 3 {
			fmt.Println("Error: Please provide an amount and a key file.")
			printUsage()
			return
		}
		amount, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Println("Error: Invalid amount.")
			return
		}
		stake(amount, args[2])
	case "un-stake": // NEW: Handle the un-stake command
		if len(args) < 3 {
			fmt.Println("Error: Please provide an amount and a key file.")
			printUsage()
			return
		}
		amount, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Println("Error: Invalid amount.")
			return
		}
		unStake(amount, args[2])
	case "heartbeat": // NEW: Handle the heartbeat command
		if len(args) < 2 {
			fmt.Println("Error: Please provide a key file.")
			printUsage()
			return
		}
		heartbeat(args[1])
	case "deploy": // NEW: Handle the deploy command
		if len(args) < 3 {
			fmt.Println("Error: Please provide a bytecode file and a key file.")
			printUsage()
			return
		}
		deploy(args[1], args[2])
	case "id":
		code := runIdentityCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "escrow":
		code := runEscrowCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "claimable":
		code := runClaimableCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "p2p":
		code := runP2PCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "potso":
		code := runPotsoCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "swap":
		code := runSwapCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "gov":
		code := runGovCommand(args[1:], os.Stdout, os.Stderr)
		if code != 0 {
			os.Exit(code)
		}
		return
	case "loyalty-create-business":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-create-business <owner> <name>")
			return
		}
		name := strings.Join(args[2:], " ")
		loyaltyCreateBusiness(args[1], name)
	case "loyalty-set-paymaster":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-set-paymaster <caller> <businessId> <paymaster>")
			return
		}
		loyaltySetPaymaster(args[1], args[2], args[3])
	case "loyalty-add-merchant":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-add-merchant <caller> <businessId> <merchant>")
			return
		}
		loyaltyModifyMerchant("loyalty_addMerchant", args[1], args[2], args[3])
	case "loyalty-remove-merchant":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-remove-merchant <caller> <businessId> <merchant>")
			return
		}
		loyaltyModifyMerchant("loyalty_removeMerchant", args[1], args[2], args[3])
	case "loyalty-create-program":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-create-program <caller> <businessId> <programSpecJSON>")
			return
		}
		loyaltyCreateProgram(args[1], args[2], args[3])
	case "loyalty-update-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-update-program <caller> <programSpecJSON>")
			return
		}
		loyaltyUpdateProgram(args[1], args[2])
	case "loyalty-pause-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-pause-program <caller> <programId>")
			return
		}
		loyaltyLifecycle("loyalty_pauseProgram", args[1], args[2])
	case "loyalty-resume-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-resume-program <caller> <programId>")
			return
		}
		loyaltyLifecycle("loyalty_resumeProgram", args[1], args[2])
	case "loyalty-get-business":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-get-business <businessId>")
			return
		}
		loyaltyGetBusiness(args[1])
	case "loyalty-list-programs":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-list-programs <businessId>")
			return
		}
		loyaltyListPrograms(args[1])
	case "loyalty-program-stats":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-program-stats <programId> <day>")
			return
		}
		loyaltyProgramStats(args[1], args[2])
	case "loyalty-user-daily":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-user-daily <user> <programId> <day>")
			return
		}
		loyaltyUserDaily(args[1], args[2], args[3])
	case "loyalty-paymaster-balance":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-paymaster-balance <businessId>")
			return
		}
		loyaltyPaymasterBalance(args[1])
	case "loyalty-resolve-username":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-resolve-username <username>")
			return
		}
		loyaltyResolveUsername(args[1])
	case "loyalty-user-qr":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-user-qr <mode:username|address> <value>")
			return
		}
		loyaltyUserQR(args[1], args[2])
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
	}
}

func defaultRPCEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("RPC_URL")); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func applyGlobalFlags(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--rpc" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("missing value for --rpc")
			}
			rpcEndpoint = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--rpc=") {
			rpcEndpoint = strings.TrimPrefix(arg, "--rpc=")
			continue
		}
		out = append(out, arg)
	}
	return out, nil
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
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    account.Nonce,
		Value:    big.NewInt(amount), // For a stake tx, Value is the amount of ZapNHB
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
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
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeUnstake,
		Nonce:    account.Nonce,
		Value:    big.NewInt(amount),
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
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
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    account.Nonce,
		Value:    big.NewInt(0), // Heartbeats transfer no value
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
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
	fmt.Println("Store this file securely. Commands will refuse to run without a unique local key.")
}

func getBalance(addr string) {
	account, err := fetchAccount(addr)
	if err != nil {
		fmt.Printf("Error fetching balance: %v\n", err)
		return
	}

	fmt.Printf("State for: %s\n", addr)
	fmt.Printf("  Username: %s\n", account.Username)
	fmt.Printf("  NHBCoin:  %s\n", formatBigInt(account.BalanceNHB))
	fmt.Printf("  ZapNHB:   %s\n", formatBigInt(account.BalanceZNHB))
	fmt.Printf("  Staked:   %s ZapNHB\n", formatBigInt(account.Stake))
	fmt.Printf("  Locked:   %s ZapNHB\n", formatBigInt(account.LockedZNHB))
	if strings.TrimSpace(account.DelegatedValidator) != "" {
		fmt.Printf("  Delegated Validator: %s\n", account.DelegatedValidator)
	}
	if len(account.PendingUnbonds) > 0 {
		fmt.Println("  Pending Unbonds:")
		for _, entry := range account.PendingUnbonds {
			fmt.Printf("    - ID %d: %s ZapNHB unlocking at %s (validator %s)\n",
				entry.ID,
				formatBigInt(entry.Amount),
				time.Unix(int64(entry.ReleaseTime), 0).UTC().Format(time.RFC3339),
				entry.Validator)
		}
	}
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
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeRegisterIdentity, // Type 2
		Nonce:    account.Nonce,
		Data:     []byte(username),
		Value:    big.NewInt(0), // This transaction transfers no NHBCoin
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
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

type balanceResponse struct {
	Address            string        `json:"address"`
	BalanceNHB         *big.Int      `json:"balanceNHB"`
	BalanceZNHB        *big.Int      `json:"balanceZNHB"`
	Stake              *big.Int      `json:"stake"`
	LockedZNHB         *big.Int      `json:"lockedZNHB"`
	DelegatedValidator string        `json:"delegatedValidator"`
	PendingUnbonds     []unbondEntry `json:"pendingUnbonds"`
	Username           string        `json:"username"`
	Nonce              uint64        `json:"nonce"`
	EngagementScore    uint64        `json:"engagementScore"`
}

type unbondEntry struct {
	ID          uint64   `json:"id"`
	Validator   string   `json:"validator"`
	Amount      *big.Int `json:"amount"`
	ReleaseTime uint64   `json:"releaseTime"`
}

func formatBigInt(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func fetchAccount(addr string) (*balanceResponse, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"id": 1, "method": "nhb_getBalance", "params": []string{addr},
	})

	resp, err := doRPCRequest(payload, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result balanceResponse `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return &rpcResp.Result, nil
}

func sendTransaction(tx *types.Transaction) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"id": 1, "method": "nhb_sendTransaction", "params": []interface{}{tx},
	})
	resp, err := doRPCRequest(payload, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return nil
}

func doRPCRequest(payload []byte, requireAuth bool) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, rpcEndpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if requireAuth {
		if rpcAuthToken == "" {
			return nil, fmt.Errorf("privileged RPC call requires NHB_RPC_TOKEN to be set")
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(rpcAuthToken))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", rpcEndpoint, err)
	}
	return resp, nil
}

func loadPrivateKey(path string) (*crypto.PrivateKey, error) {
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("private key file %s not found. run ./nhb-cli generate-key first", path)
		}
		return nil, fmt.Errorf("failed to read private key file %s: %w", path, err)
	}
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("private key file %s is empty. run ./nhb-cli generate-key first", path)
	}
	if bytes.Equal(keyBytes, legacyWalletKeyMaterial) {
		return nil, fmt.Errorf("private key file %s contains deprecated placeholder material. delete it and run ./nhb-cli generate-key to rotate", path)
	}
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key in %s: %w", path, err)
	}
	return privKey, nil
}

func callLoyaltyRPC(method string, param interface{}, requireAuth bool) (json.RawMessage, error) {
	payload := map[string]interface{}{"id": 1, "method": method}
	if param != nil {
		payload["params"] = []interface{}{param}
	} else {
		payload["params"] = []interface{}{}
	}
	body, _ := json.Marshal(payload)
	resp, err := doRPCRequest(body, requireAuth)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func printJSONResult(result json.RawMessage) {
	if len(result) == 0 {
		fmt.Println("No result.")
		return
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, result, "", "  "); err != nil {
		fmt.Println(string(result))
		return
	}
	fmt.Println(buf.String())
}

func decodeStringResult(result json.RawMessage) (string, error) {
	var out string
	if err := json.Unmarshal(result, &out); err != nil {
		return "", err
	}
	return out, nil
}

func loyaltyCreateBusiness(owner, name string) {
	param := map[string]string{"caller": owner, "name": name}
	result, err := callLoyaltyRPC("loyalty_createBusiness", param, true)
	if err != nil {
		fmt.Printf("Error creating business: %v\n", err)
		return
	}
	id, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}
	fmt.Printf("Business created: %s\n", id)
}

func loyaltySetPaymaster(caller, businessID, paymaster string) {
	param := map[string]string{
		"caller":     caller,
		"businessId": businessID,
		"paymaster":  paymaster,
	}
	if _, err := callLoyaltyRPC("loyalty_setPaymaster", param, true); err != nil {
		fmt.Printf("Error setting paymaster: %v\n", err)
		return
	}
	fmt.Println("Paymaster updated.")
}

func loyaltyModifyMerchant(method, caller, businessID, merchant string) {
	param := map[string]string{
		"caller":     caller,
		"businessId": businessID,
		"merchant":   merchant,
	}
	if _, err := callLoyaltyRPC(method, param, true); err != nil {
		fmt.Printf("Error modifying merchant: %v\n", err)
		return
	}
	fmt.Println("Operation successful.")
}

func loyaltyCreateProgram(caller, businessID, spec string) {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(spec), &raw); err != nil {
		fmt.Printf("Invalid program spec JSON: %v\n", err)
		return
	}
	param := map[string]interface{}{
		"caller":     caller,
		"businessId": businessID,
		"spec":       raw,
	}
	result, err := callLoyaltyRPC("loyalty_createProgram", param, true)
	if err != nil {
		fmt.Printf("Error creating program: %v\n", err)
		return
	}
	id, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}
	fmt.Printf("Program created: %s\n", id)
}

func loyaltyUpdateProgram(caller, spec string) {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(spec), &raw); err != nil {
		fmt.Printf("Invalid program spec JSON: %v\n", err)
		return
	}
	param := map[string]interface{}{
		"caller": caller,
		"spec":   raw,
	}
	if _, err := callLoyaltyRPC("loyalty_updateProgram", param, true); err != nil {
		fmt.Printf("Error updating program: %v\n", err)
		return
	}
	fmt.Println("Program updated.")
}

func loyaltyLifecycle(method, caller, programID string) {
	param := map[string]string{
		"caller":    caller,
		"programId": programID,
	}
	if _, err := callLoyaltyRPC(method, param, true); err != nil {
		fmt.Printf("Error performing operation: %v\n", err)
		return
	}
	fmt.Println("Operation successful.")
}

func loyaltyGetBusiness(businessID string) {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_getBusiness", param, false)
	if err != nil {
		fmt.Printf("Error fetching business: %v\n", err)
		return
	}
	printJSONResult(result)
}

func loyaltyListPrograms(businessID string) {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_listPrograms", param, false)
	if err != nil {
		fmt.Printf("Error listing programs: %v\n", err)
		return
	}
	printJSONResult(result)
}

func loyaltyProgramStats(programID, day string) {
	param := map[string]string{"programId": programID, "day": day}
	result, err := callLoyaltyRPC("loyalty_programStats", param, false)
	if err != nil {
		fmt.Printf("Error fetching stats: %v\n", err)
		return
	}
	printJSONResult(result)
}

func loyaltyUserDaily(user, programID, day string) {
	param := map[string]string{"user": user, "programId": programID, "day": day}
	result, err := callLoyaltyRPC("loyalty_userDaily", param, false)
	if err != nil {
		fmt.Printf("Error fetching user meter: %v\n", err)
		return
	}
	value, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}
	fmt.Printf("Daily accrued: %s\n", value)
}

func loyaltyPaymasterBalance(businessID string) {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_paymasterBalance", param, false)
	if err != nil {
		fmt.Printf("Error fetching paymaster balance: %v\n", err)
		return
	}
	balance, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}
	fmt.Printf("Paymaster balance (ZNHB): %s\n", balance)
}

func loyaltyResolveUsername(username string) {
	param := map[string]string{"username": username}
	result, err := callLoyaltyRPC("loyalty_resolveUsername", param, false)
	if err != nil {
		fmt.Printf("Error resolving username: %v\n", err)
		return
	}
	address, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return
	}
	fmt.Printf("Username %s resolves to %s\n", username, address)
}

func loyaltyUserQR(mode, value string) {
	param := make(map[string]string)
	switch strings.ToLower(mode) {
	case "username":
		param["username"] = value
	case "address":
		param["address"] = value
	default:
		fmt.Println("Mode must be either 'username' or 'address'.")
		return
	}
	result, err := callLoyaltyRPC("loyalty_userQR", param, false)
	if err != nil {
		fmt.Printf("Error fetching QR payload: %v\n", err)
		return
	}
	printJSONResult(result)
}

// NEW: deploy reads contract bytecode and sends a creation transaction.
func deploy(bytecodeFile string, keyFile string) {
	bytecodeHex, err := os.ReadFile(bytecodeFile)
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
		ChainID:  types.NHBChainID(),
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
	fmt.Println()
	fmt.Println("Most commands require a locally generated signing key. Run ./nhb-cli generate-key first;")
	fmt.Println("the CLI aborts if wallet.key is missing or contains placeholder material.")
	fmt.Println("Commands:")
	fmt.Println("  generate-key                      - Generates a new key and saves to wallet.key")
	fmt.Println("  balance <address>                 - Checks the balance and stake of an address")
	fmt.Println("  claim-username <username> <key_file>     - Claims a unique username for your wallet") // NEW LINE
	fmt.Println("  stake <amount> <path_to_key_file> - Stakes a specified amount of ZapNHB")
	fmt.Println("  un-stake <amount> <path_to_key_file> - Un-stakes a specified amount of ZapNHB")
	fmt.Println("  heartbeat <path_to_key_file>        - Sends a heartbeat to increase engagement score")
	fmt.Println("  deploy <bytecode_file> <key_file>    - Deploys a smart contract")
	fmt.Println("  id                                 - Identity alias management subcommands")
	fmt.Println("  escrow                             - Escrow management subcommands")
	fmt.Println("  claimable                          - Hash-lock claimable subcommands")
	fmt.Println("  p2p                                - P2P trade orchestration subcommands")
	fmt.Println("  potso                              - POTSO telemetry subcommands")
	fmt.Println("  swap                               - Swap voucher queries and export")
}
