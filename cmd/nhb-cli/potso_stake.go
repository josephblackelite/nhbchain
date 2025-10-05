package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/crypto"
)

type potsoStakeLockParams struct {
        Owner     string `json:"owner"`
        Amount    string `json:"amount"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeUnbondParams struct {
        Owner     string `json:"owner"`
        Amount    string `json:"amount"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeWithdrawParams struct {
        Owner     string `json:"owner"`
        Nonce     uint64 `json:"nonce"`
        Signature string `json:"signature"`
}

type potsoStakeInfoParams struct {
	Owner string `json:"owner"`
}

func potsoStakeDigest(action, owner string, amount *big.Int, nonce uint64) []byte {
        normalizedOwner := strings.ToLower(strings.TrimSpace(owner))
        payload := "potso_stake_" + action + "|" + normalizedOwner
        if amount != nil {
                payload += "|" + amount.String()
        }
        payload += "|" + strconv.FormatUint(nonce, 10)
        digest := sha256.Sum256([]byte(payload))
        return digest[:]
}

func runPotsoStake(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, potsoStakeUsage())
		return 1
	}
	switch args[0] {
	case "lock":
		return runPotsoStakeLock(args[1:], stdout, stderr)
	case "unbond":
		return runPotsoStakeUnbond(args[1:], stdout, stderr)
	case "withdraw":
		return runPotsoStakeWithdraw(args[1:], stdout, stderr)
	case "info":
		return runPotsoStakeInfo(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown potso stake subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, potsoStakeUsage())
		return 1
	}
}

func potsoStakeUsage() string {
	return "Usage: nhb-cli potso stake <lock|unbond|withdraw|info> [options]"
}

func parseStakeAmount(value string) (*big.Int, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if cleaned == "" {
		return nil, fmt.Errorf("amount is required")
	}
	var exp int
	base := cleaned
	if strings.ContainsAny(cleaned, "eE") {
		parts := strings.Split(strings.ToLower(cleaned), "e")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid amount format")
		}
		base = parts[0]
		parsedExp, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid exponent: %v", err)
		}
		exp = parsedExp
	}
	scale := 0
	if strings.Contains(base, ".") {
		comps := strings.SplitN(base, ".", 2)
		scale = len(comps[1])
		base = comps[0] + comps[1]
	}
	digits := strings.TrimLeft(base, "+")
	if digits == "" {
		return nil, fmt.Errorf("amount is required")
	}
	amt := new(big.Int)
	if _, ok := amt.SetString(digits, 10); !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	expTotal := exp - scale
	if expTotal < 0 {
		return nil, fmt.Errorf("amount must be an integer")
	}
	if expTotal > 0 {
		ten := big.NewInt(10)
		pow := new(big.Int).Exp(ten, big.NewInt(int64(expTotal)), nil)
		amt.Mul(amt, pow)
	}
	if amt.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return amt, nil
}

func signStakeAction(action, owner string, amount *big.Int, nonce uint64, key *crypto.PrivateKey) (string, error) {
        payload := potsoStakeDigest(action, owner, amount, nonce)
        sig, err := ethcrypto.Sign(payload, key.PrivateKey)
        if err != nil {
                return "", err
        }
        return "0x" + hex.EncodeToString(sig), nil
}

func runPotsoStakeLock(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake lock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
                owner  string
                amount string
                nonce  uint64
                key    string
        )
        fs.StringVar(&owner, "owner", "", "bech32 address of the owner")
        fs.StringVar(&amount, "amount", "", "amount of ZNHB to lock (supports scientific notation)")
        fs.Uint64Var(&nonce, "nonce", 0, "monotonic staking nonce authorising the request")
        fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
        if err := fs.Parse(args); err != nil {
                return 1
        }
        if owner == "" || amount == "" || nonce == 0 {
                fmt.Fprintln(stderr, "Error: --owner, --amount, and --nonce are required")
                return 1
        }
	amt, err := parseStakeAmount(amount)
	if err != nil {
		fmt.Fprintf(stderr, "Error parsing amount: %v\n", err)
		return 1
	}
	privKey, err := loadPrivateKey(key)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading key: %v\n", err)
		return 1
	}
	signer := privKey.PubKey().Address().String()
	if !strings.EqualFold(strings.TrimSpace(signer), strings.TrimSpace(owner)) {
		fmt.Fprintf(stderr, "Error: signing key belongs to %s but --owner was %s\n", signer, owner)
		return 1
	}
        signature, err := signStakeAction("lock", owner, amt, nonce, privKey)
        if err != nil {
                fmt.Fprintf(stderr, "Error signing request: %v\n", err)
                return 1
        }
        params := potsoStakeLockParams{Owner: owner, Amount: amt.String(), Nonce: nonce, Signature: signature}
        result, err := callPotsoRPCWithAuth("potso_stake_lock", params, true)
        if err != nil {
                fmt.Fprintf(stderr, "Error locking stake: %v\n", err)
                return 1
        }
	printJSONResult(result)
	return 0
}

