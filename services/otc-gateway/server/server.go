package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"nhbchain/services/otc-gateway/auth"
	otcmw "nhbchain/services/otc-gateway/middleware"
	"nhbchain/services/otc-gateway/models"
)

// Server encapsulates dependencies for the HTTP API.
type Server struct {
	DB       *gorm.DB
	TZ       *time.Location
	ChainID  string
	S3Bucket string
	RPCBase  string
}

// New constructs a configured HTTP router with authentication and idempotency support.
func New(db *gorm.DB, tz *time.Location, chainID, s3Bucket, rpcBase string) http.Handler {
	s := &Server{DB: db, TZ: tz, ChainID: chainID, S3Bucket: s3Bucket, RPCBase: rpcBase}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(func(next http.Handler) http.Handler { return otcmw.WithIdempotency(db, next) })
	r.Use(auth.Authenticate)

	r.Route("/api/v1", func(api chi.Router) {
		api.Group(func(protected chi.Router) {
			protected.With(auth.RequireRole(auth.RoleTeller, auth.RoleSupervisor, auth.RoleSuperAdmin)).Post("/invoices", s.CreateInvoice)
			protected.With(auth.RequireRole(auth.RoleTeller, auth.RoleSupervisor, auth.RoleSuperAdmin)).Post("/invoices/{id}/receipt", s.UploadReceipt)
			protected.With(auth.RequireRole(auth.RoleSupervisor, auth.RoleCompliance, auth.RoleSuperAdmin)).Post("/invoices/{id}/pending-review", s.MarkPendingReview)
			protected.With(auth.RequireRole(auth.RoleSupervisor, auth.RoleCompliance, auth.RoleSuperAdmin)).Post("/invoices/{id}/approve", s.ApproveInvoice)
			protected.With(auth.RequireRole(auth.RoleAuditor, auth.RoleSupervisor, auth.RoleSuperAdmin, auth.RoleCompliance)).Get("/invoices/{id}", s.GetInvoice)
		})
	})

	return r
}

