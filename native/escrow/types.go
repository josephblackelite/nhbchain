package escrow

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/btcsuite/btcutil/bech32"
)

// ArbitrationScheme enumerates the supported strategies for evaluating an
// arbitrator allowlist. The scheme dictates how many signatures or votes are
// required from the configured members to resolve a dispute.
type ArbitrationScheme uint8

const (
	// ArbitrationSchemeUnspecified represents an unset scheme and should not
	// be persisted in state. It exists to provide a zero value for optional
	// fields during validation.
	ArbitrationSchemeUnspecified ArbitrationScheme = iota
	// ArbitrationSchemeSingle authorises a single, pre-determined arbitrator
	// to resolve disputes.
	ArbitrationSchemeSingle
	// ArbitrationSchemeCommittee requires a committee of arbitrators to meet
	// a threshold before a resolution is accepted. Thresholds are expressed
	// as the number of required signatures.
	ArbitrationSchemeCommittee
)

// Valid reports whether the arbitration scheme is supported by the runtime.
func (s ArbitrationScheme) Valid() bool {
	switch s {
	case ArbitrationSchemeSingle, ArbitrationSchemeCommittee:
		return true
	default:
		return false
	}
}

// ArbitratorSet defines the active allowlist of addresses and voting scheme a
// realm uses when freezing dispute policies into individual escrows.
type ArbitratorSet struct {
	Scheme    ArbitrationScheme
	Threshold uint32
	Members   [][20]byte
}

// Clone deep copies the arbitrator set allowing callers to mutate the result
// without affecting the original value.
func (s *ArbitratorSet) Clone() *ArbitratorSet {
	if s == nil {
		return nil
	}
	clone := &ArbitratorSet{
		Scheme:    s.Scheme,
		Threshold: s.Threshold,
	}
	if len(s.Members) > 0 {
		clone.Members = make([][20]byte, len(s.Members))
		copy(clone.Members, s.Members)
	}
	return clone
}

