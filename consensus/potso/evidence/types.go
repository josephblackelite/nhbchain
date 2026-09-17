package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"lukechampine.com/blake3"
)

type Type string

const (
	TypeDowntime             Type = "DOWNTIME"
	TypeEquivocation         Type = "EQUIVOCATION"
	TypeInvalidBlockProposal Type = "INVALID_BLOCK_PROPOSAL"
)

var validTypes = map[Type]struct{}{
	TypeDowntime:             {},
	TypeEquivocation:         {},
	TypeInvalidBlockProposal: {},
}

func ParseType(value string) (Type, error) {
	trimmed := strings.TrimSpace(value)
	upper := Type(strings.ToUpper(trimmed))
	if _, ok := validTypes[upper]; !ok {
		return "", fmt.Errorf("unknown evidence type %q", value)
	}
	return upper, nil
}

func (t Type) Valid() bool {
	_, ok := validTypes[t]
	return ok
}

const signingDomain = "potso_evidence"

// Evidence captures a misbehaviour report submitted to POTSO consensus.
type Evidence struct {
	Type        Type
	Offender    [20]byte
	Heights     []uint64
	Details     []byte
	Reporter    [20]byte
	ReporterSig []byte
	Timestamp   int64
}

func (e Evidence) Clone() Evidence {
	clone := Evidence{
		Type:      e.Type,
		Offender:  e.Offender,
		Reporter:  e.Reporter,
		Timestamp: e.Timestamp,
	}
	if len(e.Heights) > 0 {
		clone.Heights = append([]uint64(nil), e.Heights...)
	}
	if len(e.Details) > 0 {
		clone.Details = append([]byte(nil), e.Details...)
	}
	if len(e.ReporterSig) > 0 {
		clone.ReporterSig = append([]byte(nil), e.ReporterSig...)
	}
	return clone
}

func (e Evidence) CanonicalHash() ([32]byte, error) {
	var zero [32]byte
	if !e.Type.Valid() {
		return zero, fmt.Errorf("unknown evidence type %q", e.Type)
	}
	buf := bytes.NewBuffer(nil)
	if err := writeDelimited(buf, []byte(string(e.Type))); err != nil {
		return zero, err
	}
	buf.Write(e.Offender[:])
	heights := append([]uint64(nil), e.Heights...)
	sort.SliceStable(heights, func(i, j int) bool { return heights[i] < heights[j] })
	if err := binary.Write(buf, binary.BigEndian, uint32(len(heights))); err != nil {
		return zero, err
	}
	for _, height := range heights {
		if err := binary.Write(buf, binary.BigEndian, height); err != nil {
			return zero, err
		}
	}
	if err := writeDelimited(buf, e.Details); err != nil {
		return zero, err
	}
	return blake3.Sum256(buf.Bytes()), nil
}

func (e Evidence) SigningDigest(hash [32]byte) []byte {
	payload := fmt.Sprintf("%s|%s|%d", signingDomain, hex.EncodeToString(hash[:]), e.Timestamp)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}

func writeDelimited(buf *bytes.Buffer, data []byte) error {
	length := uint32(0)
	if data != nil {
		length = uint32(len(data))
	}
	if err := binary.Write(buf, binary.BigEndian, length); err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	if _, err := buf.Write(data); err != nil {
		return err
	}
	return nil
}

// RejectReason captures the reason an evidence submission was rejected.
type RejectReason string

const (
	RejectReasonUnknown          RejectReason = "unknown"
	RejectReasonInvalidType      RejectReason = "invalid_type"
	RejectReasonInvalidReporter  RejectReason = "invalid_reporter"
	RejectReasonInvalidSignature RejectReason = "invalid_signature"
	RejectReasonInvalidOffender  RejectReason = "invalid_offender"
	RejectReasonEmptyHeights     RejectReason = "empty_heights"
	RejectReasonUnsortedHeights  RejectReason = "unsorted_heights"
	RejectReasonFutureHeight     RejectReason = "future_height"
	RejectReasonExpired          RejectReason = "expired"
	RejectReasonUnknownHeight    RejectReason = "unknown_height"
)

// ValidationError surfaces deterministic validation failures to callers.
type ValidationError struct {
	Reason  RejectReason
	Message string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Reason)
}

// Record persists an accepted evidence submission.
type Record struct {
	Hash       [32]byte
	Evidence   Evidence
	ReceivedAt int64
}

// Clone produces a deep copy of the record.
func (r *Record) Clone() *Record {
	if r == nil {
		return nil
	}
	clone := &Record{
		Hash:       r.Hash,
		Evidence:   r.Evidence.Clone(),
		ReceivedAt: r.ReceivedAt,
	}
	return clone
}

// MinHeight returns the smallest height referenced by the evidence.
func (r *Record) MinHeight() uint64 {
	if r == nil || len(r.Evidence.Heights) == 0 {
		return 0
	}
	min := r.Evidence.Heights[0]
	for _, h := range r.Evidence.Heights[1:] {
		if h < min {
			min = h
		}
	}
	return min
}

// ReceiptStatus captures the outcome of an evidence submission.
type ReceiptStatus string

const (
	ReceiptStatusAccepted   ReceiptStatus = "accepted"
	ReceiptStatusIdempotent ReceiptStatus = "idempotent"
	ReceiptStatusRejected   ReceiptStatus = "rejected"
)

// Receipt summarises the processing result for a submission.
type Receipt struct {
	Hash   [32]byte
	Status ReceiptStatus
	Record *Record
	Reason *ValidationError
}

// Filter constraints applied when listing stored evidence.
type Filter struct {
	Offender   *[20]byte
	Type       Type
	FromHeight *uint64
	ToHeight   *uint64
	Offset     int
	Limit      int
}

const (
	DefaultMaxAgeBlocks uint64 = 8640
	DefaultPageLimit           = 50
)
