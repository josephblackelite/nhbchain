package config_test

import (
	"regexp"
	"strings"
	"testing"
)

// Every chain-rule activation setting that is pinned off for the live network
// (cmd/nhb-snapshot/pins.go) is read from the loaded config by both validator
// mains. A setting that exists in the config and the tests but that a main never
// passes on would be silently ignored by the binary it is documented for.
func TestEveryPinnedTopLevelActivationSettingReachesBothMains(t *testing.T) {
	pins := string(readRepoFile(t, "cmd/nhb-snapshot/pins.go"))
	re := regexp.MustCompile(`(?m)^\t\{"", "(\w+)", 0\},$`)
	matches := re.FindAllStringSubmatch(pins, -1)
	if len(matches) < 9 {
		t.Fatalf("expected the pinned top-level settings, found %d", len(matches))
	}
	mains := map[string]string{
		"cmd/nhb/main.go":        string(readRepoFile(t, "cmd/nhb/main.go")),
		"cmd/consensusd/main.go": string(readRepoFile(t, "cmd/consensusd/main.go")),
	}
	// The validator-set, slashing and staking rules reach the node as one value built
	// from the whole config (core.ValidatorRulesFromConfig fills each field of
	// core.ValidatorRules from the setting of the same name), not one setter per
	// setting: a setting is wired through it when it is a field of that struct and the
	// main hands the result to the node.
	rulesFile := string(readRepoFile(t, "core/validator_rules.go"))
	start := strings.Index(rulesFile, "type ValidatorRules struct")
	if start < 0 {
		t.Fatalf("core/validator_rules.go no longer declares ValidatorRules")
	}
	rulesBody := rulesFile[start:]
	rulesBody = rulesBody[:strings.Index(rulesBody, "\n}\n")]
	for _, m := range matches {
		name := m[1]
		for file, text := range mains {
			// The value must be handed to the node: a mention of cfg.Name in a log line or a comment is no wiring.
			if regexp.MustCompile(`\.Set\w+\([^\n]*\bcfg\.` + name + `\b`).MatchString(text) {
				continue
			}
			viaRules := strings.Contains(text, "node.SetValidatorRules(core.ValidatorRulesFromConfig(cfg))") &&
				regexp.MustCompile(`(?m)^\t`+name+`\s+uint64\b`).MatchString(rulesBody)
			if !viaRules {
				t.Errorf("%s never hands cfg.%s to a node setter: the setting is pinned and documented but the binary would ignore it", file, name)
			}
		}
	}
}

// The vote binding (PL-R5T-31) is applied by the BFT engine, which a main builds with an option:
// a main that read the setting for the node but not for the engine would verify bound certificates
// and sign legacy votes.
func TestBothMainsBuildTheBFTEngineWithTheVoteBinding(t *testing.T) {
	for _, file := range []string{"cmd/nhb/main.go", "cmd/consensusd/main.go"} {
		text := string(readRepoFile(t, file))
		if !strings.Contains(text, "bft.WithVoteBinding(node.GenesisHash(), cfg.VoteChainBindingActivationHeight)") {
			t.Errorf("%s builds the BFT engine without bft.WithVoteBinding(node.GenesisHash(), cfg.VoteChainBindingActivationHeight)", file)
		}
	}
}

// The challenge-response extension of the p2p handshake (PL-R5T-20, PL-DC-25) is read from the
// shipped config and is not a chain rule: the shipped value offers it and falls back for a peer
// that does not.
func TestShippedConfigReadsTheHandshakeChallengeMode(t *testing.T) {
	cfg := loadShippedConfigWith(t, nil)
	if cfg.P2P.HandshakeChallenge != "auto" {
		t.Fatalf("shipped [p2p] HandshakeChallenge = %q, want %q", cfg.P2P.HandshakeChallenge, "auto")
	}
}

// A main that reads the handshake mode but never hands it to the p2p server would run every node in
// the default mode whatever the operator wrote.
func TestMainsPassTheHandshakeChallengeMode(t *testing.T) {
	for _, file := range []string{"cmd/nhb/main.go", "cmd/p2pd/main.go"} {
		if !strings.Contains(string(readRepoFile(t, file)), "HandshakeChallenge: cfg.P2P.HandshakeChallenge") {
			t.Errorf("%s builds the p2p server without HandshakeChallenge: cfg.P2P.HandshakeChallenge", file)
		}
	}
}

// The advisory digest of the node's consensus settings (PL-R5T-16, PL-R5T-30) is handed to the same
// server by the validator main: a main that computed the digest, logged it and wrote its file but never
// sent it would leave every peer unable to compare, with no test failing.
func TestValidatorMainPassesTheConsensusDigestToTheP2PServer(t *testing.T) {
	if !strings.Contains(string(readRepoFile(t, "cmd/nhb/main.go")), "ConsensusConfigDigest: consensusDigest.Root,") {
		t.Errorf("cmd/nhb/main.go builds the p2p server without ConsensusConfigDigest: consensusDigest.Root,")
	}
}
