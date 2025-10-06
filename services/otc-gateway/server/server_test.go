package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := models.AutoMigrate(db); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func newAuthHeader(user uuid.UUID, role auth.Role) map[string]string {
	return map[string]string{
		"Authorization":       "Bearer " + user.String() + "|" + string(role),
		"X-WebAuthn-Verified": "true",
	}
}

func TestInvoiceLifecycle(t *testing.T) {
	db := setupTestDB(t)

	branchID := uuid.New()
	branch := models.Branch{ID: branchID, Name: "HQ", Region: "US", RegionCap: 100000, InvoiceLimit: 25000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}

	tellerID := uuid.New()
	supervisorID := uuid.New()

	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", VoucherTTL: time.Minute})
	handler := srv.Handler()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invoices", strings.NewReader(`{"branch_id":"`+branchID.String()+`","amount":1000,"currency":"USD"}`))
	for k, v := range newAuthHeader(tellerID, auth.RoleTeller) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201 got %d", recorder.Code)
	}

	var invoice models.Invoice
	if err := json.Unmarshal(recorder.Body.Bytes(), &invoice); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Upload receipt
	recorder = httptest.NewRecorder()
	receiptReq := httptest.NewRequest(http.MethodPost, "/api/v1/invoices/"+invoice.ID.String()+"/receipt", strings.NewReader(`{"object_key":"s3://receipt"}`))
	for k, v := range newAuthHeader(tellerID, auth.RoleTeller) {
		receiptReq.Header.Set(k, v)
	}
	receiptReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, receiptReq)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", recorder.Code)
	}

	// Mark pending review
	recorder = httptest.NewRecorder()
	pendingReq := httptest.NewRequest(http.MethodPost, "/api/v1/invoices/"+invoice.ID.String()+"/pending-review", nil)
	for k, v := range newAuthHeader(supervisorID, auth.RoleSupervisor) {
		pendingReq.Header.Set(k, v)
	}
	handler.ServeHTTP(recorder, pendingReq)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", recorder.Code)
	}

	// Approve invoice
	recorder = httptest.NewRecorder()
	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/invoices/"+invoice.ID.String()+"/approve", strings.NewReader(`{"notes":"ok"}`))
	for k, v := range newAuthHeader(supervisorID, auth.RoleSupervisor) {
		approveReq.Header.Set(k, v)
	}
	approveReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, approveReq)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", recorder.Code)
	}

	// Check audit events exist
	var count int64
	if err := db.Model(&models.Event{}).Where("invoice_id = ?", invoice.ID).Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count < 4 {
		t.Fatalf("expected at least 4 events got %d", count)
	}
}

func TestHandleFundingWebhook(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	branch := models.Branch{ID: uuid.New(), Name: "Global", Region: "US", RegionCap: 5_000_000, InvoiceLimit: 1_000_000, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}

	partner := models.Partner{ID: uuid.New(), Name: "Atlas", LegalName: "Atlas LLC", KYBDossierKey: "s3://kyb/atlas.json", Approved: true, SubmittedBy: uuid.New(), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&partner).Error; err != nil {
		t.Fatalf("create partner: %v", err)
	}

	operator := uuid.New()
	contact := models.PartnerContact{ID: uuid.New(), PartnerID: partner.ID, Subject: strings.ToLower(operator.String()), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&contact).Error; err != nil {
		t.Fatalf("create contact: %v", err)
	}

	invoice := models.Invoice{
		ID:            uuid.New(),
		BranchID:      branch.ID,
		CreatedByID:   operator,
		Amount:        500000,
		Currency:      "USDC",
		FiatCurrency:  "USD",
		FundingStatus: models.FundingStatusPending,
		State:         models.StateApproved,
		Region:        branch.Region,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}

	rootAdmin := uuid.New()
	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", VoucherTTL: time.Minute, RootAdminSubjects: []string{rootAdmin.String()}})
	srv.Now = func() time.Time { return now }

	payload := map[string]interface{}{
		"invoice_id":        invoice.ID,
		"fiat_amount":       500000,
		"fiat_currency":     "usd",
		"funding_reference": "BNK-REF-9000",
		"dossier_key":       partner.KYBDossierKey,
		"custodian":         "First National",
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/integrations/otc/funding/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(rootAdmin, auth.RoleRootAdmin) {
		req.Header.Set(k, v)
	}

	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body=%s", resp.Code, resp.Body.String())
	}

	var stored models.Invoice
	if err := db.First(&stored, "id = ?", invoice.ID).Error; err != nil {
		t.Fatalf("load invoice: %v", err)
	}
	if stored.State != models.StateFiatConfirmed {
		t.Fatalf("expected FIAT_CONFIRMED got %s", stored.State)
	}
	if stored.FundingReference != "BNK-REF-9000" {
		t.Fatalf("expected funding reference to persist")
	}
}

func testTZ() *time.Location {
	loc, _ := time.LoadLocation("UTC")
	return loc
}
