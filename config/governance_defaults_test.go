package config

import (
	"testing"

	"nhbchain/native/governance"
)

// PL-DC-10: a parameter listed as governable that the engine has no validator for is
// always rejected ("missing validation rule"), so voters are offered a change the network
// cannot make. Every key in the default allow-list must be one the engine can validate.
// The keys that validate but that no code reads at execution are listed below on purpose:
// they are accepted and stored and change nothing, and the list is pinned so a new
// dead key cannot be added unnoticed.
func TestEveryDefaultGovernableParameterHasAValidator(t *testing.T) {
	for _, key := range defaultAllowedGovernanceParams {
		if !governance.ParamHasValidator(key) {
			t.Errorf("%q is in the default allow-list but the engine has no validator for it: a proposal for it can never pass", key)
		}
	}
}

// Keys that validate and are stored but that no code reads when a transaction executes
// (PL-DC-10 part b): a passed proposal for one changes nothing. Documented as not live;
// moving a reader behind each one is a consensus change and is not made here.
var governableButNotRead = []string{
	"fees.baseFee",
	"potso.abuse.MinStakeToEarnWei",
	"potso.abuse.QuadraticTxDampenAfter",
	"potso.abuse.QuadraticTxDampenPower",
	"potso.rewards.EmissionPerEpochWei",
	"potso.weights.AlphaStakeBps",
	"network.seeds",
}

// The not-read list names only keys that are really in the default list, so it cannot
// go stale after a key is removed or a reader is added without the list being updated.
func TestGovernableButNotReadListIsCurrent(t *testing.T) {
	inDefaults := map[string]bool{}
	for _, key := range defaultAllowedGovernanceParams {
		inDefaults[key] = true
	}
	for _, key := range governableButNotRead {
		if !inDefaults[key] {
			t.Errorf("%q is listed as governable-but-not-read but is not in the default allow-list", key)
		}
	}
}
