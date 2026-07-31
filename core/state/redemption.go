package state

import (
	"encoding/hex"
	"fmt"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

var redemptionRequestPrefix = []byte("redemption:request:")

// RedemptionStatus captures the lifecycle of an NHB redemption (swap-out)
// request. See docs/issue30.md item 5/35.
type RedemptionStatus string

const (
	// RedemptionStatusPending marks a request whose NHB has been burned
	// on-chain and is awaiting an off-chain stablecoin payout.
	RedemptionStatusPending RedemptionStatus = "pending"
	// RedemptionStatusPaid marks a request whose off-chain payout has been
	// confirmed by an authorized attestor.
	RedemptionStatusPaid RedemptionStatus = "paid"
	// RedemptionStatusFailed marks a request whose off-chain payout could
	// not be completed. The underlying NHB burn is NOT automatically
	// reversed -- this status exists to surface the failure for manual
	// operator reconciliation, not to trigger an automated refund.
	RedemptionStatusFailed RedemptionStatus = "failed"
)

// StoredRedemptionRequest is the RLP-encoded, on-chain representation of a
// redemption request.
type StoredRedemptionRequest struct {
	RequestID          string
	Account            []byte
	NHBAmountWei       string
	DestinationAsset   string
	DestinationAddress string
	Status             string
	CreatedAt          uint64
	SettledAt          uint64
	PayoutReference    string
	FailureReason      string
}

func redemptionRequestKey(requestID string) []byte {
	trimmed := strings.TrimSpace(requestID)
	buf := make([]byte, len(redemptionRequestPrefix)+len(trimmed))
	copy(buf, redemptionRequestPrefix)
	copy(buf[len(redemptionRequestPrefix):], trimmed)
	return ethcrypto.Keccak256(buf)
}

// RedemptionRequestID derives the canonical request ID for a redemption from
// the burn transaction's hash, hex-encoded. Using the transaction hash
// (rather than a client-supplied identifier) means the ID can't collide or be
// chosen adversarially -- it's exactly the transaction that performed the
// burn.
func RedemptionRequestID(txHash []byte) string {
	return hex.EncodeToString(txHash)
}

// PutRedemptionRequest persists a redemption request. Returns an error if a
// request already exists for this ID -- requests are create-once (by the
// burn transaction) and update-in-place thereafter via
// UpdateRedemptionStatus, never recreated.
func (m *Manager) PutRedemptionRequest(req *StoredRedemptionRequest) error {
	if req == nil {
		return fmt.Errorf("redemption: request must not be nil")
	}
	trimmed := strings.TrimSpace(req.RequestID)
	if trimmed == "" {
		return fmt.Errorf("redemption: request id required")
	}
	key := redemptionRequestKey(trimmed)
	var existing StoredRedemptionRequest
	ok, err := m.KVGet(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("redemption: request %s already exists", trimmed)
	}
	return m.KVPut(key, req)
}

// GetRedemptionRequest loads a previously recorded redemption request.
func (m *Manager) GetRedemptionRequest(requestID string) (*StoredRedemptionRequest, bool, error) {
	trimmed := strings.TrimSpace(requestID)
	if trimmed == "" {
		return nil, false, fmt.Errorf("redemption: request id required")
	}
	var stored StoredRedemptionRequest
	ok, err := m.KVGet(redemptionRequestKey(trimmed), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &stored, true, nil
}

// UpdateRedemptionStatus transitions a pending redemption request to paid or
// failed. Only pending requests may be transitioned -- this is a one-shot
// finalization, not a general-purpose mutable record.
func (m *Manager) UpdateRedemptionStatus(requestID string, status RedemptionStatus, settledAt uint64, payoutReference, failureReason string) error {
	trimmed := strings.TrimSpace(requestID)
	if trimmed == "" {
		return fmt.Errorf("redemption: request id required")
	}
	key := redemptionRequestKey(trimmed)
	var stored StoredRedemptionRequest
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("redemption: request %s not found", trimmed)
	}
	if RedemptionStatus(stored.Status) != RedemptionStatusPending {
		return fmt.Errorf("redemption: request %s is not pending", trimmed)
	}
	if status != RedemptionStatusPaid && status != RedemptionStatusFailed {
		return fmt.Errorf("redemption: invalid target status %q", status)
	}
	stored.Status = string(status)
	stored.SettledAt = settledAt
	stored.PayoutReference = strings.TrimSpace(payoutReference)
	stored.FailureReason = strings.TrimSpace(failureReason)
	return m.KVPut(key, stored)
}
