package recon

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/xitongsys/parquet-go-source/writerfile"
	"github.com/xitongsys/parquet-go/parquet"
	"github.com/xitongsys/parquet-go/writer"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/swaprpc"
)

const (
	// ReceiptRetentionDays defines how long physical receipts must be retained.
	ReceiptRetentionDays = 365
	// DecisionRetentionDays defines how long compliance decisions must be retained.
	DecisionRetentionDays = 730
	// ReportRetentionDays specifies how long generated reconciliation reports remain on disk.
	ReportRetentionDays = 545 // 18 months

	// Anomaly types emitted by the reconciler.
	AnomalyMissingMint    = "missing_mint"
	AnomalyAmountMismatch = "amount_mismatch"
	AnomalyExpiredVoucher = "expired_voucher"
	AnomalyCapBreach      = "cap_breach"
)

// Exporter exposes the swap voucher export RPC the reconciler depends on.
type Exporter interface {
	ExportVouchers(ctx context.Context, start, end time.Time) ([]swaprpc.VoucherExportRecord, error)
}

// AlertFunc is invoked for every anomaly detected during reconciliation.
type AlertFunc func(ctx context.Context, anomaly Anomaly) error

// Config captures the dependencies required to construct a Reconciler.
type Config struct {
	DB        *gorm.DB
	TZ        *time.Location
	Exporter  Exporter
	OutputDir string
	DryRun    bool
	Now       func() time.Time
	Alert     AlertFunc
	Logger    *log.Logger
}

// RunOptions specifies overrides when executing a reconciliation window.
type RunOptions struct {
	Start  time.Time
	End    time.Time
	DryRun bool
}

// Reconciler materialises nightly reports joining invoices, vouchers, and on-chain mints.
type Reconciler struct {
	db        *gorm.DB
	tz        *time.Location
	exporter  Exporter
	outputDir string
	dryRun    bool
	now       func() time.Time
	alert     AlertFunc
	logger    *log.Logger
}

// Anomaly captures a reconciliation failure requiring operator review.
type Anomaly struct {
	Type         string
	InvoiceID    *uuid.UUID
	BranchID     *uuid.UUID
	ProviderTxID string
	Details      string
}

// ReportRow summarises reconciliation status for a single invoice.
type ReportRow struct {
	InvoiceID             uuid.UUID
	BranchID              uuid.UUID
	BranchName            string
	Region                string
	Currency              string
	InvoiceAmount         float64
	InvoiceState          string
	ProviderTxID          string
	VoucherStatus         string
	OnChainStatus         string
	OnChainFiatAmount     string
	OnChainMintAmountWei  string
	OnChainRate           string
	OnChainUSD            string
	MissingMint           bool
	AmountMismatch        bool
	VoucherExpired        bool
	CapBreached           bool
	CreatedAt             time.Time
	ReceiptCount          int
	DecisionCount         int
	FirstReceiptAt        *time.Time
	ApprovedAt            *time.Time
	MintedAt              *time.Time
	ReceiptLatency        time.Duration
	ApprovalLatency       time.Duration
	MintLatency           time.Duration
	SLAWithin24h          bool
	ReceiptRetentionDays  int
	DecisionRetentionDays int
	ReportRetentionDays   int
	BranchInvoiceLimit    float64
	BranchRegionCap       float64
}

// ReportFile references the CSV and Parquet artefacts generated for a branch/currency combination.
type ReportFile struct {
	BranchID    uuid.UUID
	BranchName  string
	Currency    string
	CSVPath     string
	ParquetPath string
	Count       int
}

// Result summarises a reconciliation run.
type Result struct {
	Start     time.Time
	End       time.Time
	Rows      []*ReportRow
	Files     []ReportFile
	Anomalies []Anomaly
	Totals    map[uuid.UUID]float64
	Retention struct {
		Receipts  int
		Decisions int
		Reports   int
	}
}

