package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML unmarshalling.
type Duration struct {
	time.Duration
}

// MarshalYAML renders the duration in the same human readable string form
// consumed by UnmarshalYAML (e.g. "30s"), so that a Config value can be
// marshaled back to YAML and parsed again symmetrically. Without this,
// yaml.Marshal falls back to the default struct representation of the
// embedded time.Duration field (a nested "duration: 30s" mapping) which
// UnmarshalYAML then rejects, since it requires a scalar node.
func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// UnmarshalYAML parses human readable duration strings.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be string")
	}
	raw := value.Value
	if raw == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

// Config captures runtime configuration for swapd.
type Config struct {
	ListenAddress string `yaml:"listen"`
	// DatabasePath must resolve to an on-disk SQLite database file.
	DatabasePath string           `yaml:"database"`
	Oracle       OracleConfig     `yaml:"oracle"`
	Sources      []Source         `yaml:"sources"`
	Pairs        []Pair           `yaml:"pairs"`
	Policy       PolicyConfig     `yaml:"policy"`
	Stable       StableConfig     `yaml:"stable"`
	Admin        AdminConfig      `yaml:"admin"`
	PriceProof   PriceProofConfig `yaml:"price_proof"`
}

type loadOptions struct {
	allowInsecureBearerWithoutTLS bool
}

// Option customises behaviour when loading swapd configuration.
type Option func(*loadOptions)

// WithAllowInsecureBearerWithoutTLS permits bearer authentication without TLS.
// Intended for development overrides only.
func WithAllowInsecureBearerWithoutTLS() Option {
	return func(o *loadOptions) {
		if o == nil {
			return
		}
		o.allowInsecureBearerWithoutTLS = true
	}
}

// AdminConfig captures security settings for the admin API.
type AdminConfig struct {
	BearerToken     string         `yaml:"bearer_token"`
	BearerTokenFile string         `yaml:"bearer_token_file"`
	MTLS            MTLSConfig     `yaml:"mtls"`
	TLS             AdminTLSConfig `yaml:"tls"`
}

// MTLSConfig tunes mutual TLS requirements.
type MTLSConfig struct {
	Enabled      bool   `yaml:"enabled"`
	ClientCAPath string `yaml:"client_ca"`
}

// AdminTLSConfig captures TLS key material configuration.
type AdminTLSConfig struct {
	Disable  bool   `yaml:"disable"`
	CertPath string `yaml:"cert"`
	KeyPath  string `yaml:"key"`
}

// OracleConfig tunes the aggregation loop.
type OracleConfig struct {
	Interval Duration `yaml:"interval"`
	MaxAge   Duration `yaml:"max_age"`
	MinFeeds int      `yaml:"min_feeds"`
}

// Source describes an upstream oracle feed. APIKeyFile mirrors the
// bearer_token_file pattern used elsewhere in this config -- it lets a real
// deployment inject a secret from a file (e.g. a mounted Kubernetes Secret
// or a host-local path outside git) instead of ever committing the key
// inline in a tracked config file.
type Source struct {
	Name       string            `yaml:"name"`
	Type       string            `yaml:"type"`
	Endpoint   string            `yaml:"endpoint"`
	APIKey     string            `yaml:"api_key"`
	APIKeyFile string            `yaml:"api_key_file"`
	Assets     map[string]string `yaml:"assets"`
}

// normalise resolves APIKeyFile into APIKey if set.
func (s *Source) normalise() error {
	if s == nil {
		return nil
	}
	path := strings.TrimSpace(s.APIKeyFile)
	if path == "" {
		return nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read api_key_file for source %s: %w", s.Name, err)
	}
	s.APIKey = strings.TrimSpace(string(contents))
	return nil
}

// Pair identifies a base/quote pair to publish.
type Pair struct {
	Base  string `yaml:"base"`
	Quote string `yaml:"quote"`
}

// PolicyConfig controls mint/redeem throttling.
type PolicyConfig struct {
	ID          string   `yaml:"id"`
	MintLimit   int      `yaml:"mint_limit"`
	RedeemLimit int      `yaml:"redeem_limit"`
	Window      Duration `yaml:"window"`
}

// StableConfig captures configuration for the experimental stable engine.
type StableConfig struct {
	Assets        []StableAsset    `yaml:"assets"`
	QuoteTTL      Duration         `yaml:"quote_ttl"`
	MaxSlippage   int              `yaml:"max_slippage_bps"`
	SoftInventory int64            `yaml:"soft_inventory"`
	Paused        bool             `yaml:"paused"`
	Partners      []StablePartner  `yaml:"partners"`
	Settlement    SettlementConfig `yaml:"settlement"`
}

