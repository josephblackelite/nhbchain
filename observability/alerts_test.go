package observability

import (
	"os"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

type alertRule struct {
	Alert string `yaml:"alert"`
	Expr  string `yaml:"expr"`
	For   string `yaml:"for"`
}

type alertGroup struct {
	Name  string      `yaml:"name"`
	Rules []alertRule `yaml:"rules"`
}

type alertFile struct {
	Groups []alertGroup `yaml:"groups"`
}

// The consensus-liveness alert group must parse and may only reference metrics
// that are actually registered: an alert on a misspelled metric never fires,
// which for a liveness alert is the worst possible failure.
func TestConsensusLivenessAlertsReferenceRegisteredMetrics(t *testing.T) {
	raw, err := os.ReadFile("alerts.yaml")
	if err != nil {
		t.Fatalf("read alerts.yaml: %v", err)
	}
	var parsed alertFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("alerts.yaml does not parse: %v", err)
	}
	var group *alertGroup
	for i := range parsed.Groups {
		if parsed.Groups[i].Name == "consensus-liveness" {
			group = &parsed.Groups[i]
		}
	}
	if group == nil {
		t.Fatalf("alerts.yaml has no consensus-liveness group")
	}
	want := map[string]bool{
		"ConsensusNoCommit":                   false,
		"ConsensusBuildFailing":               false,
		"ConsensusEmptyBlockFallbackRate":     false,
		"MempoolNondeterministicTransactions": false,
		"ConsensusTxPanicRecovered":           false,
	}

	// Registering the collectors makes the names visible to the gatherer.
	Consensus().SetConsecutiveBuildFailures(0)
	Consensus().RecordEmptyBlockFallback()
	Consensus().RecordTxPanicRecovered()
	Consensus().SetSecondsSinceLastCommit(0)
	Mempool().RecordTxFailure("nondeterministic")

	nameRE := regexp.MustCompile(`nhb_[a-z0-9_]+`)
	for _, rule := range group.Rules {
		if _, ok := want[rule.Alert]; ok {
			want[rule.Alert] = true
		}
		names := nameRE.FindAllString(rule.Expr, -1)
		if len(names) == 0 {
			t.Errorf("alert %s references no nhb_ metric: %q", rule.Alert, rule.Expr)
		}
		for _, name := range names {
			if found, _ := metricFamily(t, name); !found {
				t.Errorf("alert %s references %s, which is not a registered metric", rule.Alert, name)
			}
		}
	}
	for alert, seen := range want {
		if !seen {
			t.Errorf("missing alert %s", alert)
		}
	}
}
