package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"nhbchain/core"
	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/swaprpc"
)

type stubSigner struct {
	sig      []byte
	signerDN string
	err      error
	calls    int
}

func (s *stubSigner) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	s.calls++
	if s.err != nil {
		return nil, "", s.err
	}
	return s.sig, s.signerDN, nil
}

type stubSwapClient struct {
	mu          sync.Mutex
	submitted   []core.MintVoucher
	sigs        []string
	providerIDs []string
	txHash      string
	minted      bool
	submitErr   error
	status      *swaprpc.VoucherStatus
	statusErr   error
	getCalls    int
}

func (c *stubSwapClient) SubmitMintVoucher(ctx context.Context, voucher core.MintVoucher, signatureHex, providerTxID string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submitted = append(c.submitted, voucher)
	c.sigs = append(c.sigs, signatureHex)
	c.providerIDs = append(c.providerIDs, providerTxID)
	if c.submitErr != nil {
		return "", false, c.submitErr
	}
	return c.txHash, c.minted, nil
}

func (c *stubSwapClient) GetVoucher(ctx context.Context, providerTxID string) (*swaprpc.VoucherStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCalls++
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	if c.status == nil {
		return nil, nil
	}
	return c.status, nil
}

func TestSignAndSubmit_Minted(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("Branch-%s", uuid.NewString()), Region: "US", RegionCap: 1_000_000, InvoiceLimit: 100_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	creator := uuid.New()
	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, creator, approver, 5000)

	signer := &stubSigner{sig: bytes.Repeat([]byte{0x1}, 65), signerDN: "CN=HSM,O=NHB"}
	swap := &stubSwapClient{txHash: "0xabc", minted: true, status: &swaprpc.VoucherStatus{Status: "MINTED", TxHash: "0xabc"}}
	srv := New(Config{
		DB:         db,
		TZ:         testTZ(),
		ChainID:    1,
		S3Bucket:   "bucket",
		Signer:     signer,
		SwapClient: swap,
		VoucherTTL: time.Minute,
		Provider:   "otc-gateway",
	})
	handler := srv.Handler()

	payload := map[string]string{
		"recipient": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsn7d3c",
		"amount":    "1000",
		"token":     "NHB",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(uuid.New(), auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", resp.Code, resp.Body.String())
	}
	var bodyResp map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &bodyResp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := bodyResp["voucherHash"].(string); !ok {
		t.Fatalf("expected voucherHash in response")
	}

	if signer.calls != 1 {
		t.Fatalf("expected signer to be called once, got %d", signer.calls)
	}
	swap.mu.Lock()
	if len(swap.submitted) != 1 {
		swap.mu.Unlock()
		t.Fatalf("expected 1 submission, got %d", len(swap.submitted))
	}
	swap.mu.Unlock()

	var stored models.Invoice
	if err := db.First(&stored, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if stored.State != models.StateMinted {
		t.Fatalf("expected invoice state MINTED got %s", stored.State)
	}
	var voucher models.Voucher
	if err := db.First(&voucher, "invoice_id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load voucher: %v", err)
	}
	if voucher.Status != voucherStatusMinted {
		t.Fatalf("expected voucher minted got %s", voucher.Status)
	}
	if voucher.TxHash != "0xabc" {
		t.Fatalf("unexpected tx hash %s", voucher.TxHash)
	}
	if voucher.VoucherHash == "" {
		t.Fatalf("expected voucher hash to be stored")
	}
	if voucher.SignerDN != "CN=HSM,O=NHB" {
		t.Fatalf("unexpected signer dn %s", voucher.SignerDN)
	}
	if !strings.HasPrefix(voucher.Signature, "0x") {
		t.Fatalf("expected hex signature got %s", voucher.Signature)
	}

	var event models.Event
	if err := db.Where("invoice_id = ? AND action = ?", invoice.ID, "invoice.signed").First(&event).Error; err != nil {
		t.Fatalf("load signed event: %v", err)
	}
	if !strings.Contains(event.Details, "hash=") || !strings.Contains(event.Details, "signer_dn=") {
		t.Fatalf("expected hash and signer dn in event: %s", event.Details)
	}
}

