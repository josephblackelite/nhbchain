package governance

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bigFromString(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer literal %q", s)
	}
	return v
}

// TestMinimumValidatorStakeFromParamRoundTrip covers every spelling
// proposal validation accepts for staking.minimumValidatorStake, which
// governance persists verbatim: a bare number and a quoted decimal string
// must decode to the same amount.
func TestMinimumValidatorStakeFromParamRoundTrip(t *testing.T) {
	amount := bigFromString(t, "20000000000000000000000")
	for _, raw := range []string{
		`20000000000000000000000`,
		`"20000000000000000000000"`,
		" 20000000000000000000000 ",
		` "20000000000000000000000" `,
		`"  20000000000000000000000  "`,
		`"+20000000000000000000000"`,
		`"++20000000000000000000000"`,
		`+20000000000000000000000`,
	} {
		if got := MinimumValidatorStakeFromParam([]byte(raw)); got.Cmp(amount) != 0 {
			t.Errorf("MinimumValidatorStakeFromParam(%q) = %s, want %s", raw, got, amount)
		}
	}
	if got := MinimumValidatorStakeFromParam([]byte("1")); got.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("a stored 1 must stay 1, got %s", got)
	}
}

func TestMinimumValidatorStakeFromParamDefaults(t *testing.T) {
	def := DefaultMinimumValidatorStake()
	// Unset or blank means "not configured": the documented default.
	for _, raw := range []string{"", "   ", `""`, `"  "`} {
		if got := MinimumValidatorStakeFromParam([]byte(raw)); got.Cmp(def) != 0 {
			t.Errorf("MinimumValidatorStakeFromParam(%q) = %s, want the default %s", raw, got, def)
		}
	}
	// Anything that is not a positive base-10 integer cannot come through
	// proposal validation. It degrades to the default (never an error that
	// would fail every account write) and is reported.
	for _, raw := range []string{"abc", `"abc"`, "-5", `"-5"`, "0", `"0"`, "1.5", `"1e3"`, "true", `{"a":1}`, `"12`, "0x10", "1_000"} {
		got := MinimumValidatorStakeFromParam([]byte(raw))
		if got == nil || got.Cmp(def) != 0 {
			t.Errorf("MinimumValidatorStakeFromParam(%q) = %v, want the default %s", raw, got, def)
		}
	}
}

// TestExecuteQuotedParamIsStoredVerbatimAndReadable pins both halves of the
// DC-02 fix: applying a param.update whose numeric value is quoted still
// stores the proposer's exact bytes (a replaying node must reproduce
// identical state), and the reader accepts what was stored.
func TestExecuteQuotedParamIsStoredVerbatimAndReadable(t *testing.T) {
	now := time.Unix(1_700_007_000, 0).UTC()
	state := newMockGovernanceState(nil)
	proposal := &Proposal{
		ID:             9,
		Status:         ProposalStatusPassed,
		TimelockEnd:    now.Add(-time.Second),
		Target:         ProposalKindParamUpdate,
		ProposedChange: `{"staking.minimumValidatorStake":"20000000000000000000000"}`,
		Queued:         true,
	}
	if err := state.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{AllowedParams: []string{ParamKeyMinimumValidatorStake}})
	if err := engine.Execute(9); err != nil {
		t.Fatalf("execute proposal: %v", err)
	}

	stored, ok := state.ParamStoreGet(ParamKeyMinimumValidatorStake)
	if !ok {
		t.Fatalf("expected staking.minimumValidatorStake to be stored")
	}
	if string(stored) != `"20000000000000000000000"` {
		t.Fatalf("stored bytes changed: got %q, want the proposer's exact quoted string", stored)
	}
	want := bigFromString(t, "20000000000000000000000")
	if got := MinimumValidatorStakeFromParam(stored); got.Cmp(want) != 0 {
		t.Fatalf("stored quoted value decodes to %s, want %s", got, want)
	}
}

var loudLogRuns atomic.Uint64

type stakeParamLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *stakeParamLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *stakeParamLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *stakeParamLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *stakeParamLogCapture) WithGroup(string) slog.Handler      { return h }

// TestMalformedMinimumValidatorStakeIsReportedLoudly: degrading to the default
// must not be silent.
func TestMalformedMinimumValidatorStakeIsReportedLoudly(t *testing.T) {
	capture := &stakeParamLogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// A value unique to this run, so the once-per-value deduplication cannot
	// have swallowed the report (a repeated run with -count is another run).
	MinimumValidatorStakeFromParam([]byte(fmt.Sprintf("loud-log-check-not-a-number-%d", loudLogRuns.Add(1))))

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.records) != 1 {
		t.Fatalf("expected exactly one log record, got %d", len(capture.records))
	}
	record := capture.records[0]
	if record.Level != slog.LevelError {
		t.Fatalf("expected an error-level record, got %s", record.Level)
	}
	found := false
	record.Attrs(func(a slog.Attr) bool {
		if a.Key == "param" && a.Value.String() == ParamKeyMinimumValidatorStake {
			found = true
		}
		return true
	})
	if !found {
		t.Fatalf("log record does not name the parameter %s", ParamKeyMinimumValidatorStake)
	}
}
