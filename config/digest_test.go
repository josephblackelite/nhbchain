package config

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadShipped loads the config.toml shipped with the repository through the real
// loader, so the digest is taken over what a node would actually run with.
func loadShipped(t *testing.T) *Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read shipped config.toml: %v", err)
	}
	// The loader creates the validator keystore next to the file when it is
	// missing: load a copy in a scratch directory so the repository stays clean.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("copy config.toml: %v", err)
	}
	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load shipped config.toml: %v", err)
	}
	return cfg
}

// updateDigest rewrites the golden file: go test ./config -run TestShippedConfigConsensusDigestIsPinned -update-digest
var updateDigest = flag.Bool("update-digest", false, "rewrite config/testdata/shipped_consensus_digest.golden")

func noEnv(string) string { return "" }

func mustDigest(t *testing.T, cfg *Config, env func(string) string) ConsensusDigest {
	t.Helper()
	d, err := cfg.ConsensusDigest(env)
	if err != nil {
		t.Fatalf("ConsensusDigest: %v", err)
	}
	return d
}

func sectionDigest(d ConsensusDigest, name string) string {
	for _, s := range d.Sections {
		if s.Name == name {
			return s.Digest
		}
	}
	return ""
}

// The same config gives the same digest, every time, and the digest has the
// shape the operators compare: a root and one line per section.
func TestConsensusDigestIsDeterministic(t *testing.T) {
	a := mustDigest(t, loadShipped(t), noEnv)
	b := mustDigest(t, loadShipped(t), noEnv)
	if a.Root == "" || len(a.Root) != 64 {
		t.Fatalf("root %q is not a hex sha256", a.Root)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two loads of the same file digest differently:\n%+v\n%+v", a, b)
	}
	if len(a.Sections) < 10 {
		t.Fatalf("only %d sections: %+v", len(a.Sections), a.Sections)
	}
	for i := 1; i < len(a.Sections); i++ {
		if a.Sections[i-1].Name >= a.Sections[i].Name {
			t.Fatalf("sections are not sorted by name: %q then %q", a.Sections[i-1].Name, a.Sections[i].Name)
		}
	}
}

// Every top-level setting of the config must be classified: it belongs to a digest
// section, or it is named as node-local on purpose. A setting added later with
// neither fails here, so a new consensus input cannot be left out of the digest by
// forgetting it (PL-R5T-16: nothing compared these between validators).
func TestEveryTopLevelConfigSettingIsClassified(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, ok := digestFieldSections[name]; ok {
			continue
		}
		if _, ok := nodeLocalFields[name]; ok {
			continue
		}
		t.Errorf("Config.%s is neither in digestFieldSections nor in nodeLocalFields: decide whether two validators must agree on it and add it to one of them", name)
	}
	for name := range digestFieldSections {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("digestFieldSections names %s, which is not a Config field", name)
		}
		if _, dup := nodeLocalFields[name]; dup {
			t.Errorf("%s is both digested and node-local", name)
		}
	}
	for name := range nodeLocalFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("nodeLocalFields names %s, which is not a Config field", name)
		}
	}
}

// Every field of the global section is digested: the section is the state
// machine's own parameters (fees, quotas, pauses, paymaster, staking, slashing).
func TestEveryGlobalSectionFieldIsDigested(t *testing.T) {
	d := mustDigest(t, loadShipped(t), noEnv)
	typ := reflect.TypeOf(Global{})
	for i := 0; i < typ.NumField(); i++ {
		want := "global." + strings.ToLower(typ.Field(i).Name)
		if sectionDigest(d, want) == "" {
			t.Errorf("no digest section %q for Global.%s", want, typ.Field(i).Name)
		}
	}
}

