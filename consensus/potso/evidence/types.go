package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"
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

// evidenceRLPShadow mirrors Evidence field-for-field except Timestamp, which
// is carried as uint64 instead of int64 -- go-ethereum's rlp package has no
// encoding for signed integers at all ("rlp: type int64 is not
// RLP-serializable"), so a plain rlp.EncodeToBytes(Evidence{...}) has always
// failed for every real submission (Timestamp is never the zero value in
// practice). EncodeRLP/DecodeRLP below route every RLP encode/decode of an
// Evidence value through this shadow so consensus/potso/evidence/store.go's
// Store (and, for the NHB-AUDIT-C10 follow-up, the state-trie-backed
// TxTypeSubmitEvidence record) can actually round-trip evidence at all. This
// is purely a wire-format fix: it does not change CanonicalHash or
// SigningDigest, which never used RLP to begin with (see their own
// hand-rolled, delimited-buffer encodings above) -- so no signature a
// reporter has ever produced is affected.
type evidenceRLPShadow struct {
	Type        string
	Offender    [20]byte
	Heights     []uint64
	Details     []byte
	Reporter    [20]byte
	ReporterSig []byte
	Timestamp   uint64
}

// EncodeRLP implements rlp.Encoder. Timestamp must be non-negative to be
// representable -- true of every Evidence this codebase has ever
// constructed (always a Unix timestamp near time.Now()).
func (e Evidence) EncodeRLP(w io.Writer) error {
	if e.Timestamp < 0 {
		return fmt.Errorf("evidence: timestamp must not be negative, got %d", e.Timestamp)
	}
	shadow := evidenceRLPShadow{
		Type:        string(e.Type),
		Offender:    e.Offender,
		Heights:     e.Heights,
		Details:     e.Details,
		Reporter:    e.Reporter,
		ReporterSig: e.ReporterSig,
		Timestamp:   uint64(e.Timestamp),
	}
	return rlp.Encode(w, &shadow)
}

// DecodeRLP implements rlp.Decoder, reconstructing an Evidence value from
// the shadow layout EncodeRLP writes.
func (e *Evidence) DecodeRLP(s *rlp.Stream) error {
	var shadow evidenceRLPShadow
	if err := s.Decode(&shadow); err != nil {
		return err
	}
	e.Type = Type(shadow.Type)
	e.Offender = shadow.Offender
	e.Heights = shadow.Heights
	e.Details = shadow.Details
	e.Reporter = shadow.Reporter
	e.ReporterSig = shadow.ReporterSig
	e.Timestamp = int64(shadow.Timestamp)
	return nil
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
	// NHB-AUDIT-C10: Details failed to decode as an EquivocationProof, or
	// decoded but didn't cryptographically prove the offender themselves
	// signed two conflicting votes, or proved a decision point the report
	// does not list among its heights or the chain has not reached. Only
	// reachable for TypeEquivocation -- see equivocation.go and verify.go.
	RejectReasonInvalidEquivocationProof RejectReason = "invalid_equivocation_proof"
	// The payload is larger than the fixed bounds below allow (too many
	// heights, or Details longer than MaxDetailsBytes). Evidence is stored
	// in state for its whole retention window, so its size is bounded.
	RejectReasonOversized RejectReason = "oversized"
	// Timestamp is negative. It is part of the signed digest but is stored
	// as an unsigned value, so a negative one could never be recorded.
	RejectReasonInvalidTimestamp RejectReason = "invalid_timestamp"
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

// recordRLPShadow mirrors Record field-for-field except ReceivedAt, carried
// as uint64 -- see evidenceRLPShadow's doc comment (same file) for why a
// plain int64 field is never RLP-serializable via go-ethereum's rlp
// package. Evidence itself round-trips via its own EncodeRLP/DecodeRLP
// (recursed into automatically by rlp.Encode/Stream.Decode below), so only
// this struct's own ReceivedAt field needs the same treatment here.
type recordRLPShadow struct {
	Hash       [32]byte
	Evidence   Evidence
	ReceivedAt uint64
}

// EncodeRLP implements rlp.Encoder.
func (r Record) EncodeRLP(w io.Writer) error {
	if r.ReceivedAt < 0 {
		return fmt.Errorf("evidence: receivedAt must not be negative, got %d", r.ReceivedAt)
	}
	shadow := recordRLPShadow{Hash: r.Hash, Evidence: r.Evidence, ReceivedAt: uint64(r.ReceivedAt)}
	return rlp.Encode(w, &shadow)
}

// DecodeRLP implements rlp.Decoder.
func (r *Record) DecodeRLP(s *rlp.Stream) error {
	var shadow recordRLPShadow
	if err := s.Decode(&shadow); err != nil {
		return err
	}
	r.Hash = shadow.Hash
	r.Evidence = shadow.Evidence
	r.ReceivedAt = int64(shadow.ReceivedAt)
	return nil
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

// Offense is what a report accuses its offender of, taken apart from how the
// report happens to be written down. The canonical hash of a report covers
// everything the reporter chooses -- the heights it lists, the exact bytes of
// its details, its timestamp -- so several reports with different hashes can
// accuse an offender of one and the same misconduct. Being recorded once, and
// being penalised once, are properties of the offense, not of one encoding of it.
type Offense struct {
	// Key identifies the offense. Two reports with the same Key accuse the
	// offender of the same thing, whoever reports it and however it is written,
	// and the offender answers for it once.
	Key [32]byte
	// Height is the height the offense was committed at. A report can only be
	// submitted while every height it lists is inside the evidence window and it
	// has to list this one, so this height is what a record's stay in state is
	// measured from: the record must outlive every report that could still be
	// submitted about the same offense, or the offense could be reported again.
	Height uint64
}

// Offense returns the identity the record is recorded and penalised under and the
// height its stay in the evidence window is measured from.
//
// For an EQUIVOCATION report that is the double-sign its proof is about: the
// offender and the decision point (height, round, vote type) at which it signed
// conflicting votes. Any other report has no proof to identify a misdeed by, so
// it is its own offense: its hash, and the oldest height it references.
func (r *Record) Offense() Offense {
	if r == nil {
		return Offense{}
	}
	if offense, ok := r.Evidence.equivocationOffense(); ok {
		return offense
	}
	return Offense{Key: r.Hash, Height: r.MinHeight()}
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
	// MaxPageLimit caps how many records one list call returns, whatever
	// Limit the caller asks for.
	MaxPageLimit = 200

	// MaxPendingRecords is the hard bound on evidence records held in state
	// at once. A record leaves the index once every height it references is
	// older than DefaultMaxAgeBlocks (the same window ValidateEvidence
	// admits), so the index -- and the per-block work that walks it -- can
	// never grow past this many entries.
	MaxPendingRecords = 256
	// MaxRecordsPerReporter bounds how many live records one reporter can
	// hold, so filling the index takes MaxPendingRecords/MaxRecordsPerReporter
	// distinct bonded reporters rather than one.
	MaxRecordsPerReporter = 4
	// MaxHeightsPerEvidence and MaxDetailsBytes bound the size of a single
	// stored record. A genuine equivocation proof is well under 1 KiB.
	MaxHeightsPerEvidence = 64
	MaxDetailsBytes       = 2048
)

var (
	// ErrIndexFull is returned when a new record would push the index past
	// MaxPendingRecords. It is transient: room returns as old records age out.
	ErrIndexFull = errors.New("evidence: pending evidence index is full")
	// ErrReporterQuota is returned when a reporter already holds
	// MaxRecordsPerReporter live records. Also transient.
	ErrReporterQuota = errors.New("evidence: reporter already holds the maximum number of live evidence records")
)
