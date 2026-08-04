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
	DatabasePath string       `yaml:"database"`
	Oracle       OracleConfig `yaml:"oracle"`
	Sources      []Source     `yaml:"sources"`
	Pairs        []Pair       `yaml:"pairs"`
	Policy       PolicyConfig `yaml:"policy"`
	Stable       StableConfig `yaml:"stable"`
	Admin        AdminConfig  `yaml:"admin"`
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
}

func validate(cfg Config) error {
	if len(cfg.Pairs) == 0 {
		return fmt.Errorf("at least one pair must be configured")
	}
	if len(cfg.Sources) == 0 {
		return fmt.Errorf("at least one oracle source must be configured")
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