// SettlementConfig controls how stable-swap cash-out intents are actually
// settled once created. DefaultRail applies to any partner without an
// explicit override (see StablePartner.SettlementRail).
type SettlementConfig struct {
	// DefaultRail is "nowpayments" or "manual_treasury". Empty defaults to
	// manual_treasury -- the safest choice, since it never attempts an
	// automated payout for a partner nobody explicitly configured.
	DefaultRail string                      `yaml:"default_rail"`
	NowPayments NowPaymentsSettlementConfig `yaml:"nowpayments"`
}

// NowPaymentsSettlementConfig holds credentials for the automated
// NOWPayments mass-payout rail. Only required when default_rail or any
// partner's settlement_rail is "nowpayments".
type NowPaymentsSettlementConfig struct {
	Email        string `yaml:"email"`
	EmailFile    string `yaml:"email_file"`
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
	APIKey       string `yaml:"api_key"`
	APIKeyFile   string `yaml:"api_key_file"`
	BaseURL      string `yaml:"base_url"`
}

// StableAsset allows per-asset overrides for the stable engine.
type StableAsset struct {
	Symbol        string   `yaml:"symbol"`
	BasePair      string   `yaml:"base"`
	QuotePair     string   `yaml:"quote"`
	QuoteTTL      Duration `yaml:"quote_ttl"`
	MaxSlippage   int      `yaml:"max_slippage_bps"`
	SoftInventory int64    `yaml:"soft_inventory"`
}

// StablePartner enumerates partner credentials and quota knobs.
type StablePartner struct {
	ID     string             `yaml:"id"`
	APIKey string             `yaml:"api_key"`
	Secret string             `yaml:"secret"`
	Quota  StablePartnerQuota `yaml:"quota"`
	// SettlementRail overrides stable.settlement.default_rail for this
	// partner specifically. Empty means "use the default."
	SettlementRail string `yaml:"settlement_rail"`
}

// StablePartnerQuota exposes soft quota configuration per partner.
type StablePartnerQuota struct {
	Daily float64 `yaml:"daily"`
}

// PriceProofConfig configures the optional price-proof signing endpoint
// (POST /v1/price-proof), used by external callers such as the otc-gateway
// fiat onramp to obtain a freshly signed swap.PriceProof at voucher
// submission time. Disabled unless Enabled is true. Partners is a
// deliberately independent authentication list from stable.partners -- an
// operator can grant a caller price-proof access without also granting it
// stable-engine trading access.
type PriceProofConfig struct {
	Enabled  bool                   `yaml:"enabled"`
	Provider string                 `yaml:"provider"`
	Pairs    []string               `yaml:"pairs"`
	Signer   PriceProofSignerConfig `yaml:"signer"`
	Partners []StablePartner        `yaml:"partners"`
}

// Recognised values for PriceProofSignerConfig.Type. PriceProofSignerTypeHSM
// is the default -- selected automatically whenever Type is left blank --
// so every deployment configured before the "local" option existed keeps
// working unmodified.
const (
	PriceProofSignerTypeHSM   = "hsm"
	PriceProofSignerTypeLocal = "local"
)

// PriceProofSignerConfig configures the signer used to sign price proofs.
// Type selects between two mutually exclusive implementations:
//
//   - "hsm" (default): an mTLS-fronted HSM proxy, configured via BaseURL,
//     KeyLabel, CACertPath, ClientCertPath, ClientKeyPath, and optionally
//     SignPath. This is the only option that existed before local signing
//     was added, so it stays the default for backward compatibility with
//     any already-deployed config that predates the Type field.
//   - "local": a local encrypted keystore file (the same Ethereum V3
//     keystore format this repo already uses for validator keys), decrypted
//     once at startup. Configured via KeystorePath and PassphraseEnv. This
//     exists for operators who want to reuse a wallet they already hold as
//     a keystore file without provisioning real HSM infrastructure.
//
// Whichever type is chosen, the referenced key MUST be distinct from any
// mint-voucher-signing key used elsewhere (e.g. the otc-gateway's
// MINTER_ZNHB signer) -- a price-signer key must not carry the same blast
// radius as the ZNHB mint-authority key.
type PriceProofSignerConfig struct {
	Type string `yaml:"type"`

	// HSM signer fields. Required when Type is "hsm" (or left blank).
	BaseURL        string `yaml:"base_url"`
	KeyLabel       string `yaml:"key_label"`
	CACertPath     string `yaml:"ca_cert"`
	ClientCertPath string `yaml:"client_cert"`
	ClientKeyPath  string `yaml:"client_key"`
	SignPath       string `yaml:"sign_path"`

	// Local keystore signer fields. Required when Type is "local".

	// KeystorePath is the path to an encrypted keystore file created via
	// `nhb-cli keystore import` (see cmd/nhb-cli/keystore_cmd.go) -- never a
	// raw private key pasted directly into this config.
	KeystorePath string `yaml:"keystore_path"`
	// PassphraseEnv is the NAME of an environment variable holding the
	// keystore's decryption passphrase -- never the passphrase value itself.
	// A passphrase must never appear in this (or any) config file, since
	// config files are the kind of thing that ends up committed to git or
	// captured in a log/debug dump.
	PassphraseEnv string `yaml:"passphrase_env"`
}

