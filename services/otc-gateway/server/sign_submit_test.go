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

	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/identity"
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
	submissions []swaprpc.MintSubmission
	txHash      string
	minted      bool
	submitErr   error
	status      *swaprpc.VoucherStatus
	statusErr   error
	getCalls    int
}

func (c *stubSwapClient) SubmitMintVoucher(ctx context.Context, submission swaprpc.MintSubmission) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submissions = append(c.submissions, submission)
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

type stubIdentityClient struct {
	mu         sync.Mutex
	resolution *identity.Resolution
	err        error
	calls      int
}

func (s *stubIdentityClient) ResolvePartner(ctx context.Context, partnerID uuid.UUID) (*identity.Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.resolution == nil {
		return nil, fmt.Errorf("identity missing resolution")
	}
	return s.resolution, nil
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
	if len(swap.submissions) != 1 {
		swap.mu.Unlock()
		t.Fatalf("expected 1 submission, got %d", len(swap.submissions))
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
	if !strings.Contains(event.Details, "hash=") || !strings.Contains(event.Details, "signer_dn=") || !strings.Contains(event.Details, "funding_ref=") {
		t.Fatalf("expected hash, signer dn, and funding ref in event: %s", event.Details)
	}
}

func TestSignAndSubmit_PartnerComplianceMetadata(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: "Partner-Branch", Region: "EU", RegionCap: 2_000_000, InvoiceLimit: 250_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	partnerID := uuid.New()
	partnerUser := uuid.New()
	now := time.Now().UTC()
	partner := models.Partner{ID: partnerID, Name: "Partner", LegalName: "Partner LLC", Approved: true, SubmittedBy: uuid.New(), CreatedAt: now, UpdatedAt: now}
	partner.ApprovedAt = &now
	if err := db.Create(&partner).Error; err != nil {
		t.Fatalf("create partner: %v", err)
	}
	contact := models.PartnerContact{ID: uuid.New(), PartnerID: partner.ID, Subject: strings.ToLower(partnerUser.String()), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&contact).Error; err != nil {
		t.Fatalf("create partner contact: %v", err)
	}

	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, partnerUser, approver, 1500)

	signer := &stubSigner{sig: bytes.Repeat([]byte{0x3}, 65), signerDN: "CN=ComplianceSigner"}
	swap := &stubSwapClient{txHash: "0xpartner", minted: true, status: &swaprpc.VoucherStatus{Status: "MINTED", TxHash: "0xpartner"}}
	travelRule := json.RawMessage(`{"originator":"alice","beneficiary":"bob"}`)
	identityStub := &stubIdentityClient{resolution: &identity.Resolution{
		PartnerDID:       "did:example:partner123",
		Verified:         true,
		SanctionsStatus:  "clear",
		ComplianceTags:   []string{"travel-rule:complete", "kyc:pass"},
		TravelRulePacket: travelRule,
	}}

	srv := New(Config{
		DB:         db,
		TZ:         testTZ(),
		ChainID:    1,
		S3Bucket:   "bucket",
		Signer:     signer,
		SwapClient: swap,
		Identity:   identityStub,
		VoucherTTL: time.Minute,
	})
	handler := srv.Handler()

	payload := map[string]string{"recipient": "nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqsn7d3c", "amount": "42", "token": "NHB"}
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

	if identityStub.calls != 1 {
		t.Fatalf("expected identity resolver to be invoked once, got %d", identityStub.calls)
	}

	var storedInvoice models.Invoice
	if err := db.First(&storedInvoice, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if storedInvoice.PartnerDID != "did:example:partner123" {
		t.Fatalf("unexpected partner DID %s", storedInvoice.PartnerDID)
	}
	if storedInvoice.SanctionsStatus != "clear" {
		t.Fatalf("unexpected sanctions status %s", storedInvoice.SanctionsStatus)
	}
	var tags []string
	if err := json.Unmarshal(storedInvoice.ComplianceTags, &tags); err != nil {
		t.Fatalf("decode invoice compliance tags: %v", err)
	}
	if len(tags) != 2 || tags[0] != "travel-rule:complete" || tags[1] != "kyc:pass" {
		t.Fatalf("unexpected invoice compliance tags: %#v", tags)
	}
	if string(storedInvoice.TravelRulePacket) != string(travelRule) {
		t.Fatalf("unexpected travel rule packet %s", storedInvoice.TravelRulePacket)
	}

	var storedVoucher models.Voucher
	if err := db.First(&storedVoucher, "invoice_id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load voucher: %v", err)
	}
	if storedVoucher.PartnerDID != "did:example:partner123" {
		t.Fatalf("voucher missing partner DID")
	}
	if storedVoucher.SanctionsStatus != "clear" {
		t.Fatalf("unexpected voucher sanctions status %s", storedVoucher.SanctionsStatus)
	}
	var voucherTags []string
	if err := json.Unmarshal(storedVoucher.ComplianceTags, &voucherTags); err != nil {
		t.Fatalf("decode voucher tags: %v", err)
	}
	if len(voucherTags) != 2 || voucherTags[0] != "travel-rule:complete" {
		t.Fatalf("unexpected voucher tags %#v", voucherTags)
	}
	if string(storedVoucher.TravelRulePacket) != string(travelRule) {
		t.Fatalf("unexpected voucher travel rule packet %s", storedVoucher.TravelRulePacket)
	}

	swap.mu.Lock()
	if len(swap.submissions) != 1 {
		swap.mu.Unlock()
		t.Fatalf("expected one submission recorded")
	}
	submission := swap.submissions[0]
	swap.mu.Unlock()
	if submission.Compliance == nil {
		t.Fatalf("expected compliance metadata to be forwarded")
	}
	if submission.Compliance.PartnerDID != "did:example:partner123" {
		t.Fatalf("unexpected submission partner DID %s", submission.Compliance.PartnerDID)
	}
	if submission.Compliance.SanctionsStatus != "clear" {
		t.Fatalf("unexpected submission sanctions status %s", submission.Compliance.SanctionsStatus)
	}
	if string(submission.Compliance.TravelRulePacket) != string(travelRule) {
		t.Fatalf("unexpected submission travel rule payload %s", submission.Compliance.TravelRulePacket)
	}
	if len(submission.Compliance.ComplianceTags) != 2 {
		t.Fatalf("unexpected submission tags %#v", submission.Compliance.ComplianceTags)
	}
}

