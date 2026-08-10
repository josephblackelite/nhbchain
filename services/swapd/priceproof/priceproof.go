// Package priceproof builds and signs the swap.PriceProof payloads that
// gate real ZNHB minting via the on-chain TxTypeSwapVoucherMint path
// (core/swap_voucher_tx.go's applySwapVoucherMintTransaction, verified by
// native/swap.PriceProofEngine.Verify against a registered
// swap.priceSigners entry -- see native/governance's
// ProposalKindSwapPriceSignerUpdate for how that entry gets registered).
//
// This closes gap 2b from the fiat-onramp investigation: nothing in this
// repository previously produced a signed PriceProof at all. Every call to
// Sign re-aggregates a fresh oracle median and signs it at request time --
// swap.Config's MaxQuoteAgeSeconds (~2 minutes in production) rules out ever
// serving a cached or batched proof.
package priceproof

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	swap "nhbchain/native/swap"
)

// Signer abstracts the capability to sign an arbitrary 32-byte digest.
// nhbchain/services/otc-gateway/hsm.Client satisfies this today (Sign is
// payload-agnostic), but this package declares its own minimal interface so
// it does not need to import that package's types directly. Whatever
// concrete signer is wired in here MUST use a key distinct from the
// ZNHB mint-voucher-signing key -- a price-signer key must not carry the
// mint-authority key's blast radius.
type Signer interface {
	Sign(ctx context.Context, digest []byte) ([]byte, string, error)
}

// QuoteSource resolves a freshly-aggregated median rate for a base/quote
// pair. *nhbchain/services/swapd/oracle.Manager satisfies this via its
// Quote method.
type QuoteSource interface {
	Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error)
}

// Service signs swap.PriceProof payloads on demand.
type Service struct {
	source   QuoteSource
	signer   Signer
	provider string
}

// New constructs a price-proof signing service. provider is the identifier
// embedded in every produced proof and must match the provider string a
// governance ProposalKindSwapPriceSignerUpdate proposal registers the
// signer's address under (e.g. "nowpayments", "otc-gateway").
func New(source QuoteSource, signer Signer, provider string) (*Service, error) {
	if source == nil {
		return nil, fmt.Errorf("priceproof: quote source required")
	}
	if signer == nil {
		return nil, fmt.Errorf("priceproof: signer required")
	}
	trimmedProvider := strings.TrimSpace(provider)
	if trimmedProvider == "" {
		return nil, fmt.Errorf("priceproof: provider required")
	}
	return &Service{source: source, signer: signer, provider: trimmedProvider}, nil
}

// Sign produces a freshly-aggregated, freshly-signed price proof for the
// supplied "BASE/QUOTE" pair (e.g. "ZNHB/USD").
func (s *Service) Sign(ctx context.Context, pair string) (*swap.PriceProof, error) {
	if s == nil {
		return nil, fmt.Errorf("priceproof: service not configured")
	}
	base, quote, err := splitPair(pair)
	if err != nil {
		return nil, err
	}

	rate, _, ts, err := s.source.Quote(ctx, base, quote)
	if err != nil {
		return nil, fmt.Errorf("priceproof: quote %s/%s: %w", base, quote, err)
	}
	if rate == nil || rate.Sign() <= 0 {
		return nil, fmt.Errorf("priceproof: non-positive rate for %s/%s", base, quote)
	}

	proof, err := swap.NewPriceProof(swap.PriceProofDomainV1, s.provider, base+"/"+quote, rate.FloatString(18), ts.UTC().Unix(), nil)
	if err != nil {
		return nil, fmt.Errorf("priceproof: build proof: %w", err)
	}
	hash, err := proof.Hash()
	if err != nil {
		return nil, fmt.Errorf("priceproof: hash proof: %w", err)
	}
	sig, _, err := s.signer.Sign(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("priceproof: sign: %w", err)
	}
	if len(sig) != 65 {
		return nil, fmt.Errorf("priceproof: signer returned unexpected signature length %d (want 65)", len(sig))
	}
	proof.Signature = sig
	return proof, nil
}

func splitPair(pair string) (base, quote string, err error) {
	trimmed := strings.TrimSpace(pair)
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("priceproof: invalid pair %q", pair)
	}
	base = strings.TrimSpace(parts[0])
	quote = strings.TrimSpace(parts[1])
	if base == "" || quote == "" {
		return "", "", fmt.Errorf("priceproof: invalid pair %q", pair)
	}
	return base, quote, nil
}