// NormalizedType returns c.Type, lower-cased and trimmed, defaulting to
// PriceProofSignerTypeHSM when blank. Both validatePriceProof and
// services/swapd/main.go call this rather than comparing c.Type directly, so
// "unset" and "hsm" are always treated identically no matter which of the
// two ever gets the field populated first (e.g. applyDefaults, or a test
// constructing a Config literal by hand without going through Load).
func (c PriceProofSignerConfig) NormalizedType() string {
	t := strings.ToLower(strings.TrimSpace(c.Type))
	if t == "" {
		return PriceProofSignerTypeHSM
	}
	return t
}

// Load reads configuration from the supplied path.
func Load(path string, opts ...Option) (Config, error) {
	cfg := Config{}
	options := loadOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return cfg, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	dec := yaml.NewDecoder(file)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	applyDefaults(&cfg)
	if err := cfg.Admin.normalise(options.allowInsecureBearerWithoutTLS); err != nil {
		return cfg, fmt.Errorf("admin security: %w", err)
	}
	if err := cfg.Stable.Settlement.NowPayments.normalise(); err != nil {
		return cfg, fmt.Errorf("stable settlement: %w", err)
	}
	for i := range cfg.Sources {
		if err := cfg.Sources[i].normalise(); err != nil {
			return cfg, fmt.Errorf("oracle source: %w", err)
		}
	}
	if err := validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (n *NowPaymentsSettlementConfig) normalise() error {
	if n == nil {
		return nil
	}
	email := strings.TrimSpace(n.Email)
	if path := strings.TrimSpace(n.EmailFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read nowpayments email_file: %w", err)
		}
		email = strings.TrimSpace(string(contents))
	}
	n.Email = email

	password := strings.TrimSpace(n.Password)
	if path := strings.TrimSpace(n.PasswordFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read nowpayments password_file: %w", err)
		}
		password = strings.TrimSpace(string(contents))
	}
	n.Password = password

	apiKey := strings.TrimSpace(n.APIKey)
	if path := strings.TrimSpace(n.APIKeyFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read nowpayments api_key_file: %w", err)
		}
		apiKey = strings.TrimSpace(string(contents))
	}
	n.APIKey = apiKey
	return nil
}

func applyDefaults(cfg *Config) {
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":7074"
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "/var/data/swapd.sqlite"
	}
	if cfg.Oracle.Interval.Duration == 0 {
		cfg.Oracle.Interval.Duration = 30 * time.Second
	}
	if cfg.Oracle.MaxAge.Duration == 0 {
		cfg.Oracle.MaxAge.Duration = 2 * time.Minute
	}
	if cfg.Oracle.MinFeeds <= 0 {
		cfg.Oracle.MinFeeds = 1
	}
	if cfg.Policy.Window.Duration == 0 {
		cfg.Policy.Window.Duration = time.Hour
	}
	if cfg.Policy.ID == "" {
		cfg.Policy.ID = "default"
	}
	if cfg.Stable.QuoteTTL.Duration == 0 {
		cfg.Stable.QuoteTTL.Duration = time.Minute
	}
	if cfg.Stable.MaxSlippage == 0 {
		cfg.Stable.MaxSlippage = 50
	}
	if cfg.Stable.SoftInventory == 0 {
		cfg.Stable.SoftInventory = 1_000_000
	}
	if strings.TrimSpace(cfg.Stable.Settlement.DefaultRail) == "" {
		cfg.Stable.Settlement.DefaultRail = "manual_treasury"
	}
	if strings.TrimSpace(cfg.Stable.Settlement.NowPayments.BaseURL) == "" {
		cfg.Stable.Settlement.NowPayments.BaseURL = "https://api.nowpayments.io/v1"
	}
	if cfg.PriceProof.Enabled && len(cfg.PriceProof.Pairs) == 0 {
		cfg.PriceProof.Pairs = []string{"ZNHB/USD"}
	}
	if strings.TrimSpace(cfg.PriceProof.Signer.Type) == "" {
		cfg.PriceProof.Signer.Type = PriceProofSignerTypeHSM
	}
}

