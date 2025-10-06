package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"nhbchain/core"
	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/models"
)

// SignAndSubmit constructs a mint voucher, signs it via the HSM, and submits it to the swap RPC.
func (s *Server) SignAndSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Signer == nil || s.SwapClient == nil {
		http.Error(w, "signing disabled", http.StatusServiceUnavailable)
		return
	}
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
		Recipient     string `json:"recipient"`
		Token         string `json:"token"`
		Amount        string `json:"amount"`
		ProviderTxID  string `json:"provider_tx_id"`
		FiatAmount    string `json:"fiat_amount"`
		FiatCurrency  string `json:"fiat_currency"`
		SubmissionRef string `json:"submission_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	recipient := strings.TrimSpace(req.Recipient)
	if recipient == "" {
		http.Error(w, "recipient is required", http.StatusBadRequest)
		return
	}
	amount := strings.TrimSpace(req.Amount)
	if amount == "" {
		http.Error(w, "amount is required", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = "NHB"
	}
	providerTxID := strings.TrimSpace(req.ProviderTxID)

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	expiry := s.Now().Add(s.VoucherTTL).Unix()
	voucher := core.MintVoucher{
		InvoiceID: invoiceID.String(),
		Recipient: recipient,
		Token:     token,
		Amount:    amount,
		ChainID:   s.ChainID,
		Expiry:    expiry,
	}
	payload, err := voucher.CanonicalJSON()
	if err != nil {
		http.Error(w, fmt.Sprintf("voucher: %v", err), http.StatusBadRequest)
		return
	}
	digest, err := voucher.Digest()
	if err != nil {
		http.Error(w, fmt.Sprintf("digest: %v", err), http.StatusInternalServerError)
		return
	}

	if providerTxID == "" {
		providerTxID = invoiceID.String()
	}

	var (
		existingVoucher   models.Voucher
		existing          bool
		invoice           models.Invoice
		submissionBlocked bool
	)

	now := s.Now()
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Decisions").First(&invoice, "id = ?", invoiceID).Error; err != nil {
			return err
		}
		if err := s.ensureInvoicePartnerApproved(tx, invoice.CreatedByID); err != nil {
			if errors.Is(err, errPartnerPending) {
				submissionBlocked = true
			}
			return err
		}
		if invoice.State == models.StateSubmitted || invoice.State == models.StateMinted {
			if err := tx.First(&existingVoucher, "invoice_id = ?", invoice.ID).Error; err == nil {
				existing = true
				submissionBlocked = true
				return errVoucherAlreadySubmitted
			}
			return fmt.Errorf("invoice already %s", invoice.State)
		}
		if invoice.State != models.StateApproved && invoice.State != models.StateSigned {
			return fmt.Errorf("invoice must be APPROVED")
		}
		if invoice.CreatedByID == actorID {
			return fmt.Errorf("maker-checker violation")
		}
		for _, decision := range invoice.Decisions {
			if strings.EqualFold(decision.Outcome, "approved") && decision.ActorID == actorID {
				return fmt.Errorf("maker-checker violation")
			}
		}

		var branch models.Branch
		if err := tx.First(&branch, "id = ?", invoice.BranchID).Error; err != nil {
			return err
		}
		var outstanding float64
		if err := tx.Model(&models.Invoice{}).
			Where("branch_id = ? AND state IN ?", invoice.BranchID, []models.InvoiceState{models.StateSigned, models.StateSubmitted, models.StateMinted}).
			Select("COALESCE(SUM(amount),0)").
			Scan(&outstanding).Error; err != nil {
			return err
		}
		if outstanding+invoice.Amount > branch.RegionCap {
			return fmt.Errorf("branch cap exceeded")
		}

		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existingVoucher, "provider_tx_id = ?", providerTxID).Error; err == nil {
			if existingVoucher.InvoiceID != invoice.ID {
				return fmt.Errorf("providerTxId already used")
			}
			switch existingVoucher.Status {
			case voucherStatusSubmitted, voucherStatusMinted:
				existing = true
				submissionBlocked = true
				return errVoucherAlreadySubmitted
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		invoice.State = models.StateSigned
		invoice.UpdatedAt = now
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}

		expiryAt := time.Unix(expiry, 0).In(s.TZ)
		if existingVoucher.ID != uuid.Nil {
			existingVoucher.Payload = string(payload)
			existingVoucher.Hash = hex.EncodeToString(digest)
			existingVoucher.ChainID = strconv.FormatUint(s.ChainID, 10)
			existingVoucher.Status = voucherStatusSigning
			existingVoucher.ExpiresAt = expiryAt
			existingVoucher.UpdatedAt = now
			if err := tx.Save(&existingVoucher).Error; err != nil {
				return err
			}
		} else {
			record := models.Voucher{
				ID:           uuid.New(),
				InvoiceID:    invoice.ID,
				ChainID:      strconv.FormatUint(s.ChainID, 10),
				Payload:      string(payload),
				ProviderTxID: providerTxID,
				Hash:         hex.EncodeToString(digest),
				Status:       voucherStatusSigning,
				ExpiresAt:    expiryAt,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			existingVoucher = record
		}
		return nil
	})
	switch {
	case errors.Is(err, errVoucherAlreadySubmitted):
		if !existing {
			http.Error(w, "voucher already submitted", http.StatusConflict)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":       invoice.State,
			"providerTxId": existingVoucher.ProviderTxID,
			"txHash":       existingVoucher.TxHash,
			"voucherHash":  existingVoucher.VoucherHash,
			"signature":    existingVoucher.Signature,
		})
		return
	case errors.Is(err, errPartnerPending):
		http.Error(w, "partner pending review - minting disabled", http.StatusForbidden)
		return
	case errors.Is(err, gorm.ErrRecordNotFound):
		http.Error(w, "invoice not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if submissionBlocked {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":       invoice.State,
			"providerTxId": existingVoucher.ProviderTxID,
			"txHash":       existingVoucher.TxHash,
			"voucherHash":  existingVoucher.VoucherHash,
			"signature":    existingVoucher.Signature,
		})
		return
	}

	sigBytes, signerDN, err := s.Signer.Sign(r.Context(), digest)
	if err != nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, err.Error())
		http.Error(w, fmt.Sprintf("sign voucher: %v", err), http.StatusBadGateway)
		return
	}
	voucherHash, err := core.MintVoucherHash(&voucher, sigBytes)
	if err != nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, err.Error())
		http.Error(w, fmt.Sprintf("voucher hash: %v", err), http.StatusInternalServerError)
		return
	}
	sigHex := hex.EncodeToString(sigBytes)
	txHash, minted, err := s.SwapClient.SubmitMintVoucher(r.Context(), voucher, "0x"+sigHex, existingVoucher.ProviderTxID)
	if err != nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, err.Error())
		http.Error(w, fmt.Sprintf("submit voucher: %v", err), http.StatusBadGateway)
		return
	}

	status := voucherStatusSubmitted
	nextState := models.StateSubmitted
	if minted {
		status = voucherStatusMinted
		nextState = models.StateMinted
	}
	submittedAt := s.Now()
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invoice, "id = ?", invoiceID).Error; err != nil {
			return err
		}
		invoice.State = nextState
		invoice.UpdatedAt = submittedAt
		if err := tx.Save(&invoice).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&existingVoucher, "provider_tx_id = ?", existingVoucher.ProviderTxID).Error; err != nil {
			return err
		}
		existingVoucher.Signature = "0x" + sigHex
		existingVoucher.SignerDN = signerDN
		existingVoucher.TxHash = txHash
		existingVoucher.VoucherHash = voucherHash
		existingVoucher.Status = status
		existingVoucher.SubmittedAt = &submittedAt
		existingVoucher.SubmittedBy = &actorID
		existingVoucher.UpdatedAt = submittedAt
		if err := tx.Save(&existingVoucher).Error; err != nil {
			return err
		}
		if err := s.appendEvent(tx, invoice.ID, claims.Subject, "invoice.signed", fmt.Sprintf("hash=%s signer_dn=%s", existingVoucher.Hash, signerDN)); err != nil {
			return err
		}
		details := fmt.Sprintf("provider_tx_id=%s tx_hash=%s", existingVoucher.ProviderTxID, txHash)
		if minted {
			details += " minted=true"
		}
		if err := s.appendEvent(tx, invoice.ID, claims.Subject, "invoice.submitted", details); err != nil {
			return err
		}
		if minted {
			if err := s.appendEvent(tx, invoice.ID, claims.Subject, "invoice.minted", fmt.Sprintf("tx_hash=%s", txHash)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("persist voucher: %v", err), http.StatusInternalServerError)
		return
	}

	if !minted {
		go s.awaitMinted(context.Background(), existingVoucher.ProviderTxID, invoice.ID)
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       nextState,
		"txHash":       txHash,
		"voucherHash":  voucherHash,
		"providerTxId": existingVoucher.ProviderTxID,
		"signature":    "0x" + sigHex,
	})
}

func (s *Server) markVoucherFailure(invoiceID uuid.UUID, providerTxID, reason string) {
	_ = s.DB.Transaction(func(tx *gorm.DB) error {
		var voucher models.Voucher
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&voucher, "provider_tx_id = ?", providerTxID).Error; err != nil {
			return err
		}
		voucher.Status = voucherStatusFailed
		voucher.UpdatedAt = s.Now()
		if err := tx.Save(&voucher).Error; err != nil {
			return err
		}
		return s.appendEvent(tx, invoiceID, uuid.Nil.String(), "voucher.failed", fmt.Sprintf("provider_tx_id=%s reason=%s", providerTxID, reason))
	})
}

func (s *Server) awaitMinted(ctx context.Context, providerTxID string, invoiceID uuid.UUID) {
	ticker := time.NewTicker(s.PollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(s.VoucherTTL + 5*time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			return
		case <-ticker.C:
			status, txHash, ok := s.lookupVoucher(ctx, providerTxID)
			if !ok {
				continue
			}
			if strings.EqualFold(status, string(models.StateMinted)) || strings.EqualFold(status, voucherStatusMinted) {
				_ = s.DB.Transaction(func(tx *gorm.DB) error {
					var invoice models.Invoice
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invoice, "id = ?", invoiceID).Error; err != nil {
						return err
					}
					if invoice.State == models.StateMinted {
						return nil
					}
					invoice.State = models.StateMinted
					invoice.UpdatedAt = s.Now()
					if err := tx.Save(&invoice).Error; err != nil {
						return err
					}
					var voucher models.Voucher
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&voucher, "provider_tx_id = ?", providerTxID).Error; err != nil {
						return err
					}
					voucher.Status = voucherStatusMinted
					if strings.TrimSpace(voucher.TxHash) == "" {
						voucher.TxHash = txHash
					}
					voucher.UpdatedAt = s.Now()
					if err := tx.Save(&voucher).Error; err != nil {
						return err
					}
					return s.appendEvent(tx, invoice.ID, uuid.Nil.String(), "invoice.minted", fmt.Sprintf("tx_hash=%s provider_tx_id=%s", voucher.TxHash, providerTxID))
				})
				return
			}
		}
	}
}

func (s *Server) lookupVoucher(ctx context.Context, providerTxID string) (string, string, bool) {
	status, err := s.SwapClient.GetVoucher(ctx, providerTxID)
	if err != nil {
		return "", "", false
	}
	if status == nil {
		return "", "", false
	}
	txHash := strings.TrimSpace(status.TxHash)
	return strings.TrimSpace(status.Status), txHash, true
}
