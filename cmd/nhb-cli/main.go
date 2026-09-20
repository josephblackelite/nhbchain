package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
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
	if code := run(os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

// run executes one command line and returns the process exit code: 0 only when
// the command did what it was asked to. Every failure, including a usage error
// and an unknown command, is non-zero, so scripts can rely on it.
func run(args []string) int {
	var err error
	rpcEndpoint = defaultRPCEndpoint()
	args, err = applyGlobalFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if len(args) < 1 {
		printUsage()
		return 1
	}

	command := args[0]
	switch command {
	case "help", "-h", "--help":
		printUsage()
		return 0
	case "generate-key":
		return generateKey(args[1:])
	case "balance":
		if len(args) < 2 {
			fmt.Println("Error: Please provide an address.")
			printUsage()
			return 1
		}
		return getBalance(args[1])
	case "claim-username": // NEW: Handle the new command
		if len(args) < 3 {
			fmt.Println("Error: Please provide a username and a key file.")
			printUsage()
			return 1
		}
		return claimUsername(args[1], args[2])
	case "stake":
		return runStakeCommand(args[1:], os.Stdout, os.Stderr)
	case "un-stake": // NEW: Handle the un-stake command
		if len(args) < 3 {
			fmt.Println("Error: Please provide an amount and a key file.")
			printUsage()
			return 1
		}
		amount, ok := new(big.Int).SetString(strings.TrimSpace(args[1]), 10)
		if !ok || amount.Sign() <= 0 {
			fmt.Println("Error: Invalid amount.")
			return 1
		}
		return unStake(amount, args[2])
	case "heartbeat": // NEW: Handle the heartbeat command
		if len(args) < 2 {
			fmt.Println("Error: Please provide a key file.")
			printUsage()
			return 1
		}
		return heartbeat(args[1])
	case "set-reward-beneficiary":
		if len(args) < 3 {
			fmt.Println("Error: Please provide a beneficiary address (or \"\" to clear) and a key file.")
			printUsage()
			return 1
		}
		return setRewardBeneficiary(args[1], args[2])
	case "register-validator":
		if len(args) < 3 {
			fmt.Println("Error: Please provide an amount (0 for no additional stake) and a key file.")
			printUsage()
			return 1
		}
		return registerValidator(args[1], args[2])
	case "deregister-validator":
		if len(args) < 2 {
			fmt.Println("Error: Please provide a key file.")
			printUsage()
			return 1
		}
		return deregisterValidator(args[1])
	case "address":
		if len(args) < 2 {
			fmt.Println("Error: Please provide a key file.")
			printUsage()
			return 1
		}
		return printAddressForKeyFile(args[1])
	case "send-nhb":
		return runSendNHBCommand(args[1:])
	case "send-znhb":
		return runSendZNHBCommand(args[1:])
	case "id":
		return runIdentityCommand(args[1:], os.Stdout, os.Stderr)
	case "escrow":
		return runEscrowCommand(args[1:], os.Stdout, os.Stderr)
	case "claimable":
		return runClaimableCommand(args[1:], os.Stdout, os.Stderr)
	case "p2p":
		return runP2PCommand(args[1:], os.Stdout, os.Stderr)
	case "potso":
		return runPotsoCommand(args[1:], os.Stdout, os.Stderr)
	case "pos":
		return runPOSCommand(args[1:], os.Stdout, os.Stderr)
	case "swap":
		return runSwapCommand(args[1:], os.Stdout, os.Stderr)
	case "fees":
		return runFeesCommand(args[1:], os.Stdout, os.Stderr)
	case "keystore":
		return runKeystoreCommand(args[1:], os.Stdout, os.Stderr)
	case "rpc-token":
		return runRPCTokenCommand(args[1:], os.Stdin, os.Stdout, os.Stderr)
	case "gov":
		return runGovCommand(args[1:], os.Stdout, os.Stderr)
	case "subscriptions":
		return runSubscriptionsCommand(args[1:], os.Stdout, os.Stderr)
	case "loyalty-create-business":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-create-business <name> <key_file>")
			fmt.Println("The signing key's own address becomes the business owner -- there is no separate <owner> argument anymore.")
			return 1
		}
		return loyaltyCreateBusiness(args[1], args[2])
	case "loyalty-set-paymaster":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-set-paymaster <businessId> <paymaster> <key_file>")
			return 1
		}
		return loyaltySetPaymaster(args[1], args[2], args[3])
	case "loyalty-add-merchant":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-add-merchant <businessId> <merchant> <key_file>")
			return 1
		}
		return loyaltyModifyMerchant(types.TxTypeLoyaltyAddMerchant, args[1], args[2], args[3])
	case "loyalty-remove-merchant":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-remove-merchant <businessId> <merchant> <key_file>")
			return 1
		}
		return loyaltyModifyMerchant(types.TxTypeLoyaltyRemoveMerchant, args[1], args[2], args[3])
	case "loyalty-create-program":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-create-program <businessId> <programSpecJSON> <key_file>")
			fmt.Println("The signing key must already be a registered merchant of <businessId> (see loyalty-add-merchant), or hold ROLE_LOYALTY_ADMIN.")
			return 1
		}
		return loyaltyCreateProgram(args[1], args[2], args[3])
	case "loyalty-update-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-update-program <programSpecJSON> <key_file>")
			fmt.Println("Full replace, not a partial patch -- resend every field, not just what's changing. <programSpecJSON> must include \"id\".")
			return 1
		}
		return loyaltyUpdateProgram(args[1], args[2])
	case "loyalty-pause-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-pause-program <programId> <key_file>")
			return 1
		}
		return loyaltyLifecycle(types.TxTypePauseLoyaltyProgram, args[1], args[2])
	case "loyalty-resume-program":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-resume-program <programId> <key_file>")
			return 1
		}
		return loyaltyLifecycle(types.TxTypeResumeLoyaltyProgram, args[1], args[2])
	case "loyalty-get-business":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-get-business <businessId>")
			return 1
		}
		return loyaltyGetBusiness(args[1])
	case "loyalty-list-businesses":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-list-businesses <owner>")
			fmt.Println("Lists every business <owner> owns -- use this to find the businessId a loyalty-create-business transaction was just assigned, since transactions return no synchronous result.")
			return 1
		}
		return loyaltyListBusinesses(args[1])
	case "loyalty-list-programs":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-list-programs <businessId>")
			return 1
		}
		return loyaltyListPrograms(args[1])
	case "loyalty-program-stats":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-program-stats <programId> <day>")
			return 1
		}
		return loyaltyProgramStats(args[1], args[2])
	case "loyalty-user-daily":
		if len(args) < 4 {
			fmt.Println("Usage: loyalty-user-daily <user> <programId> <day>")
			return 1
		}
		return loyaltyUserDaily(args[1], args[2], args[3])
	case "loyalty-paymaster-balance":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-paymaster-balance <businessId>")
			return 1
		}
		return loyaltyPaymasterBalance(args[1])
	case "loyalty-resolve-username":
		if len(args) < 2 {
			fmt.Println("Usage: loyalty-resolve-username <username>")
			return 1
		}
		return loyaltyResolveUsername(args[1])
	case "loyalty-user-qr":
		if len(args) < 3 {
			fmt.Println("Usage: loyalty-user-qr <mode:username|address> <value>")
			return 1
		}
		return loyaltyUserQR(args[1], args[2])
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		return 1
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
func stake(amount *big.Int, keyFile string) int {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Printf("Error loading private key: %v\n", err)
		return 1
	}
	pubAddr := privKey.PubKey().Address().String()

	// Get the latest account info (especially the nonce) before creating the transaction.
	account, err := fetchAccount(pubAddr)
	if err != nil {
		fmt.Printf("Error fetching account details: %v\n", err)
		return 1
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeStake,
		Nonce:    account.Nonce,
		Value:    amount, // For a stake tx, Value is the amount of ZapNHB
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	tx.Sign(privKey.PrivateKey)

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending stake transaction: %v\n", err)
		return 1
	}

	fmt.Printf("Successfully sent stake transaction for %s ZapNHB.\n", amount.String())
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
	return 0
}

