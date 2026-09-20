package evidence

import (
	"encoding/hex"
	"fmt"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type HeightLookup func(height uint64) bool

func ValidateEvidence(e *Evidence, hash [32]byte, currentHeight uint64, maxAge uint64, heightLookup HeightLookup) *ValidationError {
	if e == nil {
		return &ValidationError{Reason: RejectReasonUnknown, Message: "evidence payload required"}
	}
	if !e.Type.Valid() {
		return &ValidationError{Reason: RejectReasonInvalidType, Message: fmt.Sprintf("unknown type %q", e.Type)}
	}
	if isZeroAddress(e.Offender) {
		return &ValidationError{Reason: RejectReasonInvalidOffender, Message: "offender address required"}
	}
	if isZeroAddress(e.Reporter) {
		return &ValidationError{Reason: RejectReasonInvalidReporter, Message: "reporter address required"}
	}
	if len(e.Heights) == 0 {
		return &ValidationError{Reason: RejectReasonEmptyHeights, Message: "at least one block height required"}
	}
	if len(e.Heights) > MaxHeightsPerEvidence {
		return &ValidationError{Reason: RejectReasonOversized, Message: fmt.Sprintf("at most %d block heights per report", MaxHeightsPerEvidence)}
	}
	if len(e.Details) > MaxDetailsBytes {
		return &ValidationError{Reason: RejectReasonOversized, Message: fmt.Sprintf("details exceed %d bytes", MaxDetailsBytes)}
	}
	if e.Timestamp < 0 {
		return &ValidationError{Reason: RejectReasonInvalidTimestamp, Message: "timestamp must not be negative"}
	}
	for i := 1; i < len(e.Heights); i++ {
		if e.Heights[i] < e.Heights[i-1] {
			return &ValidationError{Reason: RejectReasonUnsortedHeights, Message: "heights must be provided in ascending order"}
		}
	}
	// An equivocation proof is about ONE decision point: the height, round and
	// vote type at which the offender signed two conflicting votes. The window
	// checks below look only at the heights the report LISTS, which the reporter
	// chooses freely, so the proof's own height has to be one of them -- else a
	// proof for any height, however old or never reached, could ride on a report
	// that lists today's -- and cannot lie beyond the chain's tip. A proof's
	// height being in the window is then the only way to report its offense at
	// all, which is what lets a record be pruned exactly when the offense can no
	// longer be reported. Both refusals are decided by the report and the block
	// height alone. Only the shape is read here; the signatures are verified
	// last, after the cheaper checks.
	var proof *EquivocationProof
	if e.Type == TypeEquivocation {
		parsed, err := parseEquivocationProof(e.Details)
		if err != nil {
			return &ValidationError{Reason: RejectReasonInvalidEquivocationProof, Message: err.Error()}
		}
		if parsed.Height > currentHeight {
			return &ValidationError{Reason: RejectReasonInvalidEquivocationProof, Message: fmt.Sprintf("proof is for height %d, above the current height %d", parsed.Height, currentHeight)}
		}
		if !containsHeight(e.Heights, parsed.Height) {
			return &ValidationError{Reason: RejectReasonInvalidEquivocationProof, Message: fmt.Sprintf("proof is for height %d, which the report does not list among its heights", parsed.Height)}
		}
		proof = parsed
	}
	for _, height := range e.Heights {
		if height > currentHeight {
			return &ValidationError{Reason: RejectReasonFutureHeight, Message: fmt.Sprintf("height %d is in the future", height)}
		}
		if maxAge > 0 && currentHeight > height && currentHeight-height > maxAge {
			return &ValidationError{Reason: RejectReasonExpired, Message: fmt.Sprintf("height %d exceeds evidence window", height)}
		}
		if heightLookup != nil && !heightLookup(height) {
			return &ValidationError{Reason: RejectReasonUnknownHeight, Message: fmt.Sprintf("unknown block height %d", height)}
		}
	}
	if len(e.ReporterSig) != 65 {
		return &ValidationError{Reason: RejectReasonInvalidSignature, Message: "reporter signature must be 65 bytes"}
	}
	digest := e.SigningDigest(hash)
	pubKey, err := ethcrypto.SigToPub(digest, e.ReporterSig)
	if err != nil {
		return &ValidationError{Reason: RejectReasonInvalidSignature, Message: "invalid reporter signature"}
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if !strings.EqualFold(recovered.Hex()[2:], hex.EncodeToString(e.Reporter[:])) {
		return &ValidationError{Reason: RejectReasonInvalidSignature, Message: "signature does not match reporter"}
	}
	// NHB-AUDIT-C10: everything above only proves the REPORTER signed this
	// accusation -- it says nothing about whether the OFFENDER actually
	// did anything. For EQUIVOCATION specifically, require and verify a
	// concrete proof (two conflicting votes signed by the offender's own
	// key) before this evidence can ever result in a real slash.
	if proof != nil {
		if err := verifyEquivocationVotes(proof, e.Offender); err != nil {
			return &ValidationError{Reason: RejectReasonInvalidEquivocationProof, Message: err.Error()}
		}
	}
	return nil
}

func isZeroAddress(addr [20]byte) bool {
	for _, b := range addr {
		if b != 0 {
			return false
		}
	}
	return true
}
