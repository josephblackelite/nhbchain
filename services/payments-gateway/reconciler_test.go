package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecimalToWeiString(t *testing.T) {
	cases := []struct {
		name    string
		amount  string
		want    string
		wantErr bool
	}{
		{name: "whole number", amount: "20", want: "20000000000000000000"},
		{name: "fractional", amount: "14.523456", want: "14523456000000000000"},
		{name: "max precision", amount: "0.000000001", want: "1000000000"},
		{name: "leading/trailing space", amount: "  20  ", want: "20000000000000000000"},
		{name: "zero rejected", amount: "0", wantErr: true},
		{name: "negative rejected", amount: "-5", wantErr: true},
		{name: "not a number", amount: "abc", wantErr: true},
		{name: "empty", amount: "", wantErr: true},
		{name: "more precision than the chain supports", amount: "1.1234567890123456789", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decimalToWeiString(tc.amount, mintDecimals)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tc.amount, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.amount, err)
			}
			if got != tc.want {
				t.Fatalf("decimalToWeiString(%q) = %q, want %q", tc.amount, got, tc.want)
			}
		})
	}
}

func TestDeductFeeFromAmount(t *testing.T) {
	cases := []struct {
		name    string
		amount  string
		feeBps  int
		wantFee string
		wantNet string
		wantErr bool
	}{
		{name: "no fee", amount: "17.5", feeBps: 0, wantFee: "0", wantNet: "17.5"},
		{name: "worked example from the product spec: $20 in, $17.5 nets from nowpayments, our own 1 fee -> mint 16.5", amount: "17.5", feeBps: 571, wantFee: "0.99925", wantNet: "16.50075"},
		{name: "fee exceeds amount clamps to zero, not negative", amount: "1", feeBps: 15000, wantFee: "1.5", wantNet: "0"},
		{name: "invalid amount", amount: "not-a-number", feeBps: 100, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, net, err := deductFeeFromAmount(tc.amount, tc.feeBps)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got fee=%q net=%q", fee, net)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fee != tc.wantFee || net != tc.wantNet {
				t.Fatalf("deductFeeFromAmount(%q, %d) = (%q, %q), want (%q, %q)", tc.amount, tc.feeBps, fee, net, tc.wantFee, tc.wantNet)
			}
		})
	}
}

// backdatePaymentUpdatedAt directly rewrites a payment row's updated_at,
// bypassing UpdatePaymentStatus (which always stamps real wall-clock time,
// not the server's injectable nowFn) -- the only way to put a row inside
// the reconciler's grace window without a real-time sleep in a test.
func backdatePaymentUpdatedAt(t *testing.T, store *SQLiteStore, paymentID string, when time.Time) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `UPDATE payments SET updated_at = ? WHERE id = ?`, when.UTC(), paymentID); err != nil {
		t.Fatalf("backdate payment %s: %v", paymentID, err)
	}
}

