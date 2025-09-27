package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to support YAML unmarshalling.
type Duration struct {
	time.Duration
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
	ListenAddress string       `yaml:"listen"`
	DatabasePath  string       `yaml:"database"`
	Oracle        OracleConfig `yaml:"oracle"`
	Sources       []Source     `yaml:"sources"`
	Pairs         []Pair       `yaml:"pairs"`
	Policy        PolicyConfig `yaml:"policy"`
}

// OracleConfig tunes the aggregation loop.
type OracleConfig struct {
	Interval Duration `yaml:"interval"`
	MaxAge   Duration `yaml:"max_age"`
	MinFeeds int      `yaml:"min_feeds"`
}

// Source describes an upstream oracle feed.
type Source struct {
	Name     string            `yaml:"name"`
	Type     string            `yaml:"type"`
	Endpoint string            `yaml:"endpoint"`
	APIKey   string            `yaml:"api_key"`
	Assets   map[string]string `yaml:"assets"`
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

// Load reads configuration from the supplied path.
func Load(path string) (Config, error) {
	cfg := Config{}
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
	if err := validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
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
}

func validate(cfg Config) error {
	if len(cfg.Pairs) == 0 {
		return fmt.Errorf("at least one pair must be configured")
	}
	if len(cfg.Sources) == 0 {
		return fmt.Errorf("at least one oracle source must be configured")
	}
	return nil
}