// SortedMembers returns a copy of the member addresses sorted lexicographically
// to provide stable ordering for event emission and hashing.
func (s *ArbitratorSet) SortedMembers() [][20]byte {
	if s == nil || len(s.Members) == 0 {
		return nil
	}
	out := make([][20]byte, len(s.Members))
	copy(out, s.Members)
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// EscrowRealm captures the arbitrator governance configuration for a group of
// escrows. Realms are versioned to allow governance to update allowlists while
// preserving deterministic frozen policies on existing cases.
type EscrowRealm struct {
	ID              string
	Version         uint64
	NextPolicyNonce uint64
	CreatedAt       int64
	UpdatedAt       int64
	Arbitrators     *ArbitratorSet
	FeeSchedule     *RealmFeeSchedule
	Metadata        *EscrowRealmMetadata
}

// Clone returns a deep copy of the realm definition.
func (r *EscrowRealm) Clone() *EscrowRealm {
	if r == nil {
		return nil
	}
	clone := *r
	clone.Arbitrators = r.Arbitrators.Clone()
	if r.FeeSchedule != nil {
		clone.FeeSchedule = r.FeeSchedule.Clone()
	if r.Metadata != nil {
		clone.Metadata = r.Metadata.Clone()
	}
	return &clone
}

// FrozenArb represents the immutable arbitrator policy captured at escrow
// creation time. It tracks the originating realm version and nonce so the policy
// can be audited even if the realm evolves.
type FrozenArb struct {
	RealmID      string
	RealmVersion uint64
	PolicyNonce  uint64
	Scheme       ArbitrationScheme
	Threshold    uint32
	Members      [][20]byte
	FrozenAt     int64
	FeeSchedule  *RealmFeeSchedule
	Metadata     *EscrowRealmMetadata
}

// Clone returns a deep copy of the frozen arbitrator policy.
func (f *FrozenArb) Clone() *FrozenArb {
	if f == nil {
		return nil
	}
	clone := &FrozenArb{
		RealmID:      f.RealmID,
		RealmVersion: f.RealmVersion,
		PolicyNonce:  f.PolicyNonce,
		Scheme:       f.Scheme,
		Threshold:    f.Threshold,
		FrozenAt:     f.FrozenAt,
	}
	if len(f.Members) > 0 {
		clone.Members = make([][20]byte, len(f.Members))
		copy(clone.Members, f.Members)
	}
	if f.FeeSchedule != nil {
		clone.FeeSchedule = f.FeeSchedule.Clone()
	if f.Metadata != nil {
		clone.Metadata = f.Metadata.Clone()
	}
	return clone
}

// EscrowRealmScope describes the intended usage scope for an escrow realm.
type EscrowRealmScope uint8

const (
	// EscrowRealmScopeUnspecified represents an unset scope.
	EscrowRealmScopeUnspecified EscrowRealmScope = iota
	// EscrowRealmScopePlatform denotes platform-wide governance managed by core teams.
	EscrowRealmScopePlatform
	// EscrowRealmScopeMarketplace denotes realms managed by marketplace operators.
	EscrowRealmScopeMarketplace
)

// Valid reports whether the realm scope is recognised by the runtime.
func (s EscrowRealmScope) Valid() bool {
	switch s {
	case EscrowRealmScopePlatform, EscrowRealmScopeMarketplace:
		return true
	default:
		return false
	}
}

// EscrowRealmMetadata captures provider context and fee routing for a realm.
type EscrowRealmMetadata struct {
	Scope              EscrowRealmScope
	ProviderProfile    string
	ArbitrationFeeBps  uint32
	FeeRecipientBech32 string
}

// Clone returns a deep copy of the metadata structure.
func (m *EscrowRealmMetadata) Clone() *EscrowRealmMetadata {
	if m == nil {
		return nil
	}
	clone := *m
	return &clone
}

const (
	// EscrowRealmMaxProviderProfileLength bounds the provider profile metadata.
	EscrowRealmMaxProviderProfileLength = 512
)

const (
	// DefaultRealmMinThreshold defines the lower bound for committee
	// thresholds when the governance parameter has not been initialised.
	DefaultRealmMinThreshold uint32 = 1
	// DefaultRealmMaxThreshold defines the upper bound for committee
	// thresholds when governance has not configured a value.
	DefaultRealmMaxThreshold uint32 = 10
)

var (
	// DefaultRealmAllowedSchemes lists the arbitration schemes enabled when
	// no governance configuration is present.
	DefaultRealmAllowedSchemes = []ArbitrationScheme{
		ArbitrationSchemeSingle,
		ArbitrationSchemeCommittee,
	}
)

// RealmFeeSchedule captures the arbitration fee routing rules for a realm.
type RealmFeeSchedule struct {
	FeeBps    uint32
	Recipient [20]byte
}

// Clone returns a copy safe for callers to mutate.
func (s *RealmFeeSchedule) Clone() *RealmFeeSchedule {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

const (
	// ParamKeyRealmMinThreshold controls the minimum allowed arbitration
	// threshold for realm policies.
	ParamKeyRealmMinThreshold = "escrow.realm.MinThreshold"
	// ParamKeyRealmMaxThreshold controls the maximum allowed arbitration
	// threshold for realm policies.
	ParamKeyRealmMaxThreshold = "escrow.realm.MaxThreshold"
	// ParamKeyRealmAllowedSchemes controls the arbitration schemes that may
	// be configured on a realm.
	ParamKeyRealmAllowedSchemes = "escrow.realm.AllowedSchemes"
)

// EscrowStatus represents the lifecycle states supported by the hardened
// escrow engine.
type EscrowStatus uint8

const (
	EscrowInit EscrowStatus = iota
	EscrowFunded
	EscrowReleased
	EscrowRefunded
	EscrowExpired
	EscrowDisputed
)

// DecisionOutcome enumerates the possible arbitration outcomes supported by the
// escrow engine.
type DecisionOutcome uint8

const (
	DecisionOutcomeUnknown DecisionOutcome = iota
	DecisionOutcomeRelease
	DecisionOutcomeRefund
)

// Valid reports whether the outcome represents a supported arbitration
// decision.
func (o DecisionOutcome) Valid() bool {
	switch o {
	case DecisionOutcomeRelease, DecisionOutcomeRefund:
		return true
	default:
		return false
	}
}

// String returns the canonical textual representation of the decision.
func (o DecisionOutcome) String() string {
	switch o {
	case DecisionOutcomeRelease:
		return "release"
	case DecisionOutcomeRefund:
		return "refund"
	default:
		return "unknown"
	}
}

// ParseDecisionOutcome normalises a textual decision string into the enum form.
func ParseDecisionOutcome(outcome string) (DecisionOutcome, error) {
	normalized := strings.ToLower(strings.TrimSpace(outcome))
	switch normalized {
	case "release":
		return DecisionOutcomeRelease, nil
	case "refund":
		return DecisionOutcomeRefund, nil
	default:
		return DecisionOutcomeUnknown, fmt.Errorf("escrow: invalid resolution outcome %s", outcome)
	}
}

// Escrow captures the immutable metadata and runtime status of a single escrow
// agreement managed by the native engine. The identifier is the keccak256 hash
// of the payer, payee and a caller-supplied nonce, ensuring deterministic IDs
// without storing the nonce on-chain.
type Escrow struct {
	ID             [32]byte
	Payer          [20]byte
	Payee          [20]byte
	Mediator       [20]byte
	Token          string
	Amount         *big.Int
	FeeBps         uint32
	Deadline       int64
	CreatedAt      int64
	Nonce          uint64
	MetaHash       [32]byte
	Status         EscrowStatus
	RealmID        string
	FrozenArb      *FrozenArb
	ResolutionHash [32]byte
}

// Clone returns a deep copy of the escrow object so callers can safely mutate
// the copy without affecting the stored instance.
func (e *Escrow) Clone() *Escrow {
	if e == nil {
		return nil
	}
	clone := *e
	if e.Amount != nil {
		clone.Amount = new(big.Int).Set(e.Amount)
	} else {
		clone.Amount = big.NewInt(0)
	}
	if e.FrozenArb != nil {
		clone.FrozenArb = e.FrozenArb.Clone()
	}
	return &clone
}

// Valid reports whether the status value is within the supported range.
func (s EscrowStatus) Valid() bool {
	switch s {
	case EscrowInit, EscrowFunded, EscrowReleased, EscrowRefunded, EscrowExpired, EscrowDisputed:
		return true
	default:
		return false
	}
}

type tokenRegistry struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
}

func newTokenRegistry(tokens ...string) *tokenRegistry {
	allowed := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		trimmed := strings.ToUpper(strings.TrimSpace(token))
		if trimmed == "" {
			continue
		}
		allowed[trimmed] = struct{}{}
	}
	return &tokenRegistry{allowed: allowed}
}

func (r *tokenRegistry) normalize(symbol string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("unsupported escrow token: %s", symbol)
	}
	trimmed := strings.ToUpper(strings.TrimSpace(symbol))
	if trimmed == "" {
		return "", fmt.Errorf("unsupported escrow token: %s", symbol)
	}
	r.mu.RLock()
	_, ok := r.allowed[trimmed]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unsupported escrow token: %s", symbol)
	}
	return trimmed, nil
}