func TestSignAndSubmit_PartnerMissingAttestation(t *testing.T) {
	db := setupTestDB(t)
	branch := models.Branch{ID: uuid.New(), Name: "Partner-Branch", Region: "EU", RegionCap: 2_000_000, InvoiceLimit: 250_000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	partnerID := uuid.New()
	partnerUser := uuid.New()
	now := time.Now().UTC()
	partner := models.Partner{ID: partnerID, Name: "Partner", Approved: true, SubmittedBy: uuid.New(), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&partner).Error; err != nil {
		t.Fatalf("create partner: %v", err)
	}
	contact := models.PartnerContact{ID: uuid.New(), PartnerID: partner.ID, Subject: strings.ToLower(partnerUser.String()), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&contact).Error; err != nil {
		t.Fatalf("create contact: %v", err)
	}

	approver := uuid.New()
	invoice := createApprovedInvoice(t, db, branch, partnerUser, approver, 1500)

	signer := &stubSigner{sig: bytes.Repeat([]byte{0x3}, 65), signerDN: "CN=ComplianceSigner"}
	swap := &stubSwapClient{}
	identityStub := &stubIdentityClient{resolution: &identity.Resolution{PartnerDID: "did:example:bad", Verified: false}}

	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", Signer: signer, SwapClient: swap, Identity: identityStub, VoucherTTL: time.Minute})
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
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 got %d body=%s", resp.Code, resp.Body.String())
	}
	if signer.calls != 0 {
		t.Fatalf("expected signer to not be invoked")
	}

	var count int64
	if err := db.Model(&models.Voucher{}).Count(&count).Error; err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no voucher to be stored, found %d", count)
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
		ID:               uuid.New(),
		BranchID:         branch.ID,
		CreatedByID:      creator,
		Amount:           amount,
		Currency:         "USD",
		FiatAmount:       amount,
		FiatCurrency:     "USD",
		FundingStatus:    models.FundingStatusConfirmed,
		FundingReference: fmt.Sprintf("FUND-%s", uuid.NewString()[:8]),
		State:            models.StateFiatConfirmed,
		Region:           branch.Region,
		CreatedAt:        now,
		UpdatedAt:        now,
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