// TestReconcilerSettlesStalePartiallyPaidPayment covers the core "no manual
// review, ever" promise: a payment that arrived short, with nothing further
// coming, gets minted for whatever actually netted to us once its grace
// window has elapsed -- with no webhook needed to trigger it.
func TestReconcilerSettlesStalePartiallyPaidPayment(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	npID := "np-reconcile-1"
	np := &stubNowPayments{
		createPaymentFn: func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
			return &NowPaymentsPayment{PaymentID: flexibleAmount(npID), PayAddress: "addr1", PayAmount: flexibleAmount("0.001"), PayCurrency: req.PayCurrency, PaymentStatus: "waiting"}, nil
		},
		getPaymentFn: func(ctx context.Context, id string) (*NowPaymentsPayment, error) {
			return &NowPaymentsPayment{PaymentID: flexibleAmount(id), PaymentStatus: "partially_paid", OutcomeAmount: flexibleAmount("18.813424")}, nil
		},
	}
	node := &stubNode{}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"20"}`)
	created := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1erin", "webhook-reconcile-1")

	webhook := NowPaymentsWebhookPayload{PaymentID: flexibleAmount(npID), OrderID: created["paymentId"], PaymentStatus: "partially_paid"}
	body, _ := json.Marshal(webhook)
	sig := computeTestHMAC("secret", body)
	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq.Header.Set(headerNowPaymentsSig, sig)
	whRes := httptest.NewRecorder()
	srv.ServeHTTP(whRes, whReq)
	if whRes.Code != http.StatusOK {
		t.Fatalf("webhook failed: %s", whRes.Body.String())
	}
	if node.callCount != 0 {
		t.Fatalf("expected no mint yet on first partially_paid webhook, got %d calls", node.callCount)
	}
	payment, err := store.GetPayment(context.Background(), created["paymentId"])
	if err != nil || payment == nil {
		t.Fatalf("fetch payment: %v", err)
	}
	if payment.Status != "partially_paid" {
		t.Fatalf("expected partially_paid status, got %s", payment.Status)
	}

	// Not stale yet -- the reconciler must leave it alone.
	srv.reconcileStalePartialPayments(context.Background())
	if node.callCount != 0 {
		t.Fatalf("expected reconciler to skip a fresh partially_paid payment, got %d mint calls", node.callCount)
	}

	// Backdate past the grace window and sweep again.
	backdatePaymentUpdatedAt(t, store, created["paymentId"], srv.nowFn().UTC().Add(-partiallyPaidGraceWindow-time.Minute))
	srv.reconcileStalePartialPayments(context.Background())

	if node.callCount != 1 {
		t.Fatalf("expected exactly one mint call after grace window elapsed, got %d", node.callCount)
	}
	wantWeiAmount := "18813424000000000000"
	if node.lastVoucher.Amount != wantWeiAmount {
		t.Fatalf("unexpected voucher amount: got %s want %s", node.lastVoucher.Amount, wantWeiAmount)
	}
	if node.lastVoucher.Recipient != "nhb1erin" {
		t.Fatalf("unexpected voucher recipient: %s", node.lastVoucher.Recipient)
	}

	settled, err := store.GetPayment(context.Background(), created["paymentId"])
	if err != nil || settled == nil {
		t.Fatalf("fetch settled payment: %v", err)
	}
	if settled.Status != "minted" || !settled.TxHash.Valid {
		t.Fatalf("expected payment marked minted with a tx hash, got %+v", settled)
	}

	// A second sweep must not mint again.
	srv.reconcileStalePartialPayments(context.Background())
	if node.callCount != 1 {
		t.Fatalf("expected mint call count to stay at 1 on a repeat sweep, got %d", node.callCount)
	}
}

// TestReconcilerLeavesStatusAloneOnConcurrentDuplicateMint covers the race
// where a webhook settles a payment between the reconciler's stale-list
// query and its own settlement attempt: the reconciler's own mint call gets
// rejected by the chain's replay protection (ErrMintDuplicate), and it must
// not clobber whatever status the webhook path already wrote.
func TestReconcilerLeavesStatusAloneOnConcurrentDuplicateMint(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { store.Close() })
	npID := "np-reconcile-race"
	np := &stubNowPayments{
		createPaymentFn: func(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
			return &NowPaymentsPayment{PaymentID: flexibleAmount(npID), PayAddress: "addr1", PayAmount: flexibleAmount("0.001"), PayCurrency: req.PayCurrency, PaymentStatus: "waiting"}, nil
		},
		getPaymentFn: func(ctx context.Context, id string) (*NowPaymentsPayment, error) {
			return &NowPaymentsPayment{PaymentID: flexibleAmount(id), PaymentStatus: "partially_paid", OutcomeAmount: flexibleAmount("9.5")}, nil
		},
	}
	node := &stubNode{err: ErrMintDuplicate}
	signer := &stubSigner{}
	srv := newTestServer(t, store, np, node, signer)

	quote := createTestQuote(t, srv, `{"fiat":"USD","mintAsset":"NHB","payCurrency":"BTC","amountMint":"10"}`)
	created := createTestPayment(t, srv, quote.QuoteID, "BTC", "nhb1frank", "webhook-reconcile-race")

	webhook := NowPaymentsWebhookPayload{PaymentID: flexibleAmount(npID), OrderID: created["paymentId"], PaymentStatus: "partially_paid"}
	body, _ := json.Marshal(webhook)
	sig := computeTestHMAC("secret", body)
	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/nowpayments", bytes.NewReader(body))
	whReq.Header.Set(headerNowPaymentsSig, sig)
	whRes := httptest.NewRecorder()
	srv.ServeHTTP(whRes, whReq)
	if whRes.Code != http.StatusOK {
		t.Fatalf("webhook failed: %s", whRes.Body.String())
	}

	backdatePaymentUpdatedAt(t, store, created["paymentId"], srv.nowFn().UTC().Add(-partiallyPaidGraceWindow-time.Minute))
	srv.reconcileStalePartialPayments(context.Background())

	// Confirms the reconciler actually reached the settlement attempt (and
	// hit the simulated duplicate-mint error) rather than trivially passing
	// because it found nothing stale to begin with.
	if node.callCount != 1 {
		t.Fatalf("expected the reconciler to attempt exactly one mint call, got %d", node.callCount)
	}

	settled, err := store.GetPayment(context.Background(), created["paymentId"])
	if err != nil || settled == nil {
		t.Fatalf("fetch payment: %v", err)
	}
	if settled.Status != "partially_paid" {
		t.Fatalf("expected status to stay partially_paid (not overwritten to error) after a duplicate-mint race, got %s", settled.Status)
	}
}