// Changing any value two validators must agree on changes the root and the
// section that holds it; changing a node-local value changes nothing.
func TestConsensusDigestMovesWithConsensusSettingsAndNotWithNodeLocalOnes(t *testing.T) {
	base := mustDigest(t, loadShipped(t), noEnv)

	changes := map[string]struct {
		section string
		mutate  func(c *Config)
	}{
		"activation height":             {"chain-rules", func(c *Config) { c.POSSweepSkipActivationHeight = 9 }},
		"supply correction height":      {"chain-rules", func(c *Config) { c.NHBSupplyGenesisCorrectionActivationHeight = 9 }},
		"governance weight height":      {"chain-rules", func(c *Config) { c.PotsoGovernanceWeightActivationHeight = 9 }},
		"quorum certificate height":     {"chain-rules", func(c *Config) { c.QuorumCertActivationHeight = 9 }},
		"rewards by stake epoch":        {"chain-rules", func(c *Config) { c.RewardsByStakeFromEpoch = 9 }},
		"loyalty budget floor":          {"chain-rules", func(c *Config) { c.LoyaltyBudgetFloorZNHBWei = "5" }},
		"governance voting period":      {"governance", func(c *Config) { c.Governance.VotingPeriodSeconds = 7 }},
		"governance allowed parameters": {"governance", func(c *Config) { c.Governance.AllowedParams = append(c.Governance.AllowedParams, "x.y") }},
		"potso reward config":           {"potso", func(c *Config) { c.Potso.Rewards.EpochLengthBlocks += 1 }},
		"transfer fee policy":           {"global.fees", func(c *Config) { c.Global.Fees.FreeTierTxPerMonth += 1 }},
		"largest block":                 {"global.blocks", func(c *Config) { c.Global.Blocks.MaxTxs += 1 }},
		"module pause":                  {"global.pauses", func(c *Config) { c.Global.Pauses.Staking = !c.Global.Pauses.Staking }},
		"staking apr":                   {"global.staking", func(c *Config) { c.Global.Staking.AprBps += 1 }},
		"subscription management fee":   {"subscriptions", func(c *Config) { c.Subscriptions.ManagementFeeBps += 1 }},
		"swap payout receipt height":    {"swap", func(c *Config) { c.Swap.PayoutReceiptActivationHeight = 9 }},
		"lending config":                {"lending", func(c *Config) { c.Lending.DeveloperFeeBps += 1 }},
		"bft round timer (liveness)":    {"bft-timing", func(c *Config) { c.Consensus.CommitTimeout += 1 }},
	}
	for name, tc := range changes {
		cfg := loadShipped(t)
		tc.mutate(cfg)
		got := mustDigest(t, cfg, noEnv)
		if sectionDigest(got, tc.section) == sectionDigest(base, tc.section) {
			t.Errorf("%s: section %s did not change", name, tc.section)
		}
		if tc.section != "bft-timing" && got.Root == base.Root {
			t.Errorf("%s: root did not change", name)
		}
		// Only that section moves.
		for _, s := range got.Sections {
			if s.Name != tc.section && s.Digest != sectionDigest(base, s.Name) {
				t.Errorf("%s: section %s moved too", name, s.Name)
			}
		}
	}

	local := map[string]func(c *Config){
		"listen address": func(c *Config) { c.ListenAddress = "127.0.0.1:1" },
		"rpc address":    func(c *Config) { c.RPCAddress = "127.0.0.1:2" },
		"data dir":       func(c *Config) { c.DataDir = "elsewhere" },
		"bootnodes":      func(c *Config) { c.Bootnodes = []string{"a"} },
		"peer limit":     func(c *Config) { c.MaxPeers += 1 },
		"keystore path":  func(c *Config) { c.ValidatorKeystorePath = "other.keystore" },
		"client version": func(c *Config) { c.ClientVersion = "other/9" },
		"rpc timeout":    func(c *Config) { c.RPCReadTimeout += 1 },
	}
	for name, mutate := range local {
		cfg := loadShipped(t)
		mutate(cfg)
		got := mustDigest(t, cfg, noEnv)
		if !reflect.DeepEqual(got, base) {
			t.Errorf("%s changed the digest, but it is node-local", name)
		}
	}
}

// The round timers change when a validator moves on, never what a block holds: a
// difference in them is reported but does not move the root two validators compare.
func TestBFTTimingIsReportedButDoesNotMoveTheRoot(t *testing.T) {
	base := mustDigest(t, loadShipped(t), noEnv)
	cfg := loadShipped(t)
	cfg.Consensus.CommitTimeout += 1
	got := mustDigest(t, cfg, noEnv)
	if got.Root != base.Root {
		t.Fatalf("a timer moved the root")
	}
	found := false
	for _, s := range got.Sections {
		if s.Name == "bft-timing" {
			found = true
			if !s.LivenessOnly {
				t.Fatalf("bft-timing must be marked liveness-only")
			}
		}
	}
	if !found {
		t.Fatalf("no bft-timing section")
	}
}

