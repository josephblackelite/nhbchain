package core

// DC-02 regression tests. A param.update proposal is validated leniently
// (a numeric value may be a bare number or a quoted decimal string) but is
// persisted verbatim, so the readers of governed numeric parameters must read
// both spellings. Before the fix a quoted staking.minimumValidatorStake made
// every account write fail (setAccount consults it), and the other readers
// failed the transactions, or the node start-up, that consulted them.
//
// Each reader below is exercised with the bare and the quoted spelling of the
// same value and must return the same result for both.

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/storage"
)

// numberSpellings lists the spellings governance validation accepts for the
// number v and persists verbatim.
func numberSpellings(v string) []string {
	return []string{
		v,
		`"` + v + `"`,
		"  " + v + "\n",
		`" ` + v + ` "`,
		`"+` + v + `"`,
	}
}

func mustParam(t *testing.T, manager *nhbstate.Manager, key, raw string) {
	t.Helper()
	if err := manager.ParamStoreSet(key, []byte(raw)); err != nil {
		t.Fatalf("set %s=%q: %v", key, raw, err)
	}
}

func bigFromDecimal(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer literal %q", s)
	}
	return v
}

// TestAccountWritesSurviveQuotedMinimumValidatorStake is the reproduction of
// the finding: with {"staking.minimumValidatorStake":"20000000000000000000000"}
// stored verbatim, every account write used to fail.
func TestAccountWritesSurviveQuotedMinimumValidatorStake(t *testing.T) {
	want := bigFromDecimal(t, "20000000000000000000000")
	for _, spelling := range numberSpellings("20000000000000000000000") {
		sp, manager := newTestStateProcessor(t)
		mustParam(t, manager, governance.ParamKeyMinimumValidatorStake, spelling)

		var addr [20]byte
		addr[19] = 0x31
		setAccountBalance(t, sp, addr, big.NewInt(5)) // fails before the fix

		got, err := sp.minimumValidatorStake()
		if err != nil {
			t.Fatalf("spelling %q: minimumValidatorStake: %v", spelling, err)
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("spelling %q: minimumValidatorStake = %s, want %s", spelling, got, want)
		}
		viaManager, err := manager.MinimumValidatorStake()
		if err != nil {
			t.Fatalf("spelling %q: Manager.MinimumValidatorStake: %v", spelling, err)
		}
		if viaManager.Cmp(want) != 0 {
			t.Fatalf("spelling %q: Manager.MinimumValidatorStake = %s, want %s", spelling, viaManager, want)
		}
	}
}

// TestMalformedMinimumValidatorStakeDegradesToDefault: a stored value that is
// not a positive integer (which proposal validation cannot produce) must not
// wedge account writes; it degrades to the documented default.
func TestMalformedMinimumValidatorStakeDegradesToDefault(t *testing.T) {
	def := governance.DefaultMinimumValidatorStake()
	for _, raw := range []string{"abc", `"abc"`, "-5", "0", `"0"`, "1.5", `{"a":1}`} {
		sp, manager := newTestStateProcessor(t)
		mustParam(t, manager, governance.ParamKeyMinimumValidatorStake, raw)

		var addr [20]byte
		addr[19] = 0x32
		setAccountBalance(t, sp, addr, big.NewInt(5))

		got, err := sp.minimumValidatorStake()
		if err != nil {
			t.Fatalf("value %q: minimumValidatorStake returned an error: %v", raw, err)
		}
		if got.Cmp(def) != 0 {
			t.Fatalf("value %q: minimumValidatorStake = %s, want the default %s", raw, got, def)
		}
	}
}