// CreateInvoice handles invoice creation and audit logging.
func (s *Server) CreateInvoice(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}

	var req struct {
		BranchID  uuid.UUID `json:"branch_id"`
		Amount    float64   `json:"amount"`
		Currency  string    `json:"currency"`
		Reference string    `json:"reference"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if req.Amount <= 0 {
		http.Error(w, "amount must be positive", http.StatusBadRequest)
		return
	}

	var branch models.Branch
	if err := s.DB.First(&branch, "id = ?", req.BranchID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "branch not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load branch", http.StatusInternalServerError)
		return
	}

	creatorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	now := time.Now().In(s.TZ)

	invoice := models.Invoice{
		ID:          uuid.New(),
		BranchID:    branch.ID,
		CreatedByID: creatorID,
		Amount:      req.Amount,
		Currency:    req.Currency,
		Reference:   req.Reference,
		State:       models.StateCreated,
		Region:      branch.Region,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if invoice.Currency == "" {
		invoice.Currency = "USD"
	}

	if err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&invoice).Error; err != nil {
			return err
		}
		return s.appendEvent(tx, invoice.ID, claims.Subject, "invoice.created", fmt.Sprintf("amount=%.2f currency=%s", invoice.Amount, invoice.Currency))
	}); err != nil {
		http.Error(w, "failed to create invoice", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, http.StatusCreated, invoice)
}

// UploadReceipt moves the invoice to RECEIPT_UPLOADED and persists receipt metadata.
func (s *Server) UploadReceipt(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}

	invoiceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid invoice id", http.StatusBadRequest)
		return
	}

	var req struct {
		ObjectKey string `json:"object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if req.ObjectKey == "" {
		http.Error(w, "object_key is required", http.StatusBadRequest)
		return
	}

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	if err := s.transitionInvoice(invoiceID, models.StateReceiptUploaded, claims.Subject, func(tx *gorm.DB, invoice *models.Invoice) error {
		receipt := models.Receipt{
			ID:         uuid.New(),
			InvoiceID:  invoice.ID,
			ObjectKey:  req.ObjectKey,
			UploadedBy: actorID,
			CreatedAt:  time.Now().In(s.TZ),
		}
		if err := tx.Create(&receipt).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		s.handleTransitionError(w, err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": string(models.StateReceiptUploaded)})
}

// MarkPendingReview transitions the invoice to PENDING_REVIEW.
func (s *Server) MarkPendingReview(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}

	invoiceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid invoice id", http.StatusBadRequest)
		return
	}

	if err := s.transitionInvoice(invoiceID, models.StatePendingReview, claims.Subject, nil); err != nil {
		s.handleTransitionError(w, err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": string(models.StatePendingReview)})
}

// ApproveInvoice enforces caps and records decision before moving to APPROVED.
func (s *Server) ApproveInvoice(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}

	invoiceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid invoice id", http.StatusBadRequest)
		return
	}

	var req struct {
		Notes string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != http.ErrBodyNotAllowed {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	err = s.transitionInvoice(invoiceID, models.StateApproved, claims.Subject, func(tx *gorm.DB, invoice *models.Invoice) error {
		var branch models.Branch
		if err := tx.First(&branch, "id = ?", invoice.BranchID).Error; err != nil {
			return err
		}
		if invoice.Amount > branch.InvoiceLimit {
			return fmt.Errorf("invoice amount %.2f exceeds branch limit %.2f", invoice.Amount, branch.InvoiceLimit)
		}

		var regionalTotal float64
		if err := tx.Model(&models.Invoice{}).
			Where("region = ? AND state IN ?", invoice.Region, []models.InvoiceState{
				models.StateApproved, models.StateSigned, models.StateSubmitted, models.StateMinted,
			}).
			Select("COALESCE(SUM(amount),0)").
			Scan(&regionalTotal).Error; err != nil {
			return err
		}

		if regionalTotal+invoice.Amount > branch.RegionCap {
			return fmt.Errorf("regional cap exceeded: %.2f + %.2f > %.2f", regionalTotal, invoice.Amount, branch.RegionCap)
		}

		decision := models.Decision{
			ID:        uuid.New(),
			InvoiceID: invoice.ID,
			ActorID:   actorID,
			Outcome:   "approved",
			Notes:     req.Notes,
			CreatedAt: time.Now().In(s.TZ),
		}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		s.handleTransitionError(w, err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": string(models.StateApproved)})
}

// GetInvoice returns invoice details including receipts and decisions for auditors.
func (s *Server) GetInvoice(w http.ResponseWriter, r *http.Request) {
	invoiceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid invoice id", http.StatusBadRequest)
		return
	}

	var invoice models.Invoice
	if err := s.DB.Preload("Receipts").Preload("Decisions").First(&invoice, "id = ?", invoiceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "invoice not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load invoice", http.StatusInternalServerError)
		return
	}

	s.writeJSON(w, http.StatusOK, invoice)
}

// transitionInvoice wraps state change with validation, persistence, and audit logging.
func (s *Server) transitionInvoice(invoiceID uuid.UUID, next models.InvoiceState, actor string, hook func(*gorm.DB, *models.Invoice) error) error {
	return s.DB.Transaction(func(tx *gorm.DB) error {
		var invoice models.Invoice
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invoice, "id = ?", invoiceID).Error; err != nil {
			return err
		}
		if err := ValidateTransition(invoice.State, next); err != nil {
			return err
		}
		invoice.State = next
		invoice.UpdatedAt = time.Now().In(s.TZ)
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}
		if hook != nil {
			if err := hook(tx, &invoice); err != nil {
				return err
			}
		}
		return s.appendEvent(tx, invoice.ID, actor, fmt.Sprintf("invoice.%s", next), "")
	})
}

func (s *Server) appendEvent(tx *gorm.DB, invoiceID uuid.UUID, actor string, action string, details string) error {
	actorID, err := uuid.Parse(actor)
	if err != nil {
		return fmt.Errorf("invalid actor id: %w", err)
	}
	event := models.Event{
		ID:        uuid.New(),
		InvoiceID: &invoiceID,
		UserID:    actorID,
		Action:    action,
		Details:   details,
		CreatedAt: time.Now().In(s.TZ),
	}
	return tx.Create(&event).Error
}

func (s *Server) handleTransitionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		http.Error(w, "invoice not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
