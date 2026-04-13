package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	repoCrypto "nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type stubSubmitter struct {
	called  bool
	voucher VoucherV1
	sig     string
}

func (s *stubSubmitter) SubmitVoucher(_ context.Context, voucher VoucherV1, sigHex string) error {
	s.called = true
	s.voucher = voucher
	s.sig = sigHex
	return nil
}

func TestHandlePaymentWebhook(t *testing.T) {
	store := newOrderStore()

	keyHex := "4f3edf983ac636a65a842ce7c78d9aa706d3b113b37e2b8c3c6d53295d85f81b"
	key, err := ethcrypto.HexToECDSA(keyHex)
	if err != nil {
		t.Fatalf("hex to ecdsa: %v", err)
	}
	minterAddr := repoCrypto.MustNewAddress(repoCrypto.NHBPrefix, ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()).String()

	recipientBytes := bytes.Repeat([]byte{0x33}, 20)
	recipient := repoCrypto.MustNewAddress(repoCrypto.NHBPrefix, recipientBytes).String()

	ord := &order{
		OrderID:    "SWP_1",
		Reference:  "SWP_1",
		Fiat:       "USD",
		AmountFiat: "100.00",
		Recipient:  recipient,
		Rate:       "0.10",
		AmountWei:  "1000000000000000000000",
		Status:     orderStatusPending,
	}
	if _, _, err := store.createOrGet(ord); err != nil {
		t.Fatalf("store order: %v", err)
	}

	payload := paymentWebhook{
		OrderID:    "SWP_1",
		Fiat:       "USD",
		AmountFiat: "100.00",
		Paid:       true,
		TxRef:      "np_test",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	secret := "hook-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhooks/payment", bytes.NewReader(body))
	req.Header.Set("X-HMAC", sig)

	recorder := httptest.NewRecorder()
	submitter := &stubSubmitter{}

	handlePaymentWebhook(recorder, req, store, nil, submitter, 187001, minterAddr, "0x"+keyHex, secret)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
	if !submitter.called {
		t.Fatalf("expected submitter to be called")
	}
	if !strings.HasPrefix(submitter.sig, "0x") {
		t.Fatalf("expected hex signature, got %s", submitter.sig)
	}

	updated, ok := store.get("SWP_1")
	if !ok {
		t.Fatalf("order missing after webhook")
	}
	if updated.Status != orderStatusMintSubmitted {
		t.Fatalf("expected status %s, got %s", orderStatusMintSubmitted, updated.Status)
	}
	if updated.MintedWei != ord.AmountWei {
		t.Fatalf("expected minted %s, got %s", ord.AmountWei, updated.MintedWei)
	}
}