// TestValidatorEligibilityHonoursQuotedMinimumStake shows the quoted value is
// actually enforced (not merely tolerated): an account below it stays out of
// the eligible set and one at or above it joins.
func TestValidatorEligibilityHonoursQuotedMinimumStake(t *testing.T) {
	for _, spelling := range numberSpellings("5000") {
		sp := newStakingStateProcessor(t)
		manager := nhbstate.NewManager(sp.Trie)
		mustParam(t, manager, governance.ParamKeyMinimumValidatorStake, spelling)

		var low, high [20]byte
		low[19] = 0x41
		high[19] = 0x42
		writeAccount(t, sp, low, &types.Account{BalanceZNHB: big.NewInt(20_000)})
		writeAccount(t, sp, high, &types.Account{BalanceZNHB: big.NewInt(20_000)})

		register := mustEncodeStakePayload(t, stakePayload{RegisterValidator: true})
		if err := sp.applyStake(&types.Transaction{Value: big.NewInt(4_000), Data: register}, low[:]); err != nil {
			t.Fatalf("spelling %q: stake below the minimum: %v", spelling, err)
		}
		if err := sp.applyStake(&types.Transaction{Value: big.NewInt(6_000), Data: register}, high[:]); err != nil {
			t.Fatalf("spelling %q: stake above the minimum: %v", spelling, err)
		}
		if _, ok := sp.EligibleValidators[string(low[:])]; ok {
			t.Fatalf("spelling %q: a stake below the governed minimum must not be eligible", spelling)
		}
		if basis, ok := sp.EligibleValidators[string(high[:])]; !ok || basis.Cmp(big.NewInt(6_000)) != 0 {
			t.Fatalf("spelling %q: a stake above the governed minimum must be eligible with basis 6000, got %v (present=%v)", spelling, basis, ok)
		}
	}
}

func TestStakingPeriodReadersAcceptQuotedParams(t *testing.T) {
	sp, manager := newTestStateProcessor(t)

	for _, spelling := range numberSpellings("14") {
		mustParam(t, manager, governance.ParamKeyStakingPayoutPeriodDays, spelling)
		seconds, err := sp.stakingPayoutPeriodSeconds(manager)
		if err != nil {
			t.Fatalf("payout period %q: %v", spelling, err)
		}
		if want := uint64(14 * 24 * 60 * 60); seconds != want {
			t.Fatalf("payout period %q = %d seconds, want %d", spelling, seconds, want)
		}

		mustParam(t, manager, governance.ParamKeyStakingUnbondingDays, spelling)
		period, err := sp.stakingUnbondingPeriod(manager)
		if err != nil {
			t.Fatalf("unbonding period %q: %v", spelling, err)
		}
		if want := 14 * 24 * time.Hour; period != want {
			t.Fatalf("unbonding period %q = %s, want %s", spelling, period, want)
		}
	}

	// Unset and zero keep meaning "use the default".
	fresh, freshManager := newTestStateProcessor(t)
	if seconds, err := fresh.stakingPayoutPeriodSeconds(freshManager); err != nil || seconds != uint64(stakePayoutPeriodSeconds) {
		t.Fatalf("unset payout period = %d, %v; want the default %d", seconds, err, stakePayoutPeriodSeconds)
	}
	if period, err := fresh.stakingUnbondingPeriod(freshManager); err != nil || period != unbondingPeriod {
		t.Fatalf("unset unbonding period = %s, %v; want the default %s", period, err, unbondingPeriod)
	}
	mustParam(t, freshManager, governance.ParamKeyStakingPayoutPeriodDays, `"0"`)
	mustParam(t, freshManager, governance.ParamKeyStakingUnbondingDays, "0")
	if seconds, err := fresh.stakingPayoutPeriodSeconds(freshManager); err != nil || seconds != uint64(stakePayoutPeriodSeconds) {
		t.Fatalf("zero payout period = %d, %v; want the default %d", seconds, err, stakePayoutPeriodSeconds)
	}
	if period, err := fresh.stakingUnbondingPeriod(freshManager); err != nil || period != unbondingPeriod {
		t.Fatalf("zero unbonding period = %s, %v; want the default %s", period, err, unbondingPeriod)
	}

	// These per-transaction readers still refuse an unusable value: the
	// transaction that consulted it fails, nothing chain-wide does.
	mustParam(t, freshManager, governance.ParamKeyStakingPayoutPeriodDays, `"abc"`)
	mustParam(t, freshManager, governance.ParamKeyStakingUnbondingDays, "-3")
	if _, err := fresh.stakingPayoutPeriodSeconds(freshManager); err == nil {
		t.Fatalf("expected a malformed payout period to be rejected")
	}
	if _, err := fresh.stakingUnbondingPeriod(freshManager); err == nil {
		t.Fatalf("expected a malformed unbonding period to be rejected")
	}
}

