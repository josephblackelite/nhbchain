// Package refprice implements buybackd's core job: aggregate a market price
// for a base/quote pair (ZNHB/USD by default), sign it with each locally
// held buyback reference-price signer key, and submit the bundle to the
// chain once per epoch.
//
// Honesty note, carried over from this session's NHB/ZNHB pricing work:
// neither ZNHB nor NHB is listed on CoinGecko or NOWPayments (see
// cmd/nhb/main.go's oracle wiring). Whatever QuoteSource this service is
// configured with will, in practice, resolve to a manually-configured peg
// rather than genuine external market discovery, until a real listing
// exists somewhere. This service does not paper over that -- it signs and
// submits whatever its QuoteSource actually returns, honestly, rather than
// pretending a peg is a market price.
//
// Custody note: applyBuybackRefPrice (core/buyback_tx.go) requires the full
// M-of-N signature bundle in a single transaction -- there is no on-chain
// mechanism to submit partial signatures across multiple transactions and
// have the chain assemble them. So this service can only produce a
// submittable bundle if it locally holds at least SignerThreshold of the
// configured keys. That is the correct shape for today's deployment (one
// operator, all signer keys held locally) but not a genuinely
// multi-party-independent one: if signer keys are ever distributed to real
// separate custodians, this service's signing step needs to become a
// coordination protocol across separate processes, not a single process
// holding every key. Not attempted here -- flagged honestly instead of
// silently designed around.
package refprice

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/services/buybackd/rpcclient"
)

// QuoteSource resolves a freshly-aggregated rate for a base/quote pair.
// *nhbchain/native/swap.OracleAggregator satisfies this via a thin adapter
// (see services/buybackd/main.go); *nhbchain/services/swapd/oracle.Manager
// satisfies it directly.
type QuoteSource interface {
	Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error)
}

// Signer abstracts signing a 32-byte digest with one local key.
// *nhbchain/services/swapd/localsigner.Client satisfies this directly.
type Signer interface {
	Sign(ctx context.Context, digest []byte) ([]byte, string, error)
}

// ChainClient abstracts the two chain RPC calls this service needs.
// *nhbchain/services/buybackd/rpcclient.Client satisfies this directly.
type ChainClient interface {
	GetRefPriceStatus(ctx context.Context, epoch *uint64) (*rpcclient.RefPriceStatus, error)
	SubmitRefPrice(ctx context.Context, rateNum, rateDenom *big.Int, epoch, timestamp uint64, signatures [][]byte) (string, error)
}

// Service runs one submission attempt at a time; the caller (buybackd's
// main loop) decides the polling cadence.
type Service struct {
	source    QuoteSource
	signers   []Signer
	threshold int
	chain     ChainClient
	base      string
	quote     string
}

// New constructs a Service. Fails loudly (refuses to construct) if fewer
// local signers are configured than the threshold requires -- an
// under-provisioned service that silently never manages to submit anything
// is a worse failure mode than one that refuses to start.
func New(source QuoteSource, signers []Signer, threshold int, chain ChainClient, pair string) (*Service, error) {
	if source == nil {
		return nil, fmt.Errorf("refprice: quote source required")
	}
	if chain == nil {
		return nil, fmt.Errorf("refprice: chain client required")
	}
	if len(signers) == 0 {
		return nil, fmt.Errorf("refprice: at least one local signer required")
	}
	if threshold <= 0 {
		return nil, fmt.Errorf("refprice: threshold must be positive")
	}
	if len(signers) < threshold {
		return nil, fmt.Errorf("refprice: only %d local signer(s) configured, need at least %d to reach quorum -- see this package's doc comment on the custody assumption", len(signers), threshold)
	}
	base, quote, err := SplitPair(pair)
	if err != nil {
		return nil, err
	}
	return &Service{source: source, signers: signers, threshold: threshold, chain: chain, base: base, quote: quote}, nil
}

// Attempt runs one submission cycle: checks whether the current epoch
// already has a recorded reference price (in which case it does nothing --
// only the first submission per epoch is ever accepted on-chain), and if
// not, aggregates a fresh price, signs it with every configured local
// signer, and submits the bundle. Returns submitted=true only if this call
// actually put a new transaction on the chain.
func (s *Service) Attempt(ctx context.Context) (submitted bool, txHash string, err error) {
	if s == nil {
		return false, "", fmt.Errorf("refprice: service not configured")
	}
	status, err := s.chain.GetRefPriceStatus(ctx, nil)
	if err != nil {
		return false, "", fmt.Errorf("refprice: check current epoch status: %w", err)
	}
	if status.Epoch == 0 {
		return false, "", fmt.Errorf("refprice: chain reports epoch 0 -- epoch scheduling is not enabled on this network")
	}
	if status.HasRefPrice {
		return false, "", nil
	}
	epoch := status.Epoch

	rate, _, ts, err := s.source.Quote(ctx, s.base, s.quote)
	if err != nil {
		return false, "", fmt.Errorf("refprice: quote %s/%s: %w", s.base, s.quote, err)
	}
	if rate == nil || rate.Sign() <= 0 {
		return false, "", fmt.Errorf("refprice: non-positive rate for %s/%s", s.base, s.quote)
	}

	rp := &buyback.ReferencePrice{Rate: rate, Epoch: epoch, Timestamp: ts.UTC()}
	digest, err := rp.Hash()
	if err != nil {
		return false, "", fmt.Errorf("refprice: hash reference price: %w", err)
	}

	signatures := make([][]byte, 0, len(s.signers))
	for i, signer := range s.signers {
		sig, _, signErr := signer.Sign(ctx, digest[:])
		if signErr != nil {
			return false, "", fmt.Errorf("refprice: sign with local signer %d: %w", i, signErr)
		}
		if len(sig) != 65 {
			return false, "", fmt.Errorf("refprice: local signer %d returned unexpected signature length %d (want 65)", i, len(sig))
		}
		signatures = append(signatures, sig)
	}

	hash, err := s.chain.SubmitRefPrice(ctx, rate.Num(), rate.Denom(), epoch, uint64(ts.UTC().Unix()), signatures)
	if err != nil {
		return false, "", fmt.Errorf("refprice: submit: %w", err)
	}
	return true, hash, nil
}