var defaultTokenRegistry = newTokenRegistry("NHB", "ZNHB")

// NormalizeToken ensures the provided token symbol matches a supported value
// ("NHB" or "ZNHB") and returns the canonical uppercase form.
func NormalizeToken(symbol string) (string, error) {
	return defaultTokenRegistry.normalize(symbol)
}

// SanitizeEscrow validates and normalises the supplied escrow definition,
// returning a cloned instance with canonical token casing and a non-nil amount
// field. The function does not mutate the original value.
func SanitizeEscrow(e *Escrow) (*Escrow, error) {
	if e == nil {
		return nil, fmt.Errorf("nil escrow")
	}
	clone := e.Clone()
	token, err := NormalizeToken(clone.Token)
	if err != nil {
		return nil, err
	}
	clone.Token = token
	if clone.Amount == nil {
		clone.Amount = big.NewInt(0)
	}
	if clone.Amount.Sign() < 0 {
		return nil, fmt.Errorf("escrow amount must be non-negative")
	}
	if clone.Nonce == 0 {
		return nil, fmt.Errorf("escrow nonce must be > 0")
	}
	if clone.FeeBps > 10_000 {
		return nil, fmt.Errorf("escrow fee bps out of range: %d", clone.FeeBps)
	}
	if !clone.Status.Valid() {
		return nil, fmt.Errorf("invalid escrow status: %d", clone.Status)
	}
	clone.RealmID = strings.TrimSpace(clone.RealmID)
	if clone.RealmID == "" {
		clone.FrozenArb = nil
	}
	if clone.FrozenArb != nil {
		sanitized, err := SanitizeFrozenArb(clone.FrozenArb)
		if err != nil {
			return nil, err
		}
		if sanitized.RealmID != clone.RealmID {
			return nil, fmt.Errorf("frozen policy realm mismatch")
		}
		clone.FrozenArb = sanitized
	}
	return clone, nil
}

