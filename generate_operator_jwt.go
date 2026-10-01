//go:build ignore

// generate_operator_jwt.go mints an operator-scoped RPC credential: a JWT
// carrying the 'role: operator' claim that rpc/operator_methods.go and
// rpc/http.go's requireOperatorInto require before letting a caller invoke
// any method in rpc.OperatorOnlyMethods (net_ban, sync_snapshot_import,
// swap_setManualQuote, buyback_submitRefPrice, and so on).
//
// This is deliberately a SEPARATE tool from generate_jwt.go, not a flag on
// it: generate_jwt.go's output is the ordinary non-operator credential every
// existing daemon (the portal, escrow-gateway, etc.) uses today, and it must
// keep minting exactly that -- a token with no 'role' claim -- so this
// change does not silently grant those daemons operator access. Only a human
// who has decided a specific caller legitimately needs an operator-only
// method should run this tool and hand out its output.
//
// Usage:
//
//	NHB_RPC_JWT_SECRET=... go run generate_operator_jwt.go [subject]
//
// subject (optional, defaults to "operator") becomes the token's 'sub'
// claim, for telling operator credentials apart from each other in logs and
// rate-limit buckets (see rpc/http.go's allowSource). It does not affect
// authorization -- only the 'role' claim does.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	secretStr := os.Getenv("NHB_RPC_JWT_SECRET")
	if secretStr == "" {
		fmt.Println("Error: NHB_RPC_JWT_SECRET must be set in the environment before running this tool.")
		os.Exit(1)
	}
	subject := "operator"
	if len(os.Args) > 1 && os.Args[1] != "" {
		subject = os.Args[1]
	}
	secret := []byte(secretStr)
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":  "nhb-rpc",
		"aud":  []string{"wallets"},
		"sub":  subject,
		"role": "operator",
		"iat":  now.Unix(),
		"nbf":  now.Unix(),
		// Deliberately much shorter-lived than generate_jwt.go's 10-year
		// daemon tokens: an operator credential grants real admin power
		// (net_ban, sync_snapshot_import, swap_setManualQuote, ...), so it
		// should be minted for a specific operational task and re-minted
		// when actually needed, not handed out once and forgotten in a
		// config file for a decade. Adjust if your workflow needs longer,
		// but prefer re-running this tool over widening this value.
		"exp": now.Add(24 * time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(secret)
	if err != nil {
		panic(err)
	}
	fmt.Println(tokenString)
}
