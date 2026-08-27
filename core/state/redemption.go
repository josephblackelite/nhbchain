package state

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// ErrRedeemRequestNotPending indicates UpdateRedemptionStatus was called for
// a redemption request that has already been settled (paid or failed) by an
// earlier attestation. Declared here, not in package core, so core/node.go's
// classifyProposalError (package core, which already imports this package as
// nhbstate) can classify it without an import cycle. A request's terminal
// status never reverts, so a resubmitted attestation for the same requestId
// can never become valid later -- prunable, not skippable.
var ErrRedeemRequestNotPending = errors.New("redemption: request not pending")

var redemptionRequestPrefix = []byte("redemption:request:")

// redemptionPendingIndexKey indexes the request IDs of every redemption
// request currently in RedemptionStatusPending, mirroring
// core/state/manager.go's potsoStakeOwnerIndexKey/appendStakeOwner/
// removeStakeOwner pattern. This is how an off-chain watcher (e.g.
// payments-gateway) discovers pending swap-out requests without scanning
// the whole state trie. Entries are appended on creation (PutRedemptionRequest)
// and removed on finalisation (UpdateRedemptionStatus) -- the removal is not
// optional: KVAppend's underlying storage is an O(n) full-rewrite per append
// (see manager.go's KVAppend), so an index that only ever grows would make
// every future redemption progressively more expensive to record.
var redemptionPendingIndexKey = []byte("redemption:pending:index")

// appendPendingRedemption adds requestID to the pending-request index.
// KVAppend already de-duplicates, so calling this more than once for the
// same ID is harmless.
func (m *Manager) appendPendingRedemption(requestID string) error {
	trimmed := strings.TrimSpace(requestID)
	if trimmed == "" {
		return fmt.Errorf("redemption: request id required")
	}
	return m.KVAppend(redemptionPendingIndexKey, []byte(trimmed))
}

// removePendingRedemption removes requestID from the pending-request index.
// Removing an ID that is not present is a no-op, not an error.
func (m *Manager) removePendingRedemption(requestID string) error {
	trimmed := strings.TrimSpace(requestID)
	if trimmed == "" {
		return fmt.Errorf("redemption: request id required")
	}
	var raw [][]byte
	if err := m.KVGetList(redemptionPendingIndexKey, &raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	target := []byte(trimmed)
	filtered := make([][]byte, 0, len(raw))
	for _, entry := range raw {
		if bytes.Equal(entry, target) {
			continue
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) == len(raw) {
		return nil
	}
	if len(filtered) == 0 {
		return m.trie.Update(kvKey(redemptionPendingIndexKey), nil)
	}
	return m.KVPut(redemptionPendingIndexKey, filtered)
}

// PendingRedemptionRequestIDs returns the request IDs of every redemption
// request currently pending, in index order (oldest-appended first).
func (m *Manager) PendingRedemptionRequestIDs() ([]string, error) {
	var raw [][]byte
	if err := m.KVGetList(redemptionPendingIndexKey, &raw); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(raw))
	for _, entry := range raw {
		ids = append(ids, string(entry))
	}
	return ids, nil
}

// PendingRedemptionRequests returns the full stored record for every
// currently pending redemption request. Entries whose underlying record is
// missing (should not happen in practice -- the index and the record are
// always written/removed together) are skipped rather than causing the
// whole call to fail.
func (m *Manager) PendingRedemptionRequests() ([]*StoredRedemptionRequest, error) {
	ids, err := m.PendingRedemptionRequestIDs()
	if err != nil {
		return nil, err
	}
	requests := make([]*StoredRedemptionRequest, 0, len(ids))
	for _, id := range ids {
		request, ok, err := m.GetRedemptionRequest(id)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		requests = append(requests, request)
	}
	return requests, nil
}

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
	if err := m.KVPut(key, req); err != nil {
		return err
	}
	if RedemptionStatus(req.Status) == RedemptionStatusPending {
		if err := m.appendPendingRedemption(trimmed); err != nil {
			return err
		}
	}
	return nil
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
		return fmt.Errorf("redemption: request %s is not pending: %w", trimmed, ErrRedeemRequestNotPending)
	}
	if status != RedemptionStatusPaid && status != RedemptionStatusFailed {
		return fmt.Errorf("redemption: invalid target status %q", status)
	}
	stored.Status = string(status)
	stored.SettledAt = settledAt
	stored.PayoutReference = strings.TrimSpace(payoutReference)
	stored.FailureReason = strings.TrimSpace(failureReason)
	if err := m.KVPut(key, stored); err != nil {
		return err
	}
	// The request just left RedemptionStatusPending for good (paid/failed are
	// both terminal -- see the one-shot finalisation comment above), so it
	// must come out of the pending index now. Skipping this is what would let
	// the index grow unbounded -- see redemptionPendingIndexKey's doc comment.
	return m.removePendingRedemption(trimmed)
}
