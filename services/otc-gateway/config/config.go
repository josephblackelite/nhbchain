package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config represents runtime configuration for the OTC gateway service.
type Config struct {
	Port             string
	DatabaseURL      string
	S3Bucket         string
	ChainID          string
	SwapRPCBase      string
	SwapAPIKey       string
	SwapAPISecret    string
	SwapMethodAllow  []string
	SwapRateLimit    int
	IdentityBaseURL  string
	IdentityAPIKey   string
	IdentityTimeout  time.Duration
	DefaultTZ        *time.Location
	HSMBaseURL       string
	HSMCACert        string
	HSMClientCert    string
	HSMClientKey     string
	HSMKeyLabel      string
	HSMOverrideDN    string
	SwapProvider     string
	VoucherTTL       time.Duration
	MintPollInterval time.Duration
	ReconOutputDir   string
	ReconRunHour     int
	ReconRunMinute   int
	ReconDryRun      bool
	ReconWindow      time.Duration
	Auth             AuthConfig
}

// AuthConfig captures authentication requirements for the OTC gateway.
type AuthConfig struct {
	RootAdminSubjects []string
	JWT               JWTConfig
	WebAuthn          WebAuthnConfig
	Secrets           SecretStoreConfig
}

// JWTConfig controls JWT verification settings including secret/key sources.
type JWTConfig struct {
	Enable             bool
	Alg                string
	Issuer             string
	Audience           []string
	MaxSkewSeconds     int
	HSSecretEnv        string
	HSSecretName       string
	RSAPublicKeyFile   string
	RSAPublicKeySecret string
	RoleClaim          string
	RoleMap            map[string]string
	RefreshInterval    time.Duration
}

// WebAuthnConfig configures WebAuthn attestation verification hooks.
type WebAuthnConfig struct {
	Enable          bool
	Endpoint        string
	Timeout         time.Duration
	APIKeyEnv       string
	APIKeySecret    string
	RPID            string
	Origin          string
	AssertionHeader string
	RequireRoles    []string
	APIKeyRefresh   time.Duration
}

// SecretStoreConfig describes how secrets are fetched at runtime.
type SecretStoreConfig struct {
	Backend   string
	Directory string
}

