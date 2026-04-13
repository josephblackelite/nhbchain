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
	for i := 1; i < len(e.Heights); i++ {
		if e.Heights[i] < e.Heights[i-1] {
			return &ValidationError{Reason: RejectReasonUnsortedHeights, Message: "heights must be provided in ascending order"}
		}
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