func TestEmissionCapReadersAcceptQuotedParams(t *testing.T) {
	want := big.NewInt(1000)
	for _, spelling := range numberSpellings("1000") {
		sp, manager := newTestStateProcessor(t)
		mustParam(t, manager, governance.ParamKeyStakingMaxEmissionPerYearWei, spelling)
		mustParam(t, manager, governance.ParamKeyMintNHBMaxEmissionPerYearWei, spelling)
		mustParam(t, manager, governance.ParamKeyMintZNHBMaxEmissionPerYearWei, spelling)

		if got, err := sp.stakingMaxEmissionPerYear(manager); err != nil || got.Cmp(want) != 0 {
			t.Fatalf("staking cap %q = %v, %v; want %s", spelling, got, err, want)
		}
		for _, token := range []string{"NHB", "ZNHB"} {
			if got, err := sp.mintMaxEmissionPerYear(manager, token); err != nil || got.Cmp(want) != 0 {
				t.Fatalf("%s mint cap %q = %v, %v; want %s", token, spelling, got, err, want)
			}
		}
	}

	// Unset means no cap, exactly as before.
	sp, manager := newTestStateProcessor(t)
	if got, err := sp.stakingMaxEmissionPerYear(manager); err != nil || got.Sign() != 0 {
		t.Fatalf("unset staking cap = %v, %v; want 0", got, err)
	}
	if got, err := sp.mintMaxEmissionPerYear(manager, "NHB"); err != nil || got.Sign() != 0 {
		t.Fatalf("unset mint cap = %v, %v; want 0", got, err)
	}

	// A safety cap that cannot be read fails closed: falling back to the
	// default (0, meaning "no cap") would silently remove the limit.
	for _, raw := range []string{"abc", `"abc"`, "-1", `"-1"`} {
		mustParam(t, manager, governance.ParamKeyStakingMaxEmissionPerYearWei, raw)
		mustParam(t, manager, governance.ParamKeyMintNHBMaxEmissionPerYearWei, raw)
		if _, err := sp.stakingMaxEmissionPerYear(manager); err == nil {
			t.Fatalf("expected staking cap %q to be rejected", raw)
		}
		if _, err := sp.mintMaxEmissionPerYear(manager, "NHB"); err == nil {
			t.Fatalf("expected mint cap %q to be rejected", raw)
		}
	}
}

func TestGovernedWeiReadersAcceptQuotedParams(t *testing.T) {
	want := bigFromDecimal(t, "250000000000000000")
	for _, spelling := range numberSpellings("250000000000000000") {
		_, manager := newTestStateProcessor(t)
		mustParam(t, manager, governance.ParamKeyMarketFlatFeeWei, spelling)
		mustParam(t, manager, governance.ParamKeyPaymasterTopUpFeeWei, spelling)

		if got, err := readGovernedMarketFlatFeeWei(manager); err != nil || got.Cmp(want) != 0 {
			t.Fatalf("market flat fee %q = %v, %v; want %s", spelling, got, err, want)
		}
		if got, err := readGovernedPaymasterTopUpFeeWei(manager); err != nil || got.Cmp(want) != 0 {
			t.Fatalf("paymaster top-up fee %q = %v, %v; want %s", spelling, got, err, want)
		}
	}

	_, manager := newTestStateProcessor(t)
	if got, err := readGovernedMarketFlatFeeWei(manager); err != nil || got.Cmp(bigFromDecimal(t, defaultMarketFlatFeeWei)) != 0 {
		t.Fatalf("unset market flat fee = %v, %v; want the default", got, err)
	}
	mustParam(t, manager, governance.ParamKeyMarketFlatFeeWei, `"abc"`)
	if _, err := readGovernedMarketFlatFeeWei(manager); err == nil {
		t.Fatalf("expected a malformed market flat fee to be rejected")
	}
}

