package penalty

import (
	"errors"
	"math/big"
	"sort"

	"nhbchain/consensus/potso/evidence"
)

type Severity string

type Rule struct {
	Type     evidence.Type
	Severity Severity
	Cooldown uint64
	Compute  PenaltyFunc
}

type PenaltyFunc func(Metadata) (Penalty, error)

type Metadata struct {
	MissedEpochs  uint64
	BaseWeight    *big.Int
	CurrentWeight *big.Int
}

type Penalty struct {
	DecayAmount *big.Int
	DecayBps    uint64
	SlashAmount *big.Int
	SlashBps    uint64
}

type Config struct {
	EquivocationThetaBps    uint64
	EquivocationMinDecay    *big.Int
	EquivocationSlashBps    uint64
	EquivocationCooldown    uint64
	DowntimeLadder          []DowntimeStep
	DowntimeCooldown        uint64
	InvalidProposalDecay    uint64
	InvalidProposalCooldown uint64
	SlashEnabled            bool
}

type DowntimeStep struct {
	Missed   uint64
	DecayBps uint64
}

const (
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
	bpsDenominator   uint64   = 10000
)

func DefaultConfig() Config {
	return Config{
		EquivocationThetaBps: 5000,
		EquivocationMinDecay: big.NewInt(100),
		EquivocationSlashBps: 0,
		EquivocationCooldown: 7,
		DowntimeLadder: []DowntimeStep{
			{Missed: 1, DecayBps: 200},
			{Missed: 2, DecayBps: 500},
			{Missed: 3, DecayBps: 1000},
		},
		DowntimeCooldown:        1,
		InvalidProposalDecay:    300,
		InvalidProposalCooldown: 1,
		SlashEnabled:            false,
	}
}

type Catalog struct {
	rules map[evidence.Type]Rule
}

func BuildCatalog(cfg Config) (*Catalog, error) {
	if cfg.EquivocationThetaBps > bpsDenominator {
		return nil, errors.New("penalty: equivocation theta exceeds 100%")
	}
	ladder := append([]DowntimeStep(nil), cfg.DowntimeLadder...)
	sort.Slice(ladder, func(i, j int) bool { return ladder[i].Missed < ladder[j].Missed })
	rules := map[evidence.Type]Rule{}
	rules[evidence.TypeEquivocation] = Rule{
		Type:     evidence.TypeEquivocation,
		Severity: SeverityCritical,
		Cooldown: cfg.EquivocationCooldown,
		Compute: func(meta Metadata) (Penalty, error) {
			base := copyOrZero(meta.BaseWeight)
			current := copyOrZero(meta.CurrentWeight)
			theta := new(big.Int).Mul(base, big.NewInt(int64(cfg.EquivocationThetaBps)))
			theta.Div(theta, big.NewInt(int64(bpsDenominator)))
			minDecay := copyOrZero(cfg.EquivocationMinDecay)
			if theta.Cmp(minDecay) < 0 {
				theta.Set(minDecay)
			}
			if theta.Cmp(current) > 0 {
				theta.Set(current)
			}
			slash := big.NewInt(0)
			slashBps := uint64(0)
			if cfg.SlashEnabled && cfg.EquivocationSlashBps > 0 {
				slashBps = cfg.EquivocationSlashBps
				slash = scaleByBps(base, slashBps)
			}
			return Penalty{
				DecayAmount: theta,
				DecayBps:    cfg.EquivocationThetaBps,
				SlashAmount: slash,
				SlashBps:    slashBps,
			}, nil
		},
	}
	rules[evidence.TypeDowntime] = Rule{
		Type:     evidence.TypeDowntime,
		Severity: SeverityMedium,
		Cooldown: cfg.DowntimeCooldown,
		Compute: func(meta Metadata) (Penalty, error) {
			current := copyOrZero(meta.CurrentWeight)
			decayBps := ladderDecay(ladder, meta.MissedEpochs)
			if decayBps == 0 {
				return Penalty{DecayAmount: big.NewInt(0)}, nil
			}
			amount := scaleByBps(current, decayBps)
			return Penalty{DecayAmount: amount, DecayBps: decayBps}, nil
		},
	}
	rules[evidence.TypeInvalidBlockProposal] = Rule{
		Type:     evidence.TypeInvalidBlockProposal,
		Severity: SeverityHigh,
		Cooldown: cfg.InvalidProposalCooldown,
		Compute: func(meta Metadata) (Penalty, error) {
			current := copyOrZero(meta.CurrentWeight)
			amount := scaleByBps(current, cfg.InvalidProposalDecay)
			return Penalty{DecayAmount: amount, DecayBps: cfg.InvalidProposalDecay}, nil
		},
	}
	return &Catalog{rules: rules}, nil
}

func (c *Catalog) Rule(typ evidence.Type) (Rule, bool) {
	if c == nil {
		return Rule{}, false
	}
	rule, ok := c.rules[typ]
	return rule, ok
}

func ladderDecay(steps []DowntimeStep, missed uint64) uint64 {
	decay := uint64(0)
	for _, step := range steps {
		if missed >= step.Missed && step.DecayBps > decay {
			decay = step.DecayBps
		}
	}
	if decay > bpsDenominator {
		decay = bpsDenominator
	}
	return decay
}

func copyOrZero(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func scaleByBps(value *big.Int, bps uint64) *big.Int {
	if value == nil || value.Sign() <= 0 || bps == 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Int).Mul(value, big.NewInt(int64(bps)))
	scaled.Div(scaled, big.NewInt(int64(bpsDenominator)))
	return scaled
}
