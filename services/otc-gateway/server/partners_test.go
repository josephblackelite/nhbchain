package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
)

func TestPartnerOnboardingFlow(t *testing.T) {
	db := setupTestDB(t)

	rootAdminID := uuid.New()
	partnerAdminID := uuid.New()

	srv := New(Config{DB: db, TZ: testTZ(), ChainID: 1, S3Bucket: "bucket", VoucherTTL: time.Minute, Authenticator: newTestMiddleware(t, []string{rootAdminID.String()})})
	handler := srv.Handler()

	branch := models.Branch{ID: uuid.New(), Name: "HQ", Region: "US", RegionCap: 100000, InvoiceLimit: 50000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}

	body := `{"name":"Atlas Capital","legal_name":"Atlas Capital LLC","kyb_object_key":"s3://kyb","licensing_object_key":"s3://license","contacts":[{"name":"Alice","email":"alice@example.com","role":"admin","subject":"` + partnerAdminID.String() + `","phone":"+1"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/partners", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(t, partnerAdminID, auth.RolePartnerAdmin) {
		req.Header.Set(k, v)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 got %d: %s", resp.Code, resp.Body.String())
	}

	var created struct {
		Partner  models.Partner          `json:"partner"`
		Contacts []models.PartnerContact `json:"contacts"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal partner: %v", err)
	}
	if created.Partner.Approved {
		t.Fatalf("partner should start pending")
	}
	if len(created.Contacts) != 1 {
		t.Fatalf("expected 1 contact got %d", len(created.Contacts))
	}

	// Partner cannot create invoices until approved.
	invoiceReq := httptest.NewRequest(http.MethodPost, "/api/v1/invoices", strings.NewReader(`{"branch_id":"`+branch.ID.String()+`","amount":1000}`))
	invoiceReq.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(t, partnerAdminID, auth.RolePartnerAdmin) {
		invoiceReq.Header.Set(k, v)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, invoiceReq)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 while pending got %d", resp.Code)
	}

	// Upload dossier details.
	dossierReq := httptest.NewRequest(http.MethodPost, "/api/v1/partners/"+created.Partner.ID.String()+"/dossier", strings.NewReader(`{"kyb_object_key":"s3://kyb/latest","licensing_object_key":"s3://license/latest"}`))
	dossierReq.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(t, partnerAdminID, auth.RolePartnerAdmin) {
		dossierReq.Header.Set(k, v)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, dossierReq)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected dossier 200 got %d", resp.Code)
	}

	// Root admin approval required.
	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/partners/"+created.Partner.ID.String()+"/approve", strings.NewReader(`{"notes":"ok"}`))
	approveReq.Header.Set("Content-Type", "application/json")
	for k, v := range newAuthHeader(t, rootAdminID, auth.RoleRootAdmin) {
		approveReq.Header.Set(k, v)
	}
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, approveReq)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected approval 200 got %d: %s", resp.Code, resp.Body.String())
	}

	var approved struct {
		Partner models.Partner `json:"partner"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &approved); err != nil {
		t.Fatalf("unmarshal approval: %v", err)
	}
	if !approved.Partner.Approved {
		t.Fatalf("partner not marked approved")
	}

	var approvals int64
	if err := db.Model(&models.PartnerApproval{}).Where("partner_id = ?", created.Partner.ID).Count(&approvals).Error; err != nil {
		t.Fatalf("count approvals: %v", err)
	}
	if approvals != 1 {
		t.Fatalf("expected 1 approval record got %d", approvals)
	}

	// Approved partner can now create invoices.
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, invoiceReq)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected 201 after approval got %d: %s", resp.Code, resp.Body.String())
	}
}
