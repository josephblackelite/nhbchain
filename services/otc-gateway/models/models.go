package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Role enumerations for persistence.
const (
	RoleTeller     = "teller"
	RoleSupervisor = "supervisor"
	RoleCompliance = "compliance"
	RoleSuperAdmin = "superadmin"
	RoleAuditor    = "auditor"
)

// InvoiceState represents a state in the OTC order workflow.
type InvoiceState string

// All workflow states.
const (
	StateCreated         InvoiceState = "CREATED"
	StateReceiptUploaded InvoiceState = "RECEIPT_UPLOADED"
	StatePendingReview   InvoiceState = "PENDING_REVIEW"
	StateApproved        InvoiceState = "APPROVED"
	StateSigned          InvoiceState = "SIGNED"
	StateSubmitted       InvoiceState = "SUBMITTED"
	StateMinted          InvoiceState = "MINTED"
	StateRejected        InvoiceState = "REJECTED"
	StateExpired         InvoiceState = "EXPIRED"
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

// Invoice describes OTC orders across their lifecycle.
type Invoice struct {
	ID          uuid.UUID    `gorm:"type:uuid;primaryKey"`
	BranchID    uuid.UUID    `gorm:"type:uuid;index"`
	CreatedByID uuid.UUID    `gorm:"type:uuid;index"`
	Amount      float64      `gorm:"not null"`
	Currency    string       `gorm:"size:16"`
	State       InvoiceState `gorm:"size:32;index"`
	Region      string       `gorm:"index"`
	Reference   string       `gorm:"size:128"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Receipts    []Receipt
	Decisions   []Decision
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
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	InvoiceID uuid.UUID `gorm:"type:uuid;uniqueIndex"`
	ChainID   string    `gorm:"index"`
	Payload   string
	CreatedAt time.Time
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
		&Invoice{},
		&Receipt{},
		&Decision{},
		&Voucher{},
		&Event{},
		&IdempotencyKey{},
	)
}