// LendingChainClient abstracts the two chain RPC calls the lending
// reference-price attempt needs. *nhbchain/services/buybackd/rpcclient.Client
// satisfies this directly.
type LendingChainClient interface {
	GetLendingRefPriceStatus(ctx context.Context) (*rpcclient.LendingRefPriceStatus, error)
	SubmitLendingRefPrice(ctx context.Context, rateNum, rateDenom *big.Int, timestamp uint64, signatures [][]byte) (string, error)
}

// AttemptLendingRefPrice runs one lending-oracle submission cycle, reusing
// this Service's already-configured quote source and local signers -- the
// same operator holding the same buyback signer keys is, today, also the
// trusted source for lending's oracle price, so a second standalone service
// with its own keystore-loading/polling loop would just duplicate this
// one's for no present benefit; see core/lending_tx.go's
// applyLendingRefPriceTransaction doc comment for the chain-side half of
// this same reasoning.
//
// Deliberately NOT gated on "does the current epoch already have a
// recorded price" the way Attempt is -- lending's oracle price is not
// epoch-scoped and is meant to be refreshed every cycle (see
// core/tokenomics/lendingoracle's doc comment on why it carries no Epoch).
// Anti-replay instead comes from signing this call's own submission time
// rather than the underlying quote's origin time (a manually-configured
// oracle can hold that fixed indefinitely -- see buildQuoteSource's manual
// oracle seeding in services/buybackd/main.go) and the chain rejecting any
// submission whose Timestamp doesn't strictly exceed the last accepted one.
func (s *Service) AttemptLendingRefPrice(ctx context.Context, chain LendingChainClient) (submitted bool, txHash string, err error) {
	if s == nil {
		return false, "", fmt.Errorf("refprice: service not configured")
	}
	if chain == nil {
		return false, "", fmt.Errorf("refprice: lending chain client required")
	}

	rate, _, _, err := s.source.Quote(ctx, s.base, s.quote)
	if err != nil {
		return false, "", fmt.Errorf("refprice: lending quote %s/%s: %w", s.base, s.quote, err)
	}
	if rate == nil || rate.Sign() <= 0 {
		return false, "", fmt.Errorf("refprice: lending non-positive rate for %s/%s", s.base, s.quote)
	}

	now := time.Now().UTC()
	status, err := chain.GetLendingRefPriceStatus(ctx)
	if err != nil {
		return false, "", fmt.Errorf("refprice: check lending ref price status: %w", err)
	}
	if status.HasRefPrice && uint64(now.Unix()) <= status.Timestamp {
		return false, "", nil
	}

	rp := &lendingoracle.ReferencePrice{Rate: rate, Timestamp: now}
	digest, err := rp.Hash()
	if err != nil {
		return false, "", fmt.Errorf("refprice: hash lending reference price: %w", err)
	}

	signatures := make([][]byte, 0, len(s.signers))
	for i, signer := range s.signers {
		sig, _, signErr := signer.Sign(ctx, digest[:])
		if signErr != nil {
			return false, "", fmt.Errorf("refprice: sign lending ref price with local signer %d: %w", i, signErr)
		}
		if len(sig) != 65 {
			return false, "", fmt.Errorf("refprice: local signer %d returned unexpected signature length %d (want 65)", i, len(sig))
		}
		signatures = append(signatures, sig)
	}

	hash, err := chain.SubmitLendingRefPrice(ctx, rate.Num(), rate.Denom(), uint64(now.Unix()), signatures)
	if err != nil {
		return false, "", fmt.Errorf("refprice: submit lending ref price: %w", err)
	}
	return true, hash, nil
}

// SplitPair parses a "BASE/QUOTE" pair string (e.g. "ZNHB/USD") the same
// way New does internally -- exported so callers building a QuoteSource
// adapter (see services/buybackd/main.go) can honor the same BASE/QUOTE
// reading this package uses without duplicating the parsing logic.
func SplitPair(pair string) (base, quote string, err error) {
	trimmed := strings.TrimSpace(pair)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("refprice: invalid pair %q (want BASE/QUOTE)", pair)
	}
	base = strings.TrimSpace(parts[0])
	quote = strings.TrimSpace(parts[1])
	if base == "" || quote == "" {
		return "", "", fmt.Errorf("refprice: invalid pair %q (want BASE/QUOTE)", pair)
	}
	return base, quote, nil
}