// SanitizeArbitratorSet validates the arbitrator allowlist definition.
func SanitizeArbitratorSet(set *ArbitratorSet) (*ArbitratorSet, error) {
	if set == nil {
		return nil, fmt.Errorf("nil arbitrator set")
	}
	sanitized := set.Clone()
	if !sanitized.Scheme.Valid() {
		return nil, fmt.Errorf("unsupported arbitration scheme")
	}
	if sanitized.Threshold == 0 {
		return nil, fmt.Errorf("arbitrator threshold must be positive")
	}
	if len(sanitized.Members) == 0 {
		return nil, fmt.Errorf("arbitrator set requires members")
	}
	if int(sanitized.Threshold) > len(sanitized.Members) {
		return nil, fmt.Errorf("arbitrator threshold exceeds member count")
	}
	for idx, member := range sanitized.Members {
		if member == ([20]byte{}) {
			return nil, fmt.Errorf("arbitrator member %d is zero address", idx)
		}
	}
	return sanitized, nil
}

// SanitizeEscrowRealmMetadata validates the governance metadata attached to a realm.
func SanitizeEscrowRealmMetadata(meta *EscrowRealmMetadata) (*EscrowRealmMetadata, error) {
	if meta == nil {
		return nil, fmt.Errorf("nil realm metadata")
	}
	clone := meta.Clone()
	if !clone.Scope.Valid() {
		return nil, fmt.Errorf("unsupported realm scope")
	}
	profile := strings.TrimSpace(clone.ProviderProfile)
	if profile == "" {
		return nil, fmt.Errorf("realm provider profile required")
	}
	if utf8.RuneCountInString(profile) > EscrowRealmMaxProviderProfileLength {
		return nil, fmt.Errorf("realm provider profile too long")
	}
	clone.ProviderProfile = profile
	if clone.ArbitrationFeeBps > 10_000 {
		return nil, fmt.Errorf("realm arbitration fee bps out of range")
	}
	trimmedRecipient := strings.TrimSpace(clone.FeeRecipientBech32)
	if trimmedRecipient != "" {
		if err := validateBech32Account(trimmedRecipient); err != nil {
			return nil, fmt.Errorf("realm fee recipient invalid: %w", err)
		}
		clone.FeeRecipientBech32 = trimmedRecipient
	} else if clone.ArbitrationFeeBps > 0 {
		return nil, fmt.Errorf("realm fee recipient required when fee bps > 0")
	}
	return clone, nil
}

func validateBech32Account(addr string) error {
	hrp, data, err := bech32.Decode(addr)
	if err != nil {
		return err
	}
	if hrp != "nhb" && hrp != "znhb" {
		return fmt.Errorf("unsupported hrp %q", hrp)
	}
	decoded, err := bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return err
	}
	if len(decoded) != 20 {
		return fmt.Errorf("invalid address length %d", len(decoded))
	}
	return nil
}

