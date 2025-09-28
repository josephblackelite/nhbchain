package swap

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"nhbchain/crypto"

	"github.com/ethereum/go-ethereum/rlp"
)

// SanctionsConfig describes how the sanctions checker should behave.
type SanctionsConfig struct {
	DenyList []string `toml:"DenyList"`
}

// Normalise trims whitespace, removes duplicates, and applies canonical casing.
func (cfg SanctionsConfig) Normalise() SanctionsConfig {
	if len(cfg.DenyList) == 0 {
		return SanctionsConfig{}
	}
	trimmed := make([]string, 0, len(cfg.DenyList))
	seen := make(map[string]struct{}, len(cfg.DenyList))
	for _, raw := range cfg.DenyList {
		normalized := strings.ToLower(strings.TrimSpace(raw))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		trimmed = append(trimmed, normalized)
	}
	sort.Strings(trimmed)
	return SanctionsConfig{DenyList: trimmed}
}

// SanctionsParameters captures the parsed runtime configuration for sanctions enforcement.
type SanctionsParameters struct {
	Denied [][20]byte
}

// Parameters converts the configuration into runtime parameters.
func (cfg SanctionsConfig) Parameters() (SanctionsParameters, error) {
	normalized := cfg.Normalise()
	params := SanctionsParameters{}
	if len(normalized.DenyList) == 0 {
		return params, nil
	}
	denied := make([][20]byte, 0, len(normalized.DenyList))
	for _, entry := range normalized.DenyList {
		decoded, err := crypto.DecodeAddress(entry)
		if err != nil {
			return params, fmt.Errorf("sanctions: decode deny list entry %q: %w", entry, err)
		}
		bytes := decoded.Bytes()
		if len(bytes) != 20 {
			return params, fmt.Errorf("sanctions: deny list entry %q must be 20 bytes", entry)
		}
		var addr [20]byte
		copy(addr[:], bytes)
		denied = append(denied, addr)
	}
	params.Denied = denied
	return params, nil
}

// Checker returns a sanctions checker implementation honouring the configured deny list.
func (params SanctionsParameters) Checker() SanctionsChecker {
	if len(params.Denied) == 0 {
		return DefaultSanctionsChecker
	}
	blocked := make(map[[20]byte]struct{}, len(params.Denied))
	for _, addr := range params.Denied {
		blocked[addr] = struct{}{}
	}
	return func(addr [20]byte) bool {
		_, denied := blocked[addr]
		return !denied
	}
}

// SanctionsFailure captures a persisted sanction failure for audit trails.
type SanctionsFailure struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
	Timestamp    int64
}

type sanctionAuditEntry struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
	Timestamp    uint64
}

// SanctionsLog records sanction failures for later inspection.
type SanctionsLog struct {
	store Storage
	clock func() time.Time
}

// NewSanctionsLog constructs a sanctions log backed by the provided storage adapter.
func NewSanctionsLog(store Storage) *SanctionsLog {
	return &SanctionsLog{store: store, clock: time.Now}
}

// SetClock overrides the time source, primarily for deterministic tests.
func (sl *SanctionsLog) SetClock(clock func() time.Time) {
	if sl == nil || clock == nil {
		return
	}
	sl.clock = clock
}

// RecordFailure appends a new sanctions failure entry for the provided address.
func (sl *SanctionsLog) RecordFailure(addr [20]byte, provider, providerTxID string) error {
	if sl == nil {
		return fmt.Errorf("sanctions log not initialised")
	}
	if sl.store == nil {
		return fmt.Errorf("sanctions log storage unavailable")
	}
	now := sl.clock().UTC()
	entry := sanctionAuditEntry{Address: addr, Provider: strings.TrimSpace(provider), ProviderTxID: strings.TrimSpace(providerTxID)}
	if nowUnix := now.Unix(); nowUnix > 0 {
		entry.Timestamp = uint64(nowUnix)
	}
	encoded, err := rlp.EncodeToBytes(entry)
	if err != nil {
		return err
	}
	return sl.store.KVAppend(sanctionAuditKey(addr), encoded)
}

// Failures returns a copy of the persisted sanctions failures for the provided address.
func (sl *SanctionsLog) Failures(addr [20]byte) ([]SanctionsFailure, error) {
	if sl == nil {
		return nil, fmt.Errorf("sanctions log not initialised")
	}
	if sl.store == nil {
		return nil, fmt.Errorf("sanctions log storage unavailable")
	}
	var raw [][]byte
	if err := sl.store.KVGetList(sanctionAuditKey(addr), &raw); err != nil {
		return nil, err
	}
	failures := make([]SanctionsFailure, 0, len(raw))
	for _, blob := range raw {
		var entry sanctionAuditEntry
		if err := rlp.DecodeBytes(blob, &entry); err != nil {
			return nil, err
		}
		failure := SanctionsFailure{Address: entry.Address, Provider: entry.Provider, ProviderTxID: entry.ProviderTxID}
		if entry.Timestamp > 0 {
			failure.Timestamp = int64(entry.Timestamp)
		}
		failures = append(failures, failure)
	}
	return failures, nil
}

func sanctionAuditKey(addr [20]byte) []byte {
	suffix := fmt.Sprintf("%x", addr)
	key := make([]byte, len(sanctionsAuditPrefix)+len(suffix))
	copy(key, sanctionsAuditPrefix)
	copy(key[len(sanctionsAuditPrefix):], suffix)
	return key
}

var sanctionsAuditPrefix = []byte("swap/sanctions/audit/")
