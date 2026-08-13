package main

import (
	"flag"
	"fmt"
	"io"
	"math/big"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

// subscriptionCreatePlanPayload/subscriptionUpdatePlanPayload/
// subscriptionSubscribePayload/subscriptionCancelPayload mirror
// core/subscriptions_tx.go's applySubscriptionCreatePlanTransaction/
// applySubscriptionUpdatePlanTransaction/applySubscriptionSubscribeTransaction/
// applySubscriptionCancelTransaction's unexported decode-side payload
// shapes -- RLP encodes/decodes structs positionally, so these only need
// to match structurally.
type subscriptionCreatePlanPayload struct {
	Name               string
	PriceWei           *big.Int
	Asset              string
	IntervalSeconds    uint64
	TrialPeriodSeconds uint64
}

type subscriptionUpdatePlanPayload struct {
	PlanID uint64
	Name   string
	Active bool
}

type subscriptionSubscribePayload struct {
	PlanID uint64
}

type subscriptionCancelPayload struct {
	SubscriptionID uint64
}

func runSubscriptionsCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, subscriptionsUsage())
		return 1
	}
	switch args[0] {
	case "create-plan":
		return runSubscriptionsCreatePlan(args[1:], stdout, stderr)
	case "update-plan":
		return runSubscriptionsUpdatePlan(args[1:], stdout, stderr)
	case "subscribe":
		return runSubscriptionsSubscribe(args[1:], stdout, stderr)
	case "cancel":
		return runSubscriptionsCancel(args[1:], stdout, stderr)
	case "get-plan":
		return runSubscriptionsGetPlan(args[1:], stdout, stderr)
	case "list-plans":
		return runSubscriptionsListPlans(args[1:], stdout, stderr)
	case "get-subscription":
		return runSubscriptionsGetSubscription(args[1:], stdout, stderr)
	case "list-by-payer":
		return runSubscriptionsListByPayer(args[1:], stdout, stderr)
	case "list-by-merchant":
		return runSubscriptionsListByMerchant(args[1:], stdout, stderr)
	case "list-charges":
		return runSubscriptionsListCharges(args[1:], stdout, stderr)
	case "config":
		return runSubscriptionsConfig(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown subscriptions subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, subscriptionsUsage())
		return 1
	}
}

func subscriptionsUsage() string {
	return "Usage: nhb-cli subscriptions <create-plan|update-plan|subscribe|cancel|get-plan|list-plans|get-subscription|list-by-payer|list-by-merchant|list-charges|config> [options]"
}

// sendSubscriptionsTx signs payload as the given subscriptions TxType with
// the key loaded from keyFile and broadcasts it via the standard
// nhb_sendTransaction path. The merchant/payer/caller is always whichever
// key signs -- there is no separate --from/--owner address to spoof (see
// core/types/transaction.go's TxTypeSubscriptionXxx doc comment).
func sendSubscriptionsTx(keyFile string, txType types.TxType, payload interface{}) (string, error) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return "", fmt.Errorf("loading private key: %w", err)
	}
	account, err := fetchAccount(privKey.PubKey().Address().String())
	if err != nil {
		return "", fmt.Errorf("fetching account details: %w", err)
	}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return "", fmt.Errorf("encoding payload: %w", err)
	}
	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		return "", fmt.Errorf("signing transaction: %w", err)
	}
	hash, err := sendTransaction(&tx)
	if err != nil {
		return "", fmt.Errorf("sending transaction: %w", err)
	}
	return hash, nil
}

func runSubscriptionsCreatePlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions create-plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		name         string
		price        string
		asset        string
		intervalSecs uint64
		trialSecs    uint64
		key          string
	)
	fs.StringVar(&name, "name", "", "plan name (e.g. \"Pro Monthly\")")
	fs.StringVar(&price, "price", "", "price per cycle (supports scientific notation, e.g. 10e18 for 10 tokens)")
	fs.StringVar(&asset, "asset", "NHB", "asset to charge: NHB or ZNHB")
	fs.Uint64Var(&intervalSecs, "interval-seconds", 2_592_000, "billing cycle length in seconds (default: 30 days)")
	fs.Uint64Var(&trialSecs, "trial-seconds", 0, "trial period before the first charge, in seconds")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key (generate with ./nhb-cli generate-key)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if name == "" {
		fmt.Fprintln(stderr, "Error: --name is required")
		return 1
	}
	if price == "" {
		fmt.Fprintln(stderr, "Error: --price is required")
		return 1
	}
	priceWei, err := parseStakeAmount(price)
	if err != nil {
		fmt.Fprintf(stderr, "Error parsing price: %v\n", err)
		return 1
	}
	hash, err := sendSubscriptionsTx(key, types.TxTypeSubscriptionCreatePlan, subscriptionCreatePlanPayload{
		Name:               name,
		PriceWei:           priceWei,
		Asset:              asset,
		IntervalSeconds:    intervalSecs,
		TrialPeriodSeconds: trialSecs,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error creating plan: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted plan creation: %s\n", hash)
	return 0
}

func runSubscriptionsUpdatePlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions update-plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		planID uint64
		name   string
		active bool
		key    string
	)
	fs.Uint64Var(&planID, "plan-id", 0, "plan id to update")
	fs.StringVar(&name, "name", "", "new plan name")
	fs.BoolVar(&active, "active", true, "whether the plan accepts new subscribers")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if planID == 0 {
		fmt.Fprintln(stderr, "Error: --plan-id is required")
		return 1
	}
	if name == "" {
		fmt.Fprintln(stderr, "Error: --name is required")
		return 1
	}
	hash, err := sendSubscriptionsTx(key, types.TxTypeSubscriptionUpdatePlan, subscriptionUpdatePlanPayload{
		PlanID: planID,
		Name:   name,
		Active: active,
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error updating plan: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted plan update: %s\n", hash)
	return 0
}

func runSubscriptionsSubscribe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions subscribe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		planID uint64
		key    string
	)
	fs.Uint64Var(&planID, "plan-id", 0, "plan id to subscribe to")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if planID == 0 {
		fmt.Fprintln(stderr, "Error: --plan-id is required")
		return 1
	}
	hash, err := sendSubscriptionsTx(key, types.TxTypeSubscriptionSubscribe, subscriptionSubscribePayload{PlanID: planID})
	if err != nil {
		fmt.Fprintf(stderr, "Error subscribing: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted subscription: %s\n", hash)
	return 0
}

func runSubscriptionsCancel(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions cancel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		subscriptionID uint64
		key            string
	)
	fs.Uint64Var(&subscriptionID, "subscription-id", 0, "subscription id to cancel")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if subscriptionID == 0 {
		fmt.Fprintln(stderr, "Error: --subscription-id is required")
		return 1
	}
	hash, err := sendSubscriptionsTx(key, types.TxTypeSubscriptionCancel, subscriptionCancelPayload{SubscriptionID: subscriptionID})
	if err != nil {
		fmt.Fprintf(stderr, "Error cancelling subscription: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted subscription cancellation: %s\n", hash)
	return 0
}

func runSubscriptionsGetPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions get-plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var planID string
	fs.StringVar(&planID, "plan-id", "", "plan id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if planID == "" {
		fmt.Fprintln(stderr, "Error: --plan-id is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_getPlan", map[string]string{"planId": planID}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching plan: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsListPlans(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions list-plans", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var merchant string
	fs.StringVar(&merchant, "merchant", "", "bech32 merchant address")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if merchant == "" {
		fmt.Fprintln(stderr, "Error: --merchant is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_listPlansByMerchant", map[string]string{"merchant": merchant}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error listing plans: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsGetSubscription(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions get-subscription", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var subscriptionID string
	fs.StringVar(&subscriptionID, "subscription-id", "", "subscription id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if subscriptionID == "" {
		fmt.Fprintln(stderr, "Error: --subscription-id is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_getSubscription", map[string]string{"subscriptionId": subscriptionID}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching subscription: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsListByPayer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions list-by-payer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var payer string
	fs.StringVar(&payer, "payer", "", "bech32 payer address")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if payer == "" {
		fmt.Fprintln(stderr, "Error: --payer is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_listByPayer", map[string]string{"payer": payer}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error listing subscriptions: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsListByMerchant(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions list-by-merchant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var merchant string
	fs.StringVar(&merchant, "merchant", "", "bech32 merchant address")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if merchant == "" {
		fmt.Fprintln(stderr, "Error: --merchant is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_listByMerchant", map[string]string{"merchant": merchant}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error listing subscriptions: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsListCharges(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("subscriptions list-charges", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var subscriptionID string
	fs.StringVar(&subscriptionID, "subscription-id", "", "subscription id")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if subscriptionID == "" {
		fmt.Fprintln(stderr, "Error: --subscription-id is required")
		return 1
	}
	result, err := callPotsoRPCWithAuth("subscriptions_listCharges", map[string]string{"subscriptionId": subscriptionID}, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error listing charges: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runSubscriptionsConfig(args []string, stdout, stderr io.Writer) int {
	result, err := callPotsoRPCWithAuth("subscriptions_getConfig", nil, false)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching subscriptions config: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}
