package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEstimateDecodesNumericAmounts proves the exact failure confirmed live
// against the running payments-gateway on 2026-08-05: NOWPayments' real
// /estimate response returns amount_from (and, per their API docs,
// potentially amount_to/estimated_amount) as JSON numbers, not strings.
// Before flexibleAmount, json.Decode aborted the entire response on that
// type mismatch -- "json: cannot unmarshal number into Go struct field
// NowPaymentsEstimate.amount_from of type string" -- which meant Buy NHB
// failed every time a pay currency needed cross-conversion, even though
// nothing downstream ever reads AmountFrom.
func TestEstimateDecodesNumericAmounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"currency_from": "usd",
			"amount_from": 100,
			"currency_to": "usdttrc20",
			"estimated_amount": 99.87654321
		}`))
	}))
	defer server.Close()

	client := NewHTTPNowPaymentsClient(server.URL, "test-key")
	estimate, err := client.Estimate(context.Background(), &NowPaymentsEstimateRequest{
		Amount:       "100",
		CurrencyFrom: "usd",
		CurrencyTo:   "usdttrc20",
	})
	if err != nil {
		t.Fatalf("expected success decoding a numeric amount_from, got %v", err)
	}
	if string(estimate.AmountFrom) != "100" {
		t.Fatalf("unexpected amount_from: %q", estimate.AmountFrom)
	}
	if string(estimate.EstimatedAmount) != "99.87654321" {
		t.Fatalf("unexpected estimated_amount: %q", estimate.EstimatedAmount)
	}
}

// TestEstimateStillDecodesStringAmounts guards the other direction: some
// NOWPayments endpoints (or a future API revision) may still send these as
// quoted strings -- flexibleAmount must accept both forms, not just numbers.
func TestEstimateStillDecodesStringAmounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"currency_from": "usd",
			"amount_from": "100",
			"currency_to": "usdttrc20",
			"estimated_amount": "99.87654321"
		}`))
	}))
	defer server.Close()

	client := NewHTTPNowPaymentsClient(server.URL, "test-key")
	estimate, err := client.Estimate(context.Background(), &NowPaymentsEstimateRequest{
		Amount:       "100",
		CurrencyFrom: "usd",
		CurrencyTo:   "usdttrc20",
	})
	if err != nil {
		t.Fatalf("expected success decoding a string amount_from, got %v", err)
	}
	if string(estimate.AmountFrom) != "100" {
		t.Fatalf("unexpected amount_from: %q", estimate.AmountFrom)
	}
	if string(estimate.EstimatedAmount) != "99.87654321" {
		t.Fatalf("unexpected estimated_amount: %q", estimate.EstimatedAmount)
	}
}

// TestCreateInvoiceDecodesNumericPriceAmount covers the same bug class for
// invoice creation -- untested live since Estimate always failed first, but
// NOWPayments' invoice response is the same API family and plausibly shares
// the numeric-amount convention.
func TestCreateInvoiceDecodesNumericPriceAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "np-1",
			"invoice_id": "np-1",
			"order_id": "order-1",
			"price_amount": 100.5,
			"price_currency": "usd",
			"pay_currency": "usdttrc20",
			"invoice_url": "https://nowpay/invoice/np-1"
		}`))
	}))
	defer server.Close()

	client := NewHTTPNowPaymentsClient(server.URL, "test-key")
	invoice, err := client.CreateInvoice(context.Background(), &NowPaymentsInvoiceRequest{
		PriceAmount:   "100.5",
		PriceCurrency: "usd",
		PayCurrency:   "usdttrc20",
		OrderID:       "order-1",
	})
	if err != nil {
		t.Fatalf("expected success decoding a numeric price_amount, got %v", err)
	}
	if string(invoice.PriceAmount) != "100.5" {
		t.Fatalf("unexpected price_amount: %q", invoice.PriceAmount)
	}
}

// TestFlexibleAmountRoundTripsThroughEncoding proves flexibleAmount marshals
// back out as a plain JSON string (its zero-friction default via the
// underlying string type -- no custom MarshalJSON needed) so nothing that
// re-serializes a decoded NowPaymentsInvoice/NowPaymentsEstimate breaks.
func TestFlexibleAmountRoundTripsThroughEncoding(t *testing.T) {
	var a flexibleAmount
	if err := json.Unmarshal([]byte(`42.5`), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `"42.5"` {
		t.Fatalf("unexpected round-trip encoding: %s", encoded)
	}
}
