package seeds

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	recordPrefix             = "nhbseed:v1:"
	defaultLookupPrefix      = "_nhbseed."
	defaultRefreshInterval   = 15 * time.Minute
	supportedRegistryVersion = 1
)

var (
	errEmptyRegistry = errors.New("network seeds registry payload must not be empty")
)

// Registry models the on-chain network.seeds configuration payload. The
// registry lists DNS authorities authorised to publish signed seed records as
// well as optional static fallbacks for emergency use.
type Registry struct {
	Version        int            `json:"version"`
	RefreshSeconds int            `json:"refreshSeconds,omitempty"`
	Authorities    []Authority    `json:"authorities"`
	StaticSeeds    []StaticRecord `json:"static"`
}

// Authority describes a DNS authority able to sign seed records for a
// particular zone.
type Authority struct {
	Domain    string `json:"domain"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"`
	Lookup    string `json:"lookup,omitempty"`
	NotBefore int64  `json:"notBefore,omitempty"`
	NotAfter  int64  `json:"notAfter,omitempty"`
}

// StaticRecord encodes a statically defined seed entry bundled with the
// registry. Static records act as an emergency fallback when DNS authorities are
// offline.
type StaticRecord struct {
	NodeID    string `json:"nodeId"`
	Address   string `json:"address"`
	Source    string `json:"source,omitempty"`
	NotBefore int64  `json:"notBefore,omitempty"`
	NotAfter  int64  `json:"notAfter,omitempty"`
}

// ResolvedSeed captures a fully validated seed entry produced by either a DNS
// authority or the static registry section.
type ResolvedSeed struct {
	NodeID    string
	Address   string
	Source    string
	NotBefore int64
	NotAfter  int64
}

// Active reports whether the seed is currently live given the supplied time.
func (s ResolvedSeed) Active(now time.Time) bool {
	if s.NotBefore > 0 && now.Unix() < s.NotBefore {
		return false
	}
	if s.NotAfter > 0 && now.Unix() > s.NotAfter {
		return false
	}
	return true
}

// Resolver abstracts DNS TXT lookups so tests can supply in-memory fixtures.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// Parse builds a Registry from a governance parameter payload.
func Parse(raw []byte) (*Registry, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errEmptyRegistry
	}
	var reg Registry
	if err := json.Unmarshal([]byte(trimmed), &reg); err != nil {
		return nil, fmt.Errorf("network.seeds: invalid JSON payload: %w", err)
	}
	if reg.Version == 0 {
		reg.Version = supportedRegistryVersion
	}
	if reg.Version != supportedRegistryVersion {
		return nil, fmt.Errorf("network.seeds: unsupported version %d", reg.Version)
	}
	if err := reg.validate(); err != nil {
		return nil, err
	}
	return &reg, nil
}

// RefreshInterval returns the configured refresh cadence for DNS seed polls.
func (r *Registry) RefreshInterval() time.Duration {
	if r == nil {
		return defaultRefreshInterval
	}
	if r.RefreshSeconds <= 0 {
		return defaultRefreshInterval
	}
	return time.Duration(r.RefreshSeconds) * time.Second
}

// Static resolves the static fallback entries that are currently active.
func (r *Registry) Static(now time.Time) []ResolvedSeed {
	if r == nil {
		return nil
	}
	results := make([]ResolvedSeed, 0, len(r.StaticSeeds))
	for _, entry := range r.StaticSeeds {
		seed, err := entry.toSeed()
		if err != nil {
			continue
		}
		if !seed.Active(now) {
			continue
		}
		results = append(results, seed)
	}
	return dedupeSeeds(results)
}