// FromEnv loads configuration from environment variables required by the service.
func FromEnv() (*Config, error) {
	port := getEnvDefault("OTC_PORT", "8080")
	dbURL := os.Getenv("OTC_DB_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("OTC_DB_URL is required")
	}

	bucket := os.Getenv("OTC_S3_BUCKET")
	if bucket == "" {
		return nil, fmt.Errorf("OTC_S3_BUCKET is required")
	}

	chainID := os.Getenv("OTC_CHAIN_ID")
	if chainID == "" {
		return nil, fmt.Errorf("OTC_CHAIN_ID is required")
	}

	rpcBase := os.Getenv("OTC_SWAP_RPC_BASE")
	if rpcBase == "" {
		rpcBase = os.Getenv("NHB_RPC_BASE")
	}
	if rpcBase == "" {
		return nil, fmt.Errorf("OTC_SWAP_RPC_BASE is required")
	}

	swapAPIKey := strings.TrimSpace(os.Getenv("OTC_SWAP_API_KEY"))
	if swapAPIKey == "" {
		return nil, fmt.Errorf("OTC_SWAP_API_KEY is required")
	}
	swapAPISecret := strings.TrimSpace(os.Getenv("OTC_SWAP_API_SECRET"))
	if swapAPISecret == "" {
		return nil, fmt.Errorf("OTC_SWAP_API_SECRET is required")
	}
	swapMethods := parseCSVEnv("OTC_SWAP_METHOD_ALLOWLIST")
	if len(swapMethods) == 0 {
		swapMethods = []string{"swap_submitVoucher", "swap_voucher_get", "swap_voucher_list", "swap_voucher_export"}
	}
	rateLimit := parseIntEnv("OTC_SWAP_RATE_LIMIT_PER_MINUTE", 60)
	if rateLimit < 0 {
		rateLimit = 0
	}

	identityBase := os.Getenv("OTC_IDENTITY_BASE_URL")
	if identityBase == "" {
		return nil, fmt.Errorf("OTC_IDENTITY_BASE_URL is required")
	}
	identityAPIKey := os.Getenv("OTC_IDENTITY_API_KEY")
	identityTimeoutSeconds := getEnvDefault("OTC_IDENTITY_TIMEOUT_SECONDS", "10")
	identityTimeoutValue, err := strconv.Atoi(identityTimeoutSeconds)
	if err != nil || identityTimeoutValue <= 0 {
		return nil, fmt.Errorf("invalid OTC_IDENTITY_TIMEOUT_SECONDS %q", identityTimeoutSeconds)
	}

	hsmBase := os.Getenv("OTC_HSM_BASE_URL")
	if hsmBase == "" {
		return nil, fmt.Errorf("OTC_HSM_BASE_URL is required")
	}
	hsmCACert := os.Getenv("OTC_HSM_CA_CERT")
	if hsmCACert == "" {
		return nil, fmt.Errorf("OTC_HSM_CA_CERT is required")
	}
	hsmClientCert := os.Getenv("OTC_HSM_CLIENT_CERT")
	if hsmClientCert == "" {
		return nil, fmt.Errorf("OTC_HSM_CLIENT_CERT is required")
	}
	hsmClientKey := os.Getenv("OTC_HSM_CLIENT_KEY")
	if hsmClientKey == "" {
		return nil, fmt.Errorf("OTC_HSM_CLIENT_KEY is required")
	}
	hsmKeyLabel := getEnvDefault("OTC_HSM_KEY_LABEL", "MINTER_NHB")
	swapProvider := getEnvDefault("OTC_SWAP_PROVIDER", "otc-gateway")

	ttlSeconds := getEnvDefault("OTC_VOUCHER_TTL_SECONDS", "900")
	ttl, err := strconv.Atoi(ttlSeconds)
	if err != nil || ttl <= 0 {
		return nil, fmt.Errorf("invalid OTC_VOUCHER_TTL_SECONDS %q", ttlSeconds)
	}

	pollSeconds := getEnvDefault("OTC_MINT_POLL_INTERVAL_SECONDS", "10")
	poll, err := strconv.Atoi(pollSeconds)
	if err != nil || poll <= 0 {
		return nil, fmt.Errorf("invalid OTC_MINT_POLL_INTERVAL_SECONDS %q", pollSeconds)
	}

	tzName := getEnvDefault("OTC_TZ_DEFAULT", "UTC")
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("invalid OTC_TZ_DEFAULT %q: %w", tzName, err)
	}

	reconDir := getEnvDefault("OTC_RECON_OUTPUT_DIR", "nhb-data-local/recon")
	reconHour := parseIntEnv("OTC_RECON_RUN_HOUR", 1)
	reconMinute := parseIntEnv("OTC_RECON_RUN_MINUTE", 5)
	reconDryRun := parseBoolEnv("OTC_RECON_DRY_RUN", false)
	windowHours := parseIntEnv("OTC_RECON_WINDOW_HOURS", 24)
	reconWindow := time.Duration(windowHours) * time.Hour

	rootAdmins := parseCSVEnv("OTC_ROOT_ADMIN_SUBJECTS")

	jwtRefreshSeconds := parseIntEnv("OTC_AUTH_JWT_REFRESH_SECONDS", 0)
	if jwtRefreshSeconds < 0 {
		jwtRefreshSeconds = 0
	}

	jwtCfg := JWTConfig{
		Enable:             parseBoolEnv("OTC_AUTH_JWT_ENABLE", true),
		Alg:                strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_ALG")),
		Issuer:             strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_ISSUER")),
		Audience:           parseCSVEnv("OTC_AUTH_JWT_AUDIENCE"),
		MaxSkewSeconds:     parseIntEnv("OTC_AUTH_JWT_MAX_SKEW_SECONDS", 60),
		HSSecretEnv:        strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_HS_SECRET_ENV")),
		HSSecretName:       strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_HS_SECRET_NAME")),
		RSAPublicKeyFile:   strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_RSA_PUBLIC_KEY_FILE")),
		RSAPublicKeySecret: strings.TrimSpace(os.Getenv("OTC_AUTH_JWT_RSA_PUBLIC_KEY_SECRET")),
		RoleClaim:          strings.TrimSpace(getEnvDefault("OTC_AUTH_JWT_ROLE_CLAIM", "role")),
		RoleMap:            parseKeyValueMapEnv("OTC_AUTH_JWT_ROLE_MAP"),
		RefreshInterval:    time.Duration(jwtRefreshSeconds) * time.Second,
	}
	if jwtCfg.Enable {
		if jwtCfg.Alg == "" {
			jwtCfg.Alg = "HS256"
		}
		if jwtCfg.Issuer == "" {
			return nil, fmt.Errorf("OTC_AUTH_JWT_ISSUER is required when JWT auth is enabled")
		}
		if len(jwtCfg.Audience) == 0 {
			return nil, fmt.Errorf("OTC_AUTH_JWT_AUDIENCE is required when JWT auth is enabled")
		}
		switch strings.ToUpper(jwtCfg.Alg) {
		case "HS256":
			if jwtCfg.HSSecretEnv == "" && jwtCfg.HSSecretName == "" {
				return nil, fmt.Errorf("OTC_AUTH_JWT_HS_SECRET_ENV or OTC_AUTH_JWT_HS_SECRET_NAME must be set for HS256")
			}
		case "RS256":
			if jwtCfg.RSAPublicKeyFile == "" && jwtCfg.RSAPublicKeySecret == "" {
				return nil, fmt.Errorf("OTC_AUTH_JWT_RSA_PUBLIC_KEY_FILE or OTC_AUTH_JWT_RSA_PUBLIC_KEY_SECRET must be set for RS256")
			}
		default:
			return nil, fmt.Errorf("unsupported OTC_AUTH_JWT_ALG %q", jwtCfg.Alg)
		}
	}

	timeoutSeconds := parseIntEnv("OTC_WEBAUTHN_TIMEOUT_SECONDS", 15)
	if timeoutSeconds <= 0 {
		timeoutSeconds = 15
	}
	webAuthnRefresh := parseIntEnv("OTC_WEBAUTHN_API_KEY_REFRESH_SECONDS", 0)
	if webAuthnRefresh < 0 {
		webAuthnRefresh = 0
	}

	webAuthnCfg := WebAuthnConfig{
		Enable:          parseBoolEnv("OTC_WEBAUTHN_ENABLE", true),
		Endpoint:        strings.TrimSpace(os.Getenv("OTC_WEBAUTHN_ENDPOINT")),
		Timeout:         time.Duration(timeoutSeconds) * time.Second,
		APIKeyEnv:       strings.TrimSpace(os.Getenv("OTC_WEBAUTHN_API_KEY_ENV")),
		APIKeySecret:    strings.TrimSpace(os.Getenv("OTC_WEBAUTHN_API_KEY_SECRET")),
		RPID:            strings.TrimSpace(os.Getenv("OTC_WEBAUTHN_RPID")),
		Origin:          strings.TrimSpace(os.Getenv("OTC_WEBAUTHN_ORIGIN")),
		AssertionHeader: strings.TrimSpace(getEnvDefault("OTC_WEBAUTHN_ASSERTION_HEADER", "X-WebAuthn-Attestation")),
		RequireRoles:    normalizeStrings(parseCSVEnv("OTC_WEBAUTHN_REQUIRE_ROLES")),
		APIKeyRefresh:   time.Duration(webAuthnRefresh) * time.Second,
	}
	if webAuthnCfg.Enable {
		if webAuthnCfg.Endpoint == "" {
			return nil, fmt.Errorf("OTC_WEBAUTHN_ENDPOINT is required when WebAuthn is enabled")
		}
		if webAuthnCfg.RPID == "" {
			return nil, fmt.Errorf("OTC_WEBAUTHN_RPID is required when WebAuthn is enabled")
		}
	}

	secretBackend := strings.ToLower(strings.TrimSpace(getEnvDefault("OTC_SECRET_BACKEND", "env")))
	secretDirectory := strings.TrimSpace(os.Getenv("OTC_SECRET_DIR"))
	secretsCfg := SecretStoreConfig{Backend: secretBackend, Directory: secretDirectory}
	if secretsCfg.Backend == "filesystem" && secretsCfg.Directory == "" {
		return nil, fmt.Errorf("OTC_SECRET_DIR is required when OTC_SECRET_BACKEND=filesystem")
	}

	return &Config{
		Port:             normalizePort(port),
		DatabaseURL:      dbURL,
		S3Bucket:         bucket,
		ChainID:          chainID,
		SwapRPCBase:      rpcBase,
		SwapAPIKey:       swapAPIKey,
		SwapAPISecret:    swapAPISecret,
		SwapMethodAllow:  swapMethods,
		SwapRateLimit:    rateLimit,
		IdentityBaseURL:  identityBase,
		IdentityAPIKey:   identityAPIKey,
		IdentityTimeout:  time.Duration(identityTimeoutValue) * time.Second,
		DefaultTZ:        tz,
		HSMBaseURL:       hsmBase,
		HSMCACert:        hsmCACert,
		HSMClientCert:    hsmClientCert,
		HSMClientKey:     hsmClientKey,
		HSMKeyLabel:      hsmKeyLabel,
		HSMOverrideDN:    os.Getenv("OTC_HSM_SIGNER_DN"),
		SwapProvider:     swapProvider,
		VoucherTTL:       time.Duration(ttl) * time.Second,
		MintPollInterval: time.Duration(poll) * time.Second,
		ReconOutputDir:   reconDir,
		ReconRunHour:     reconHour,
		ReconRunMinute:   reconMinute,
		ReconDryRun:      reconDryRun,
		ReconWindow:      reconWindow,
		Auth: AuthConfig{
			RootAdminSubjects: rootAdmins,
			JWT:               jwtCfg,
			WebAuthn:          webAuthnCfg,
			Secrets:           secretsCfg,
		},
	}, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func normalizePort(port string) string {
	if port == "" {
		return "8080"
	}
	if _, err := strconv.Atoi(port); err == nil {
		return port
	}
	// Allow values like ":8080".
	if len(port) > 0 && port[0] == ':' {
		return port[1:]
	}
	return port
}

func parseIntEnv(key string, def int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return def
}

func parseBoolEnv(key string, def bool) bool {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return def
}

func parseCSVEnv(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	})
	return fields
}

func parseKeyValueMapEnv(key string) map[string]string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	pairs := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	result := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		cleaned := strings.TrimSpace(pair)
		if cleaned == "" {
			continue
		}
		parts := strings.SplitN(cleaned, ":", 2)
		if len(parts) != 2 {
			continue
		}
		keyPart := strings.ToLower(strings.TrimSpace(parts[0]))
		valuePart := strings.TrimSpace(parts[1])
		if keyPart == "" || valuePart == "" {
			continue
		}
		result[keyPart] = valuePart
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, strings.ToLower(trimmed))
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}
