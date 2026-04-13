package funding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"nhbchain/services/otc-gateway/models"
)

// Processor coordinates funding confirmation state transitions based on custodian webhooks.
//
// It validates inbound notifications against approved partner dossiers before
// marking the invoice as FIAT_CONFIRMED. Successful confirmations emit an audit
// event attributed to the system actor (uuid.Nil).
type Processor struct {
	db  *gorm.DB
	now func() time.Time
}

// NewProcessor constructs a funding processor backed by the provided database.
func NewProcessor(db *gorm.DB, now func() time.Time) *Processor {
	if now == nil {
		now = time.Now
	}
	return &Processor{db: db, now: now}
}

// Notification represents the expected payload from a banking or custodian webhook.
type Notification struct {
	InvoiceID        uuid.UUID         `json:"invoice_id"`
	FiatAmount       float64           `json:"fiat_amount"`
	FiatCurrency     string            `json:"fiat_currency"`
	FundingReference string            `json:"funding_reference"`
	DossierKey       string            `json:"dossier_key"`
	Custodian        string            `json:"custodian"`
	Status           string            `json:"status"`
	Metadata         map[string]string `json:"metadata"`
}

var (
	// ErrInvoiceNotFound indicates the supplied invoice identifier was unknown.
	ErrInvoiceNotFound = errors.New("funding: invoice not found")
	// ErrInvoiceFinalised indicates the invoice can no longer transition into FIAT_CONFIRMED.
	ErrInvoiceFinalised = errors.New("funding: invoice already finalised")
	// ErrInvalidState is returned when the invoice is not ready for funding confirmation.
	ErrInvalidState = errors.New("funding: invoice not eligible for confirmation")
	// ErrPartnerNotApproved denotes that the associated partner record is not approved.
	ErrPartnerNotApproved = errors.New("funding: partner not approved")
	// ErrDossierMismatch indicates the webhook dossier key did not match the stored dossier.
	ErrDossierMismatch = errors.New("funding: dossier mismatch")
	// ErrMissingDossier indicates the partner has no dossier on record to compare against.
	ErrMissingDossier = errors.New("funding: missing dossier for partner")
)

// Process validates and applies a funding confirmation notification.
func (p *Processor) Process(ctx context.Context, payload Notification) (*models.Invoice, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("funding: processor not configured")
	}
	if payload.InvoiceID == uuid.Nil {
		return nil, fmt.Errorf("funding: invoice_id is required")
	}
	if payload.FiatAmount <= 0 {
		return nil, fmt.Errorf("funding: fiat_amount must be positive")
	}
	currency := strings.ToUpper(strings.TrimSpace(payload.FiatCurrency))
	if currency == "" {
		return nil, fmt.Errorf("funding: fiat_currency is required")
	}
	reference := strings.TrimSpace(payload.FundingReference)
	if reference == "" {
		return nil, fmt.Errorf("funding: funding_reference is required")
	}
	dossier := strings.TrimSpace(payload.DossierKey)

	var updated models.Invoice
	err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var invoice models.Invoice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invoice, "id = ?", payload.InvoiceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvoiceNotFound
			}
			return err
		}
		switch invoice.State {
		case models.StateMinted, models.StateSubmitted:
			return ErrInvoiceFinalised
		case models.StateApproved, models.StateFiatConfirmed:
			// permitted
		default:
			return ErrInvalidState
		}

		var contact models.PartnerContact
		if err := tx.Preload("Partner").First(&contact, "subject = ?", strings.ToLower(invoice.CreatedByID.String())).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPartnerNotApproved
			}
			return err
		}
		if contact.Partner == nil || !contact.Partner.Approved {
			return ErrPartnerNotApproved
		}
		expectedDossier := strings.TrimSpace(contact.Partner.KYBDossierKey)
		if expectedDossier == "" {
			return ErrMissingDossier
		}
		if dossier != "" && !strings.EqualFold(expectedDossier, dossier) {
			return ErrDossierMismatch
		}

		now := p.now()
		invoice.FiatAmount = payload.FiatAmount
		invoice.FiatCurrency = currency
		invoice.FundingReference = reference
		invoice.FundingStatus = models.FundingStatusConfirmed
		if invoice.State != models.StateFiatConfirmed {
			invoice.State = models.StateFiatConfirmed
		}
		invoice.UpdatedAt = now
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}

		details := fmt.Sprintf("funding_ref=%s custodian=%s amount=%.2f currency=%s", reference, strings.TrimSpace(payload.Custodian), payload.FiatAmount, currency)
		event := models.Event{
			ID:        uuid.New(),
			InvoiceID: &invoice.ID,
			UserID:    uuid.Nil,
			Action:    "invoice.funding_confirmed",
			Details:   details,
			CreatedAt: now,
		}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}

		updated = invoice
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}
