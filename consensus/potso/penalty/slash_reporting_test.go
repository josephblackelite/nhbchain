package penalty

import (
	"math/big"
	"testing"

	statepotso "nhbchain/state/potso"
)

// cappedSlasher forfeits at most limit, like a slasher whose offender has less
// bonded than the penalty asks for, and says how much it took.
type cappedSlasher struct {
	limit *big.Int
	asked *big.Int
}

func (s *cappedSlasher) take(amount *big.Int) *big.Int {
	s.asked = new(big.Int).Set(amount)
	if amount.Cmp(s.limit) > 0 {
		return new(big.Int).Set(s.limit)
	}
	return new(big.Int).Set(amount)
}

func (s *cappedSlasher) Slash(addr [20]byte, amount *big.Int) error {
	s.take(amount)
	return nil
}

func (s *cappedSlasher) SlashApplied(addr [20]byte, amount *big.Int) (*big.Int, error) {
	return s.take(amount), nil
}

func engineWithSlasher(t *testing.T, offender [20]byte, slasher interface {
	Slash(addr [20]byte, amount *big.Int) error
}) *Engine {
	t.Helper()
	ledger, err := statepotso.NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	if _, err := ledger.EnsureBaseline(offender, big.NewInt(800)); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	cfg := DefaultConfig()
	cfg.SlashEnabled = true
	cfg.EquivocationSlashBps = 10000
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return NewEngine(catalog, ledger, slasher).WithRecords(newMemRecords())
}

// A penalty asks for 800 but the offender only has 300 bonded: the result and
// the event report the 300 that was forfeited, not the 800 that was asked for.
func TestEngineReportsTheAmountTheSlasherForfeited(t *testing.T) {
	offender := [20]byte{7}
	slasher := &cappedSlasher{limit: big.NewInt(300)}
	engine := engineWithSlasher(t, offender, slasher)

	res, err := engine.Apply(equivocationRecord(offender), Context{BlockHeight: 10})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if slasher.asked == nil || slasher.asked.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("the slasher was asked for %v, want 800", slasher.asked)
	}
	if res.SlashApplied.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("result reports %s slashed, want the 300 the slasher forfeited", res.SlashApplied)
	}
	if got := res.Event.Attributes["slashAmt"]; got != "300" {
		t.Fatalf("event slashAmt = %q, want 300", got)
	}
}

// A slasher that cannot report what it forfeited is taken to have forfeited
// what it was asked for, as it always was.
func TestEngineFallsBackToTheRequestedAmountForASlasherThatCannotReport(t *testing.T) {
	offender := [20]byte{8}
	slasher := &countingSlasher{}
	engine := engineWithSlasher(t, offender, slasher)

	res, err := engine.Apply(equivocationRecord(offender), Context{BlockHeight: 10})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if slasher.calls != 1 || res.SlashApplied.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("expected one slash reported as 800, got calls=%d slash=%s", slasher.calls, res.SlashApplied)
	}
	if got := res.Event.Attributes["slashAmt"]; got != "800" {
		t.Fatalf("event slashAmt = %q, want 800", got)
	}
}
