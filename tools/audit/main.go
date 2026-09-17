package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type checkSpec struct {
	Name     string   `yaml:"name"`
	Target   string   `yaml:"target"`
	Expect   string   `yaml:"expect"`
	Severity string   `yaml:"severity"`
	Coverage []string `yaml:"coverage"`
}

type artifactSpec struct {
	Name   string `yaml:"name"`
	Path   string `yaml:"path"`
	Format string `yaml:"format"`
}

type supplySpec struct {
	Minted      string `yaml:"minted"`
	Burned      string `yaml:"burned"`
	Circulating string `yaml:"circulating"`
}

type allocationSpec struct {
	Label  string `yaml:"label"`
	Amount string `yaml:"amount"`
}

type ledgerRecord struct {
	Provider     string `yaml:"provider"`
	ProviderTxID string `yaml:"provider_tx_id"`
	AmountWei    string `yaml:"amount_wei"`
	Status       string `yaml:"status"`
}

type ledgerExpectations struct {
	UniqueVouchers int            `yaml:"unique_vouchers"`
	StatusCounts   map[string]int `yaml:"status_counts"`
	Providers      map[string]int `yaml:"providers"`
	Notes          string         `yaml:"notes"`
}

type configSpec struct {
	Phase         string              `yaml:"phase"`
	Description   string              `yaml:"description"`
	Checks        []checkSpec         `yaml:"checks"`
	Artifacts     []artifactSpec      `yaml:"artifacts"`
	Metadata      map[string]string   `yaml:"metadata"`
	Supply        *supplySpec         `yaml:"supply"`
	Allocations   []allocationSpec    `yaml:"allocations"`
	LedgerRecords []ledgerRecord      `yaml:"records"`
	LedgerExpect  *ledgerExpectations `yaml:"ledger_expectations"`
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

type checkSummary struct {
	Name     string   `json:"name"`
	Target   string   `json:"target,omitempty"`
	Expect   string   `json:"expect,omitempty"`
	Severity string   `json:"severity,omitempty"`
	Coverage []string `json:"coverage,omitempty"`
	Status   string   `json:"status"`
}

type artifactSummary struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Format string `json:"format,omitempty"`
}

type digestSummary struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type composeSummary struct {
	Path     string   `json:"path"`
	Services []string `json:"services"`
}

type supplySummary struct {
	Minted          string   `json:"minted"`
	Burned          string   `json:"burned"`
	Circulating     string   `json:"circulating"`
	Consistent      bool     `json:"consistent"`
	Difference      string   `json:"difference"`
	AllocationTotal string   `json:"allocation_total,omitempty"`
	AllocationNotes []string `json:"allocation_notes,omitempty"`
}

type ledgerStatusSummary struct {
	TotalRecords int            `json:"total_records"`
	StatusCounts map[string]int `json:"status_counts"`
	Providers    map[string]int `json:"providers"`
}

type ledgerSummary struct {
	Records    ledgerStatusSummary `json:"records"`
	Expect     *ledgerExpectations `json:"expectations,omitempty"`
	Consistent bool                `json:"consistent"`
}

