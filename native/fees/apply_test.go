package fees

import (
	"sync"
	"testing"
)

func resetFreeTierWarnings() {
	freeTierDefaultWarned = sync.Map{}
}

func TestDomainPolicyNormalizedFreeTierTxPerMonth(t *testing.T) {
	t.Run("defaults when unset", func(t *testing.T) {
		resetFreeTierWarnings()
		policy := DomainPolicy{}
		normalized := policy.normalized("Example.COM")
		if normalized.FreeTierTxPerMonth != DefaultFreeTierTxPerMonth {
			t.Fatalf("expected default free tier %d, got %d", DefaultFreeTierTxPerMonth, normalized.FreeTierTxPerMonth)
		}
		if !normalized.FreeTierTxPerMonthSet {
			t.Fatalf("expected free tier flag to be set after normalization")
		}
		if _, logged := freeTierDefaultWarned.Load("example.com"); !logged {
			t.Fatalf("expected domain to be marked as defaulted")
		}
	})

	t.Run("preserves explicit zero", func(t *testing.T) {
		resetFreeTierWarnings()
		policy := DomainPolicy{FreeTierTxPerMonthSet: true}
		normalized := policy.normalized("explicit.zero")
		if normalized.FreeTierTxPerMonth != 0 {
			t.Fatalf("expected zero free tier to persist, got %d", normalized.FreeTierTxPerMonth)
		}
		if !normalized.FreeTierTxPerMonthSet {
			t.Fatalf("expected explicit zero to remain marked as set")
		}
		if _, logged := freeTierDefaultWarned.Load("explicit.zero"); logged {
			t.Fatalf("did not expect default warning for explicit zero configuration")
		}
	})

	t.Run("preserves positive configuration", func(t *testing.T) {
		resetFreeTierWarnings()
		policy := DomainPolicy{FreeTierTxPerMonth: 42}
		normalized := policy.normalized("positive.example")
		if normalized.FreeTierTxPerMonth != 42 {
			t.Fatalf("expected configured free tier to persist, got %d", normalized.FreeTierTxPerMonth)
		}
		if !normalized.FreeTierTxPerMonthSet {
			t.Fatalf("expected positive value to be marked as set")
		}
		if _, logged := freeTierDefaultWarned.Load("positive.example"); logged {
			t.Fatalf("did not expect default warning for positive configuration")
		}
	})
}
