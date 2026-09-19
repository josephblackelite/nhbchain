package state

// DC-02 regression tests for the parameter readers that live in this package.
// A param.update value is persisted exactly as the proposal spelled it, so a
// number may be stored bare or as a quoted decimal string; both must read back
// the same.

import (
	"math/big"
	"testing"

	"nhbchain/native/governance"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func newParamTestManager(t *testing.T) *Manager {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return NewManager(tr)
}

func paramSpellings(v string) []string {
	return []string{v, `"` + v + `"`, "  " + v + "\n", `" ` + v + ` "`, `"+` + v + `"`}
}

func TestMinimumValidatorStakeReadsBareAndQuotedValues(t *testing.T) {
	want, _ := new(big.Int).SetString("20000000000000000000000", 10)
	for _, spelling := range paramSpellings("20000000000000000000000") {
		manager := newParamTestManager(t)
		if err := manager.ParamStoreSet(governance.ParamKeyMinimumValidatorStake, []byte(spelling)); err != nil {
			t.Fatalf("set: %v", err)
		}
		got, err := manager.MinimumValidatorStake()
		if err != nil {
			t.Fatalf("spelling %q: %v", spelling, err)
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("spelling %q: got %s, want %s", spelling, got, want)
		}
	}

	// Unset and unusable values give the documented default, not an error.
	def := governance.DefaultMinimumValidatorStake()
	manager := newParamTestManager(t)
	if got, err := manager.MinimumValidatorStake(); err != nil || got.Cmp(def) != 0 {
		t.Fatalf("unset: got %v, %v; want the default", got, err)
	}
	for _, raw := range []string{"abc", `"abc"`, "-1", "0", `""`} {
		if err := manager.ParamStoreSet(governance.ParamKeyMinimumValidatorStake, []byte(raw)); err != nil {
			t.Fatalf("set: %v", err)
		}
		if got, err := manager.MinimumValidatorStake(); err != nil || got.Cmp(def) != 0 {
			t.Fatalf("value %q: got %v, %v; want the default", raw, got, err)
		}
	}
}

func TestRewardEngineParamReadersAcceptQuotedValues(t *testing.T) {
	for _, spelling := range paramSpellings("1750") {
		engine := NewRewardEngine(newParamTestManager(t))
		if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingAprBps, []byte(spelling)); err != nil {
			t.Fatalf("set apr: %v", err)
		}
		if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingPayoutPeriodDays, []byte(paramSpellings("14")[0])); err != nil {
			t.Fatalf("set payout: %v", err)
		}
		apr, days, err := engine.stakingParams()
		if err != nil {
			t.Fatalf("apr spelling %q: %v", spelling, err)
		}
		if apr != 1750 || days != 14 {
			t.Fatalf("apr spelling %q: got apr=%d days=%d, want 1750 and 14", spelling, apr, days)
		}
	}
	for _, spelling := range paramSpellings("14") {
		engine := NewRewardEngine(newParamTestManager(t))
		if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingPayoutPeriodDays, []byte(spelling)); err != nil {
			t.Fatalf("set payout: %v", err)
		}
		apr, days, err := engine.stakingParams()
		if err != nil {
			t.Fatalf("payout spelling %q: %v", spelling, err)
		}
		if apr != defaultStakingAprBps || days != 14 {
			t.Fatalf("payout spelling %q: got apr=%d days=%d, want the default apr and 14", spelling, apr, days)
		}
	}

	// Unset keeps the defaults.
	apr, days, err := NewRewardEngine(newParamTestManager(t)).stakingParams()
	if err != nil || apr != defaultStakingAprBps || days != defaultPayoutPeriodDays {
		t.Fatalf("unset: got %d, %d, %v; want the defaults", apr, days, err)
	}

	// A value that cannot be read still fails the claim that consulted it.
	engine := NewRewardEngine(newParamTestManager(t))
	if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingAprBps, []byte(`"abc"`)); err != nil {
		t.Fatalf("set apr: %v", err)
	}
	if _, _, err := engine.stakingParams(); err == nil {
		t.Fatalf("expected a malformed apr to be rejected")
	}
}

func TestRewardEngineEmissionCapAcceptsQuotedValues(t *testing.T) {
	want := big.NewInt(750)
	for _, spelling := range paramSpellings("750") {
		engine := NewRewardEngine(newParamTestManager(t))
		if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingMaxEmissionPerYearWei, []byte(spelling)); err != nil {
			t.Fatalf("set cap: %v", err)
		}
		got, err := engine.stakingEmissionCap()
		if err != nil {
			t.Fatalf("spelling %q: %v", spelling, err)
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("spelling %q: got %s, want %s", spelling, got, want)
		}
	}

	engine := NewRewardEngine(newParamTestManager(t))
	if got, err := engine.stakingEmissionCap(); err != nil || got.Sign() != 0 {
		t.Fatalf("unset cap: got %v, %v; want 0", got, err)
	}
	// A cap that cannot be read fails closed (the default, 0, means no cap).
	for _, raw := range []string{"abc", `"abc"`, "-1", `"-1"`} {
		if err := engine.mgr.ParamStoreSet(governance.ParamKeyStakingMaxEmissionPerYearWei, []byte(raw)); err != nil {
			t.Fatalf("set cap: %v", err)
		}
		if _, err := engine.stakingEmissionCap(); err == nil {
			t.Fatalf("expected cap %q to be rejected", raw)
		}
	}
}