type summary struct {
	Phase        string            `json:"phase"`
	Description  string            `json:"description,omitempty"`
	GeneratedAt  string            `json:"generated_at"`
	ConfigPath   string            `json:"config_path"`
	ConfigSHA256 string            `json:"config_sha256"`
	Checks       []checkSummary    `json:"checks"`
	Artifacts    []artifactSummary `json:"artifacts,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Compose      *composeSummary   `json:"compose,omitempty"`
	Digests      []digestSummary   `json:"digests,omitempty"`
	Supply       *supplySummary    `json:"supply,omitempty"`
	Ledger       *ledgerSummary    `json:"ledger,omitempty"`
}

func main() {
	var (
		phaseFlag   = flag.String("phase", "", "override phase identifier")
		configPath  = flag.String("config", "", "path to YAML configuration")
		outPath     = flag.String("out", "", "path to JSON summary output")
		markdown    = flag.String("markdown", "", "optional markdown report path")
		composePath = flag.String("compose", "", "docker compose manifest to inspect")
	)
	var hashes multiFlag
	flag.Var(&hashes, "hash", "file to hash and include in the report (repeatable)")
	flag.Parse()

	if *configPath == "" {
		fatal("config path is required")
	}
	if *outPath == "" {
		fatal("output path is required")
	}

	cfgData, err := os.ReadFile(*configPath)
	if err != nil {
		fatal(fmt.Sprintf("read config: %v", err))
	}

	var cfg configSpec
	if err := yaml.Unmarshal(cfgData, &cfg); err != nil {
		fatal(fmt.Sprintf("decode config: %v", err))
	}

	phase := strings.TrimSpace(cfg.Phase)
	if *phaseFlag != "" {
		if phase != "" && !strings.EqualFold(phase, *phaseFlag) {
			fatal(fmt.Sprintf("phase mismatch: config=%q override=%q", phase, *phaseFlag))
		}
		phase = *phaseFlag
	}
	if phase == "" {
		fatal("phase must be provided via config or --phase")
	}

	configSHA := sha256.Sum256(cfgData)

	checks := make([]checkSummary, 0, len(cfg.Checks))
	for _, chk := range cfg.Checks {
		checks = append(checks, checkSummary{
			Name:     chk.Name,
			Target:   chk.Target,
			Expect:   chk.Expect,
			Severity: chk.Severity,
			Coverage: append([]string(nil), chk.Coverage...),
			Status:   "pending",
		})
	}

	artifacts := make([]artifactSummary, 0, len(cfg.Artifacts))
	for _, art := range cfg.Artifacts {
		artifacts = append(artifacts, artifactSummary{
			Name:   art.Name,
			Path:   art.Path,
			Format: art.Format,
		})
	}

	digests := make([]digestSummary, 0, len(hashes)+1)
	digests = append(digests, digestSummary{
		Path:   toRelative(*configPath),
		SHA256: hex.EncodeToString(configSHA[:]),
	})
	for _, path := range hashes {
		sum, err := hashFile(path)
		if err != nil {
			fatal(fmt.Sprintf("hash %s: %v", path, err))
		}
		digests = append(digests, digestSummary{Path: toRelative(path), SHA256: sum})
	}

	report := summary{
		Phase:        phase,
		Description:  strings.TrimSpace(cfg.Description),
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		ConfigPath:   toRelative(*configPath),
		ConfigSHA256: hex.EncodeToString(configSHA[:]),
		Checks:       checks,
		Artifacts:    artifacts,
		Metadata:     cfg.Metadata,
		Digests:      digests,
	}

	if *composePath != "" {
		comp, err := parseCompose(*composePath)
		if err != nil {
			fatal(fmt.Sprintf("compose %s: %v", *composePath, err))
		}
		report.Compose = comp
	}

	if cfg.Supply != nil {
		sup, err := summariseSupply(cfg.Supply, cfg.Allocations)
		if err != nil {
			fatal(fmt.Sprintf("supply summary: %v", err))
		}
		report.Supply = sup
	}

	if len(cfg.LedgerRecords) > 0 {
		report.Ledger = summariseLedger(cfg.LedgerRecords, cfg.LedgerExpect)
	}

	if err := writeJSON(*outPath, report); err != nil {
		fatal(fmt.Sprintf("write json: %v", err))
	}
	if *markdown != "" {
		if err := writeMarkdown(*markdown, &report); err != nil {
			fatal(fmt.Sprintf("write markdown: %v", err))
		}
	}
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "audit: %s\n", msg)
	os.Exit(1)
}

func toRelative(path string) string {
	if path == "" {
		return path
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	if rel, err := filepath.Rel(cwd, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseCompose(path string) (*composeSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Services map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw.Services))
	for name := range raw.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return &composeSummary{Path: toRelative(path), Services: names}, nil
}

func summariseSupply(spec *supplySpec, allocations []allocationSpec) (*supplySummary, error) {
	minted, ok := parseBig(spec.Minted)
	if !ok {
		return nil, fmt.Errorf("invalid minted amount %q", spec.Minted)
	}
	burned, ok := parseBig(spec.Burned)
	if !ok {
		return nil, fmt.Errorf("invalid burned amount %q", spec.Burned)
	}
	circulating, ok := parseBig(spec.Circulating)
	if !ok {
		return nil, fmt.Errorf("invalid circulating amount %q", spec.Circulating)
	}
	diff := new(big.Int).Sub(minted, burned)
	consistent := diff.Cmp(circulating) == 0

	allocTotal := new(big.Int)
	notes := make([]string, 0, len(allocations))
	for _, alloc := range allocations {
		amt, ok := parseBig(alloc.Amount)
		if !ok {
			return nil, fmt.Errorf("invalid allocation amount %q", alloc.Amount)
		}
		allocTotal.Add(allocTotal, amt)
		notes = append(notes, fmt.Sprintf("%s: %s", alloc.Label, alloc.Amount))
	}

	allocTotalStr := ""
	if allocTotal.Sign() > 0 {
		allocTotalStr = allocTotal.String()
	}

	return &supplySummary{
		Minted:          minted.String(),
		Burned:          burned.String(),
		Circulating:     circulating.String(),
		Consistent:      consistent,
		Difference:      diff.String(),
		AllocationTotal: allocTotalStr,
		AllocationNotes: notes,
	}, nil
}

func parseBig(value string) (*big.Int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0), true
	}
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		v := new(big.Int)
		_, ok := v.SetString(trimmed[2:], 16)
		return v, ok
	}
	v := new(big.Int)
	_, ok := v.SetString(trimmed, 10)
	return v, ok
}

func summariseLedger(records []ledgerRecord, expect *ledgerExpectations) *ledgerSummary {
	statusCounts := make(map[string]int)
	providerCounts := make(map[string]int)
	for _, rec := range records {
		status := strings.ToLower(strings.TrimSpace(rec.Status))
		if status == "" {
			status = "unspecified"
		}
		statusCounts[status]++
		provider := strings.ToLower(strings.TrimSpace(rec.Provider))
		if provider == "" {
			provider = "unknown"
		}
		providerCounts[provider]++
	}
	total := 0
	for _, count := range statusCounts {
		total += count
	}

	consistent := true
	if expect != nil {
		if expect.UniqueVouchers > 0 && expect.UniqueVouchers != total {
			consistent = false
		}
		for status, want := range expect.StatusCounts {
			if statusCounts[strings.ToLower(status)] != want {
				consistent = false
				break
			}
		}
		if consistent {
			for provider, want := range expect.Providers {
				if providerCounts[strings.ToLower(provider)] != want {
					consistent = false
					break
				}
			}
		}
	}

	return &ledgerSummary{
		Records: ledgerStatusSummary{
			TotalRecords: total,
			StatusCounts: statusCounts,
			Providers:    providerCounts,
		},
		Expect:     expect,
		Consistent: consistent,
	}
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func writeMarkdown(path string, sum *summary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s Audit Phase\n\n", strings.ToUpper(sum.Phase))
	if sum.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", sum.Description)
	}
	fmt.Fprintf(&b, "- Generated: `%s`\n", sum.GeneratedAt)
	fmt.Fprintf(&b, "- Config: `%s`\n", sum.ConfigPath)
	fmt.Fprintf(&b, "- Config SHA256: `%s`\n\n", sum.ConfigSHA256)

	if len(sum.Checks) > 0 {
		b.WriteString("## Checks\n\n")
		b.WriteString("| Name | Target | Expectation | Severity |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, chk := range sum.Checks {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", escapeMD(chk.Name), escapeMD(chk.Target), escapeMD(chk.Expect), escapeMD(chk.Severity))
		}
		b.WriteString("\n")
	}

	if sum.Compose != nil {
		b.WriteString("## Compose Stack\n\n")
		fmt.Fprintf(&b, "Manifest: `%s`\n\n", sum.Compose.Path)
		for _, svc := range sum.Compose.Services {
			fmt.Fprintf(&b, "- %s\n", escapeMD(svc))
		}
		b.WriteString("\n")
	}

	if len(sum.Digests) > 0 {
		b.WriteString("## Digests\n\n")
		for _, d := range sum.Digests {
			fmt.Fprintf(&b, "- `%s` → `%s`\n", d.Path, d.SHA256)
		}
		b.WriteString("\n")
	}

	if sum.Supply != nil {
		b.WriteString("## Supply\n\n")
		fmt.Fprintf(&b, "- Minted: `%s`\n", sum.Supply.Minted)
		fmt.Fprintf(&b, "- Burned: `%s`\n", sum.Supply.Burned)
		fmt.Fprintf(&b, "- Circulating: `%s`\n", sum.Supply.Circulating)
		fmt.Fprintf(&b, "- Consistent: `%t`\n", sum.Supply.Consistent)
		if sum.Supply.AllocationTotal != "" {
			fmt.Fprintf(&b, "- Allocation Total: `%s`\n", sum.Supply.AllocationTotal)
		}
		if len(sum.Supply.AllocationNotes) > 0 {
			b.WriteString("\n### Allocations\n\n")
			for _, note := range sum.Supply.AllocationNotes {
				fmt.Fprintf(&b, "- %s\n", escapeMD(note))
			}
			b.WriteString("\n")
		}
	}

	if sum.Ledger != nil {
		b.WriteString("## Ledger\n\n")
		fmt.Fprintf(&b, "Total Records: %d\n\n", sum.Ledger.Records.TotalRecords)
		if len(sum.Ledger.Records.StatusCounts) > 0 {
			b.WriteString("### Status Counts\n\n")
			for _, entry := range sortedPairs(sum.Ledger.Records.StatusCounts) {
				fmt.Fprintf(&b, "- %s: %d\n", entry.Key, entry.Value)
			}
			b.WriteString("\n")
		}
		if len(sum.Ledger.Records.Providers) > 0 {
			b.WriteString("### Providers\n\n")
			for _, entry := range sortedPairs(sum.Ledger.Records.Providers) {
				fmt.Fprintf(&b, "- %s: %d\n", entry.Key, entry.Value)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Consistent With Expectations: %t\n\n", sum.Ledger.Consistent)
		if sum.Ledger.Expect != nil && strings.TrimSpace(sum.Ledger.Expect.Notes) != "" {
			b.WriteString("### Notes\n\n")
			b.WriteString(sum.Ledger.Expect.Notes)
			b.WriteString("\n\n")
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type pair struct {
	Key   string
	Value int
}

func sortedPairs(m map[string]int) []pair {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]pair, 0, len(keys))
	for _, k := range keys {
		out = append(out, pair{Key: k, Value: m[k]})
	}
	return out
}

func escapeMD(input string) string {
	return strings.ReplaceAll(input, "|", "\\|")
}
