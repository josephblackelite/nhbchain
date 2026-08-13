// Package lendingoracle implements the canonical message, digest, and
// signature-quorum verification for the lending module's ZNHB/NHB
// reference-price mechanism -- the same M-of-N genesis-signer-quorum shape
// core/tokenomics/buyback uses for its own reference price, given its own
// small importable package so both the chain (core/lending_tx.go, for
// verification) and an off-chain submission service (services/buybackd, for
// signing) can share the exact same canonical-message logic instead of it
// being duplicated by hand in two places -- any drift between two
// hand-duplicated implementations would make every signature silently fail
// verification, or worse, verify against the wrong message.
//
// Deliberately domain-separated from buyback's own ReferencePriceDomainV1
// even though, today, both are signed by the very same genesis-declared
// signer keys (see core/lending_tx.go's applyLendingRefPriceTransaction doc
// comment on why): a signature minted for one purpose must never verify for
// the other.
package lendingoracle

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// ReferencePriceDomainV1 domain-separates lending oracle reference-price
// signatures from every other signed-message purpose in this codebase.
const ReferencePriceDomainV1 = "NHB_LENDING_REFPRICE_V1"

// ReferencePrice is the independently-signed ZNHB/NHB market price lending's
// oracle guard (native/lending.Engine.guardOracle) reads back. Unlike
// buyback.ReferencePrice this carries no Epoch: lending's oracle price is
// not epoch-gated, so replay protection instead comes from Timestamp being
// required to strictly increase across accepted submissions (see
// core/state.LendingRefPriceRecord).
type ReferencePrice struct {
	Rate      *big.Rat
	Timestamp time.Time
}

// CanonicalMessage renders the exact message the M-of-N signers sign over.
func (r *ReferencePrice) CanonicalMessage() (string, error) {
	if r == nil {
		return "", fmt.Errorf("lendingoracle: reference price not initialised")
	}
	if r.Rate == nil || r.Rate.Sign() <= 0 {
		return "", fmt.Errorf("lendingoracle: reference price rate must be positive")
	}
	if r.Timestamp.IsZero() {
		return "", fmt.Errorf("lendingoracle: reference price timestamp required")
	}
	builder := strings.Builder{}
	builder.WriteString(ReferencePriceDomainV1)
	builder.WriteString("|rate=")
	builder.WriteString(r.Rate.FloatString(18))
	builder.WriteString("|ts=")
	fmt.Fprintf(&builder, "%d", r.Timestamp.UTC().Unix())
	return builder.String(), nil
}

// Hash computes the keccak256 digest of the canonical message -- the digest
// every signer in the bundle must have signed.
func (r *ReferencePrice) Hash() ([32]byte, error) {
	var digest [32]byte
	message, err := r.CanonicalMessage()
	if err != nil {
		return digest, err
	}
	copy(digest[:], ethcrypto.Keccak256([]byte(message)))
	return digest, nil
}

// VerifyReferencePrice checks a bundle of signatures over rp's canonical
// digest against the provided signer set, requiring at least threshold
// unique valid signatures from distinct authorized signers. Reimplements
// (does not import) buyback.VerifyReferencePrice's logic: the underlying
// "recover signer, check membership, require a threshold of unique matches"
// shape is generic, but this package's ReferencePrice type and domain are
// lending's own -- see this package's doc comment for why the logic isn't
// shared via a cross-domain import instead. Takes the signer set/threshold
// directly (rather than a buyback.Config) so this package never needs to
// import buyback at all, even though callers today happen to pass buyback's
// own genesis-declared quorum.
func VerifyReferencePrice(signers [][20]byte, threshold uint32, rp *ReferencePrice, signatures [][]byte) ([][20]byte, error) {
	digest, err := rp.Hash()
	if err != nil {
		return nil, err
	}
	if len(signatures) == 0 {
		return nil, fmt.Errorf("lendingoracle: signature bundle required")
	}
	allowed := make(map[[20]byte]struct{}, len(signers))
	for _, signer := range signers {
		allowed[signer] = struct{}{}
	}
	seen := make(map[[20]byte]struct{})
	unique := make([][20]byte, 0, len(signatures))
	for i, sig := range signatures {
		if len(sig) != 65 {
			return nil, fmt.Errorf("lendingoracle: signature %d must be 65 bytes", i)
		}
		buf := make([]byte, len(sig))
		copy(buf, sig)
		if buf[64] >= 27 {
			buf[64] -= 27
		}
		if buf[64] != 0 && buf[64] != 1 {
			return nil, fmt.Errorf("lendingoracle: signature %d has invalid recovery id", i)
		}
		pubKey, err := ethcrypto.SigToPub(digest[:], buf)
		if err != nil {
			return nil, fmt.Errorf("lendingoracle: invalid signature %d: %w", i, err)
		}
		addr := ethcrypto.PubkeyToAddress(*pubKey)
		var signer [20]byte
		copy(signer[:], addr[:])
		if _, ok := allowed[signer]; !ok {
			return nil, fmt.Errorf("lendingoracle: signature %d not from an authorized reference-price signer", i)
		}
		if _, dup := seen[signer]; dup {
			continue
		}
		seen[signer] = struct{}{}
		unique = append(unique, signer)
	}
	if len(unique) < int(threshold) {
		return nil, fmt.Errorf("lendingoracle: insufficient signer quorum: have %d need %d", len(unique), threshold)
	}
	return unique, nil
}
