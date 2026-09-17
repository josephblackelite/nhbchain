package market

import (
	"encoding/binary"
	"errors"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/crypto"
)

// ListingStatus represents the lifecycle state of a peer-to-peer
// ZNHB-for-NHB listing.
type ListingStatus uint8

const (
	// ListingOpen marks a listing that is still discoverable and has
	// remaining ZNHB available to be filled.
	ListingOpen ListingStatus = iota
	// ListingFilled marks a listing whose RemainingAmount has reached zero
	// via one or more fills.
	ListingFilled
	// ListingCancelled marks a listing the seller withdrew before it was
	// fully filled. Any unsold ZNHB has already been returned to the
	// seller by the time this status is observed.
	ListingCancelled
)

// String returns the canonical textual representation of the listing
// status.
func (s ListingStatus) String() string {
	switch s {
	case ListingOpen:
		return "open"
	case ListingFilled:
		return "filled"
	case ListingCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// Error variables for the market engine, matching native/lending's style of
// package-level sentinel errors.
var (
	errNilState              = errors.New("market engine: state not configured")
	errInvalidAmount         = errors.New("market engine: amount must be positive")
	errListingNotFound       = errors.New("market engine: listing not found")
	errListingNotOpen        = errors.New("market engine: listing is not open")
	errPartialFillNotAllowed = errors.New("market engine: listing does not allow partial fills")
	errInsufficientRemaining = errors.New("market engine: fill amount exceeds remaining listing amount")
	errInsufficientBalance   = errors.New("market engine: insufficient balance")
	errNotSeller             = errors.New("market engine: caller is not the listing seller")
	errInvalidRate           = errors.New("market engine: rate must be positive")
)

// Listing captures a single peer-to-peer offer to sell ZNHB for NHB. The
// price is expressed as an exact rational -- RateNumerator ZNHB per
// RateDenominator NHB -- and is never approximated with a float anywhere in
// this package (see FillListing's ceil-division cost calculation in
// engine.go).
type Listing struct {
	ID              [32]byte
	Seller          crypto.Address
	RateNumerator   *big.Int
	RateDenominator *big.Int
	// TotalAmount is the ZNHB (wei) originally listed. It never changes
	// after creation.
	TotalAmount *big.Int
	// RemainingAmount is the ZNHB (wei) still unsold. It only ever
	// decreases via a fill, or is fully returned to the seller via
	// cancellation.
	RemainingAmount *big.Int
	AllowPartial    bool
	Status          ListingStatus
	CreatedAt       int64
	UpdatedAt       int64
}

// Clone returns a deep copy of the listing so callers can safely mutate the
// copy without affecting the stored instance. Mirrors native/escrow's
// Escrow.Clone pattern.
func (l *Listing) Clone() *Listing {
	if l == nil {
		return nil
	}
	clone := *l
	if l.Seller.Bytes() != nil {
		clone.Seller = crypto.MustNewAddress(l.Seller.Prefix(), append([]byte(nil), l.Seller.Bytes()...))
	}
	if l.RateNumerator != nil {
		clone.RateNumerator = new(big.Int).Set(l.RateNumerator)
	} else {
		clone.RateNumerator = big.NewInt(0)
	}
	if l.RateDenominator != nil {
		clone.RateDenominator = new(big.Int).Set(l.RateDenominator)
	} else {
		clone.RateDenominator = big.NewInt(0)
	}
	if l.TotalAmount != nil {
		clone.TotalAmount = new(big.Int).Set(l.TotalAmount)
	} else {
		clone.TotalAmount = big.NewInt(0)
	}
	if l.RemainingAmount != nil {
		clone.RemainingAmount = new(big.Int).Set(l.RemainingAmount)
	} else {
		clone.RemainingAmount = big.NewInt(0)
	}
	return &clone
}

// Fill captures a single execution against a listing -- either a full or
// partial purchase of the seller's ZNHB by a buyer.
type Fill struct {
	ID         [32]byte
	ListingID  [32]byte
	Buyer      crypto.Address
	Seller     crypto.Address
	ZNHBAmount *big.Int
	// NHBAmount is what the seller actually received (excludes the fee).
	NHBAmount *big.Int
	// FeeAmount is what the buyer paid on top, routed to the fee
	// collector.
	FeeAmount *big.Int
	// TxHash is the hash of the TxTypeMarketFillListing transaction that
	// settled this fill, stamped by the caller (core/market_native.go's
	// applyMarketFillListing) -- the engine itself never computes a
	// transaction hash. Lets a client render an explorer link for a trade
	// history entry.
	TxHash      [32]byte
	CreatedAt   int64
	BlockHeight uint64
}

// Clone returns a deep copy of the fill record.
func (f *Fill) Clone() *Fill {
	if f == nil {
		return nil
	}
	clone := *f
	if f.Buyer.Bytes() != nil {
		clone.Buyer = crypto.MustNewAddress(f.Buyer.Prefix(), append([]byte(nil), f.Buyer.Bytes()...))
	}
	if f.Seller.Bytes() != nil {
		clone.Seller = crypto.MustNewAddress(f.Seller.Prefix(), append([]byte(nil), f.Seller.Bytes()...))
	}
	if f.ZNHBAmount != nil {
		clone.ZNHBAmount = new(big.Int).Set(f.ZNHBAmount)
	} else {
		clone.ZNHBAmount = big.NewInt(0)
	}
	if f.NHBAmount != nil {
		clone.NHBAmount = new(big.Int).Set(f.NHBAmount)
	} else {
		clone.NHBAmount = big.NewInt(0)
	}
	if f.FeeAmount != nil {
		clone.FeeAmount = new(big.Int).Set(f.FeeAmount)
	} else {
		clone.FeeAmount = big.NewInt(0)
	}
	return &clone
}

// Domain separation tags for deterministic ID derivation (see newListingID
// and newFillID below). Distinct tags for the two ID families keep a
// listing ID and a fill ID from ever colliding even if every other hashed
// field happened to match by coincidence.
const (
	listingIDDomainTag = "nhb-market-listing"
	fillIDDomainTag    = "nhb-market-fill"
)

func bigIntBytes(v *big.Int) []byte {
	if v == nil {
		return nil
	}
	return v.Bytes()
}

func boolByte(b bool) []byte {
	if b {
		return []byte{1}
	}
	return []byte{0}
}

func int64Bytes(v int64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	return buf[:]
}

// newListingID derives the deterministic identifier for a new listing.
//
// CreateListing's signature (fixed by the calling integration's contract --
// see engine.go) intentionally carries no caller-supplied nonce, unlike
// native/escrow's Create (payer/payee/metaHash/nonce) or native/escrow's
// TradeEngine.CreateTrade (offerID/buyer/seller/nonce). In its place, the ID
// is derived from every parameter that defines the listing plus CreatedAt
// -- the block timestamp injected via Engine.SetNowFunc, mirroring how
// core/state_transition.go wires EscrowEngine.SetNowFunc to the
// deterministic block clock rather than wall-clock time, so this stays
// consensus-safe across validators. Two listings collide only if the same
// seller submits the exact same amount/rate/partial-flag combination inside
// the same block second, which this package intentionally treats as an
// idempotent no-op retry of the same intent rather than as an error (see
// CreateListing).
func newListingID(seller crypto.Address, createdAt int64, totalAmount, rateNumerator, rateDenominator *big.Int, allowPartial bool) [32]byte {
	return ethcrypto.Keccak256Hash(
		[]byte(listingIDDomainTag),
		seller.Bytes(),
		int64Bytes(createdAt),
		bigIntBytes(totalAmount),
		bigIntBytes(rateNumerator),
		bigIntBytes(rateDenominator),
		boolByte(allowPartial),
	)
}

// newFillID derives the deterministic identifier for a new fill.
//
// Like newListingID, FillListing's fixed signature carries no explicit
// nonce. remainingBefore -- the listing's RemainingAmount immediately
// before this fill is applied -- stands in for one: it strictly decreases
// with every successful fill against the same listing (see FillListing), so
// two fills against the same listing can never share a remainingBefore
// value even when submitted by the same buyer inside the same block second.
func newFillID(listingID [32]byte, buyer crypto.Address, createdAt int64, remainingBefore, znhbAmount *big.Int) [32]byte {
	return ethcrypto.Keccak256Hash(
		[]byte(fillIDDomainTag),
		listingID[:],
		buyer.Bytes(),
		int64Bytes(createdAt),
		bigIntBytes(remainingBefore),
		bigIntBytes(znhbAmount),
	)
}
