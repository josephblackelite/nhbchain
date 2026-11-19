package core

import (
	"reflect"
	"testing"

	"nhbchain/config"
)

// Every rule of the group must be read from a config setting of its own name, set on
// the node and its processor, and survive a copy of the processor and a rebuild of it.
// The test sets EVERY field to a distinct non-zero value by reflection, so a rule added
// to ValidatorRules and left out of one of those paths fails here without anyone
// remembering to extend this test.
func distinctRules() ValidatorRules {
	var rules ValidatorRules
	v := reflect.ValueOf(&rules).Elem()
	for i := 0; i < v.NumField(); i++ {
		v.Field(i).SetUint(uint64(1000 + i))
	}
	return rules
}

func TestEveryValidatorRuleIsReadFromTheConfigUnderItsOwnName(t *testing.T) {
	want := distinctRules()
	var cfg config.Config
	cfgV := reflect.ValueOf(&cfg).Elem()
	wantV := reflect.ValueOf(want)
	for i := 0; i < wantV.NumField(); i++ {
		name := wantV.Type().Field(i).Name
		field := cfgV.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.Uint64 {
			t.Fatalf("ValidatorRules.%s has no uint64 setting of that name in config.Config", name)
		}
		field.SetUint(wantV.Field(i).Uint())
	}
	if got := ValidatorRulesFromConfig(&cfg); got != want {
		t.Fatalf("read %+v from the config, want %+v", got, want)
	}
	if got := ValidatorRulesFromConfig(nil); got != (ValidatorRules{}) {
		t.Fatalf("a nil config must leave every rule off, got %+v", got)
	}
	if got := ValidatorRulesFromConfig(&config.Config{}); got != (ValidatorRules{}) {
		t.Fatalf("an empty config must leave every rule off, got %+v", got)
	}
}

func TestEveryValidatorRuleIsCarriedByCopyAndRebuild(t *testing.T) {
	want := distinctRules()

	sp := newStakingStateProcessor(t)
	sp.SetValidatorRules(want)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := copied.ValidatorRulesInForce(); got != want {
		t.Fatalf("a copy of the processor holds %+v, want %+v", got, want)
	}

	node := newTestNode(t)
	node.SetValidatorRules(want)
	node.stateMu.Lock()
	onProcessor := node.state.ValidatorRulesInForce()
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.ValidatorRulesInForce()
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if onProcessor != want {
		t.Fatalf("the node's setter left the processor at %+v, want %+v", onProcessor, want)
	}
	if rebuilt != want {
		t.Fatalf("a rebuilt processor holds %+v, want %+v", rebuilt, want)
	}
}

// A rule with a zero height is off at every height, and one with a height applies from
// that height on and not before.
func TestRuleActiveAt(t *testing.T) {
	for _, tc := range []struct {
		from, height uint64
		want         bool
	}{
		{0, 0, false}, {0, 1, false}, {0, ^uint64(0), false},
		{100, 99, false}, {100, 100, true}, {100, 101, true},
	} {
		if got := ruleActiveAt(tc.from, tc.height); got != tc.want {
			t.Errorf("ruleActiveAt(%d, %d) = %v, want %v", tc.from, tc.height, got, tc.want)
		}
	}
}
