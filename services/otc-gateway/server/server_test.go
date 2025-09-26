package server

import (
	"encoding/json"
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
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
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

func testTZ() *time.Location {
	loc, _ := time.LoadLocation("UTC")
	return loc
}