// NEW: unStake creates and sends a transaction to un-stake ZapNHB.
func unStake(amount *big.Int, keyFile string) int {
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

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeUnstake,
		Nonce:    account.Nonce,
		Value:    amount,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	tx.Sign(privKey.PrivateKey)

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending un-stake transaction: %v\n", err)
		return 1
	}

	fmt.Printf("Successfully sent un-stake transaction for %s ZapNHB.\n", amount.String())
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
	return 0
}

// NEW: heartbeat sends a transaction to increase Engagement Score.
func heartbeat(keyFile string) int {
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

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    account.Nonce,
		Value:    big.NewInt(0), // Heartbeats transfer no value
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	tx.Sign(privKey.PrivateKey)

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending heartbeat transaction: %v\n", err)
		return 1
	}

	fmt.Println("Successfully sent heartbeat transaction.")
	return 0
}

// generateKey writes a new signing key to wallet.key. It never overwrites an
// existing file: a key that is overwritten is gone, and so is whatever it
// controlled. With --force an existing wallet.key is first copied, byte for
// byte, to wallet.key.bak-<UTC time> (created exclusively, mode 0600) and the
// key is replaced only once that copy is on disk; if the copy cannot be made
// nothing is touched.
func generateKey(args []string) int {
	force := false
	for _, arg := range args {
		if arg != "--force" {
			fmt.Printf("Error: unexpected argument %q. Usage: generate-key [--force]\n", arg)
			return 1
		}
		force = true
	}

	fileName := "wallet.key"
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if existing, err := os.ReadFile(fileName); err == nil {
		if !force {
			fmt.Printf("Error: %s already exists and was left untouched. Move it away, or run generate-key --force to keep a backup copy and replace it.\n", fileName)
			return 1
		}
		backup := fileName + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
		if err := writeNewFile(backup, existing); err != nil {
			fmt.Printf("Error: could not back up %s to %s, so it was left untouched: %v\n", fileName, backup, err)
			return 1
		}
		fmt.Printf("Backed up the existing key to %s\n", backup)
		flags = os.O_WRONLY | os.O_TRUNC
	} else if !os.IsNotExist(err) {
		fmt.Printf("Error: could not read %s, so it was left untouched: %v\n", fileName, err)
		return 1
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		fmt.Printf("Error generating key: %v\n", err)
		return 1
	}
	file, err := os.OpenFile(fileName, flags, 0600)
	if err != nil {
		fmt.Printf("Error: could not write %s: %v\n", fileName, err)
		return 1
	}
	if _, err := file.Write(key.Bytes()); err != nil {
		file.Close()
		fmt.Printf("Error: could not write %s: %v\n", fileName, err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Printf("Error: could not write %s: %v\n", fileName, err)
		return 1
	}

	fmt.Printf("Generated new key and saved to %s\n", fileName)
	fmt.Printf("Your public address is: %s\n", key.PubKey().Address().String())
	fmt.Println("Store this file securely. Commands will refuse to run without a unique local key.")
	return 0
}

// writeNewFile creates path, which must not exist yet, with mode 0600 and the
// given contents.
func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func getBalance(addr string) int {
	account, err := fetchAccount(addr)
	if err != nil {
		fmt.Printf("Error fetching balance: %v\n", err)
		return 1
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
	if account.ValidatorRegistered {
		fmt.Println("  Validator Registered: yes")
		if account.ValidatorRegisteredAt > 0 {
			fmt.Printf("    Registered At: %s (%d)\n", time.Unix(int64(account.ValidatorRegisteredAt), 0).UTC().Format(time.RFC3339), account.ValidatorRegisteredAt)
		}
	} else {
		fmt.Println("  Validator Registered: no")
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
	return 0
}

func claimUsername(username string, keyFile string) int {
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

	if _, err := sendTransaction(&tx); err != nil {
		fmt.Printf("Error sending claim-username transaction: %v\n", err)
		return 1
	}

	fmt.Printf("Successfully sent transaction to claim username '%s'.\n", username)
	fmt.Println("Check the node logs for confirmation and wait for the next block.")
	return 0
}

// --- RPC HELPER FUNCTIONS ---

type balanceResponse struct {
	Address               string        `json:"address"`
	BalanceNHB            *big.Int      `json:"balanceNHB"`
	BalanceZNHB           *big.Int      `json:"balanceZNHB"`
	Stake                 *big.Int      `json:"stake"`
	LockedZNHB            *big.Int      `json:"lockedZNHB"`
	DelegatedValidator    string        `json:"delegatedValidator"`
	PendingUnbonds        []unbondEntry `json:"pendingUnbonds"`
	Username              string        `json:"username"`
	Nonce                 uint64        `json:"nonce"`
	EngagementScore       uint64        `json:"engagementScore"`
	ValidatorRegistered   bool          `json:"validatorRegistered"`
	ValidatorRegisteredAt uint64        `json:"validatorRegisteredAt"`
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

func sendTransaction(tx *types.Transaction) (string, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"id": 1, "method": "nhb_sendTransaction", "params": []interface{}{tx},
	})
	resp, err := doRPCRequest(payload, true)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return "", fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		if len(rpcResp.Error.Data) > 0 {
			// The server puts the actual validation reason (e.g. "nonce N
			// has already been used") in `data`, not `message` -- the
			// latter is often just a generic wrapper like "invalid
			// transaction". Surface both, since the detail is what an
			// operator actually needs to act on.
			var detail string
			if err := json.Unmarshal(rpcResp.Error.Data, &detail); err == nil && detail != "" {
				return "", fmt.Errorf("error from node: %s: %s", rpcResp.Error.Message, detail)
			}
			return "", fmt.Errorf("error from node: %s: %s", rpcResp.Error.Message, string(rpcResp.Error.Data))
		}
		return "", fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return strings.TrimSpace(rpcResp.Result), nil
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

// privateKeyKeystorePassphraseEnv holds the passphrase for an encrypted V3
// keystore file passed to loadPrivateKey. Deliberately an environment
// variable, never a CLI argument -- same discipline as
// keystoreImportPassphraseEnv above, for the same reason (shell history,
// /proc/<pid>/cmdline).
const privateKeyKeystorePassphraseEnv = "NHB_KEYSTORE_PASSPHRASE"

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
	// Encrypted V3 keystore files (e.g. a running validator's
	// ValidatorKeystorePath) are JSON; legacy plaintext key files are raw
	// hex/binary bytes and are never valid JSON, so this is an unambiguous
	// format switch.
	if json.Valid(keyBytes) {
		passphrase, ok := os.LookupEnv(privateKeyKeystorePassphraseEnv)
		if !ok || strings.TrimSpace(passphrase) == "" {
			return nil, fmt.Errorf("%s looks like an encrypted keystore; set %s to its passphrase", path, privateKeyKeystorePassphraseEnv)
		}
		return crypto.LoadFromKeystore(path, passphrase)
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

// signAndSendTx signs a transaction with keyFile's key and its current
// on-chain nonce, then broadcasts it. Shared by every loyalty and escrow
// write subcommand -- both used to trust a plaintext "caller" string with
// no cryptographic proof behind it (the exact bug their respective RPC
// disablements fixed on-chain); a real transaction signature is the
// replacement, so every one of these commands now needs the actual signing
// key, not just an address string.
func signAndSendTx(txType types.TxType, data []byte, keyFile string) error {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return fmt.Errorf("loading private key: %w", err)
	}
	pubAddr := privKey.PubKey().Address().String()

	account, err := fetchAccount(pubAddr)
	if err != nil {
		return fmt.Errorf("fetching account details: %w", err)
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    account.Nonce,
		Data:     data,
		Value:    big.NewInt(0),
		GasLimit: 50000,
		GasPrice: big.NewInt(1),
	}
	tx.Sign(privKey.PrivateKey)

	if _, err := sendTransaction(&tx); err != nil {
		return fmt.Errorf("sending transaction: %w", err)
	}
	return nil
}

func loyaltyCreateBusiness(name, keyFile string) int {
	data, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}
	if err := signAndSendTx(types.TxTypeCreateLoyaltyBusiness, data, keyFile); err != nil {
		fmt.Printf("Error creating business: %v\n", err)
		return 1
	}
	fmt.Println("Business creation transaction sent.")
	fmt.Println("Check the node logs for confirmation, then run 'loyalty-list-businesses <your address>' to find the assigned businessId.")
	return 0
}

func loyaltySetPaymaster(businessID, paymaster, keyFile string) int {
	data, err := json.Marshal(map[string]string{
		"businessId": businessID,
		"paymaster":  paymaster,
	})
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}
	if err := signAndSendTx(types.TxTypeLoyaltySetPaymaster, data, keyFile); err != nil {
		fmt.Printf("Error setting paymaster: %v\n", err)
		return 1
	}
	fmt.Println("Paymaster update transaction sent. Check the node logs for confirmation.")
	return 0
}

func loyaltyModifyMerchant(txType types.TxType, businessID, merchant, keyFile string) int {
	data, err := json.Marshal(map[string]string{
		"businessId": businessID,
		"merchant":   merchant,
	})
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}
	if err := signAndSendTx(txType, data, keyFile); err != nil {
		fmt.Printf("Error modifying merchant: %v\n", err)
		return 1
	}
	fmt.Println("Merchant update transaction sent. Check the node logs for confirmation.")
	return 0
}