// NHB_MASTER_TREASURY silently replaces the genesis admin wallet on the node
// that has it set (PL-DC-04): two validators, one with it set, must not digest
// alike. An unset and an empty variable are the same.
func TestTreasuryOverrideMovesTheDigest(t *testing.T) {
	cfg := loadShipped(t)
	base := mustDigest(t, cfg, noEnv)
	set := mustDigest(t, cfg, func(k string) string {
		if k == "NHB_MASTER_TREASURY" {
			return "nhb1something"
		}
		return ""
	})
	if set.Root == base.Root {
		t.Fatalf("an NHB_MASTER_TREASURY override left the digest alone")
	}
	if sectionDigest(set, "environment") == sectionDigest(base, "environment") {
		t.Fatalf("the environment section did not change")
	}
	spaces := mustDigest(t, cfg, func(k string) string {
		if k == "NHB_MASTER_TREASURY" {
			return "  "
		}
		return ""
	})
	if spaces.Root != base.Root {
		t.Fatalf("a blank override must read as no override")
	}
}

// The digest never contains a value: a peer or a log reader learns whether two
// nodes agree, not what the settings are.
func TestConsensusDigestHoldsOnlyHashes(t *testing.T) {
	d := mustDigest(t, loadShipped(t), func(k string) string {
		if k == "NHB_MASTER_TREASURY" {
			return "nhb1secretlookingvalue"
		}
		return ""
	})
	blob := d.Root
	for _, s := range d.Sections {
		blob += s.Name + s.Digest
	}
	if strings.Contains(blob, "nhb1secretlookingvalue") {
		t.Fatalf("a value leaked into the digest")
	}
	for _, s := range d.Sections {
		if len(s.Digest) != 64 {
			t.Fatalf("section %s digest %q is not a hex sha256", s.Name, s.Digest)
		}
	}
}

func TestCompareConsensusDigestsNamesTheDifferingSections(t *testing.T) {
	a := mustDigest(t, loadShipped(t), noEnv)
	if diff := CompareConsensusDigests(a, a); len(diff) != 0 {
		t.Fatalf("identical digests differ in %v", diff)
	}
	cfg := loadShipped(t)
	cfg.Global.Blocks.MaxTxs += 1
	cfg.Governance.QuorumBps += 1
	cfg.Consensus.CommitTimeout += 1
	b := mustDigest(t, cfg, noEnv)
	diff := CompareConsensusDigests(a, b)
	want := []string{"bft-timing", "global.blocks", "governance"}
	if !reflect.DeepEqual(diff, want) {
		t.Fatalf("differing sections = %v, want %v", diff, want)
	}
	c := a
	c.Version++
	if diff := CompareConsensusDigests(a, c); len(diff) == 0 {
		t.Fatalf("digests of different versions must not compare equal")
	}
}

