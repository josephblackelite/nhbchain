package swap

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// PriceProofDomainV1 defines the domain separator used when signing price proofs.
const PriceProofDomainV1 = "NHB_SWAP_PRICE_V1"

// PriceProof captures the signed oracle payload supplied alongside voucher submissions.
type PriceProof struct {
	Domain    string
	Provider  string
	Base      string
	Quote     string
	Rate      *big.Rat
	Timestamp time.Time
	Signature []byte
}

// NewPriceProof constructs a price proof instance from the raw submission payload.
func NewPriceProof(domain, provider, pair, rate string, ts int64, signature []byte) (*PriceProof, error) {
	trimmedDomain := strings.TrimSpace(domain)
	if trimmedDomain == "" {
		return nil, fmt.Errorf("price proof: domain required")
	}
	trimmedProvider := strings.TrimSpace(provider)
	if trimmedProvider == "" {
		return nil, fmt.Errorf("price proof: provider required")
	}
	trimmedPair := strings.TrimSpace(pair)
	if trimmedPair == "" {
		return nil, fmt.Errorf("price proof: pair required")
	}
	parts := strings.Split(trimmedPair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("price proof: invalid pair %q", pair)
	}
	base := strings.TrimSpace(parts[0])
	quote := strings.TrimSpace(parts[1])
	if base == "" || quote == "" {
		return nil, fmt.Errorf("price proof: invalid pair %q", pair)
	}
	trimmedRate := strings.TrimSpace(rate)
	if trimmedRate == "" {
		return nil, fmt.Errorf("price proof: rate required")
	}
	rat, ok := new(big.Rat).SetString(trimmedRate)
	if !ok {
		return nil, fmt.Errorf("price proof: invalid rate %q", rate)
	}
	if rat.Sign() <= 0 {
		return nil, fmt.Errorf("price proof: rate must be positive")
	}
	if ts <= 0 {
		return nil, fmt.Errorf("price proof: timestamp required")
	}
	proof := &PriceProof{
		Domain:    trimmedDomain,
		Provider:  trimmedProvider,
		Base:      base,
		Quote:     quote,
		Rate:      rat,
		Timestamp: time.Unix(ts, 0).UTC(),
	}
	if len(signature) > 0 {
		proof.Signature = append([]byte(nil), signature...)
	}
	return proof, nil
}

// CanonicalMessage renders the canonical message used for signature verification.
func (p *PriceProof) CanonicalMessage() (string, error) {
	if p == nil {
		return "", fmt.Errorf("price proof not initialised")
	}
	domain := strings.TrimSpace(p.Domain)
	if domain == "" {
		return "", fmt.Errorf("price proof: domain required")
	}
	provider := strings.ToLower(strings.TrimSpace(p.Provider))
	if provider == "" {
		return "", fmt.Errorf("price proof: provider required")
	}
	base := strings.ToUpper(strings.TrimSpace(p.Base))
	quote := strings.ToUpper(strings.TrimSpace(p.Quote))
	if base == "" || quote == "" {
		return "", fmt.Errorf("price proof: pair required")
	}
	rateStr := ""
	if p.Rate != nil {
		rateStr = p.Rate.FloatString(18)
	}
	if strings.TrimSpace(rateStr) == "" {
		return "", fmt.Errorf("price proof: rate required")
	}
	if p.Timestamp.IsZero() {
		return "", fmt.Errorf("price proof: timestamp required")
	}
	builder := strings.Builder{}
	builder.WriteString(strings.ToUpper(domain))
	builder.WriteString("|provider=")
	builder.WriteString(provider)
	builder.WriteString("|pair=")
	builder.WriteString(base)
	builder.WriteString("/")
	builder.WriteString(quote)
	builder.WriteString("|rate=")
	builder.WriteString(rateStr)
	builder.WriteString("|ts=")
	builder.WriteString(fmt.Sprintf("%d", p.Timestamp.UTC().Unix()))
	return builder.String(), nil
}

// Hash computes the keccak256 digest of the canonical message.
func (p *PriceProof) Hash() ([]byte, error) {
	message, err := p.CanonicalMessage()
	if err != nil {
		return nil, err
	}
	digest := ethcrypto.Keccak256([]byte(message))
	return digest, nil
}

// ID returns the hexadecimal representation of the canonical message digest.
func (p *PriceProof) ID() (string, error) {
	hash, err := p.Hash()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash), nil
}

// PriceProofRecord stores the last accepted price proof for deviation checks.
type PriceProofRecord struct {
	Rate      *big.Rat
	Timestamp time.Time
}

// Clone returns a defensive copy of the record.
func (r *PriceProofRecord) Clone() *PriceProofRecord {
	if r == nil {
		return nil
	}
	clone := &PriceProofRecord{Timestamp: r.Timestamp}
	if r.Rate != nil {
		clone.Rate = new(big.Rat).Set(r.Rate)
	}
	return clone
}

type storedPriceProofRecord struct {
	Rate      string
	Timestamp int64
}

func newStoredPriceProofRecord(record *PriceProofRecord) storedPriceProofRecord {
	stored := storedPriceProofRecord{}
	if record == nil {
		return stored
	}
	if record.Rate != nil {
		stored.Rate = strings.TrimSpace(record.Rate.FloatString(18))
	}
	if !record.Timestamp.IsZero() {
		stored.Timestamp = record.Timestamp.UTC().Unix()
	}
	return stored
}

func (s storedPriceProofRecord) toRecord() (*PriceProofRecord, error) {
	record := &PriceProofRecord{}
	trimmedRate := strings.TrimSpace(s.Rate)
	if trimmedRate != "" {
		rat, ok := new(big.Rat).SetString(trimmedRate)
		if !ok {
			return nil, fmt.Errorf("price proof record: invalid rate %q", s.Rate)
		}
		record.Rate = rat
	}
	if s.Timestamp != 0 {
		record.Timestamp = time.Unix(s.Timestamp, 0).UTC()
	}
	return record, nil
}
