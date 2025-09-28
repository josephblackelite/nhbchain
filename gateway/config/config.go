package config

import (
	"fmt"
	"net/url"
	"os"
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
}

type AuthConfig struct {
	Enabled        bool     `yaml:"enabled"`
	HMACSecret     string   `yaml:"hmacSecret"`
	Issuer         string   `yaml:"issuer"`
	Audience       string   `yaml:"audience"`
	ScopeClaim     string   `yaml:"scopeClaim"`
	OptionalPaths  []string `yaml:"optionalPaths"`
	AllowAnonymous bool     `yaml:"allowAnonymous"`
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
			Enabled:        false,
			ScopeClaim:     "scope",
			AllowAnonymous: true,
		},
	}
	if path == "" {
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
	return cfg, nil
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