// NewReconciler builds a configured reconciler.
func NewReconciler(cfg Config) (*Reconciler, error) {
	if cfg.DB == nil {
		return nil, errors.New("recon: db is required")
	}
	if cfg.TZ == nil {
		cfg.TZ = time.UTC
	}
	if cfg.Exporter == nil {
		return nil, errors.New("recon: exporter is required")
	}
	outputDir := cfg.OutputDir
	if strings.TrimSpace(outputDir) == "" {
		outputDir = filepath.Join("nhb-data-local", "recon")
	}
	alert := cfg.Alert
	if alert == nil {
		alert = func(ctx context.Context, anomaly Anomaly) error {
			return nil
		}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().In(cfg.TZ) }
	}
	return &Reconciler{
		db:        cfg.DB,
		tz:        cfg.TZ,
		exporter:  cfg.Exporter,
		outputDir: outputDir,
		dryRun:    cfg.DryRun,
		now:       nowFn,
		alert:     alert,
		logger:    logger,
	}, nil
}

// Run executes reconciliation for the supplied window.
func (r *Reconciler) Run(ctx context.Context, opts RunOptions) (*Result, error) {
	start := opts.Start.In(r.tz)
	end := opts.End.In(r.tz)
	if end.Before(start) {
		return nil, fmt.Errorf("recon: end before start")
	}
	dryRun := r.dryRun || opts.DryRun

	var invoices []models.Invoice
	query := r.db.Preload("Receipts").Preload("Decisions")
	if err := query.Where("(created_at BETWEEN ? AND ?) OR (updated_at BETWEEN ? AND ?)", start, end, start, end).
		Find(&invoices).Error; err != nil {
		return nil, fmt.Errorf("recon: load invoices: %w", err)
	}

	invoiceIDs := make([]uuid.UUID, 0, len(invoices))
	branchIDs := make([]uuid.UUID, 0, len(invoices))
	branchSeen := map[uuid.UUID]bool{}
	for _, inv := range invoices {
		invoiceIDs = append(invoiceIDs, inv.ID)
		if !branchSeen[inv.BranchID] {
			branchIDs = append(branchIDs, inv.BranchID)
			branchSeen[inv.BranchID] = true
		}
	}

	branchMap := map[uuid.UUID]models.Branch{}
	if len(branchIDs) > 0 {
		var branches []models.Branch
		if err := r.db.Where("id IN ?", branchIDs).Find(&branches).Error; err != nil {
			return nil, fmt.Errorf("recon: load branches: %w", err)
		}
		for _, branch := range branches {
			branchMap[branch.ID] = branch
		}
	}

	voucherMap := map[uuid.UUID]models.Voucher{}
	if len(invoiceIDs) > 0 {
		var vouchers []models.Voucher
		if err := r.db.Where("invoice_id IN ?", invoiceIDs).Find(&vouchers).Error; err != nil {
			return nil, fmt.Errorf("recon: load vouchers: %w", err)
		}
		for _, voucher := range vouchers {
			voucherMap[voucher.InvoiceID] = voucher
		}
	}

	eventMap := map[uuid.UUID][]models.Event{}
	if len(invoiceIDs) > 0 {
		var events []models.Event
		if err := r.db.Where("invoice_id IN ?", invoiceIDs).Find(&events).Error; err != nil {
			return nil, fmt.Errorf("recon: load events: %w", err)
		}
		for _, evt := range events {
			if evt.InvoiceID == nil {
				continue
			}
			id := *evt.InvoiceID
			eventMap[id] = append(eventMap[id], evt)
		}
	}

	exports, err := r.exporter.ExportVouchers(ctx, start, end)
	if err != nil {
		return nil, fmt.Errorf("recon: export vouchers: %w", err)
	}
	exportMap := map[string]swaprpc.VoucherExportRecord{}
	for _, rec := range exports {
		exportMap[strings.TrimSpace(rec.ProviderTxID)] = rec
	}

	rows := make([]*ReportRow, 0, len(invoices))
	totals := make(map[uuid.UUID]float64)
	anomalies := make([]Anomaly, 0)
	matchedExports := make(map[string]bool)
	now := r.now()

	for _, invoice := range invoices {
		branch := branchMap[invoice.BranchID]
		voucher, hasVoucher := voucherMap[invoice.ID]
		var export swaprpc.VoucherExportRecord
		var hasExport bool
		if hasVoucher {
			export, hasExport = exportMap[strings.TrimSpace(voucher.ProviderTxID)]
		}
		if !hasExport {
			export, hasExport = exportMap[strings.TrimSpace(invoice.Reference)]
		}
		if hasExport {
			matchedExports[strings.TrimSpace(export.ProviderTxID)] = true
		}
		receiptCount := len(invoice.Receipts)
		decisionCount := len(invoice.Decisions)

		var firstReceipt *time.Time
		if receiptCount > 0 {
			sort.Slice(invoice.Receipts, func(i, j int) bool {
				return invoice.Receipts[i].CreatedAt.Before(invoice.Receipts[j].CreatedAt)
			})
			first := invoice.Receipts[0].CreatedAt.In(r.tz)
			firstReceipt = &first
		}

		var approvedAt *time.Time
		for _, decision := range invoice.Decisions {
			if strings.EqualFold(strings.TrimSpace(decision.Outcome), "approved") {
				ts := decision.CreatedAt.In(r.tz)
				if approvedAt == nil || ts.Before(*approvedAt) {
					approvedAt = &ts
				}
			}
		}

		var mintedAt *time.Time
		if hasExport && export.CreatedAt > 0 {
			ts := time.Unix(export.CreatedAt, 0).In(r.tz)
			mintedAt = &ts
		} else if hasVoucher && !voucher.UpdatedAt.IsZero() {
			ts := voucher.UpdatedAt.In(r.tz)
			mintedAt = &ts
		} else {
			if evts := eventMap[invoice.ID]; len(evts) > 0 {
				for _, evt := range evts {
					if evt.Action == "invoice.minted" {
						ts := evt.CreatedAt.In(r.tz)
						mintedAt = &ts
						break
					}
				}
			}
		}

		receiptLatency := durationBetween(invoice.CreatedAt, firstReceipt)
		approvalLatency := durationBetween(invoice.CreatedAt, approvedAt)
		mintLatency := durationBetween(invoice.CreatedAt, mintedAt)

		slaWithin24h := false
		if mintLatency > 0 && mintLatency <= 24*time.Hour {
			slaWithin24h = true
		}

		missingMint := false
		amountMismatch := false
		voucherExpired := false

		invoiceState := string(invoice.State)
		voucherStatus := ""
		providerTxID := ""

		if hasVoucher {
			voucherStatus = voucher.Status
			providerTxID = voucher.ProviderTxID
			if !voucher.ExpiresAt.IsZero() && voucher.ExpiresAt.Before(now) && !strings.EqualFold(voucher.Status, "MINTED") {
				voucherExpired = true
				anomalies = append(anomalies, r.raise(ctx, Anomaly{
					Type:         AnomalyExpiredVoucher,
					InvoiceID:    ptrUUID(invoice.ID),
					BranchID:     ptrUUID(invoice.BranchID),
					ProviderTxID: providerTxID,
					Details:      fmt.Sprintf("voucher expired at %s without mint", voucher.ExpiresAt.In(r.tz)),
				}))
			}
		}

		onChainStatus := ""
		onChainFiat := ""
		onChainMintWei := ""
		onChainRate := ""
		onChainUSD := ""

		if hasExport {
			onChainStatus = export.Status
			onChainFiat = export.FiatAmount
			onChainMintWei = export.MintAmountWei
			onChainRate = export.Rate
			onChainUSD = export.USD
			if hasVoucher {
				if !strings.EqualFold(export.Status, "minted") && strings.EqualFold(voucher.Status, "MINTED") {
					missingMint = true
				}
			}
			if amt, ok := parseAmount(export.FiatAmount); ok {
				if math.Abs(amt-invoice.Amount) > 0.01 {
					amountMismatch = true
					anomalies = append(anomalies, r.raise(ctx, Anomaly{
						Type:         AnomalyAmountMismatch,
						InvoiceID:    ptrUUID(invoice.ID),
						BranchID:     ptrUUID(invoice.BranchID),
						ProviderTxID: providerTxID,
						Details:      fmt.Sprintf("invoice %.2f %s vs on-chain %.2f", invoice.Amount, invoice.Currency, amt),
					}))
				}
			}
		} else if strings.EqualFold(invoiceState, string(models.StateMinted)) || strings.EqualFold(voucherStatus, "MINTED") {
			missingMint = true
		}

		if missingMint {
			anomalies = append(anomalies, r.raise(ctx, Anomaly{
				Type:         AnomalyMissingMint,
				InvoiceID:    ptrUUID(invoice.ID),
				BranchID:     ptrUUID(invoice.BranchID),
				ProviderTxID: providerTxID,
				Details:      fmt.Sprintf("invoice state %s without minted export", invoiceState),
			}))
		}

		row := &ReportRow{
			InvoiceID:             invoice.ID,
			BranchID:              invoice.BranchID,
			BranchName:            branch.Name,
			Region:                branch.Region,
			Currency:              invoice.Currency,
			InvoiceAmount:         invoice.Amount,
			InvoiceState:          invoiceState,
			ProviderTxID:          providerTxID,
			VoucherStatus:         voucherStatus,
			OnChainStatus:         onChainStatus,
			OnChainFiatAmount:     onChainFiat,
			OnChainMintAmountWei:  onChainMintWei,
			OnChainRate:           onChainRate,
			OnChainUSD:            onChainUSD,
			MissingMint:           missingMint,
			AmountMismatch:        amountMismatch,
			VoucherExpired:        voucherExpired,
			CreatedAt:             invoice.CreatedAt.In(r.tz),
			ReceiptCount:          receiptCount,
			DecisionCount:         decisionCount,
			FirstReceiptAt:        firstReceipt,
			ApprovedAt:            approvedAt,
			MintedAt:              mintedAt,
			ReceiptLatency:        receiptLatency,
			ApprovalLatency:       approvalLatency,
			MintLatency:           mintLatency,
			SLAWithin24h:          slaWithin24h,
			ReceiptRetentionDays:  ReceiptRetentionDays,
			DecisionRetentionDays: DecisionRetentionDays,
			ReportRetentionDays:   ReportRetentionDays,
			BranchInvoiceLimit:    branch.InvoiceLimit,
			BranchRegionCap:       branch.RegionCap,
		}
		rows = append(rows, row)
		totals[invoice.BranchID] += invoice.Amount
	}

	capExceeded := make(map[uuid.UUID]bool)
	alertedCap := make(map[uuid.UUID]bool)
	for branchID, total := range totals {
		branch := branchMap[branchID]
		if branch.RegionCap > 0 && total > branch.RegionCap {
			capExceeded[branchID] = true
			if !alertedCap[branchID] {
				anomalies = append(anomalies, r.raise(ctx, Anomaly{
					Type:     AnomalyCapBreach,
					BranchID: ptrUUID(branchID),
					Details:  fmt.Sprintf("branch %s cap %.2f exceeded with total %.2f", branch.Name, branch.RegionCap, total),
				}))
				alertedCap[branchID] = true
			}
		}
	}
	for _, row := range rows {
		if capExceeded[row.BranchID] {
			row.CapBreached = true
		}
	}

	files := make([]ReportFile, 0)
	if !dryRun {
		runDir := filepath.Join(r.outputDir, fmt.Sprintf("%s_%s", start.Format("20060102"), end.Format("20060102")))
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return nil, fmt.Errorf("recon: ensure output dir: %w", err)
		}
		grouped := groupRows(rows)
		for key, entries := range grouped {
			csvPath, parquetPath, err := r.writeReportFiles(runDir, key, entries)
			if err != nil {
				return nil, err
			}
			if csvPath != "" || parquetPath != "" {
				files = append(files, ReportFile{
					BranchID:    entries[0].BranchID,
					BranchName:  entries[0].BranchName,
					Currency:    entries[0].Currency,
					CSVPath:     csvPath,
					ParquetPath: parquetPath,
					Count:       len(entries),
				})
			}
		}
	}

	result := &Result{Start: start, End: end, Rows: rows, Files: files, Anomalies: anomalies, Totals: totals}
	result.Retention.Receipts = ReceiptRetentionDays
	result.Retention.Decisions = DecisionRetentionDays
	result.Retention.Reports = ReportRetentionDays
	return result, nil
}

