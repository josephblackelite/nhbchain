package swap

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcommon "github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

var (
	// ErrPriceProofNil indicates the submission did not include a price proof payload.
	ErrPriceProofNil = errors.New("swap: price proof required")
	// ErrPriceProofDomain indicates the supplied proof domain did not match the expected identifier.
	ErrPriceProofDomain = errors.New("swap: price proof domain invalid")
	// ErrPriceProofPair indicates the supplied base/quote pair is not supported.
	ErrPriceProofPair = errors.New("swap: price proof pair invalid")
	// ErrPriceProofProviderMismatch indicates the voucher provider and proof provider differ.
	ErrPriceProofProviderMismatch = errors.New("swap: price proof provider mismatch")
	// ErrPriceProofSignerUnknown indicates the signer address is not registered in state.
	ErrPriceProofSignerUnknown = errors.New("swap: price proof signer unknown")
	// ErrPriceProofSignatureInvalid indicates the signature could not be recovered or did not match the registered signer.
	ErrPriceProofSignatureInvalid = errors.New("swap: price proof signature invalid")
	// ErrPriceProofStale indicates the proof exceeded the configured freshness window.
	ErrPriceProofStale = errors.New("swap: price proof stale")
	// ErrPriceProofDeviation indicates the proof deviated beyond the allowed threshold from the previous observation.
	ErrPriceProofDeviation = errors.New("swap: price proof deviation too large")
)

// PriceProofStore exposes the state access required by the price proof engine.
type PriceProofStore interface {
	SwapPriceSigner(provider string) ([20]byte, bool, error)
	SwapLastPriceProof(base string) (*PriceProofRecord, bool, error)
	SwapPutPriceProof(base string, record *PriceProofRecord) error
}

// PriceProofEngine validates signed price proofs emitted by off-chain oracles.
type PriceProofEngine struct {
	store           PriceProofStore
	maxAge          time.Duration
	maxDeviationBps uint64
	now             func() time.Time
	futureTolerance time.Duration
}

// NewPriceProofEngine constructs an engine backed by the supplied store.
func NewPriceProofEngine(store PriceProofStore, maxAge time.Duration, maxDeviationBps uint64) *PriceProofEngine {
	engine := &PriceProofEngine{
		store:           store,
		maxAge:          maxAge,
		maxDeviationBps: maxDeviationBps,
		futureTolerance: 30 * time.Second,
	}
	return engine
}

// SetClock overrides the engine clock, primarily for deterministic testing.
func (e *PriceProofEngine) SetClock(now func() time.Time) {
	if e == nil {
		return
	}
	e.now = now
}

// Verify validates the supplied price proof against the configured guards.
func (e *PriceProofEngine) Verify(proof *PriceProof, provider string, token string) error {
	if e == nil {
		return fmt.Errorf("price proof engine not configured")
	}
	if proof == nil {
		return ErrPriceProofNil
	}
	domain := strings.TrimSpace(proof.Domain)
	if !strings.EqualFold(domain, PriceProofDomainV1) {
		return ErrPriceProofDomain
	}
	proofProvider := strings.ToLower(strings.TrimSpace(proof.Provider))
	if proofProvider == "" {
		return ErrPriceProofProviderMismatch
	}
	if strings.TrimSpace(provider) != "" && !strings.EqualFold(provider, proofProvider) {
		return ErrPriceProofProviderMismatch
	}
	base := normaliseSymbol(proof.Base)
	quote := normaliseSymbol(proof.Quote)
	if base == "" || quote == "" {
		return ErrPriceProofPair
	}
	switch base {
	case "NHB", "ZNHB":
		// valid base
	default:
		return ErrPriceProofPair
	}
	if quote != "USD" {
		return ErrPriceProofPair
	}
	if strings.TrimSpace(token) != "" && !strings.EqualFold(token, base) {
		return ErrPriceProofPair
	}
	signer, ok, err := e.store.SwapPriceSigner(proofProvider)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPriceProofSignerUnknown
	}
	hash, err := proof.Hash()
	if err != nil {
		return err
	}
	if len(proof.Signature) != 65 {
		return ErrPriceProofSignatureInvalid
	}
	pubKey, err := ethcrypto.SigToPub(hash, proof.Signature)
	if err != nil {
		return ErrPriceProofSignatureInvalid
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if recovered != ethcommon.BytesToAddress(signer[:]) {
		return ErrPriceProofSignatureInvalid
	}
	ts := proof.Timestamp
	if ts.IsZero() {
		return fmt.Errorf("price proof: timestamp required")
	}
	now := time.Now()
	if e.now != nil {
		now = e.now()
	}
	if e.futureTolerance > 0 && ts.After(now.Add(e.futureTolerance)) {
		return ErrPriceProofStale
	}
	if e.maxAge > 0 && now.Sub(ts) > e.maxAge {
		return ErrPriceProofStale
	}
	if e.maxDeviationBps > 0 {
		prev, ok, err := e.store.SwapLastPriceProof(base)
		if err != nil {
			return err
		}
		if ok && prev != nil && prev.Rate != nil && prev.Rate.Sign() > 0 && proof.Rate != nil {
			diff := new(big.Rat).Sub(proof.Rate, prev.Rate)
			if diff.Sign() < 0 {
				diff.Neg(diff)
			}
			threshold := new(big.Rat).Mul(prev.Rate, big.NewRat(int64(e.maxDeviationBps), 10000))
			if threshold.Sign() > 0 && diff.Cmp(threshold) == 1 {
				return ErrPriceProofDeviation
			}
		}
	}
	return nil
}

// Record persists the supplied proof as the latest observation for deviation checks.
func (e *PriceProofEngine) Record(proof *PriceProof) error {
	if e == nil {
		return fmt.Errorf("price proof engine not configured")
	}
	if proof == nil {
		return ErrPriceProofNil
	}
	base := normaliseSymbol(proof.Base)
	if base == "" {
		return ErrPriceProofPair
	}
	record := &PriceProofRecord{Timestamp: proof.Timestamp}
	if proof.Rate != nil {
		record.Rate = new(big.Rat).Set(proof.Rate)
	}
	return e.store.SwapPutPriceProof(base, record)
}

// ensure the file compiles when unused helpers trigger lint complaints.
