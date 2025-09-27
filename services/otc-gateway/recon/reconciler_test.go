package recon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/swaprpc"
)

type stubExporter struct {
	records []swaprpc.VoucherExportRecord
}

func (s *stubExporter) ExportVouchers(ctx context.Context, start, end time.Time) ([]swaprpc.VoucherExportRecord, error) {
	return s.records, nil
}

func setupReconDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	if err := models.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestReconcilerDryRunNoAnomalies(t *testing.T) {
	db := setupReconDB(t)
	tz := time.FixedZone("UTC", 0)
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("HQ-%s", uuid.NewString()), Region: "US", RegionCap: 250000, InvoiceLimit: 50000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("create branch: %v", err)
	}
	now := time.Date(2024, 12, 1, 12, 0, 0, 0, tz)
	invoice := models.Invoice{
		ID:        uuid.New(),
		BranchID:  branch.ID,
		Amount:    1000,
		Currency:  "USD",
		State:     models.StateMinted,
		CreatedAt: now.Add(-3 * time.Hour),
		UpdatedAt: now.Add(-30 * time.Minute),
	}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	receipt := models.Receipt{ID: uuid.New(), InvoiceID: invoice.ID, ObjectKey: "s3://receipt", CreatedAt: invoice.CreatedAt.Add(10 * time.Minute)}
	decision := models.Decision{ID: uuid.New(), InvoiceID: invoice.ID, Outcome: "approved", CreatedAt: invoice.CreatedAt.Add(20 * time.Minute)}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatalf("create receipt: %v", err)
	}
	if err := db.Create(&decision).Error; err != nil {
		t.Fatalf("create decision: %v", err)
	}
	voucher := models.Voucher{
		ID:           uuid.New(),
		InvoiceID:    invoice.ID,
		ProviderTxID: "PROV-1",
		Status:       "MINTED",
		CreatedAt:    invoice.CreatedAt.Add(30 * time.Minute),
		UpdatedAt:    invoice.CreatedAt.Add(1 * time.Hour),
	}
	if err := db.Create(&voucher).Error; err != nil {
		t.Fatalf("create voucher: %v", err)
	}

	exporter := &stubExporter{records: []swaprpc.VoucherExportRecord{{
		ProviderTxID: "PROV-1",
		Provider:     "nowpayments",
		FiatCurrency: "USD",
		FiatAmount:   "1000.00",
		Status:       "minted",
		CreatedAt:    invoice.CreatedAt.Add(1 * time.Hour).Unix(),
	}}}

	reconciler, err := NewReconciler(Config{
		DB:        db,
		TZ:        tz,
		Exporter:  exporter,
		OutputDir: filepath.Join(t.TempDir(), "recon"),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}

	res, err := reconciler.Run(context.Background(), RunOptions{Start: now.Add(-4 * time.Hour), End: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Anomalies) != 0 {
		t.Fatalf("expected no anomalies, got %d", len(res.Anomalies))
	}
	if len(res.Files) != 0 {
		t.Fatalf("expected no files in dry-run, got %d", len(res.Files))
	}
}

func TestReconcilerDetectsAmountMismatch(t *testing.T) {
	db := setupReconDB(t)
	tz := time.UTC
	branch := models.Branch{ID: uuid.New(), Name: fmt.Sprintf("HQ-%s", uuid.NewString()), Region: "US", RegionCap: 50000, InvoiceLimit: 20000}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("branch: %v", err)
	}
	created := time.Now().Add(-6 * time.Hour).UTC()
	invoice := models.Invoice{ID: uuid.New(), BranchID: branch.ID, Amount: 1500, Currency: "USD", State: models.StateMinted, CreatedAt: created, UpdatedAt: created.Add(2 * time.Hour)}
	if err := db.Create(&invoice).Error; err != nil {
		t.Fatalf("invoice: %v", err)
	}
	voucher := models.Voucher{ID: uuid.New(), InvoiceID: invoice.ID, ProviderTxID: "PROV-2", Status: "MINTED", UpdatedAt: created.Add(90 * time.Minute)}
	if err := db.Create(&voucher).Error; err != nil {
		t.Fatalf("voucher: %v", err)
	}

	exporter := &stubExporter{records: []swaprpc.VoucherExportRecord{{
		ProviderTxID: "PROV-2",
		FiatCurrency: "USD",
		FiatAmount:   "1200.00",
		Status:       "minted",
		CreatedAt:    created.Add(2 * time.Hour).Unix(),
	}}}

	var alerts []Anomaly
	reconciler, err := NewReconciler(Config{
		DB:        db,
		TZ:        tz,
		Exporter:  exporter,
		OutputDir: filepath.Join(t.TempDir(), "recon"),
		DryRun:    true,
		Alert: func(_ context.Context, a Anomaly) error {
			alerts = append(alerts, a)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new reconciler: %v", err)
	}
	res, err := reconciler.Run(context.Background(), RunOptions{Start: created.Add(-time.Hour), End: created.Add(3 * time.Hour)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Anomalies) == 0 {
		t.Fatalf("expected anomalies")
	}
	foundMismatch := false
	for _, a := range res.Anomalies {
		if a.Type == AnomalyAmountMismatch {
			foundMismatch = true
			break
		}
	}
	if !foundMismatch {
		t.Fatalf("expected amount mismatch anomaly, got %+v", res.Anomalies)
	}
	if len(alerts) == 0 {
		t.Fatalf("expected alerts to be emitted")
	}
}