func (r *Reconciler) raise(ctx context.Context, anomaly Anomaly) Anomaly {
	if r.alert != nil {
		if err := r.alert(ctx, anomaly); err != nil {
			r.logger.Printf("recon alert delivery failed: %v", err)
		}
	}
	return anomaly
}

func groupRows(rows []*ReportRow) map[string][]*ReportRow {
	grouped := make(map[string][]*ReportRow)
	for _, row := range rows {
		key := fmt.Sprintf("%s|%s", row.BranchID.String(), strings.ToUpper(row.Currency))
		grouped[key] = append(grouped[key], row)
	}
	return grouped
}

func (r *Reconciler) writeReportFiles(baseDir, key string, rows []*ReportRow) (string, string, error) {
	if len(rows) == 0 {
		return "", "", nil
	}
	branchSlug := slugify(rows[0].BranchName)
	if branchSlug == "" {
		branchSlug = rows[0].BranchID.String()
	}
	currency := strings.ToUpper(rows[0].Currency)
	filename := fmt.Sprintf("%s_%s", branchSlug, currency)
	csvPath := filepath.Join(baseDir, filename+".csv")
	if err := writeCSV(csvPath, rows); err != nil {
		return "", "", err
	}
	parquetPath := filepath.Join(baseDir, filename+".parquet")
	if err := writeParquet(parquetPath, rows); err != nil {
		return "", "", err
	}
	r.logger.Printf("recon: wrote %s (%d rows)", csvPath, len(rows))
	r.logger.Printf("recon: wrote %s (%d rows)", parquetPath, len(rows))
	return csvPath, parquetPath, nil
}