// loyaltyCreateProgram merges businessId into the caller-supplied spec JSON
// (overwriting any businessId already present in it, since the CLI's own
// <businessId> argument is authoritative) before sending -- the on-chain
// payload is a single flat object, unlike the old RPC's separate
// caller/businessId/spec envelope. Also fills in a random 32-byte "id" if
// the spec doesn't already have one, matching the documented convention
// (docs/loyalty/loyalty.md) that a program's ID is client-generated.
func loyaltyCreateProgram(businessID, spec, keyFile string) int {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(spec), &fields); err != nil {
		fmt.Printf("Invalid program spec JSON: %v\n", err)
		return 1
	}
	businessIDJSON, err := json.Marshal(businessID)
	if err != nil {
		fmt.Printf("Error encoding businessId: %v\n", err)
		return 1
	}
	fields["businessId"] = businessIDJSON
	if raw, ok := fields["id"]; !ok || string(raw) == `""` || string(raw) == "null" {
		id, err := generateLoyaltyID()
		if err != nil {
			fmt.Printf("Error generating program id: %v\n", err)
			return 1
		}
		idJSON, err := json.Marshal(id)
		if err != nil {
			fmt.Printf("Error encoding program id: %v\n", err)
			return 1
		}
		fields["id"] = idJSON
		fmt.Printf("Generated program id: %s\n", id)
	}
	data, err := json.Marshal(fields)
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}
	if err := signAndSendTx(types.TxTypeCreateLoyaltyProgram, data, keyFile); err != nil {
		fmt.Printf("Error creating program: %v\n", err)
		return 1
	}
	fmt.Println("Program creation transaction sent. Check the node logs for confirmation.")
	return 0
}

