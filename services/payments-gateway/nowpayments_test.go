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

// TestCreatePaymentSendsFixedRateAndFeePolicy proves the headless payment
// client sends the exact same rate/fee policy as invoice creation: NOWPayments
// absorbs FX risk (is_fixed_rate=true) and the payer -- not the NHBCoin
// treasury -- bears NOWPayments' processing fee (is_fee_paid_by_user=true).
func TestCreatePaymentSendsFixedRateAndFeePolicy(t *testing.T) {
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"payment_id": "np-payment-1",
			"pay_address": "bc1qexampleaddress",
			"pay_amount": 0.0012,
			"pay_currency": "btc",
			"payment_status": "waiting"
		}`))
	}))
	defer server.Close()

	client := NewHTTPNowPaymentsClient(server.URL, "test-key")
	payment, err := client.CreatePayment(context.Background(), &NowPaymentsPaymentRequest{
		PriceAmount:     "100.00",
		PriceCurrency:   "usd",
		PayCurrency:     "btc",
		OrderID:         "order-1",
		FixedRate:       true,
		IsFeePaidByUser: true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if captured["is_fixed_rate"] != true {
		t.Fatalf("expected is_fixed_rate=true in request body, got %v", captured["is_fixed_rate"])
	}
	if captured["is_fee_paid_by_user"] != true {
		t.Fatalf("expected is_fee_paid_by_user=true in request body, got %v", captured["is_fee_paid_by_user"])
	}
	if payment.PayAddress != "bc1qexampleaddress" {
		t.Fatalf("unexpected pay_address: %s", payment.PayAddress)
	}
	if string(payment.PayAmount) != "0.0012" {
		t.Fatalf("unexpected pay_amount: %s", payment.PayAmount)
	}
}

// TestCreatePaymentDecodesNumericPaymentID guards against the same bug class
// flexibleAmount was created for: NOWPayments' payment product commonly
// returns payment_id as a bare JSON number, which would abort the entire
// decode if the field were a plain string.
func TestCreatePaymentDecodesNumericPaymentID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"payment_id": 5077125097,
			"pay_address": "addr",
			"pay_amount": 0.5,
			"pay_currency": "ltc",
			"payment_status": "waiting"
		}`))
	}))
	defer server.Close()

	client := NewHTTPNowPaymentsClient(server.URL, "test-key")
	payment, err := client.CreatePayment(context.Background(), &NowPaymentsPaymentRequest{
		PriceAmount: "50.00", PriceCurrency: "usd", PayCurrency: "ltc", OrderID: "order-2",
	})
	if err != nil {
		t.Fatalf("expected success decoding a numeric payment_id, got %v", err)
	}
	if payment.NowPaymentsID() != "5077125097" {
		t.Fatalf("unexpected payment id: %q", payment.NowPaymentsID())
	}
}

// TestListMerchantCoinsParsesKnownShapes covers the response-shape variance
// NOWPayments' merchant/coins endpoint has been observed to return.
func TestListMerchantCoinsParsesKnownShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{name: "bare array", body: `["btc","eth","usdttrc20"]`, want: []string{"btc", "eth", "usdttrc20"}},
		{name: "selectedCurrencies wrapper", body: `{"selectedCurrencies":["btc","ltc"]}`, want: []string{"btc", "ltc"}},
		{name: "currencies wrapper", body: `{"currencies":["sol","trx"]}`, want: []string{"sol", "trx"}},
		{name: "result wrapper", body: `{"result":["bnb","doge"]}`, want: []string{"bnb", "doge"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := NewHTTPNowPaymentsClient(server.URL, "test-key")
			coins, err := client.ListMerchantCoins(context.Background())
			if err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if len(coins) != len(tc.want) {
				t.Fatalf("unexpected coins: got %v want %v", coins, tc.want)
			}
			for i, c := range tc.want {
				if coins[i] != c {
					t.Fatalf("unexpected coins: got %v want %v", coins, tc.want)
				}
			}
		})
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