func validate(cfg Config) error {
	if len(cfg.Pairs) == 0 {
		return fmt.Errorf("at least one pair must be configured")
	}
	if len(cfg.Sources) == 0 {
		return fmt.Errorf("at least one oracle source must be configured")
	}
	// Validated unconditionally (unlike the stable-engine checks below) --
	// the price-proof signing endpoint is an independent feature that must
	// work whether or not the stable trading engine is paused.
	if err := validatePriceProof(cfg.PriceProof); err != nil {
		return err
	}
	if cfg.Stable.Paused {
		return nil
	}
	if len(cfg.Stable.Assets) == 0 {
		return fmt.Errorf("stable assets must be configured when stable engine is enabled")
	}
	if len(cfg.Stable.Partners) == 0 {
		return fmt.Errorf("stable partners must be configured when stable engine is enabled")
	}
	if !validSettlementRail(cfg.Stable.Settlement.DefaultRail) {
		return fmt.Errorf("stable settlement default_rail must be %q or %q", "nowpayments", "manual_treasury")
	}
	usesNowPayments := cfg.Stable.Settlement.DefaultRail == "nowpayments"
	seenIDs := make(map[string]struct{}, len(cfg.Stable.Partners))
	seenKeys := make(map[string]struct{}, len(cfg.Stable.Partners))
	for _, partner := range cfg.Stable.Partners {
		id := strings.TrimSpace(partner.ID)
		if id == "" {
			return fmt.Errorf("stable partner id required")
		}
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("duplicate stable partner id: %s", id)
		}
		seenIDs[id] = struct{}{}
		apiKey := strings.TrimSpace(partner.APIKey)
		if apiKey == "" {
			return fmt.Errorf("stable partner api_key required")
		}
		if _, exists := seenKeys[apiKey]; exists {
			return fmt.Errorf("duplicate stable partner api_key: %s", apiKey)
		}
		seenKeys[apiKey] = struct{}{}
		secret := strings.TrimSpace(partner.Secret)
		if secret == "" {
			return fmt.Errorf("stable partner secret required")
		}
		if rail := strings.TrimSpace(partner.SettlementRail); rail != "" {
			if !validSettlementRail(rail) {
				return fmt.Errorf("stable partner %s settlement_rail must be %q or %q", id, "nowpayments", "manual_treasury")
			}
			if rail == "nowpayments" {
				usesNowPayments = true
			}
		}
	}
	if usesNowPayments {
		now := cfg.Stable.Settlement.NowPayments
		if now.Email == "" || now.Password == "" || now.APIKey == "" {
			return fmt.Errorf("stable settlement nowpayments email, password, and api_key are required when the nowpayments rail is used")
		}
	}
	if cfg.Admin.TLS.Disable {
		return fmt.Errorf("stable runtime requires admin TLS to be enabled")
	}
	return nil
}