// Golden pin of the shipped config.toml. A change to any consensus setting, or to
// the shipped defaults, fails this test on purpose: update the pin in the same
// commit as the change, so the change cannot go unnoticed in review. The root is
// what both validators log at start-up and what check-parity compares.
func TestShippedConfigConsensusDigestIsPinned(t *testing.T) {
	golden := filepath.Join("testdata", "shipped_consensus_digest.golden")
	if *updateDigest {
		if err := os.WriteFile(golden, []byte(mustDigest(t, loadShipped(t), noEnv).Text()), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read the golden digest: %v (create it with go test ./config -run TestShippedConfigConsensusDigestIsPinned -update-digest)", err)
	}
	d := mustDigest(t, loadShipped(t), noEnv)
	got := d.Text()
	if strings.TrimSpace(string(raw)) != strings.TrimSpace(got) {
		t.Fatalf("the shipped config.toml's consensus digest changed.\nwant:\n%s\ngot:\n%s\nIf the change is intended, replace config/testdata/shipped_consensus_digest.golden with the got text.", raw, got)
	}
}

// A digest written to a file by one node is read back by the operator's tool on the
// other: the text form round-trips, and nothing else is accepted.
func TestConsensusDigestTextRoundTrips(t *testing.T) {
	d := mustDigest(t, loadShipped(t), noEnv)
	back, err := ParseConsensusDigest(d.Text())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(d, back) {
		t.Fatalf("round trip changed the digest:\n%+v\n%+v", d, back)
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"no root":          "version 1\nchain-rules " + strings.Repeat("a", 64) + "\n",
		"short root":       "version 1\nroot abcd\n",
		"not hex":          "version 1\nroot " + strings.Repeat("z", 64) + "\n",
		"no version":       "root " + strings.Repeat("a", 64) + "\n",
		"short section":    "version 1\nroot " + strings.Repeat("a", 64) + "\nx abcd\n",
		"duplicate":        "version 1\nroot " + strings.Repeat("a", 64) + "\nx " + strings.Repeat("b", 64) + "\nx " + strings.Repeat("c", 64) + "\n",
		"unknown trailing": "version 1\nroot " + strings.Repeat("a", 64) + "\nx " + strings.Repeat("b", 64) + " nonsense\n",
	} {
		if _, err := ParseConsensusDigest(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The root of a parsed digest must be the one its sections give: a file edited by
// hand to show agreement does not pass for a digest.
func TestParsedConsensusDigestRootMustMatchItsSections(t *testing.T) {
	d := mustDigest(t, loadShipped(t), noEnv)
	// The first section that counts toward the root (the timing section is liveness only and is not in it).
	var target DigestSection
	for _, s := range d.Sections {
		if !s.LivenessOnly {
			target = s
			break
		}
	}
	if target.Digest == "" {
		t.Fatalf("no section counts toward the root")
	}
	forged := strings.Replace(d.Text(), target.Digest, strings.Repeat("0", 64), 1)
	if _, err := ParseConsensusDigest(forged); err == nil {
		t.Fatalf("a digest whose section was edited but whose root was kept was accepted")
	}
}

// A digest whose root matches its sections can still be malformed: the root check
// alone does not make a section line valid. Each of these is given a root that
// matches what it says, so only the line itself can be the reason for the refusal.
func TestParseRefusesMalformedSectionsEvenWithAMatchingRoot(t *testing.T) {
	good := strings.Repeat("b", 64)
	cases := map[string][]DigestSection{
		"duplicate section":         {{Name: "x", Digest: good}, {Name: "x", Digest: strings.Repeat("c", 64)}},
		"section digest too short":  {{Name: "x", Digest: "abcd"}},
		"section digest not hex":    {{Name: "x", Digest: strings.Repeat("z", 64)}},
		"section digest upper case": {{Name: "x", Digest: strings.Repeat("B", 64)}},
	}
	for name, sections := range cases {
		var b strings.Builder
		b.WriteString("version 1\nroot " + digestRoot(1, sections) + "\n")
		for _, s := range sections {
			b.WriteString(s.Name + " " + s.Digest + "\n")
		}
		if _, err := ParseConsensusDigest(b.String()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// And the control: the same shape with valid lines is accepted.
	ok := []DigestSection{{Name: "x", Digest: good}, {Name: "y", Digest: strings.Repeat("c", 64)}}
	text := "version 1\nroot " + digestRoot(1, ok) + "\nx " + ok[0].Digest + "\ny " + ok[1].Digest + "\n"
	if _, err := ParseConsensusDigest(text); err != nil {
		t.Errorf("a well-formed digest was refused: %v", err)
	}
}

// A section only one side has is a difference, whichever side has it.
func TestCompareConsensusDigestsCountsASectionOnlyOneSideHas(t *testing.T) {
	full := mustDigest(t, loadShipped(t), noEnv)
	fewer := full
	fewer.Sections = append([]DigestSection(nil), full.Sections[1:]...)
	missing := full.Sections[0].Name
	if diff := CompareConsensusDigests(full, fewer); !reflect.DeepEqual(diff, []string{missing}) {
		t.Fatalf("left has the extra section: differing = %v, want [%s]", diff, missing)
	}
	if diff := CompareConsensusDigests(fewer, full); !reflect.DeepEqual(diff, []string{missing}) {
		t.Fatalf("right has the extra section: differing = %v, want [%s]", diff, missing)
	}
}