func writeCSV(path string, rows []*ReportRow) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("recon: create csv: %w", err)
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	header := []string{
		"invoice_id", "branch_id", "branch_name", "region", "currency", "invoice_amount", "invoice_state", "provider_tx_id",
		"voucher_status", "onchain_status", "onchain_fiat_amount", "onchain_mint_amount_wei", "onchain_rate", "onchain_usd",
		"missing_mint", "amount_mismatch", "voucher_expired", "cap_breached", "created_at", "first_receipt_at", "approved_at",
		"minted_at", "receipt_latency_minutes", "approval_latency_minutes", "mint_latency_minutes", "sla_within_24h",
		"receipt_retention_days", "decision_retention_days", "report_retention_days", "branch_invoice_limit", "branch_region_cap",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("recon: write csv header: %w", err)
	}
	for _, row := range rows {
		record := []string{
			row.InvoiceID.String(),
			row.BranchID.String(),
			row.BranchName,
			row.Region,
			row.Currency,
			fmt.Sprintf("%.2f", row.InvoiceAmount),
			row.InvoiceState,
			row.ProviderTxID,
			row.VoucherStatus,
			row.OnChainStatus,
			row.OnChainFiatAmount,
			row.OnChainMintAmountWei,
			row.OnChainRate,
			row.OnChainUSD,
			boolString(row.MissingMint),
			boolString(row.AmountMismatch),
			boolString(row.VoucherExpired),
			boolString(row.CapBreached),
			row.CreatedAt.Format(time.RFC3339),
			formatTime(row.FirstReceiptAt),
			formatTime(row.ApprovedAt),
			formatTime(row.MintedAt),
			formatMinutes(row.ReceiptLatency),
			formatMinutes(row.ApprovalLatency),
			formatMinutes(row.MintLatency),
			boolString(row.SLAWithin24h),
			fmt.Sprintf("%d", row.ReceiptRetentionDays),
			fmt.Sprintf("%d", row.DecisionRetentionDays),
			fmt.Sprintf("%d", row.ReportRetentionDays),
			fmt.Sprintf("%.2f", row.BranchInvoiceLimit),
			fmt.Sprintf("%.2f", row.BranchRegionCap),
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("recon: write csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("recon: flush csv: %w", err)
	}
	return nil
}

type parquetRow struct {
	InvoiceID              string  `parquet:"name=invoice_id, type=BYTE_ARRAY, convertedtype=UTF8"`
	BranchID               string  `parquet:"name=branch_id, type=BYTE_ARRAY, convertedtype=UTF8"`
	BranchName             string  `parquet:"name=branch_name, type=BYTE_ARRAY, convertedtype=UTF8"`
	Region                 string  `parquet:"name=region, type=BYTE_ARRAY, convertedtype=UTF8"`
	Currency               string  `parquet:"name=currency, type=BYTE_ARRAY, convertedtype=UTF8"`
	InvoiceAmount          float64 `parquet:"name=invoice_amount, type=DOUBLE"`
	InvoiceState           string  `parquet:"name=invoice_state, type=BYTE_ARRAY, convertedtype=UTF8"`
	ProviderTxID           string  `parquet:"name=provider_tx_id, type=BYTE_ARRAY, convertedtype=UTF8"`
	VoucherStatus          string  `parquet:"name=voucher_status, type=BYTE_ARRAY, convertedtype=UTF8"`
	OnChainStatus          string  `parquet:"name=onchain_status, type=BYTE_ARRAY, convertedtype=UTF8"`
	OnChainFiatAmount      string  `parquet:"name=onchain_fiat_amount, type=BYTE_ARRAY, convertedtype=UTF8"`
	OnChainMintAmountWei   string  `parquet:"name=onchain_mint_amount_wei, type=BYTE_ARRAY, convertedtype=UTF8"`
	OnChainRate            string  `parquet:"name=onchain_rate, type=BYTE_ARRAY, convertedtype=UTF8"`
	OnChainUSD             string  `parquet:"name=onchain_usd, type=BYTE_ARRAY, convertedtype=UTF8"`
	MissingMint            bool    `parquet:"name=missing_mint, type=BOOLEAN"`
	AmountMismatch         bool    `parquet:"name=amount_mismatch, type=BOOLEAN"`
	VoucherExpired         bool    `parquet:"name=voucher_expired, type=BOOLEAN"`
	CapBreached            bool    `parquet:"name=cap_breached, type=BOOLEAN"`
	CreatedAt              string  `parquet:"name=created_at, type=BYTE_ARRAY, convertedtype=UTF8"`
	FirstReceiptAt         string  `parquet:"name=first_receipt_at, type=BYTE_ARRAY, convertedtype=UTF8"`
	ApprovedAt             string  `parquet:"name=approved_at, type=BYTE_ARRAY, convertedtype=UTF8"`
	MintedAt               string  `parquet:"name=minted_at, type=BYTE_ARRAY, convertedtype=UTF8"`
	ReceiptLatencyMinutes  float64 `parquet:"name=receipt_latency_minutes, type=DOUBLE"`
	ApprovalLatencyMinutes float64 `parquet:"name=approval_latency_minutes, type=DOUBLE"`
	MintLatencyMinutes     float64 `parquet:"name=mint_latency_minutes, type=DOUBLE"`
	SLAWithin24h           bool    `parquet:"name=sla_within_24h, type=BOOLEAN"`
	ReceiptRetentionDays   int32   `parquet:"name=receipt_retention_days, type=INT32"`
	DecisionRetentionDays  int32   `parquet:"name=decision_retention_days, type=INT32"`
	ReportRetentionDays    int32   `parquet:"name=report_retention_days, type=INT32"`
	BranchInvoiceLimit     float64 `parquet:"name=branch_invoice_limit, type=DOUBLE"`
	BranchRegionCap        float64 `parquet:"name=branch_region_cap, type=DOUBLE"`
}

func writeParquet(path string, rows []*ReportRow) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("recon: create parquet: %w", err)
	}
	fw := writerfile.NewWriterFile(file)
	pw, err := writer.NewParquetWriter(fw, new(parquetRow), 1)
	if err != nil {
		file.Close()
		return fmt.Errorf("recon: parquet schema: %w", err)
	}
	pw.RowGroupSize = 128 * 1024 * 1024
	pw.CompressionType = parquet.CompressionCodec_SNAPPY

	for _, row := range rows {
		pr := &parquetRow{
			InvoiceID:              row.InvoiceID.String(),
			BranchID:               row.BranchID.String(),
			BranchName:             row.BranchName,
			Region:                 row.Region,
			Currency:               row.Currency,
			InvoiceAmount:          row.InvoiceAmount,
			InvoiceState:           row.InvoiceState,
			ProviderTxID:           row.ProviderTxID,
			VoucherStatus:          row.VoucherStatus,
			OnChainStatus:          row.OnChainStatus,
			OnChainFiatAmount:      row.OnChainFiatAmount,
			OnChainMintAmountWei:   row.OnChainMintAmountWei,
			OnChainRate:            row.OnChainRate,
			OnChainUSD:             row.OnChainUSD,
			MissingMint:            row.MissingMint,
			AmountMismatch:         row.AmountMismatch,
			VoucherExpired:         row.VoucherExpired,
			CapBreached:            row.CapBreached,
			CreatedAt:              row.CreatedAt.Format(time.RFC3339),
			FirstReceiptAt:         formatTime(row.FirstReceiptAt),
			ApprovedAt:             formatTime(row.ApprovedAt),
			MintedAt:               formatTime(row.MintedAt),
			ReceiptLatencyMinutes:  minutesFloat(row.ReceiptLatency),
			ApprovalLatencyMinutes: minutesFloat(row.ApprovalLatency),
			MintLatencyMinutes:     minutesFloat(row.MintLatency),
			SLAWithin24h:           row.SLAWithin24h,
			ReceiptRetentionDays:   int32(row.ReceiptRetentionDays),
			DecisionRetentionDays:  int32(row.DecisionRetentionDays),
			ReportRetentionDays:    int32(row.ReportRetentionDays),
			BranchInvoiceLimit:     row.BranchInvoiceLimit,
			BranchRegionCap:        row.BranchRegionCap,
		}
		if err := pw.Write(pr); err != nil {
			pw.WriteStop()
			file.Close()
			return fmt.Errorf("recon: parquet write: %w", err)
		}
	}
	if err := pw.WriteStop(); err != nil {
		file.Close()
		return fmt.Errorf("recon: parquet flush: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("recon: close parquet file: %w", err)
	}
	return nil
}

func minutesFloat(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return d.Minutes()
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func formatMinutes(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f", d.Minutes())
}

func durationBetween(start time.Time, end *time.Time) time.Duration {
	if end == nil || end.IsZero() {
		return 0
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	v := id
	return &v
}

func parseAmount(raw string) (float64, bool) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	if trimmed == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func slugify(input string) string {
	trimmed := strings.TrimSpace(strings.ToLower(input))
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", "#", "", "?", "", "&", "and", ":", "-", ";", "-", ",", "-", "..", "-", "__", "-")
	slug := replacer.Replace(trimmed)
	cleaned := make([]rune, 0, len(slug))
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			cleaned = append(cleaned, r)
		}
	}
	return strings.Trim(strings.TrimSpace(string(cleaned)), "-")
}
