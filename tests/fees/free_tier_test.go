package fees_test

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/fees"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestApplyFreeTierThreshold(t *testing.T) {
	value := big.NewInt(1_000)
	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
	}
	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 100,
		MDRBasisPoints:     150,
		OwnerWallet:        owner,
	}
	monthStart := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	usage := uint64(0)
	for i := 0; i < 102; i++ {
		result := fees.Apply(fees.ApplyInput{
			Domain:        fees.DomainPOS,
			Gross:         value,
			UsageCount:    usage,
			PolicyVersion: 1,
			Config:        policy,
			WindowStart:   monthStart,
		})
		usage = result.Counter
		if i < 100 {
			if !result.FreeTierApplied {
				t.Fatalf("expected free tier for tx %d", i)
			}
			if result.Fee.Sign() != 0 {
				t.Fatalf("expected zero fee during free tier")
			}
		} else {
			if result.FreeTierApplied {
				t.Fatalf("expected fee to apply after free tier exhaustion")
			}
			expectedFee := new(big.Int).Mul(value, big.NewInt(int64(policy.MDRBasisPoints)))
			expectedFee.Div(expectedFee, big.NewInt(10_000))
			if result.Fee.Cmp(expectedFee) != 0 {
				t.Fatalf("unexpected fee amount: got %s want %s", result.Fee, expectedFee)
			}
			if result.OwnerWallet != owner {
				t.Fatalf("unexpected owner wallet: %x", result.OwnerWallet)
			}
			if result.FreeTierRemaining != 0 {
				t.Fatalf("expected free tier to be exhausted, remaining=%d", result.FreeTierRemaining)
			}
		}
	}
}

func TestMonthlyCounterReset(t *testing.T) {
	db := storage.NewMemDB()
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := nhbstate.NewManager(trie)

	var payer [20]byte
	for i := range payer {
		payer[i] = 0xAA
	}
	jan := time.Date(2024, time.January, 10, 0, 0, 0, 0, time.UTC)
	janStart := monthStartUTC(jan)
	if err := manager.FeesPutCounter(fees.DomainPOS, payer, 50, janStart); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	count, windowStart, ok, err := manager.FeesGetCounter(fees.DomainPOS, payer)
	if err != nil || !ok {
		t.Fatalf("load counter: %v (ok=%v)", err, ok)
	}
	if count != 50 {
		t.Fatalf("unexpected count: %d", count)
	}
	if !windowStart.Equal(janStart) {
		t.Fatalf("unexpected window start: %s", windowStart)
	}

	feb := time.Date(2024, time.February, 2, 0, 0, 0, 0, time.UTC)
	febStart := monthStartUTC(feb)
	usage := count
	if !sameMonth(windowStart, febStart) {
		usage = 0
		windowStart = febStart
	}

	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 100,
		MDRBasisPoints:     150,
	}
	result := fees.Apply(fees.ApplyInput{
		Domain:        fees.DomainPOS,
		Gross:         big.NewInt(500),
		UsageCount:    usage,
		PolicyVersion: 2,
		Config:        policy,
		WindowStart:   windowStart,
	})
	if !result.FreeTierApplied {
		t.Fatalf("expected free tier to apply after rollover")
	}
	if result.Counter != 1 {
		t.Fatalf("expected counter 1 after rollover, got %d", result.Counter)
	}
	if result.FreeTierRemaining != 99 {
		t.Fatalf("expected 99 remaining, got %d", result.FreeTierRemaining)
	}
	if !result.WindowStart.Equal(febStart) {
		t.Fatalf("unexpected window start in result: %s", result.WindowStart)
	}

	if err := manager.FeesPutCounter(fees.DomainPOS, payer, result.Counter, result.WindowStart); err != nil {
		t.Fatalf("update counter: %v", err)
	}
	stored, newWindow, ok, err := manager.FeesGetCounter(fees.DomainPOS, payer)
	if err != nil || !ok {
		t.Fatalf("reload counter: %v (ok=%v)", err, ok)
	}
	if stored != 1 {
		t.Fatalf("unexpected stored count: %d", stored)
	}
	if !newWindow.Equal(febStart) {
		t.Fatalf("unexpected stored window start: %s", newWindow)
	}
}

func monthStartUTC(ts time.Time) time.Time {
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func sameMonth(a, b time.Time) bool {
	ua := a.UTC()
	ub := b.UTC()
	return ua.Year() == ub.Year() && ua.Month() == ub.Month()
}
