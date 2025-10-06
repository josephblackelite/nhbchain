package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
)

type partnerContactRequest struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	Subject string `json:"subject"`
	Phone   string `json:"phone"`
}

type partnerApplicationRequest struct {
	Name             string                  `json:"name"`
	LegalName        string                  `json:"legal_name"`
	KYBDossierKey    string                  `json:"kyb_object_key"`
	LicensingDocsKey string                  `json:"licensing_object_key"`
	Contacts         []partnerContactRequest `json:"contacts"`
}

type partnerDecisionRequest struct {
	Notes string `json:"notes"`
}

var (
	errUnauthorized   = errors.New("unauthorized")
	errPartnerPending = errors.New("partner pending approval")
)

// SubmitPartnerApplication captures initial KYB data and creates a pending partner record.
func (s *Server) SubmitPartnerApplication(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	if claims.Role != auth.RolePartner && claims.Role != auth.RolePartnerAdmin {
		http.Error(w, "insufficient role", http.StatusForbidden)
		return
	}

	var req partnerApplicationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(req.Contacts) == 0 {
		http.Error(w, "at least one contact is required", http.StatusBadRequest)
		return
	}

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	now := s.Now()
	partner := models.Partner{
		ID:               uuid.New(),
		Name:             name,
		LegalName:        strings.TrimSpace(req.LegalName),
		KYBDossierKey:    strings.TrimSpace(req.KYBDossierKey),
		LicensingDocsKey: strings.TrimSpace(req.LicensingDocsKey),
		Approved:         false,
		SubmittedBy:      actorID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	contacts := make([]models.PartnerContact, 0, len(req.Contacts))
	seenSubjects := make(map[string]struct{})
	for _, c := range req.Contacts {
		subject := strings.TrimSpace(c.Subject)
		if subject == "" {
			http.Error(w, "contact subject is required", http.StatusBadRequest)
			return
		}
		subject = strings.ToLower(subject)
		if _, ok := seenSubjects[subject]; ok {
			http.Error(w, "duplicate contact subject", http.StatusBadRequest)
			return
		}
		seenSubjects[subject] = struct{}{}
		contacts = append(contacts, models.PartnerContact{
			ID:        uuid.New(),
			PartnerID: partner.ID,
			Name:      strings.TrimSpace(c.Name),
			Email:     strings.TrimSpace(c.Email),
			Role:      strings.TrimSpace(c.Role),
			Subject:   subject,
			Phone:     strings.TrimSpace(c.Phone),
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	if _, ok := seenSubjects[strings.ToLower(claims.Subject)]; !ok {
		http.Error(w, "requesting subject must be included in contacts", http.StatusBadRequest)
		return
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&partner).Error; err != nil {
			return err
		}
		if len(contacts) > 0 {
			if err := tx.Create(&contacts).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			http.Error(w, "contact already registered", http.StatusConflict)
			return
		}
		http.Error(w, "failed to create partner", http.StatusInternalServerError)
		return
	}

	response := struct {
		Partner  models.Partner          `json:"partner"`
		Contacts []models.PartnerContact `json:"contacts"`
	}{Partner: partner, Contacts: contacts}
	s.writeJSON(w, http.StatusCreated, response)
}

// UploadPartnerDossier records refreshed KYB / licensing documentation.
func (s *Server) UploadPartnerDossier(w http.ResponseWriter, r *http.Request) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}

	partnerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid partner id", http.StatusBadRequest)
		return
	}

	var req struct {
		KYBDossierKey    string `json:"kyb_object_key"`
		LicensingDocsKey string `json:"licensing_object_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		var partner models.Partner
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&partner, "id = ?", partnerID).Error; err != nil {
			return err
		}

		if claims.Role != auth.RoleRootAdmin {
			allowed, err := s.subjectLinkedToPartner(tx, partner.ID, claims.Subject)
			if err != nil {
				return err
			}
			if !allowed {
				return errUnauthorized
			}
		}

		now := s.Now()
		partner.KYBDossierKey = strings.TrimSpace(req.KYBDossierKey)
		partner.LicensingDocsKey = strings.TrimSpace(req.LicensingDocsKey)
		partner.UpdatedAt = now
		return tx.Save(&partner).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			http.Error(w, "partner not found", http.StatusNotFound)
		case errors.Is(err, errUnauthorized):
			http.Error(w, "insufficient permissions", http.StatusForbidden)
		default:
			http.Error(w, "failed to update dossier", http.StatusInternalServerError)
		}
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ApprovePartner marks the partner as approved and records the audit trail.
func (s *Server) ApprovePartner(w http.ResponseWriter, r *http.Request) {
	s.reviewPartner(w, r, true)
}

// RejectPartner records a rejection decision for the partner.
func (s *Server) RejectPartner(w http.ResponseWriter, r *http.Request) {
	s.reviewPartner(w, r, false)
}

func (s *Server) reviewPartner(w http.ResponseWriter, r *http.Request, approved bool) {
	claims, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	if claims.Role != auth.RoleRootAdmin || !auth.IsRootAdmin(claims.Subject) {
		http.Error(w, "root admin required", http.StatusForbidden)
		return
	}

	partnerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid partner id", http.StatusBadRequest)
		return
	}

	var req partnerDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	var response struct {
		Partner models.Partner `json:"partner"`
	}

	err = s.DB.Transaction(func(tx *gorm.DB) error {
		var partner models.Partner
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&partner, "id = ?", partnerID).Error; err != nil {
			return err
		}

		now := s.Now()
		approval := models.PartnerApproval{
			ID:        uuid.New(),
			PartnerID: partner.ID,
			Approved:  approved,
			Notes:     strings.TrimSpace(req.Notes),
			ActorID:   actorID,
			CreatedAt: now,
		}
		if err := tx.Create(&approval).Error; err != nil {
			return err
		}

		if approved {
			partner.Approved = true
			partner.ApprovedAt = &now
			partner.ApprovedBy = &actorID
		} else {
			partner.Approved = false
			partner.ApprovedAt = nil
			partner.ApprovedBy = nil
		}
		partner.UpdatedAt = now
		if err := tx.Save(&partner).Error; err != nil {
			return err
		}

		if err := s.appendEvent(tx, uuid.Nil, claims.Subject, fmt.Sprintf("partner.%s", partnerState(approved)), fmt.Sprintf("partner_id=%s", partner.ID)); err != nil {
			return err
		}

		response.Partner = partner
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			http.Error(w, "partner not found", http.StatusNotFound)
		default:
			http.Error(w, "failed to record decision", http.StatusInternalServerError)
		}
		return
	}

	s.writeJSON(w, http.StatusOK, response)
}

func partnerState(approved bool) string {
	if approved {
		return "approved"
	}
	return "rejected"
}

func (s *Server) subjectLinkedToPartner(tx *gorm.DB, partnerID uuid.UUID, subject string) (bool, error) {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return false, nil
	}
	var contact models.PartnerContact
	if err := tx.First(&contact, "partner_id = ? AND subject = ?", partnerID, subject).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Server) partnerForSubject(db *gorm.DB, subject string) (*models.Partner, error) {
	if db == nil {
		db = s.DB
	}
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var contact models.PartnerContact
	if err := db.Preload("Partner").First(&contact, "subject = ?", subject).Error; err != nil {
		return nil, err
	}
	if contact.Partner == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return contact.Partner, nil
}

func (s *Server) ensureApprovedPartner(w http.ResponseWriter, claims *auth.Claims) (*models.Partner, bool) {
	if claims.Role != auth.RolePartner && claims.Role != auth.RolePartnerAdmin {
		return nil, false
	}
	partner, err := s.partnerForSubject(nil, claims.Subject)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "partner account not found", http.StatusForbidden)
			return nil, true
		}
		http.Error(w, "failed to lookup partner", http.StatusInternalServerError)
		return nil, true
	}
	if !partner.Approved {
		http.Error(w, "partner pending review - submit KYB dossier for approval", http.StatusForbidden)
		return partner, true
	}
	return partner, false
}

func (s *Server) ensureInvoicePartnerApproved(tx *gorm.DB, creator uuid.UUID) error {
	partner, err := s.partnerForSubject(tx, creator.String())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if !partner.Approved {
		return errPartnerPending
	}
	return nil
}
