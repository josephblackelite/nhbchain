package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config captures runtime configuration for the payments gateway service.
type Config struct {
	ListenAddress        string
	DatabasePath         string
	NodeURL              string
	NodeAuthToken        string
	QuoteTTL             time.Duration
	QuoteCurrency        string
	DefaultMintAsset     string
	OracleTTL            time.Duration
	OracleMaxDeviation   float64
	OracleCircuitBreaker float64
	ServiceFeeBps        int
	NowPaymentsAPIKey    string
	NowPaymentsIPNSecret string
	NowPaymentsBaseURL   string
	MinterKMSEnv         string
	PublicIPNCallbackURL string
	AdminToken           string

	// Redemption (Swap Out) payout-side config. All optional/unset by
	// default -- the redeem watcher only starts (see main.go) once every
	// value it needs is present, so a payments-gateway deployment that
	// predates the chain-side swap_listPendingRedemptions RPC method and the
	// RoleSwapPayoutAttestor governance grant (see the rollout plan) keeps
	// serving the existing mint/deposit flows normally instead of refusing
	// to start.
	//
	// AttestorKMSEnv names another environment variable holding the
	// redemption attestor's raw hex-encoded private key -- mirroring
	// MinterKMSEnv's indirection above. Deliberately a SEPARATE key from the
	// mint signer: see the isolation requirement in the Swap Out plan
	// (services/swapd/settlement is reused for the payout API integration,
	// but credentials and the on-chain signing key must never be shared
	// with any other NOWPayments-account-scoped integration).
	AttestorKMSEnv string

	// PayoutNowPaymentsEmail/Password/APIKey/BaseURL configure the
	// NOWPayments mass-payout (withdrawal) API client used exclusively for
	// redemption payouts (services/swapd/settlement.HTTPPayoutClient).
	// Deliberately fully separate env vars from NowPaymentsAPIKey/
	// NowPaymentsIPNSecret above (the deposit-side invoice/payment API) --
	// per the plan's explicit isolation requirement, this integration must
	// never share credentials or code path with the deposit side.
	PayoutNowPaymentsEmail    string
	PayoutNowPaymentsPassword string
	PayoutNowPaymentsAPIKey   string
	PayoutNowPaymentsBaseURL  string

	// PayoutNowPaymentsTOTPSecret is the base32 TOTP secret for the
	// NOWPayments account's payout 2FA (Account Settings > 2fa, set to
	// Google Authenticator rather than email). Optional: when unset, every
	// payout batch falls back to requiring a human to complete NOWPayments'
	// email 2FA step by hand, which is what this field exists to eliminate
	// (see settlement.NowPaymentsConfig.TOTPSecret's doc comment for why
	// email verification is unsafe to rely on for anything but the lowest
	// volumes -- an unverified batch auto-rejects on a timer).
	PayoutNowPaymentsTOTPSecret string

	// RedeemWatcherInterval is the redeem watcher's ticker period, mirroring
	// reconcileInterval's role for the deposit-side reconciler.
	RedeemWatcherInterval time.Duration
}

const (
	envListen          = "PAY_GATEWAY_LISTEN"
	envDBPath          = "PAY_GATEWAY_DB"
	envNodeURL         = "PAY_GATEWAY_NODE_URL"
	envNodeToken       = "PAY_GATEWAY_NODE_TOKEN"
	envQuoteTTL        = "PAY_GATEWAY_QUOTE_TTL"
	envOracleTTL       = "PAY_GATEWAY_ORACLE_TTL"
	envOracleDeviation = "PAY_GATEWAY_ORACLE_DEVIATION"
	envOracleBreaker   = "PAY_GATEWAY_ORACLE_BREAKER"
	envDefaultMint     = "PAY_GATEWAY_DEFAULT_MINT_ASSET"
	envServiceFeeBps   = "PAY_GATEWAY_SERVICE_FEE_BPS"
	envNowAPIKey       = "PAY_GATEWAY_NOW_API_KEY"
	envNowIPNSecret    = "PAY_GATEWAY_NOW_IPN_SECRET"
	envNowBaseURL      = "PAY_GATEWAY_NOW_BASE"
	envKMSEnv          = "PAY_GATEWAY_MINTER_KMS_ENV"
	envIPNCallbackURL  = "PAY_GATEWAY_PUBLIC_IPN_URL"
	envAdminToken      = "PAY_GATEWAY_ADMIN_TOKEN"

	// Redemption (Swap Out) payout-side env vars -- see Config's doc
	// comments above for why these are all optional.
	envAttestorKMSEnv        = "PAY_GATEWAY_ATTESTOR_KMS_ENV"
	envPayoutNowEmail        = "PAY_GATEWAY_PAYOUT_NOW_EMAIL"
	envPayoutNowPassword     = "PAY_GATEWAY_PAYOUT_NOW_PASSWORD"
	envPayoutNowAPIKey       = "PAY_GATEWAY_PAYOUT_NOW_API_KEY"
	envPayoutNowBaseURL      = "PAY_GATEWAY_PAYOUT_NOW_BASE"
	envPayoutNowTOTPSecret   = "PAY_GATEWAY_PAYOUT_NOW_TOTP_SECRET"
	envRedeemWatcherInterval = "PAY_GATEWAY_REDEEM_WATCHER_INTERVAL"
)

