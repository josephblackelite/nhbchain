package emissions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	basisPoints uint32 = 10_000
)

type Schedule struct {
	entries []scheduleEntry
}

type scheduleEntry struct {
	startEpoch uint64
	amount     *big.Int
	decay      *decaySchedule
}

type decaySchedule struct {
	mode     decayMode
	ratioBps uint32
	duration uint64
	floor    *big.Int
}

type decayMode string

const (
	decayModeNone      decayMode = "none"
	decayModeGeometric decayMode = "geometric"
)

type fileSchedule struct {
	Entries []fileEntry `json:"entries" toml:"entries"`
}

type fileEntry struct {
	StartEpoch uint64     `json:"startEpoch" toml:"startEpoch"`
	Amount     string     `json:"amount" toml:"amount"`
	Decay      *fileDecay `json:"decay" toml:"decay"`
}

type fileDecay struct {
	Mode     string `json:"mode" toml:"mode"`
	RatioBps uint32 `json:"ratioBps" toml:"ratioBps"`
	Duration uint64 `json:"durationEpochs" toml:"durationEpochs"`
	Floor    string `json:"floor" toml:"floor"`
}

func LoadSchedule(path string) (*Schedule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("emissions: schedule path required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("emissions: read schedule: %w", err)
	}
	var parsed fileSchedule
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&parsed); err != nil {
			return nil, fmt.Errorf("emissions: decode schedule json: %w", err)
		}
	case ".toml", ".tml":
		meta, err := toml.DecodeReader(bytes.NewReader(data), &parsed)
		if err != nil {
			return nil, fmt.Errorf("emissions: decode schedule toml: %w", err)
		}
		if undecoded := meta.Undecoded(); len(undecoded) > 0 {
			return nil, fmt.Errorf("emissions: unknown schedule fields %v", undecoded)
		}
	default:
		return nil, fmt.Errorf("emissions: unsupported schedule format %q", ext)
	}
	if len(parsed.Entries) == 0 {
		return nil, errors.New("emissions: schedule requires at least one entry")
	}
	entries := make([]scheduleEntry, len(parsed.Entries))
	for i := range parsed.Entries {
		entry := parsed.Entries[i]
		if entry.StartEpoch == 0 {
			return nil, fmt.Errorf("emissions: entry %d startEpoch must be greater than zero", i)
		}
		amount, ok := new(big.Int).SetString(strings.TrimSpace(entry.Amount), 10)
		if !ok {
			return nil, fmt.Errorf("emissions: entry %d amount invalid", i)
		}
		if amount.Sign() < 0 {
			return nil, fmt.Errorf("emissions: entry %d amount cannot be negative", i)
		}
		var decay *decaySchedule
		if entry.Decay != nil {
			parsedDecay, err := parseDecay(entry.Decay, i)
			if err != nil {
				return nil, err
			}
			decay = parsedDecay
		}
		entries[i] = scheduleEntry{startEpoch: entry.StartEpoch, amount: amount, decay: decay}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].startEpoch < entries[j].startEpoch
	})
	for i := 1; i < len(entries); i++ {
		if entries[i].startEpoch == entries[i-1].startEpoch {
			return nil, fmt.Errorf("emissions: duplicate startEpoch %d", entries[i].startEpoch)
		}
	}
	return &Schedule{entries: entries}, nil
}

func parseDecay(spec *fileDecay, index int) (*decaySchedule, error) {
	mode := decayMode(strings.ToLower(strings.TrimSpace(spec.Mode)))
	switch mode {
	case "", decayModeNone:
		return nil, nil
	case decayModeGeometric:
	default:
		return nil, fmt.Errorf("emissions: entry %d decay mode %q unsupported", index, spec.Mode)
	}
	if spec.RatioBps == 0 {
		return nil, fmt.Errorf("emissions: entry %d decay ratioBps must be greater than zero", index)
	}
	if spec.RatioBps > basisPoints {
		return nil, fmt.Errorf("emissions: entry %d decay ratioBps cannot exceed %d", index, basisPoints)
	}
	var floor *big.Int
	if strings.TrimSpace(spec.Floor) != "" {
		value, ok := new(big.Int).SetString(strings.TrimSpace(spec.Floor), 10)
		if !ok {
			return nil, fmt.Errorf("emissions: entry %d decay floor invalid", index)
		}
		if value.Sign() < 0 {
			return nil, fmt.Errorf("emissions: entry %d decay floor cannot be negative", index)
		}
		floor = value
	}
	return &decaySchedule{
		mode:     decayModeGeometric,
		ratioBps: spec.RatioBps,
		duration: spec.Duration,
		floor:    floor,
	}, nil
}

func (s *Schedule) AmountForEpoch(epoch uint64) *big.Int {
	if s == nil || epoch == 0 {
		return big.NewInt(0)
	}
	entry := s.lookup(epoch)
	if entry == nil {
		return big.NewInt(0)
	}
	amount := new(big.Int).Set(entry.amount)
	if entry.decay == nil || epoch == entry.startEpoch {
		return amount
	}
	switch entry.decay.mode {
	case decayModeGeometric:
		steps := epoch - entry.startEpoch
		if entry.decay.duration > 0 && steps > entry.decay.duration {
			steps = entry.decay.duration
		}
		result := applyGeometricDecay(amount, entry.decay.ratioBps, steps)
		if entry.decay.floor != nil && result.Cmp(entry.decay.floor) < 0 {
			result.Set(entry.decay.floor)
		}
		return result
	default:
		return amount
	}
}

func (s *Schedule) lookup(epoch uint64) *scheduleEntry {
	if len(s.entries) == 0 {
		return nil
	}
	idx := sort.Search(len(s.entries), func(i int) bool {
		return s.entries[i].startEpoch > epoch
	})
	if idx == 0 {
		return nil
	}
	return &s.entries[idx-1]
}

func applyGeometricDecay(base *big.Int, ratioBps uint32, steps uint64) *big.Int {
	if steps == 0 {
		return new(big.Int).Set(base)
	}
	ratio := new(big.Rat).SetFrac(big.NewInt(int64(ratioBps)), big.NewInt(int64(basisPoints)))
	value := new(big.Rat).SetInt(base)
	factor := powRat(ratio, steps)
	value.Mul(value, factor)
	result := new(big.Int).Quo(value.Num(), value.Denom())
	if result.Sign() < 0 {
		result.SetInt64(0)
	}
	return result
}

func powRat(r *big.Rat, exp uint64) *big.Rat {
	result := new(big.Rat).SetInt64(1)
	if exp == 0 {
		return result
	}
	base := new(big.Rat).Set(r)
	for exp > 0 {
		if exp&1 == 1 {
			result.Mul(result, base)
		}
		exp >>= 1
		if exp > 0 {
			base.Mul(base, base)
		}
	}
	return result
}
