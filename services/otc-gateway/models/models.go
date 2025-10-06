package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Role enumerations for persistence.
const (
	RoleTeller       = "teller"
	RoleSupervisor   = "supervisor"
	RoleCompliance   = "compliance"
	RoleSuperAdmin   = "superadmin"
	RoleAuditor      = "auditor"
	RolePartner      = "partner"
	RolePartnerAdmin = "partneradmin"
	RoleRootAdmin    = "rootadmin"
)

// InvoiceState represents a state in the OTC order workflow.
type InvoiceState string

// All workflow states.
const (
	StateCreated         InvoiceState = "CREATED"
	StateReceiptUploaded InvoiceState = "RECEIPT_UPLOADED"
	StatePendingReview   InvoiceState = "PENDING_REVIEW"
	StateApproved        InvoiceState = "APPROVED"
	StateFiatConfirmed   InvoiceState = "FIAT_CONFIRMED"
	StateSigned          InvoiceState = "SIGNED"
	StateSubmitted       InvoiceState = "SUBMITTED"
	StateMinted          InvoiceState = "MINTED"
	StateRejected        InvoiceState = "REJECTED"
	StateExpired         InvoiceState = "EXPIRED"
)

// FundingStatus captures the lifecycle of fiat settlement for an invoice.
type FundingStatus string

// Enumerated funding statuses.
const (
	FundingStatusPending   FundingStatus = "PENDING"
	FundingStatusConfirmed FundingStatus = "CONFIRMED"
	FundingStatusRejected  FundingStatus = "REJECTED"
)

// Branch defines staff branch metadata and risk caps.
type Branch struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name         string    `gorm:"uniqueIndex"`
	Region       string    `gorm:"index"`
	RegionCap    float64   `gorm:"not null"`
	InvoiceLimit float64   `gorm:"not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// User stores authenticated personnel information.
type User struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	Email     string    `gorm:"uniqueIndex"`
	Role      string    `gorm:"index"`
	BranchID  uuid.UUID `gorm:"type:uuid;index"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Partner represents an external counterparty participating in OTC flows.
type Partner struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name             string    `gorm:"size:255"`
	LegalName        string    `gorm:"size:255"`
	KYBDossierKey    string    `gorm:"size:512"`
	LicensingDocsKey string    `gorm:"size:512"`
	Approved         bool      `gorm:"index"`
	ApprovedAt       *time.Time
	ApprovedBy       *uuid.UUID `gorm:"type:uuid"`
	SubmittedBy      uuid.UUID  `gorm:"type:uuid;index"`
	Contacts         []PartnerContact
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// PartnerContact captures the operational roster for a partner organisation.
type PartnerContact struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	PartnerID uuid.UUID `gorm:"type:uuid;index"`
	Name      string    `gorm:"size:128"`
	Email     string    `gorm:"size:255"`
	Role      string    `gorm:"size:64"`
	Subject   string    `gorm:"size:128;uniqueIndex"`
	Phone     string    `gorm:"size:64"`
	CreatedAt time.Time
	UpdatedAt time.Time
	Partner   *Partner `gorm:"constraint:OnDelete:CASCADE"`
}

// PartnerApproval keeps an immutable log of partner approval decisions.
type PartnerApproval struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	PartnerID uuid.UUID `gorm:"type:uuid;index"`
	Approved  bool
	Notes     string    `gorm:"size:1024"`
	ActorID   uuid.UUID `gorm:"type:uuid;index"`
	CreatedAt time.Time
}

// Invoice describes OTC orders across their lifecycle.
type Invoice struct {
	ID               uuid.UUID     `gorm:"type:uuid;primaryKey"`
	BranchID         uuid.UUID     `gorm:"type:uuid;index"`
	CreatedByID      uuid.UUID     `gorm:"type:uuid;index"`
	Amount           float64       `gorm:"not null"`
	Currency         string        `gorm:"size:16"`
	FiatAmount       float64       `gorm:"not null;default:0"`
	FiatCurrency     string        `gorm:"size:16"`
	FundingStatus    FundingStatus `gorm:"size:32;index"`
	FundingReference string        `gorm:"size:128"`
	State            InvoiceState  `gorm:"size:32;index"`
	Region           string        `gorm:"index"`
	Reference        string        `gorm:"size:128"`
	PartnerDID       string        `gorm:"size:255"`
	ComplianceTags   []byte        `gorm:"type:jsonb"`
	TravelRulePacket []byte        `gorm:"type:jsonb"`
	SanctionsStatus  string        `gorm:"size:64"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Receipts         []Receipt
	Decisions        []Decision
}

// Receipt captures receipt uploads stored in S3.
type Receipt struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey"`
	InvoiceID  uuid.UUID `gorm:"type:uuid;index"`
	ObjectKey  string
	UploadedBy uuid.UUID `gorm:"type:uuid"`
	CreatedAt  time.Time
}

// Decision records compliance/supervisor actions on an invoice.
type Decision struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	InvoiceID uuid.UUID `gorm:"type:uuid;index"`
	ActorID   uuid.UUID `gorm:"type:uuid;index"`
	Outcome   string    `gorm:"size:32"`
	Notes     string    `gorm:"size:512"`
	CreatedAt time.Time
}

// Voucher represents chain submissions generated from invoices.
type Voucher struct {
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	InvoiceID        uuid.UUID `gorm:"type:uuid;uniqueIndex"`
	ChainID          string    `gorm:"index"`
	Payload          string
	ProviderTxID     string        `gorm:"size:128;uniqueIndex"`
	Hash             string        `gorm:"size:130"`
	Signature        string        `gorm:"type:text"`
	SignerDN         string        `gorm:"size:255"`
	TxHash           string        `gorm:"size:130"`
	VoucherHash      string        `gorm:"size:130"`
	FiatAmount       float64       `gorm:"not null;default:0"`
	FiatCurrency     string        `gorm:"size:16"`
	FundingStatus    FundingStatus `gorm:"size:32;index"`
	FundingReference string        `gorm:"size:128"`
	Status           string        `gorm:"size:32;index"`
	ExpiresAt        time.Time
	SubmittedAt      *time.Time
	SubmittedBy      *uuid.UUID `gorm:"type:uuid"`
	PartnerDID       string     `gorm:"size:255"`
	ComplianceTags   []byte     `gorm:"type:jsonb"`
	TravelRulePacket []byte     `gorm:"type:jsonb"`
	SanctionsStatus  string     `gorm:"size:64"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Event is the staff audit trail structure.
type Event struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey"`
	InvoiceID *uuid.UUID `gorm:"type:uuid;index"`
	UserID    uuid.UUID  `gorm:"type:uuid;index"`
	Action    string     `gorm:"size:64"`
	Details   string     `gorm:"type:text"`
	CreatedAt time.Time
}

// IdempotencyKey stores request idempotency metadata.
type IdempotencyKey struct {
	Key       string `gorm:"primaryKey;size:128"`
	RequestID string `gorm:"size:64"`
	Method    string `gorm:"size:8"`
	Path      string `gorm:"size:255"`
	Status    int
	Response  string `gorm:"type:text"`
	CreatedAt time.Time
}

// AutoMigrate performs all schema migrations for the service.
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Branch{},
		&User{},
		&Partner{},
		&PartnerContact{},
		&PartnerApproval{},
		&Invoice{},
		&Receipt{},
		&Decision{},
		&Voucher{},
		&Event{},
		&IdempotencyKey{},
	)
}
