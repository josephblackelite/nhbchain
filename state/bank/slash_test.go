package bank

import (
	"math/big"
	"testing"
)

func TestNoopSlasherDisabled(t *testing.T) {
	s := NewNoopSlasher(false)
	if err := s.Slash([20]byte{}, big.NewInt(10)); err == nil {
		t.Fatalf("expected error when disabled")
	}
}

func TestNoopSlasherZeroAmount(t *testing.T) {
	s := NewNoopSlasher(false)
	if err := s.Slash([20]byte{}, big.NewInt(0)); err != nil {
		t.Fatalf("expected no error for zero amount: %v", err)
	}
}

func TestNoopSlasherNegativeAmount(t *testing.T) {
	s := NewNoopSlasher(true)
	if err := s.Slash([20]byte{}, big.NewInt(-1)); err == nil {
		t.Fatalf("expected negative amount error")
	}
}