func generateLoyaltyID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(buf), nil
}

func loyaltyUpdateProgram(spec, keyFile string) int {
	if !json.Valid([]byte(spec)) {
		fmt.Println("Invalid program spec JSON.")
		return 1
	}
	if err := signAndSendTx(types.TxTypeUpdateLoyaltyProgram, []byte(spec), keyFile); err != nil {
		fmt.Printf("Error updating program: %v\n", err)
		return 1
	}
	fmt.Println("Program update transaction sent (full replace). Check the node logs for confirmation.")
	return 0
}

func loyaltyLifecycle(txType types.TxType, programID, keyFile string) int {
	data, err := json.Marshal(map[string]string{"id": programID})
	if err != nil {
		fmt.Printf("Error encoding payload: %v\n", err)
		return 1
	}
	if err := signAndSendTx(txType, data, keyFile); err != nil {
		fmt.Printf("Error performing operation: %v\n", err)
		return 1
	}
	fmt.Println("Transaction sent. Check the node logs for confirmation.")
	return 0
}

func loyaltyGetBusiness(businessID string) int {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_getBusiness", param, false)
	if err != nil {
		fmt.Printf("Error fetching business: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func loyaltyListBusinesses(owner string) int {
	param := map[string]string{"owner": owner}
	result, err := callLoyaltyRPC("loyalty_listBusinesses", param, false)
	if err != nil {
		fmt.Printf("Error listing businesses: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func loyaltyListPrograms(businessID string) int {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_listPrograms", param, false)
	if err != nil {
		fmt.Printf("Error listing programs: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func loyaltyProgramStats(programID, day string) int {
	param := map[string]string{"programId": programID, "day": day}
	result, err := callLoyaltyRPC("loyalty_programStats", param, false)
	if err != nil {
		fmt.Printf("Error fetching stats: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func loyaltyUserDaily(user, programID, day string) int {
	param := map[string]string{"user": user, "programId": programID, "day": day}
	result, err := callLoyaltyRPC("loyalty_userDaily", param, false)
	if err != nil {
		fmt.Printf("Error fetching user meter: %v\n", err)
		return 1
	}
	value, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return 1
	}
	fmt.Printf("Daily accrued: %s\n", value)
	return 0
}

func loyaltyPaymasterBalance(businessID string) int {
	param := map[string]string{"businessId": businessID}
	result, err := callLoyaltyRPC("loyalty_paymasterBalance", param, false)
	if err != nil {
		fmt.Printf("Error fetching paymaster balance: %v\n", err)
		return 1
	}
	balance, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return 1
	}
	fmt.Printf("Paymaster balance (ZNHB): %s\n", balance)
	return 0
}

func loyaltyResolveUsername(username string) int {
	param := map[string]string{"username": username}
	result, err := callLoyaltyRPC("loyalty_resolveUsername", param, false)
	if err != nil {
		fmt.Printf("Error resolving username: %v\n", err)
		return 1
	}
	address, err := decodeStringResult(result)
	if err != nil {
		fmt.Printf("Error decoding response: %v\n", err)
		return 1
	}
	fmt.Printf("Username %s resolves to %s\n", username, address)
	return 0
}

func loyaltyUserQR(mode, value string) int {
	param := make(map[string]string)
	switch strings.ToLower(mode) {
	case "username":
		param["username"] = value
	case "address":
		param["address"] = value
	default:
		fmt.Println("Mode must be either 'username' or 'address'.")
		return 1
	}
	result, err := callLoyaltyRPC("loyalty_userQR", param, false)
	if err != nil {
		fmt.Printf("Error fetching QR payload: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func printUsage() {
	fmt.Println("Usage: nhb-cli <command> [arguments]")
	fmt.Println()
	fmt.Println("Most commands require a locally generated signing key. Run ./nhb-cli generate-key first;")
	fmt.Println("the CLI aborts if wallet.key is missing or contains placeholder material.")
	fmt.Println("Every command that fails exits non-zero.")
	fmt.Println("Commands:")
	fmt.Println("  generate-key [--force]            - Generates a new key and saves to wallet.key; never overwrites an existing wallet.key unless --force, which first saves a backup copy next to it")
	fmt.Println("  balance <address>                 - Checks the balance and stake of an address")
	fmt.Println("  claim-username <username> <key_file>     - Claims a unique username for your wallet") // NEW LINE
	fmt.Println("  stake position <address>          - Show staking share metadata for an address")
	fmt.Println("  stake preview <address>           - Preview claimable staking rewards and timing")
	fmt.Println("  stake claim <address>             - (retired) the node no longer serves stake_claimRewards; exits non-zero")
	fmt.Println("  stake <amount> <path_to_key_file> - (legacy) stake a specified amount of ZapNHB")
	fmt.Println("  un-stake <amount> <path_to_key_file> - Un-stake a specified amount of ZapNHB")
	fmt.Println("  heartbeat <path_to_key_file>        - Sends a heartbeat to increase engagement score")
	fmt.Println("  set-reward-beneficiary <address|\"\"> <key_file> - Redirect this validator's epoch reward payouts to another wallet (\"\" clears it)")
	fmt.Println("  register-validator <amount> <key_file>   - Explicitly register this address as a validator candidate (0 = no additional stake)")
	fmt.Println("  deregister-validator <key_file>          - Explicitly un-register this address as a validator candidate")
	fmt.Println("  address <key_file>                 - Print the public address for a local key file")
	fmt.Println("  send-nhb [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file> - Transfers NHB")
	fmt.Println("  send-znhb [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file> - Transfers ZapNHB using the new transaction type")
	fmt.Println("  id                                 - Identity lookups (resolve, reverse); the alias mutators are retired and exit non-zero")
	fmt.Println("  escrow                             - Escrow management subcommands")
	fmt.Println("  claimable                          - Hash-lock claimable lookup (get); create, claim and cancel are retired and exit non-zero")
	fmt.Println("  p2p                                - P2P trade lookup (get); create-trade, settle, dispute and resolve are retired and exit non-zero")
	fmt.Println("  potso                              - POTSO telemetry subcommands (reward claim is retired and exits non-zero)")
	fmt.Println("  pos                                - POS subcommands (sweep-voids is retired and exits non-zero)")
	fmt.Println("  swap                               - Swap voucher queries and export (needs NHB_RPC_TOKEN)")
	fmt.Println("  gov                                - Governance proposals: propose, vote, finalize, queue, execute (signed transactions), show, list")
	fmt.Println("  fees                               - Fee status queries")
	fmt.Println("  subscriptions                      - Subscription plans and charges: create, update, subscribe, cancel (signed transactions) and lookups")
	fmt.Println("  loyalty-*                          - Loyalty business and program commands (see docs/loyalty/loyalty.md)")
	fmt.Println("  keystore import --out <path>       - Encrypt a private key (env vars only) into a local keystore file")
	fmt.Println("  rpc-token [--ttl <duration>]       - Print a short-lived NHB_RPC_TOKEN signed with NHB_RPC_JWT_SECRET (run on the node host)")
	fmt.Println("  help                               - Print this text")
}