func runPotsoStakeUnbond(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake unbond", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
                owner  string
                amount string
                nonce  uint64
                key    string
        )
        fs.StringVar(&owner, "owner", "", "bech32 address of the owner")
        fs.StringVar(&amount, "amount", "", "amount of ZNHB to unbond")
        fs.Uint64Var(&nonce, "nonce", 0, "monotonic staking nonce authorising the request")
        fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
        if err := fs.Parse(args); err != nil {
                return 1
        }
        if owner == "" || amount == "" || nonce == 0 {
                fmt.Fprintln(stderr, "Error: --owner, --amount, and --nonce are required")
                return 1
        }
	amt, err := parseStakeAmount(amount)
	if err != nil {
		fmt.Fprintf(stderr, "Error parsing amount: %v\n", err)
		return 1
	}
	privKey, err := loadPrivateKey(key)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading key: %v\n", err)
		return 1
	}
	signer := privKey.PubKey().Address().String()
	if !strings.EqualFold(strings.TrimSpace(signer), strings.TrimSpace(owner)) {
		fmt.Fprintf(stderr, "Error: signing key belongs to %s but --owner was %s\n", signer, owner)
		return 1
	}
        signature, err := signStakeAction("unbond", owner, amt, nonce, privKey)
        if err != nil {
                fmt.Fprintf(stderr, "Error signing request: %v\n", err)
                return 1
        }
        params := potsoStakeUnbondParams{Owner: owner, Amount: amt.String(), Nonce: nonce, Signature: signature}
        result, err := callPotsoRPCWithAuth("potso_stake_unbond", params, true)
        if err != nil {
                fmt.Fprintf(stderr, "Error unbonding stake: %v\n", err)
                return 1
        }
	printJSONResult(result)
	return 0
}

func runPotsoStakeWithdraw(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake withdraw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
                owner string
                nonce uint64
                key   string
        )
        fs.StringVar(&owner, "owner", "", "bech32 address of the owner")
        fs.Uint64Var(&nonce, "nonce", 0, "monotonic staking nonce authorising the request")
        fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
        if err := fs.Parse(args); err != nil {
                return 1
        }
        if owner == "" || nonce == 0 {
                fmt.Fprintln(stderr, "Error: --owner and --nonce are required")
                return 1
        }
	privKey, err := loadPrivateKey(key)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading key: %v\n", err)
		return 1
	}
	signer := privKey.PubKey().Address().String()
	if !strings.EqualFold(strings.TrimSpace(signer), strings.TrimSpace(owner)) {
		fmt.Fprintf(stderr, "Error: signing key belongs to %s but --owner was %s\n", signer, owner)
		return 1
	}
        signature, err := signStakeAction("withdraw", owner, nil, nonce, privKey)
        if err != nil {
                fmt.Fprintf(stderr, "Error signing request: %v\n", err)
                return 1
        }
        params := potsoStakeWithdrawParams{Owner: owner, Nonce: nonce, Signature: signature}
        result, err := callPotsoRPCWithAuth("potso_stake_withdraw", params, true)
	if err != nil {
		fmt.Fprintf(stderr, "Error withdrawing stake: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runPotsoStakeInfo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var owner string
	fs.StringVar(&owner, "owner", "", "bech32 address of the owner")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if owner == "" {
		fmt.Fprintln(stderr, "Error: --owner is required")
		return 1
	}
	params := potsoStakeInfoParams{Owner: owner}
	result, err := callPotsoRPCWithAuth("potso_stake_info", params, true)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching stake info: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}