// Resolve queries the configured DNS authorities and returns the validated
// signed seeds alongside the static fallback entries.
func (r *Registry) Resolve(ctx context.Context, now time.Time, resolver Resolver) ([]ResolvedSeed, error) {
	if r == nil {
		return nil, nil
	}
	results := r.Static(now)
	if len(r.Authorities) == 0 {
		return results, nil
	}
	if resolver == nil {
		resolver = &netResolver{resolver: net.DefaultResolver}
	}
	var errs []error
	for _, auth := range r.Authorities {
		if !auth.active(now) {
			continue
		}
		seeds, err := auth.resolve(ctx, now, resolver)
		if len(seeds) > 0 {
			results = append(results, seeds...)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	results = dedupeSeeds(results)
	if len(errs) > 0 {
		return results, errors.Join(errs...)
	}
	return results, nil
}

func (r *Registry) validate() error {
	for i := range r.Authorities {
		if err := r.Authorities[i].validate(); err != nil {
			return fmt.Errorf("network.seeds: authority #%d: %w", i+1, err)
		}
	}
	for i := range r.StaticSeeds {
		if err := r.StaticSeeds[i].validate(); err != nil {
			return fmt.Errorf("network.seeds: static seed #%d: %w", i+1, err)
		}
	}
	return nil
}

func (a Authority) validate() error {
	domain := strings.TrimSpace(a.Domain)
	if domain == "" {
		return errors.New("domain must not be empty")
	}
	if a.Algorithm == "" {
		a.Algorithm = "ed25519"
	}
	if strings.ToLower(strings.TrimSpace(a.Algorithm)) != "ed25519" {
		return fmt.Errorf("unsupported algorithm %q", a.Algorithm)
	}
	if _, err := a.decodePublicKey(); err != nil {
		return err
	}
	if a.NotAfter > 0 && a.NotBefore > 0 && a.NotAfter < a.NotBefore {
		return errors.New("notAfter must be >= notBefore")
	}
	return nil
}

func (a Authority) active(now time.Time) bool {
	if a.NotBefore > 0 && now.Unix() < a.NotBefore {
		return false
	}
	if a.NotAfter > 0 && now.Unix() > a.NotAfter {
		return false
	}
	return true
}

func (a Authority) resolve(ctx context.Context, now time.Time, resolver Resolver) ([]ResolvedSeed, error) {
	if resolver == nil {
		resolver = &netResolver{resolver: net.DefaultResolver}
	}
	name := strings.TrimSpace(a.Lookup)
	if name == "" {
		name = defaultLookupPrefix + strings.TrimSpace(a.Domain)
	}
	txtRecords, err := resolver.LookupTXT(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("dns %s lookup failed: %w", name, err)
	}
	pubKey, err := a.decodePublicKey()
	if err != nil {
		return nil, err
	}
	seeds := make([]ResolvedSeed, 0, len(txtRecords))
	var errs []error
	for _, record := range txtRecords {
		seed, err := a.parseTXT(record, pubKey)
		if err != nil {
			errs = append(errs, fmt.Errorf("dns %s invalid record: %w", name, err))
			continue
		}
		if !seed.Active(now) {
			continue
		}
		seeds = append(seeds, seed)
	}
	seeds = dedupeSeeds(seeds)
	if len(errs) > 0 {
		return seeds, errors.Join(errs...)
	}
	return seeds, nil
}

func (a Authority) decodePublicKey() ([]byte, error) {
	trimmed := strings.TrimSpace(a.PublicKey)
	if trimmed == "" {
		return nil, errors.New("publicKey must not be empty")
	}
	keyBytes, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid publicKey encoding: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("publicKey must be %d bytes", ed25519.PublicKeySize)
	}
	return keyBytes, nil
}

func (a Authority) parseTXT(record string, publicKey []byte) (ResolvedSeed, error) {
	trimmed := strings.TrimSpace(record)
	if trimmed == "" {
		return ResolvedSeed{}, errors.New("empty TXT record")
	}
	if !strings.HasPrefix(trimmed, recordPrefix) {
		return ResolvedSeed{}, fmt.Errorf("record missing prefix %q", recordPrefix)
	}
	payload := strings.TrimPrefix(trimmed, recordPrefix)
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ResolvedSeed{}, fmt.Errorf("base64 decode: %w", err)
	}
	var entry dnsRecord
	if err := json.Unmarshal(raw, &entry); err != nil {
		return ResolvedSeed{}, fmt.Errorf("invalid JSON payload: %w", err)
	}
	seed, err := entry.toSeed(strings.TrimSpace(a.Domain), publicKey)
	if err != nil {
		return ResolvedSeed{}, err
	}
	return seed, nil
}

