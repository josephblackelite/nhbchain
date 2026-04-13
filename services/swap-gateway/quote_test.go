package main

import (
	"context"
	"testing"
)

func TestQuoteFixedPrice(t *testing.T) {
	quoter, err := NewQuoter("fixed:0.10")
	if err != nil {
		t.Fatalf("NewQuoter: %v", err)
	}
	rate, minted, err := quoter.Quote(context.Background(), "USD", "100.00")
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if rate != "0.10" {
		t.Fatalf("unexpected rate %s", rate)
	}
	expected := "1000000000000000000000"
	if minted != expected {
		t.Fatalf("expected %s minted, got %s", expected, minted)
	}
}
