package compat

import (
	"embed"
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var (
	//go:embed deprecations.yaml
	deprecationFS embed.FS

	planOnce sync.Once
	plan     DeprecationPlan
	planErr  error
)

// DeprecationPlan captures the staged shutdown of the JSON-RPC compatibility layer.
type DeprecationPlan struct {
	Series       string             `yaml:"series"`
	Link         string             `yaml:"link"`
	Flag         DeprecationFlag    `yaml:"flag"`
	CurrentPhase string             `yaml:"currentPhase"`
	Phases       []DeprecationPhase `yaml:"phases"`
}

// DeprecationFlag defines the knobs exposed to operators.
type DeprecationFlag struct {
	CLI string `yaml:"cli"`
	Env string `yaml:"env"`
}

// DeprecationPhase holds the metadata for a single phase of the rollout.
type DeprecationPhase struct {
	Key           string   `yaml:"key"`
	Label         string   `yaml:"label"`
	Offset        string   `yaml:"offset"`
	DefaultCompat string   `yaml:"defaultCompat"`
	FlagBehavior  string   `yaml:"flagBehavior"`
	Banner        string   `yaml:"banner"`
	Actions       []string `yaml:"actions"`
}

// DeprecationNotice is surfaced on every compatibility response.
type DeprecationNotice struct {
	Phase   string
	Warning string
	Link    string
}

// Mode represents the compatibility switch state.
type Mode string

const (
	// ModeAuto defers to the active deprecation phase for behaviour.
	ModeAuto Mode = "auto"
	// ModeEnabled forces the compatibility dispatcher on.
	ModeEnabled Mode = "enabled"
	// ModeDisabled turns the compatibility dispatcher off.
	ModeDisabled Mode = "disabled"
)

// DefaultNotice returns the banner for the active deprecation phase.
func DefaultNotice() DeprecationNotice {
	plan := loadPlan()
	phase := plan.activePhase()
	notice := DeprecationNotice{
		Phase:   phase.Label,
		Warning: phase.Banner,
		Link:    plan.Link,
	}
	if notice.Phase == "" {
		notice.Phase = "Compatibility Sunset"
	}
	if notice.Warning == "" {
		notice.Warning = "Monolithic JSON-RPC compatibility is deprecated. Review the migration timeline."
	}
	if notice.Link == "" {
		notice.Link = "https://docs.nhbchain.net/migrate/deprecation-timeline"
	}
	return notice
}

// DefaultMode resolves to the compatibility mode dictated by the current phase.
func DefaultMode() Mode {
	phase := loadPlan().activePhase()
	switch strings.ToLower(strings.TrimSpace(phase.DefaultCompat)) {
	case string(ModeEnabled):
		return ModeEnabled
	case string(ModeDisabled):
		return ModeDisabled
	case "removed":
		return ModeDisabled
	default:
		return ModeEnabled
	}
}

// ShouldEnable resolves whether the dispatcher should be active for the provided mode.
func ShouldEnable(mode Mode) bool {
	switch mode {
	case ModeEnabled:
		return true
	case ModeDisabled:
		return false
	default:
		return DefaultMode() == ModeEnabled
	}
}

// ParseMode validates CLI/env-provided values.
func ParseMode(value string) (Mode, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", string(ModeAuto):
		return ModeAuto, nil
	case string(ModeEnabled):
		return ModeEnabled, nil
	case string(ModeDisabled):
		return ModeDisabled, nil
	default:
		return ModeAuto, fmt.Errorf("unknown compatibility mode %q (expected enabled, disabled, or auto)", value)
	}
}

// Plan exposes the parsed deprecation plan for documentation tooling.
func Plan() (DeprecationPlan, error) {
	plan := loadPlan()
	return plan, planErr
}

func (p DeprecationPlan) activePhase() DeprecationPhase {
	if len(p.Phases) == 0 {
		return DeprecationPhase{}
	}
	key := strings.TrimSpace(strings.ToLower(p.CurrentPhase))
	if key != "" {
		for _, phase := range p.Phases {
			if strings.ToLower(phase.Key) == key {
				return phase
			}
		}
	}
	return p.Phases[0]
}

func loadPlan() DeprecationPlan {
	planOnce.Do(func() {
		raw, err := deprecationFS.ReadFile("deprecations.yaml")
		if err != nil {
			planErr = fmt.Errorf("read deprecation plan: %w", err)
			return
		}
		var parsed DeprecationPlan
		if err := yaml.Unmarshal(raw, &parsed); err != nil {
			planErr = fmt.Errorf("decode deprecation plan: %w", err)
			return
		}
		plan = parsed
	})
	return plan
}