func (s StaticRecord) toSeed() (ResolvedSeed, error) {
	if err := s.validate(); err != nil {
		return ResolvedSeed{}, err
	}
	nodeID := normalizeNodeID(s.NodeID)
	addr := strings.TrimSpace(s.Address)
	if addr == "" {
		return ResolvedSeed{}, errors.New("address must not be empty")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return ResolvedSeed{}, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	source := strings.TrimSpace(s.Source)
	if source == "" {
		source = "registry.static"
	}
	return ResolvedSeed{
		NodeID:    nodeID,
		Address:   addr,
		Source:    source,
		NotBefore: s.NotBefore,
		NotAfter:  s.NotAfter,
	}, nil
}

func (s StaticRecord) validate() error {
	if strings.TrimSpace(s.NodeID) == "" {
		return errors.New("nodeId must not be empty")
	}
	if strings.TrimSpace(s.Address) == "" {
		return errors.New("address must not be empty")
	}
	if s.NotAfter > 0 && s.NotBefore > 0 && s.NotAfter < s.NotBefore {
		return errors.New("notAfter must be >= notBefore")
	}
	return nil
}

type dnsRecord struct {
	NodeID    string `json:"nodeId"`
	Address   string `json:"address"`
	NotBefore int64  `json:"notBefore,omitempty"`
	NotAfter  int64  `json:"notAfter,omitempty"`
	Signature string `json:"signature"`
}

func (d dnsRecord) toSeed(domain string, publicKey []byte) (ResolvedSeed, error) {
	nodeID := normalizeNodeID(d.NodeID)
	if nodeID == "" {
		return ResolvedSeed{}, errors.New("nodeId must not be empty")
	}
	addr := strings.TrimSpace(d.Address)
	if addr == "" {
		return ResolvedSeed{}, errors.New("address must not be empty")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return ResolvedSeed{}, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(d.Signature))
	if err != nil {
		return ResolvedSeed{}, fmt.Errorf("invalid signature encoding: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return ResolvedSeed{}, fmt.Errorf("signature must be %d bytes", ed25519.SignatureSize)
	}
	message := buildSigningMessage(nodeID, addr, d.NotBefore, d.NotAfter, domain)
	if !ed25519.Verify(publicKey, message, sig) {
		return ResolvedSeed{}, errors.New("signature verification failed")
	}
	return ResolvedSeed{
		NodeID:    nodeID,
		Address:   addr,
		Source:    "dns:" + domain,
		NotBefore: d.NotBefore,
		NotAfter:  d.NotAfter,
	}, nil
}

func buildSigningMessage(nodeID, addr string, notBefore, notAfter int64, domain string) []byte {
	normalizedDomain := strings.ToLower(strings.TrimSpace(domain))
	builder := strings.Builder{}
	builder.Grow(len(nodeID) + len(addr) + len(normalizedDomain) + 40)
	builder.WriteString(nodeID)
	builder.WriteString("\n")
	builder.WriteString(addr)
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf("%d\n%d\n", notBefore, notAfter))
	builder.WriteString(normalizedDomain)
	return []byte(builder.String())
}

func normalizeNodeID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "0x") && !strings.HasPrefix(trimmed, "0X") {
		trimmed = "0x" + trimmed
	}
	return strings.ToLower(trimmed)
}

func dedupeSeeds(in []ResolvedSeed) []ResolvedSeed {
	if len(in) <= 1 {
		return append([]ResolvedSeed(nil), in...)
	}
	seen := make(map[string]struct{}, len(in))
	result := make([]ResolvedSeed, 0, len(in))
	for _, seed := range in {
		key := seed.NodeID + "@" + seed.Address
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, seed)
	}
	return result
}

type netResolver struct {
	resolver *net.Resolver
}

func (n *netResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if n == nil || n.resolver == nil {
		return net.DefaultResolver.LookupTXT(ctx, name)
	}
	return n.resolver.LookupTXT(ctx, name)
}

// DefaultResolver exposes a resolver backed by the Go runtime's default DNS
// implementation. Callers may override this with their own implementation when
// testing.
func DefaultResolver() Resolver {
	return &netResolver{resolver: net.DefaultResolver}
}
