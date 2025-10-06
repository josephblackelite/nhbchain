package funding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/models"
)

func setupFundingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := models.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestProcessorProcess(t *testing.T) {
	db := setupFundingTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	processor := NewProcessor(db, func() time.Time { return now })

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
		Amount:        250000,
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

	notification := Notification{
		InvoiceID:        invoice.ID,
		FiatAmount:       250000,
		FiatCurrency:     "usd",
		FundingReference: "BNK-REF-1234",
		DossierKey:       partner.KYBDossierKey,
		Custodian:        "First National",
	}

	updated, err := processor.Process(context.Background(), notification)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if updated.State != models.StateFiatConfirmed {
		t.Fatalf("expected state FIAT_CONFIRMED got %s", updated.State)
	}
	if updated.FundingStatus != models.FundingStatusConfirmed {
		t.Fatalf("expected funding status CONFIRMED got %s", updated.FundingStatus)
	}
	if updated.FundingReference != notification.FundingReference {
		t.Fatalf("expected funding reference persisted")
	}

	var event models.Event
	if err := db.First(&event, "invoice_id = ? AND action = ?", invoice.ID, "invoice.funding_confirmed").Error; err != nil {
		t.Fatalf("load funding event: %v", err)
	}
	if !strings.Contains(event.Details, notification.FundingReference) {
		t.Fatalf("expected funding reference in event: %s", event.Details)
	}
}

func TestProcessorProcessDossierMismatch(t *testing.T) {
	db := setupFundingTestDB(t)
	processor := NewProcessor(db, time.Now)
	now := time.Now().UTC()

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
		Amount:        100000,
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

	notification := Notification{
		InvoiceID:        invoice.ID,
		FiatAmount:       100000,
		FiatCurrency:     "usd",
		FundingReference: "BNK-REF-5678",
		DossierKey:       "s3://kyb/other.json",
	}

	if _, err := processor.Process(context.Background(), notification); !errors.Is(err, ErrDossierMismatch) {
		t.Fatalf("expected dossier mismatch got %v", err)
	}
}
