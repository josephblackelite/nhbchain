package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ServiceConfig struct {
	Name               string        `yaml:"name"`
	Endpoint           string        `yaml:"endpoint"`
	Timeout            time.Duration `yaml:"timeout"`
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"`
}

type RateLimitConfig struct {
	ID                string   `yaml:"id"`
	RequestsPerMinute float64  `yaml:"requestsPerMinute"`
	RatePerSecond     float64  `yaml:"ratePerSecond"`
	Burst             int      `yaml:"burst"`
	Paths             []string `yaml:"paths"`
}

type ObservabilityConfig struct {
	ServiceName   string `yaml:"serviceName"`
	Metrics       bool   `yaml:"metrics"`
	Tracing       bool   `yaml:"tracing"`
	LogRequests   bool   `yaml:"logRequests"`
	MetricsPrefix string `yaml:"metricsPrefix"`
}

type Config struct {
	ListenAddress string              `yaml:"listen"`
	ReadTimeout   time.Duration       `yaml:"readTimeout"`
	WriteTimeout  time.Duration       `yaml:"writeTimeout"`
	IdleTimeout   time.Duration       `yaml:"idleTimeout"`
	Services      []ServiceConfig     `yaml:"services"`
	RateLimits    []RateLimitConfig   `yaml:"rateLimits"`
	Observability ObservabilityConfig `yaml:"observability"`
	Auth          AuthConfig          `yaml:"auth"`
	Security      SecurityConfig      `yaml:"security"`
}

type AuthConfig struct {
	Enabled           bool          `yaml:"enabled"`
	HMACSecret        string        `yaml:"hmacSecret"`
	Issuer            string        `yaml:"issuer"`
	Audience          string        `yaml:"audience"`
	ScopeClaim        string        `yaml:"scopeClaim"`
	OptionalPaths     []string      `yaml:"optionalPaths"`
	AllowAnonymous    bool          `yaml:"allowAnonymous"`
	ClockSkew         time.Duration `yaml:"clockSkew"`
	allowAnonymousSet bool          `yaml:"-"`
	enabledSet        bool          `yaml:"-"`
}

func (a *AuthConfig) UnmarshalYAML(node *yaml.Node) error {
	type rawAuthConfig struct {
		Enabled        *bool         `yaml:"enabled"`
		HMACSecret     string        `yaml:"hmacSecret"`
		Issuer         string        `yaml:"issuer"`
		Audience       string        `yaml:"audience"`
		ScopeClaim     string        `yaml:"scopeClaim"`
		OptionalPaths  []string      `yaml:"optionalPaths"`
		AllowAnonymous *bool         `yaml:"allowAnonymous"`
		ClockSkew      time.Duration `yaml:"clockSkew"`
	}
	var raw rawAuthConfig
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.Enabled != nil {
		a.Enabled = *raw.Enabled
		a.enabledSet = true
	} else {
		a.Enabled = false
		a.enabledSet = false
	}
	a.HMACSecret = raw.HMACSecret
	a.Issuer = raw.Issuer
	a.Audience = raw.Audience
	a.ScopeClaim = raw.ScopeClaim
	a.OptionalPaths = raw.OptionalPaths
	if raw.AllowAnonymous != nil {
		a.AllowAnonymous = *raw.AllowAnonymous
		a.allowAnonymousSet = true
	} else {
		a.AllowAnonymous = false
		a.allowAnonymousSet = false
	}
	a.ClockSkew = raw.ClockSkew
	return nil
}

type SecurityConfig struct {
	AutoUpgradeHTTP bool   `yaml:"autoUpgradeHTTP"`
	AllowInsecure   bool   `yaml:"allowInsecure"`
	TLSCertFile     string `yaml:"tlsCertFile"`
	TLSKeyFile      string `yaml:"tlsKeyFile"`
	TLSClientCAFile string `yaml:"tlsClientCAFile"`
}

