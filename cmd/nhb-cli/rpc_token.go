package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Defaults match the [RPCJWT] section of the repository's config.toml
// (Issuer, Audience) and the variable that section's HSSecretEnv names.
const (
	rpcTokenSecretEnv       = "NHB_RPC_JWT_SECRET"
	defaultRPCTokenIssuer   = "nhb-rpc"
	defaultRPCTokenAudience = "wallets"
	defaultRPCTokenTTL      = 10 * time.Minute
	maxRPCTokenTTL          = 24 * time.Hour
	maxRPCTokenSecretBytes  = 1 << 16
)

func rpcTokenUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli rpc-token [--secret-stdin] [--ttl <duration>] [--issuer <name>] [--audience <a,b>]

Prints a short-lived bearer token for the node's privileged RPC methods,
signed with the node's own JWT secret. Run it on the node host. The secret is
never a CLI argument (that would show in process listings): it is read from
the NHB_RPC_JWT_SECRET environment variable, or from standard input with
--secret-stdin. The output is meant to be exported as NHB_RPC_TOKEN for the
other commands:

  export NHB_RPC_TOKEN="$(nhb-cli rpc-token)"

Flags:
  --secret-stdin       Read the secret from standard input instead of the environment.
  --ttl <duration>     Token lifetime (default 10m, at most 24h).
  --issuer <name>      Must match [RPCJWT] Issuer (default nhb-rpc).
  --audience <a,b>     Must include one of [RPCJWT] Audience (default wallets).
`)
}

// mintRPCToken signs an HS256 token that the node's RPC verifier accepts:
// issuer and audience as configured, valid from now until now+ttl. The claims
// are exactly the registered ones the verifier reads (rpc/http.go).
func mintRPCToken(secret, issuer string, audience []string, ttl time.Duration, now time.Time) (string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", fmt.Errorf("the node's RPC JWT secret is empty (set %s or use --secret-stdin)", rpcTokenSecretEnv)
	}
	if strings.TrimSpace(issuer) == "" {
		return "", fmt.Errorf("issuer must not be empty")
	}
	if len(audience) == 0 {
		return "", fmt.Errorf("at least one audience is required")
	}
	if ttl <= 0 || ttl > maxRPCTokenTTL {
		return "", fmt.Errorf("ttl must be greater than zero and at most %s", maxRPCTokenTTL)
	}
	claims := jwt.RegisteredClaims{
		Issuer:    issuer,
		Audience:  jwt.ClaimStrings(audience),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// runRPCTokenCommand implements `nhb-cli rpc-token`. It prints only the token.
func runRPCTokenCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rpc-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	secretStdin := fs.Bool("secret-stdin", false, "read the JWT secret from standard input")
	ttl := fs.Duration("ttl", defaultRPCTokenTTL, "token lifetime")
	issuer := fs.String("issuer", defaultRPCTokenIssuer, "JWT issuer expected by the node")
	audience := fs.String("audience", defaultRPCTokenAudience, "comma-separated JWT audience")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "Error: rpc-token takes no positional arguments")
		fmt.Fprintln(stderr, rpcTokenUsage())
		return 1
	}

	secret := os.Getenv(rpcTokenSecretEnv)
	if *secretStdin {
		data, err := io.ReadAll(io.LimitReader(stdin, maxRPCTokenSecretBytes+1))
		if err != nil {
			fmt.Fprintf(stderr, "Error: read secret from standard input: %v\n", err)
			return 1
		}
		if len(data) > maxRPCTokenSecretBytes {
			fmt.Fprintln(stderr, "Error: the secret on standard input is too long")
			return 1
		}
		secret = string(data)
	}

	var audiences []string
	for _, entry := range strings.Split(*audience, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			audiences = append(audiences, trimmed)
		}
	}
	token, err := mintRPCToken(secret, *issuer, audiences, *ttl, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, token)
	return 0
}