// SanitizeEscrowRealm validates the supplied realm definition.
func SanitizeEscrowRealm(realm *EscrowRealm) (*EscrowRealm, error) {
	if realm == nil {
		return nil, fmt.Errorf("nil escrow realm")
	}
	clone := realm.Clone()
	clone.ID = strings.TrimSpace(clone.ID)
	if clone.ID == "" {
		return nil, fmt.Errorf("realm id must not be empty")
	}
	if clone.NextPolicyNonce == 0 {
		return nil, fmt.Errorf("realm policy nonce must be positive")
	}
	if clone.Arbitrators == nil {
		return nil, fmt.Errorf("realm requires arbitrators")
	}
	sanitized, err := SanitizeArbitratorSet(clone.Arbitrators)
	if err != nil {
		return nil, err
	}
	clone.Arbitrators = sanitized
	if clone.FeeSchedule != nil {
		schedule, err := SanitizeRealmFeeSchedule(clone.FeeSchedule)
		if err != nil {
			return nil, err
		}
		clone.FeeSchedule = schedule
	}
	if clone.Metadata == nil {
		return nil, fmt.Errorf("realm metadata required")
	}
	meta, err := SanitizeEscrowRealmMetadata(clone.Metadata)
	if err != nil {
		return nil, err
	}
	clone.Metadata = meta
	if clone.UpdatedAt != 0 && clone.UpdatedAt < clone.CreatedAt {
		return nil, fmt.Errorf("realm updatedAt before createdAt")
	}
	return clone, nil
}

// SanitizeFrozenArb validates the frozen arbitrator policy instance.
func SanitizeFrozenArb(frozen *FrozenArb) (*FrozenArb, error) {
	if frozen == nil {
		return nil, fmt.Errorf("nil frozen arbitrator policy")
	}
	clone := frozen.Clone()
	clone.RealmID = strings.TrimSpace(clone.RealmID)
	if clone.RealmID == "" {
		return nil, fmt.Errorf("frozen policy missing realm id")
	}
	if clone.RealmVersion == 0 {
		return nil, fmt.Errorf("frozen policy realm version must be positive")
	}
	if clone.PolicyNonce == 0 {
		return nil, fmt.Errorf("frozen policy nonce must be positive")
	}
	if !clone.Scheme.Valid() {
		return nil, fmt.Errorf("frozen policy scheme invalid")
	}
	if clone.Threshold == 0 {
		return nil, fmt.Errorf("frozen policy threshold must be positive")
	}
	if len(clone.Members) == 0 {
		return nil, fmt.Errorf("frozen policy requires members")
	}
	if int(clone.Threshold) > len(clone.Members) {
		return nil, fmt.Errorf("frozen policy threshold exceeds members")
	}
	for idx, member := range clone.Members {
		if member == ([20]byte{}) {
			return nil, fmt.Errorf("frozen policy member %d is zero address", idx)
		}
	}
	if clone.FeeSchedule != nil {
		schedule, err := SanitizeRealmFeeSchedule(clone.FeeSchedule)
		if err != nil {
			return nil, err
		}
		clone.FeeSchedule = schedule
	}
	return clone, nil
}

// SanitizeRealmFeeSchedule validates arbitration fee routing rules.
func SanitizeRealmFeeSchedule(schedule *RealmFeeSchedule) (*RealmFeeSchedule, error) {
	if schedule == nil {
		return nil, nil
	}
	clone := schedule.Clone()
	if clone.FeeBps > 10_000 {
		return nil, fmt.Errorf("realm arbitration fee bps out of range: %d", clone.FeeBps)
	}
	if clone.FeeBps > 0 && clone.Recipient == ([20]byte{}) {
		return nil, fmt.Errorf("realm arbitration fee recipient required")
	}
	if clone.Metadata == nil {
		return nil, fmt.Errorf("frozen policy metadata required")
	}
	meta, err := SanitizeEscrowRealmMetadata(clone.Metadata)
	if err != nil {
		return nil, err
	}
	clone.Metadata = meta
	return clone, nil
}