func Load(path string) (Config, error) {
	cfg := Config{
		ListenAddress: ":8080",
		ReadTimeout:   30 * time.Second,
		WriteTimeout:  30 * time.Second,
		IdleTimeout:   120 * time.Second,
		Observability: ObservabilityConfig{
			ServiceName:   "nhb-gateway",
			Metrics:       true,
			Tracing:       true,
			LogRequests:   true,
			MetricsPrefix: "gateway",
		},
		Auth: AuthConfig{
			Enabled:        true,
			ScopeClaim:     "scope",
			AllowAnonymous: false,
			ClockSkew:      2 * time.Minute,
			enabledSet:     true,
		},
	}
	if path == "" {
		cfg.applyAuthDefaults()
		if err := cfg.Validate(); err != nil {
			return Config{}, fmt.Errorf("validate config: %w", err)
		}
		return cfg, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	cfg.applyAuthDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func (cfg *Config) applyAuthDefaults() {
	if cfg == nil {
		return
	}
	if !cfg.Auth.enabledSet {
		cfg.Auth.Enabled = true
		cfg.Auth.enabledSet = true
	}
	if cfg.Auth.ClockSkew <= 0 {
		cfg.Auth.ClockSkew = 2 * time.Minute
	}
	if cfg.Auth.ScopeClaim == "" {
		cfg.Auth.ScopeClaim = "scope"
	}
	if !cfg.Auth.allowAnonymousSet {
		cfg.Auth.AllowAnonymous = false
	}
}

var ErrAuthEnabledNotConfigured = errors.New("auth.enabled must be explicitly set for sensitive deployments")

func (cfg *Config) Validate() error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if cfg.isSensitiveDeployment() && !cfg.Auth.enabledSet {
		return ErrAuthEnabledNotConfigured
	}
	if cfg.Auth.AllowAnonymous && !cfg.Auth.allowAnonymousSet {
		return fmt.Errorf("auth.allowAnonymous must be explicitly set to true to enable anonymous access")
	}
	trimmed := make([]string, len(cfg.Auth.OptionalPaths))
	for i, path := range cfg.Auth.OptionalPaths {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" {
			return fmt.Errorf("auth.optionalPaths[%d] cannot be empty", i)
		}
		if !strings.HasPrefix(trimmedPath, "/") {
			return fmt.Errorf("auth.optionalPaths[%d] must start with '/'", i)
		}
		trimmed[i] = trimmedPath
	}
	cfg.Auth.OptionalPaths = trimmed
	if cfg.Auth.Enabled && cfg.Auth.AllowAnonymous && len(cfg.Auth.OptionalPaths) == 0 {
		return fmt.Errorf("auth.optionalPaths must list at least one entry when auth.allowAnonymous is true")
	}
	return nil
}

func (s ServiceConfig) URL() (*url.URL, error) {
	if s.Endpoint == "" {
		return nil, fmt.Errorf("endpoint missing for service %s", s.Name)
	}
	parsed, err := url.Parse(s.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse service %s endpoint: %w", s.Name, err)
	}
	return parsed, nil
}

func (cfg Config) ServiceByName(name string) (*ServiceConfig, error) {
	for _, svc := range cfg.Services {
		if svc.Name == name {
			return &svc, nil
		}
	}
	return nil, fmt.Errorf("service %s not configured", name)
}

func (cfg *Config) isSensitiveDeployment() bool {
	if cfg == nil {
		return false
	}
	if cfg.Security.AutoUpgradeHTTP {
		return true
	}
	if strings.TrimSpace(cfg.Security.TLSCertFile) != "" {
		return true
	}
	if strings.TrimSpace(cfg.Security.TLSKeyFile) != "" {
		return true
	}
	if strings.TrimSpace(cfg.Security.TLSClientCAFile) != "" {
		return true
	}
	return false
}

// EnforceSecureScheme ensures the supplied URL uses HTTPS outside of the dev environment.
// If autoUpgrade is enabled, insecure HTTP URLs are transparently upgraded to HTTPS.
// The returned boolean indicates whether an upgrade occurred.
func EnforceSecureScheme(env string, target *url.URL, autoUpgrade bool) (*url.URL, bool, error) {
	if target == nil {
		return nil, false, fmt.Errorf("target URL is nil")
	}
	scheme := strings.ToLower(strings.TrimSpace(target.Scheme))
	switch scheme {
	case "https":
		return target, false, nil
	case "http":
		if isDevEnv(env) {
			return target, false, nil
		}
		if autoUpgrade {
			upgraded := *target
			upgraded.Scheme = "https"
			return &upgraded, true, nil
		}
		if strings.TrimSpace(env) == "" {
			env = "(unset)"
		}
		return nil, false, fmt.Errorf("plaintext HTTP endpoints are not permitted for environment %s", env)
	case "":
		return nil, false, fmt.Errorf("URL scheme is required")
	default:
		return nil, false, fmt.Errorf("unsupported URL scheme %q", target.Scheme)
	}
}

func isDevEnv(env string) bool {
	return strings.EqualFold(strings.TrimSpace(env), "dev")
}
