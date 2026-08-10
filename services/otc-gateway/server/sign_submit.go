package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	nhbcrypto "nhbchain/crypto"
	swap "nhbchain/native/swap"
	"nhbchain/services/otc-gateway/auth"
	"nhbchain/services/otc-gateway/identity"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/swaprpc"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
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
	recipientAddr, err := nhbcrypto.DecodeAddress(recipient)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid recipient: %v", err), http.StatusBadRequest)
		return
	}
	var recipientBytes [20]byte
	copy(recipientBytes[:], recipientAddr.Bytes())
	amount := strings.TrimSpace(req.Amount)
	if amount == "" {
		http.Error(w, "amount is required", http.StatusBadRequest)
		return
	}
	amountBig, ok := new(big.Int).SetString(amount, 10)
	if !ok || amountBig.Sign() <= 0 {
		http.Error(w, "invalid amount", http.StatusBadRequest)
		return
	}
	// The swap voucher path (rpc's swap_submitVoucher, the only RPC this
	// handler's SwapClient ever calls) mints ZNHB exclusively --
	// core/swap_voucher_tx.go's applySwapVoucherMintTransaction hard-rejects
	// any other token. Reject an explicit non-ZNHB request rather than
	// silently minting the wrong asset; NHB minting goes through a
	// different rail (mint_with_sig) this endpoint does not use.
	if requestedToken := strings.TrimSpace(req.Token); requestedToken != "" && !strings.EqualFold(requestedToken, "ZNHB") {
		http.Error(w, "token must be ZNHB", http.StatusBadRequest)
		return
	}
	const token = "ZNHB"
	providerTxID := strings.TrimSpace(req.ProviderTxID)

	actorID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "invalid subject", http.StatusUnauthorized)
		return
	}

	expiry := s.Now().Add(s.VoucherTTL).Unix()

	if providerTxID == "" {
		providerTxID = invoiceID.String()
	}

	var (
		identityResolution *identity.Resolution
		complianceJSON     []byte
		travelRuleJSON     []byte
		sanctionsStatus    string
	)

	var preflight models.Invoice
	if err := s.DB.First(&preflight, "id = ?", invoiceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "invoice not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to load invoice", http.StatusInternalServerError)
		return
	}

	switch preflight.State {
	case models.StateFiatConfirmed, models.StateSigned, models.StateSubmitted, models.StateMinted:
		// permitted
	default:
		http.Error(w, "invoice must be FIAT_CONFIRMED", http.StatusForbidden)
		return
	}
	if (preflight.State == models.StateFiatConfirmed || preflight.State == models.StateSigned) && preflight.FundingStatus != models.FundingStatusConfirmed {
		http.Error(w, "invoice funding not confirmed", http.StatusForbidden)
		return
	}

	fundingReference := strings.TrimSpace(preflight.FundingReference)
	if submissionRef := strings.TrimSpace(req.SubmissionRef); submissionRef != "" {
		fundingReference = submissionRef
	}
	if fundingReference == "" {
		http.Error(w, "funding reference required", http.StatusBadRequest)
		return
	}
	fiatAmount := preflight.FiatAmount
	if amtStr := strings.TrimSpace(req.FiatAmount); amtStr != "" {
		parsed, err := strconv.ParseFloat(amtStr, 64)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid fiat amount", http.StatusBadRequest)
			return
		}
		fiatAmount = parsed
	}
	if fiatAmount <= 0 {
		http.Error(w, "fiat amount required", http.StatusBadRequest)
		return
	}
	fiatCurrency := strings.ToUpper(strings.TrimSpace(preflight.FiatCurrency))
	if currency := strings.TrimSpace(req.FiatCurrency); currency != "" {
		fiatCurrency = strings.ToUpper(currency)
	}
	if fiatCurrency == "" {
		fiatCurrency = "USD"
	}

	// Build the real swap.VoucherV1 payload now that every field it needs
	// (recipient, amount, fiat currency/amount, order id, expiry) is known.
	// This is the shape swap_submitVoucher's decoder actually expects --
	// see swaprpc.MintSubmission's doc comment for why the previous
	// core.MintVoucher shape never worked here.
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, fmt.Sprintf("generate nonce: %v", err), http.StatusInternalServerError)
		return
	}
	voucher := swap.VoucherV1{
		Domain:     swap.VoucherDomainV1,
		ChainID:    s.ChainID,
		Token:      token,
		Recipient:  recipientBytes,
		Amount:     amountBig,
		Fiat:       fiatCurrency,
		FiatAmount: strconv.FormatFloat(fiatAmount, 'f', 2, 64),
		OrderID:    invoiceID.String(),
		Nonce:      nonce,
		Expiry:     expiry,
	}
	payload, err := json.Marshal(voucher)
	if err != nil {
		http.Error(w, fmt.Sprintf("voucher: %v", err), http.StatusInternalServerError)
		return
	}
	digest := voucher.Hash()

	partner, err := s.ensureInvoicePartnerApproved(nil, preflight.CreatedByID)
	if err != nil {
		if errors.Is(err, errPartnerPending) {
			http.Error(w, "partner pending review - minting disabled", http.StatusForbidden)
		} else {
			http.Error(w, "failed to verify partner", http.StatusInternalServerError)
		}
		return
	}

	requiresIdentity := partner != nil && (preflight.State == models.StateFiatConfirmed || preflight.State == models.StateSigned)
	if requiresIdentity {
		if s.Identity == nil {
			http.Error(w, "identity service unavailable", http.StatusServiceUnavailable)
			return
		}
		resolution, err := s.Identity.ResolvePartner(r.Context(), partner.ID)
		if err != nil {
			http.Error(w, fmt.Sprintf("resolve partner identity: %v", err), http.StatusBadGateway)
			return
		}
		if resolution == nil || strings.TrimSpace(resolution.PartnerDID) == "" {
			http.Error(w, "partner missing DID", http.StatusForbidden)
			return
		}
		if !resolution.Verified {
			http.Error(w, "partner DID not verified", http.StatusForbidden)
			return
		}
		if sanctionsBlocked(resolution.SanctionsStatus) {
			http.Error(w, "partner blocked by sanctions", http.StatusForbidden)
			return
		}
		if !hasTravelRuleTag(resolution.ComplianceTags) {
			http.Error(w, "partner missing travel rule attestation", http.StatusForbidden)
			return
		}
		identityResolution = resolution
		sanctionsStatus = strings.TrimSpace(resolution.SanctionsStatus)
		if len(resolution.ComplianceTags) > 0 {
			data, err := json.Marshal(resolution.ComplianceTags)
			if err != nil {
				http.Error(w, "failed to encode compliance tags", http.StatusInternalServerError)
				return
			}
			complianceJSON = data
		}
		if len(resolution.TravelRulePacket) > 0 {
			travelRuleJSON = append([]byte(nil), resolution.TravelRulePacket...)
		}
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
		if _, err := s.ensureInvoicePartnerApproved(tx, invoice.CreatedByID); err != nil {
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
		if invoice.State != models.StateFiatConfirmed && invoice.State != models.StateSigned {
			return fmt.Errorf("invoice must be FIAT_CONFIRMED")
		}
		if invoice.FundingStatus != models.FundingStatusConfirmed {
			return fmt.Errorf("invoice funding incomplete")
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
			Where("branch_id = ? AND state IN ?", invoice.BranchID, []models.InvoiceState{models.StateFiatConfirmed, models.StateSigned, models.StateSubmitted, models.StateMinted}).
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

		invoice.FiatAmount = fiatAmount
		invoice.FiatCurrency = fiatCurrency
		invoice.FundingReference = fundingReference
		invoice.FundingStatus = models.FundingStatusConfirmed
		invoice.State = models.StateSigned
		if identityResolution != nil {
			invoice.PartnerDID = strings.TrimSpace(identityResolution.PartnerDID)
			invoice.SanctionsStatus = sanctionsStatus
			invoice.ComplianceTags = append([]byte(nil), complianceJSON...)
			invoice.TravelRulePacket = append([]byte(nil), travelRuleJSON...)
		}
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
			existingVoucher.FiatAmount = fiatAmount
			existingVoucher.FiatCurrency = fiatCurrency
			existingVoucher.FundingReference = fundingReference
			existingVoucher.FundingStatus = models.FundingStatusConfirmed
			if identityResolution != nil {
				existingVoucher.PartnerDID = strings.TrimSpace(identityResolution.PartnerDID)
				existingVoucher.SanctionsStatus = sanctionsStatus
				existingVoucher.ComplianceTags = append([]byte(nil), complianceJSON...)
				existingVoucher.TravelRulePacket = append([]byte(nil), travelRuleJSON...)
			}
			if err := tx.Save(&existingVoucher).Error; err != nil {
				return err
			}
		} else {
			record := models.Voucher{
				ID:               uuid.New(),
				InvoiceID:        invoice.ID,
				ChainID:          strconv.FormatUint(s.ChainID, 10),
				Payload:          string(payload),
				ProviderTxID:     providerTxID,
				Hash:             hex.EncodeToString(digest),
				Status:           voucherStatusSigning,
				ExpiresAt:        expiryAt,
				CreatedAt:        now,
				UpdatedAt:        now,
				FiatAmount:       fiatAmount,
				FiatCurrency:     fiatCurrency,
				FundingStatus:    models.FundingStatusConfirmed,
				FundingReference: fundingReference,
			}
			if identityResolution != nil {
				record.PartnerDID = strings.TrimSpace(identityResolution.PartnerDID)
				record.SanctionsStatus = sanctionsStatus
				record.ComplianceTags = append([]byte(nil), complianceJSON...)
				record.TravelRulePacket = append([]byte(nil), travelRuleJSON...)
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

	var compliancePayload *swaprpc.MintCompliance
	if identityResolution != nil {
		compliancePayload = &swaprpc.MintCompliance{
			PartnerDID:      strings.TrimSpace(identityResolution.PartnerDID),
			ComplianceTags:  append([]string(nil), identityResolution.ComplianceTags...),
			SanctionsStatus: sanctionsStatus,
		}
		if len(identityResolution.TravelRulePacket) > 0 {
			compliancePayload.TravelRulePacket = append([]byte(nil), identityResolution.TravelRulePacket...)
		}
	}

	// Fetch a freshly-signed price proof -- swap_submitVoucher's consensus
	// execution path (applySwapVoucherMintTransaction) unconditionally
	// requires one, verified against a governance-registered
	// swap.priceSigners entry (see native/governance's
	// ProposalKindSwapPriceSignerUpdate). Fetched now, immediately before
	// signing/submitting, so the quote is as fresh as possible relative to
	// swap.Config.MaxQuoteAgeSeconds's on-chain freshness window.
	if s.PriceProof == nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, "price proof source not configured")
		http.Error(w, "price proof source not configured", http.StatusServiceUnavailable)
		return
	}
	pricePair := strings.TrimSpace(s.PriceProofPair)
	if pricePair == "" {
		pricePair = "ZNHB/USD"
	}
	priceProof, err := s.PriceProof.PriceProof(r.Context(), pricePair)
	if err != nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, err.Error())
		http.Error(w, fmt.Sprintf("fetch price proof: %v", err), http.StatusBadGateway)
		return
	}
	priceProofRate := ""
	if priceProof.Rate != nil {
		priceProofRate = priceProof.Rate.FloatString(18)
	}
	priceProofPayload := swaprpc.PriceProofPayload{
		Domain:    priceProof.Domain,
		Provider:  priceProof.Provider,
		Pair:      strings.TrimSpace(priceProof.Base) + "/" + strings.TrimSpace(priceProof.Quote),
		Rate:      priceProofRate,
		Timestamp: priceProof.Timestamp.UTC().Unix(),
		Signature: "0x" + hex.EncodeToString(priceProof.Signature),
	}

	sigBytes, signerDN, err := s.Signer.Sign(r.Context(), digest)
	if err != nil {
		s.markVoucherFailure(invoiceID, existingVoucher.ProviderTxID, err.Error())
		http.Error(w, fmt.Sprintf("sign voucher: %v", err), http.StatusBadGateway)
		return
	}
	// voucherHash is an audit/display convenience (keccak256 of the
	// voucher's canonical bytes concatenated with its signature) -- it is
	// not consumed by consensus; providerTxId/orderId are the real
	// correlation keys.
	voucherHash := "0x" + hex.EncodeToString(ethcrypto.Keccak256(append(append([]byte(nil), payload...), sigBytes...)))
	sigHex := hex.EncodeToString(sigBytes)
	usdAmount := ""
	if strings.EqualFold(fiatCurrency, "USD") {
		usdAmount = voucher.FiatAmount
	}
	submission := swaprpc.MintSubmission{
		Voucher:      voucher,
		SignatureHex: "0x" + sigHex,
		ProviderTxID: existingVoucher.ProviderTxID,
		Address:      recipient,
		USDAmount:    usdAmount,
		PriceProof:   priceProofPayload,
		Compliance:   compliancePayload,
	}
	txHash, minted, err := s.SwapClient.SubmitMintVoucher(r.Context(), submission)
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
		signedDetails := fmt.Sprintf("hash=%s signer_dn=%s funding_ref=%s", existingVoucher.Hash, signerDN, fundingReference)
		if err := s.appendEvent(tx, invoice.ID, claims.Subject, "invoice.signed", signedDetails); err != nil {
			return err
		}
		details := fmt.Sprintf("provider_tx_id=%s tx_hash=%s funding_ref=%s", existingVoucher.ProviderTxID, txHash, fundingReference)
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

func hasTravelRuleTag(tags []string) bool {
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, "travel-rule") || strings.Contains(normalized, "travel_rule") {
			return true
		}
	}
	return false
}

func sanctionsBlocked(status string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "" {
		return false
	}
	switch normalized {
	case "clear", "cleared", "none", "pass", "approved", "not_listed":
		return false
	}
	if strings.Contains(normalized, "block") || strings.Contains(normalized, "deny") || strings.Contains(normalized, "reject") || strings.Contains(normalized, "sanction") {
		return true
	}
	return false
}