// defaultRedeemWatcherInterval is how often the redeem watcher polls
// swap_listPendingRedemptions for newly-burned requests and re-checks
// in-flight settlements. 30s mirrors the plan's suggested default; unlike
// reconcileInterval's 1-minute deposit-side sweep, this watcher also drives
// the attestor's own tx submissions, so a slightly tighter default keeps
// redeemer-visible latency reasonable without hammering the node.
const defaultRedeemWatcherInterval = 30 * time.Second

// LoadConfigFromEnv resolves configuration from environment variables with sane defaults.
func LoadConfigFromEnv() (*Config, error) {
	cfg := &Config{
		ListenAddress:        getenvDefault(envListen, ":8080"),
		DatabasePath:         getenvDefault(envDBPath, "payments-gateway.db"),
		NodeURL:              os.Getenv(envNodeURL),
		NodeAuthToken:        os.Getenv(envNodeToken),
		QuoteTTL:             parseDurationDefault(envQuoteTTL, 5*time.Minute),
		QuoteCurrency:        "USD",
		DefaultMintAsset:     strings.ToUpper(getenvDefault(envDefaultMint, "NHB")),
		OracleTTL:            parseDurationDefault(envOracleTTL, time.Minute),
		OracleMaxDeviation:   parsePercentDefault(envOracleDeviation, 0.05),
		OracleCircuitBreaker: parsePercentDefault(envOracleBreaker, 0.20),
		ServiceFeeBps:        parseIntDefault(envServiceFeeBps, 0),
		NowPaymentsAPIKey:    os.Getenv(envNowAPIKey),
		NowPaymentsIPNSecret: os.Getenv(envNowIPNSecret),
		NowPaymentsBaseURL:   getenvDefault(envNowBaseURL, "https://api.nowpayments.io/v1"),
		MinterKMSEnv:         os.Getenv(envKMSEnv),
		// Optional: if unset, NOWPayments falls back to whichever IPN URL is
		// configured in the merchant dashboard for the account.
		PublicIPNCallbackURL: strings.TrimSpace(os.Getenv(envIPNCallbackURL)),
		// Optional: if unset, GET /admin/webhook-events stays permanently
		// unauthorized (fail closed) rather than crashing the whole payment
		// service over a missing audit-dashboard credential.
		AdminToken: strings.TrimSpace(os.Getenv(envAdminToken)),

		// Redemption (Swap Out) payout-side config -- all optional, see
		// Config's doc comments.
		AttestorKMSEnv:              strings.TrimSpace(os.Getenv(envAttestorKMSEnv)),
		PayoutNowPaymentsEmail:      strings.TrimSpace(os.Getenv(envPayoutNowEmail)),
		PayoutNowPaymentsPassword:   strings.TrimSpace(os.Getenv(envPayoutNowPassword)),
		PayoutNowPaymentsAPIKey:     strings.TrimSpace(os.Getenv(envPayoutNowAPIKey)),
		PayoutNowPaymentsBaseURL:    getenvDefault(envPayoutNowBaseURL, "https://api.nowpayments.io/v1"),
		PayoutNowPaymentsTOTPSecret: strings.TrimSpace(os.Getenv(envPayoutNowTOTPSecret)),
		RedeemWatcherInterval:       parseDurationDefault(envRedeemWatcherInterval, defaultRedeemWatcherInterval),
	}

	if cfg.NodeURL == "" {
		return nil, fmt.Errorf("%s is required", envNodeURL)
	}
	if cfg.NowPaymentsAPIKey == "" {
		return nil, fmt.Errorf("%s is required", envNowAPIKey)
	}
	if cfg.NowPaymentsIPNSecret == "" {
		return nil, fmt.Errorf("%s is required", envNowIPNSecret)
	}
	if cfg.MinterKMSEnv == "" {
		return nil, fmt.Errorf("%s is required", envKMSEnv)
	}

	return cfg, nil
}

// RedemptionEnabled reports whether every config value the redeem watcher
// needs is actually present. Unlike the deposit-side settings validated
// above, none of these fail LoadConfigFromEnv -- the whole redemption
// (Swap Out) feature depends on chain-side changes (swap_listPendingRedemptions,
// the RoleSwapPayoutAttestor governance grant) that roll out on their own,
// slower timeline, so a payments-gateway binary must be deployable and able
// to keep serving mint/deposit traffic before those land. main.go checks
// this once at startup and only wires the watcher goroutine when it's true,
// logging a clear one-line reason otherwise instead of silently doing
// nothing.
func (c *Config) RedemptionEnabled() bool {
	if c == nil {
		return false
	}
	return c.AttestorKMSEnv != "" &&
		c.PayoutNowPaymentsEmail != "" &&
		c.PayoutNowPaymentsPassword != "" &&
		c.PayoutNowPaymentsAPIKey != ""
}

func getenvDefault(key, def string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return def
}

func parseDurationDefault(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}

func parsePercentDefault(key string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	if f < 0 {
		f = 0
	}
	return f
}

func parseIntDefault(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if value < 0 {
		return 0
	}
	return value
}