func TestSyncStakingParamsAcceptsQuotedValues(t *testing.T) {
	type expected struct {
		apr, payout, unbonding uint32
		minStake, maxEmission  string
		asset                  string
		compound               bool
	}
	want := expected{apr: 1750, payout: 14, unbonding: 21, minStake: "1000000000000000000", maxEmission: "9000000000000000000", asset: "ZNHB", compound: true}

	for name, values := range map[string]map[string]string{
		"bare": {
			governance.ParamKeyStakingAprBps:                "1750",
			governance.ParamKeyStakingPayoutPeriodDays:      "14",
			governance.ParamKeyStakingUnbondingDays:         "21",
			governance.ParamKeyStakingMinStakeWei:           "1000000000000000000",
			governance.ParamKeyStakingMaxEmissionPerYearWei: "9000000000000000000",
			governance.ParamKeyStakingRewardAsset:           `"ZNHB"`,
			governance.ParamKeyStakingCompoundDefault:       "true",
		},
		"quoted": {
			governance.ParamKeyStakingAprBps:                `"1750"`,
			governance.ParamKeyStakingPayoutPeriodDays:      `"14"`,
			governance.ParamKeyStakingUnbondingDays:         `"21"`,
			governance.ParamKeyStakingMinStakeWei:           `"1000000000000000000"`,
			governance.ParamKeyStakingMaxEmissionPerYearWei: `"9000000000000000000"`,
			governance.ParamKeyStakingRewardAsset:           `"ZNHB"`,
			governance.ParamKeyStakingCompoundDefault:       `"true"`,
		},
	} {
		values := values
		t.Run(name, func(t *testing.T) {
			node := newSyncStakingTestNode(t)
			node.stateMu.Lock()
			manager := nhbstate.NewManager(node.state.Trie)
			for key, raw := range values {
				mustParam(t, manager, key, raw)
			}
			node.stateMu.Unlock()

			if err := node.SyncStakingParams(); err != nil {
				t.Fatalf("SyncStakingParams: %v", err)
			}
			staking := node.globalConfigSnapshot().Staking
			got := expected{
				apr: staking.AprBps, payout: staking.PayoutPeriodDays, unbonding: staking.UnbondingDays,
				minStake: staking.MinStakeWei, maxEmission: staking.MaxEmissionPerYearWei,
				asset: staking.RewardAsset, compound: staking.CompoundDefault,
			}
			if got != want {
				t.Fatalf("merged staking config = %+v, want %+v", got, want)
			}
			node.stateMu.RLock()
			apr := node.state.StakeRewardAPR()
			node.stateMu.RUnlock()
			if apr != 1750 {
				t.Fatalf("runtime staking APR = %d, want 1750", apr)
			}
		})
	}
}