func TestSignAndSubmit_MakerChecker(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("Branch-%s", uuid.NewString()), Region: "US", RegionCap: 1_000_000, InvoiceLimit: 100_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	creator := uuid.New()
	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, creator, approver, 2500)

	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", VoucherTTL: time.Minute})
	handler := srv.Handler()

	payload := map[string]string{"recipient": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsn7d3c", "amount": "10"}
	body, _ := json.Marshal(payload)

	// Creator cannot sign
	req := httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(creator, auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code == http.StatusOK {
		t.Fatalf("expected maker-checker rejection")
	}

	// Approver cannot sign
	req = httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(approver, auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code == http.StatusOK {
		t.Fatalf("expected maker-checker rejection for approver")
	}
}

func TestSignAndSubmit_IdempotentReplay(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("Branch-%s", uuid.NewString()), Region: "US", RegionCap: 1_000_000, InvoiceLimit: 100_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	creator := uuid.New()
	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, creator, approver, 1000)

	signer := &stubSigner{sig: bytes.Repeat([]byte{0x1}, 65), signerDN: "CN=Signer"}
	swap := &stubSwapClient{txHash: "0xhash", minted: true, status: &swaprpc.VoucherStatus{Status: "MINTED", TxHash: "0xhash"}}
	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", Signer: signer, SwapClient: swap, VoucherTTL: time.Minute})
	handler := srv.Handler()

	payload := map[string]string{"recipient": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsn7d3c", "amount": "10"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(uuid.New(), auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.Code)
	}

	// Replay should not re-sign
	req = httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(uuid.New(), auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay got %d", resp.Code)
	}
	if signer.calls != 1 {
		t.Fatalf("expected signer to run once, got %d", signer.calls)
	}
}

func TestSignAndSubmit_AwaitMinted(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("Branch-%s", uuid.NewString()), Region: "US", RegionCap: 1_000_000, InvoiceLimit: 100_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	creator := uuid.New()
	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, creator, approver, 500)

	signer := &stubSigner{sig: bytes.Repeat([]byte{0x2}, 65), signerDN: "CN=Signer"}
	swap := &stubSwapClient{txHash: "0xslow", minted: false}
	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", Signer: signer, SwapClient: swap, VoucherTTL: 100 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	handler := srv.Handler()

	payload := map[string]string{"recipient": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsn7d3c", "amount": "10"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/ops/otc/invoices/"+invoice.ID.String()+"/sign-and-submit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(uuid.New(), auth.RoleSuperAdmin) {
		req.Header.Set(k, v)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.Code)
	}
	swap.mu.Lock()
	swap.status = &swaprpc.VoucherStatus{Status: "MINTED", TxHash: "0xslow"}
	swap.mu.Unlock()

	// Wait for poller to update state
	time.Sleep(50 * time.Millisecond)

	var stored models.Invoice
	if err := db.First(&stored, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if stored.State != models.StateMinted {
		t.Fatalf("expected invoice state MINTED after poll got %s", stored.State)
	}
}

func createApprovedInvoice(t *testing.T, db *gorm.DB, branch models.Branch, creator, approver uuid.UUID, amount float64) models.Invoice {
	t.Helper()
	now := time.Now().UTC()
	invoice := models.Invoice{
		ID:          uuid.New(),
		BranchID:    branch.ID,
		CreatedByID: creator,
		Amount:      amount,
		Currency:    "USD",
		State:       models.StateApproved,
		Region:      branch.Region,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	decision := models.Decision{ID: uuid.New(), InvoiceID: invoice.ID, ActorID: approver, Outcome: "approved", CreatedAt: now}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatalf("create decision: %v", err)
	}
	return invoice
}