// validatePriceProof validates the optional price-proof signing endpoint's
// configuration. It is a no-op when the feature is disabled -- the whole
// point is that a deployment can leave this off (the current production
// default: nothing signs price proofs yet) without any of these fields
// being required.
func validatePriceProof(cfg PriceProofConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.Provider) == "" {
		return fmt.Errorf("price_proof.provider is required when price_proof is enabled")
	}
	if len(cfg.Pairs) == 0 {
		return fmt.Errorf("price_proof.pairs must include at least one pair when price_proof is enabled")
	}
	for _, pair := range cfg.Pairs {
		trimmed := strings.TrimSpace(pair)
		if trimmed == "" || !strings.Contains(trimmed, "/") {
			return fmt.Errorf("price_proof.pairs entries must be BASE/QUOTE, got %q", pair)
		}
	}
	switch cfg.Signer.NormalizedType() {
	case PriceProofSignerTypeHSM:
		if strings.TrimSpace(cfg.Signer.BaseURL) == "" {
			return fmt.Errorf("price_proof.signer.base_url is required when price_proof is enabled")
		}
		if strings.TrimSpace(cfg.Signer.KeyLabel) == "" {
			return fmt.Errorf("price_proof.signer.key_label is required when price_proof is enabled")
		}
		if strings.TrimSpace(cfg.Signer.CACertPath) == "" {
			return fmt.Errorf("price_proof.signer.ca_cert is required when price_proof is enabled")
		}
		if strings.TrimSpace(cfg.Signer.ClientCertPath) == "" {
			return fmt.Errorf("price_proof.signer.client_cert is required when price_proof is enabled")
		}
		if strings.TrimSpace(cfg.Signer.ClientKeyPath) == "" {
			return fmt.Errorf("price_proof.signer.client_key is required when price_proof is enabled")
		}
	case PriceProofSignerTypeLocal:
		if strings.TrimSpace(cfg.Signer.KeystorePath) == "" {
			return fmt.Errorf("price_proof.signer.keystore_path is required when price_proof.signer.type is %q", PriceProofSignerTypeLocal)
		}
		if strings.TrimSpace(cfg.Signer.PassphraseEnv) == "" {
			return fmt.Errorf("price_proof.signer.passphrase_env is required when price_proof.signer.type is %q", PriceProofSignerTypeLocal)
		}
	default:
		return fmt.Errorf("price_proof.signer.type must be %q or %q, got %q", PriceProofSignerTypeHSM, PriceProofSignerTypeLocal, cfg.Signer.Type)
	}
	if len(cfg.Partners) == 0 {
		return fmt.Errorf("price_proof.partners must be configured when price_proof is enabled")
	}
	seenIDs := make(map[string]struct{}, len(cfg.Partners))
	seenKeys := make(map[string]struct{}, len(cfg.Partners))
	for _, partner := range cfg.Partners {
		id := strings.TrimSpace(partner.ID)
		if id == "" {
			return fmt.Errorf("price_proof partner id required")
		}
		if _, exists := seenIDs[id]; exists {
			return fmt.Errorf("duplicate price_proof partner id: %s", id)
		}
		seenIDs[id] = struct{}{}
		apiKey := strings.TrimSpace(partner.APIKey)
		if apiKey == "" {
			return fmt.Errorf("price_proof partner api_key required")
		}
		if _, exists := seenKeys[apiKey]; exists {
			return fmt.Errorf("duplicate price_proof partner api_key: %s", apiKey)
		}
		seenKeys[apiKey] = struct{}{}
		if strings.TrimSpace(partner.Secret) == "" {
			return fmt.Errorf("price_proof partner secret required")
		}
	}
	return nil
}

// validSettlementRail accepts empty (unset -- resolves to manual_treasury at
// runtime, mirroring settlement.Config.RailFor) alongside the two explicit
// rail names, so validate() behaves the same whether or not applyDefaults
// already ran.
func validSettlementRail(rail string) bool {
	switch strings.TrimSpace(rail) {
	case "", "nowpayments", "manual_treasury":
		return true
	default:
		return false
	}
}

func (a *AdminConfig) normalise(allowInsecureBearerWithoutTLS bool) error {
	if a == nil {
		return fmt.Errorf("admin configuration missing")
	}
	token := strings.TrimSpace(a.BearerToken)
	if path := strings.TrimSpace(a.BearerTokenFile); path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read bearer_token_file: %w", err)
		}
		token = strings.TrimSpace(string(contents))
	}
	a.BearerToken = token

	a.MTLS.ClientCAPath = strings.TrimSpace(a.MTLS.ClientCAPath)
	a.TLS.CertPath = strings.TrimSpace(a.TLS.CertPath)
	a.TLS.KeyPath = strings.TrimSpace(a.TLS.KeyPath)

	if a.TLS.CertPath == "" && a.TLS.KeyPath == "" {
		a.TLS.Disable = true
	}
	if a.TLS.Disable && token != "" && !allowInsecureBearerWithoutTLS {
		return fmt.Errorf("admin bearer_token requires TLS to be enabled")
	}
	if !a.TLS.Disable {
		if a.TLS.CertPath == "" {
			return fmt.Errorf("tls.cert must be configured when TLS is enabled")
		}
		if a.TLS.KeyPath == "" {
			return fmt.Errorf("tls.key must be configured when TLS is enabled")
		}
	}
	if a.MTLS.Enabled && a.MTLS.ClientCAPath == "" {
		return fmt.Errorf("mtls.client_ca must be configured when mTLS is enabled")
	}
	if a.MTLS.Enabled && a.TLS.Disable {
		return fmt.Errorf("mTLS requires TLS to be enabled")
	}
	if a.BearerToken == "" && !a.MTLS.Enabled {
		return fmt.Errorf("configure either bearer_token or mTLS for admin authentication")
	}
	return nil
}