// TestSyncStakingParamsKeepsConfiguredValuesForMalformedParams: a stored value
// that cannot be read must not abort start-up; the configured value stays.
func TestSyncStakingParamsKeepsConfiguredValuesForMalformedParams(t *testing.T) {
	capture := &paramLogCapture{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	node := newSyncStakingTestNode(t)
	before := node.globalConfigSnapshot().Staking

	// Values unique to this run: a malformed value is reported once per process,
	// so fixed values would be swallowed on a repeated run (-count).
	nonce := paramTestRuns.Add(1)
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	for key, raw := range map[string]string{
		governance.ParamKeyStakingAprBps:                fmt.Sprintf("sync-test-not-a-number-%d", nonce),
		governance.ParamKeyStakingPayoutPeriodDays:      fmt.Sprintf(`"%d"`, 5_000_000_000+nonce), // beyond uint32
		governance.ParamKeyStakingUnbondingDays:         fmt.Sprintf("-%d", 1+nonce),
		governance.ParamKeyStakingMinStakeWei:           fmt.Sprintf(`"lots-%d"`, nonce),
		governance.ParamKeyStakingMaxEmissionPerYearWei: fmt.Sprintf("1.%d", nonce),
		governance.ParamKeyStakingCompoundDefault:       fmt.Sprintf(`"maybe-%d"`, nonce),
	} {
		mustParam(t, manager, key, raw)
	}
	node.stateMu.Unlock()

	if err := node.SyncStakingParams(); err != nil {
		t.Fatalf("SyncStakingParams must not fail on malformed params: %v", err)
	}
	after := node.globalConfigSnapshot().Staking
	if after != before {
		t.Fatalf("staking config changed for malformed params: got %+v, want %+v", after, before)
	}
	if err := node.ValidateStakingConfig(); err != nil {
		t.Fatalf("ValidateStakingConfig after a malformed param: %v", err)
	}

	// Each unusable value is reported loudly, by parameter name.
	reported := capture.errorParams()
	for _, key := range []string{
		governance.ParamKeyStakingAprBps,
		governance.ParamKeyStakingPayoutPeriodDays,
		governance.ParamKeyStakingUnbondingDays,
		governance.ParamKeyStakingMinStakeWei,
		governance.ParamKeyStakingMaxEmissionPerYearWei,
		governance.ParamKeyStakingCompoundDefault,
	} {
		if !reported[key] {
			t.Fatalf("expected an error-level log record for malformed %s, got %v", key, reported)
		}
	}
}

var paramTestRuns atomic.Uint64

type paramLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *paramLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *paramLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *paramLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *paramLogCapture) WithGroup(string) slog.Handler      { return h }

// errorParams returns the parameter names named by error-level records.
func (h *paramLogCapture) errorParams() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	for _, r := range h.records {
		if r.Level < slog.LevelError {
			continue
		}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "param" {
				out[a.Value.String()] = true
			}
			return true
		})
	}
	return out
}

func newSyncStakingTestNode(t *testing.T) *Node {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	node, err := NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

// TestStakePreviewClaimAcceptsQuotedPayoutPeriod covers the payout-period read
// used by the claim preview.
func TestStakePreviewClaimAcceptsQuotedPayoutPeriod(t *testing.T) {
	var addr [20]byte
	addr[19] = 0x77
	lastPayout := uint64(1_800_000_000)

	nextPayoutFor := func(t *testing.T, raw string) uint64 {
		t.Helper()
		node := newSyncStakingTestNode(t)
		node.stateMu.Lock()
		manager := nhbstate.NewManager(node.state.Trie)
		if raw != "" {
			mustParam(t, manager, governance.ParamKeyStakingPayoutPeriodDays, raw)
		}
		account := &types.Account{
			BalanceNHB:        big.NewInt(0),
			BalanceZNHB:       big.NewInt(0),
			Stake:             big.NewInt(0),
			StakeShares:       big.NewInt(0),
			StakeLastPayoutTs: lastPayout,
		}
		if err := manager.PutAccount(addr[:], account); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("put account: %v", err)
		}
		node.stateMu.Unlock()

		_, next, err := node.StakePreviewClaim(addr, time.Unix(int64(lastPayout), 0).UTC())
		if err != nil {
			t.Fatalf("preview claim: %v", err)
		}
		return next
	}

	if got, want := nextPayoutFor(t, ""), lastPayout+30*86400; got != want {
		t.Fatalf("default payout period: next payout %d, want %d", got, want)
	}
	for _, spelling := range numberSpellings("14") {
		if got, want := nextPayoutFor(t, spelling), lastPayout+14*86400; got != want {
			t.Fatalf("payout period %q: next payout %d, want %d", spelling, got, want)
		}
	}
}
