package market

import (
	"bytes"
	"math/big"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
)

const moduleName = "market"

// engineState is the persistence surface the market engine depends on.
// Mirrors native/lending's engineState naming and shape: account balance
// mutation goes through GetAccount/PutAccount (the same *types.Account the
// rest of the chain uses), while listing/fill records and their discovery
// indices go through dedicated methods.
type engineState interface {
	GetAccount(addr crypto.Address) (*types.Account, error)
	PutAccount(addr crypto.Address, account *types.Account) error

	// GetListing returns (nil, nil) when no listing exists for id -- the
	// engine is responsible for turning that into errListingNotFound,
	// mirroring how lending's ensureMarket/ensureUserAccount interpret a
	// nil result from their own state accessors.
	GetListing(id [32]byte) (*Listing, error)
	PutListing(listing *Listing) error
	// AppendOpenListing/RemoveOpenListing maintain the market-wide
	// discovery index of currently-open listings. Both must be idempotent:
	// appending an already-indexed ID or removing an absent one is a
	// no-op, not an error (mirrors core/state/redemption.go's pending
	// index and manager.go's StakeAddValidatorDelegator/
	// StakeRemoveValidatorDelegator).
	AppendOpenListing(id [32]byte) error
	RemoveOpenListing(id [32]byte) error

	// AppendFill persists the fill record and indexes it under both the
	// buyer's and the seller's fill-history indices in one call.
	AppendFill(fill *Fill) error
	ListFillsByBuyer(addr crypto.Address) ([]*Fill, error)
	ListFillsBySeller(addr crypto.Address) ([]*Fill, error)
}

// Engine orchestrates the primary state transitions for the peer-to-peer
// ZNHB-for-NHB marketplace. Mirrors native/lending's Engine shape: a state
// accessor, module-custody addresses supplied by the constructor (derived
// elsewhere -- see core/node.go's deriveModuleAddress -- and never computed
// in this package), and a pause-guard hook.
type Engine struct {
	state engineState
	// marketEscrowAddress is the module-custody account holding all
	// listed-but-unsold ZNHB, exactly like lending's collateralAddress
	// holds pledged ZNHB collateral.
	marketEscrowAddress crypto.Address
	// feeCollectorAddress receives the flat fee charged to buyers on each
	// fill.
	feeCollectorAddress crypto.Address
	pauses              nativecommon.PauseView
	// nowFn supplies CreatedAt/UpdatedAt timestamps and feeds listing/fill
	// ID derivation (see types.go). Defaults to wall-clock time so the
	// engine is usable standalone/in tests, but production wiring must
	// override it with the deterministic block timestamp -- mirroring
	// native/escrow's Engine.nowFn and core/state_transition.go's
	// `sp.EscrowEngine.SetNowFunc(func() int64 { return sp.now().Unix() })`
	// -- so that ID derivation never depends on a validator's local clock.
	nowFn func() int64
	// blockHeight is stamped onto every Fill (Fill.BlockHeight), mirroring
	// native/lending's Engine.blockHeight/SetBlockHeight.
	blockHeight uint64
}

// NewEngine constructs a market engine configured with the module treasury
// addresses. Both addresses are accepted as constructor parameters rather
// than derived here, matching native/lending's NewEngine(moduleAddr,
// collateralAddr, ...) convention.
func NewEngine(marketEscrowAddress, feeCollectorAddress crypto.Address) *Engine {
	return &Engine{
		marketEscrowAddress: marketEscrowAddress,
		feeCollectorAddress: feeCollectorAddress,
		nowFn:               func() int64 { return time.Now().Unix() },
	}
}

// SetState wires the engine to the external persistence layer.
func (e *Engine) SetState(state engineState) {
	if e == nil {
		return
	}
	e.state = state
}

// SetPauses wires the pause view used to gate state transitions, mirroring
// native/lending's Engine.SetPauses.
func (e *Engine) SetPauses(p nativecommon.PauseView) {
	if e == nil {
		return
	}
	e.pauses = p
}

// SetNowFunc overrides the time source used by the engine. Production
// callers should wire this to the deterministic block timestamp (see the
// Engine.nowFn doc comment); tests may supply a fixed clock.
func (e *Engine) SetNowFunc(now func() int64) {
	if e == nil {
		return
	}
	if now == nil {
		e.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	e.nowFn = now
}

// SetBlockHeight records the block height stamped onto newly created fills.
func (e *Engine) SetBlockHeight(height uint64) {
	if e == nil {
		return
	}
	e.blockHeight = height
}

func (e *Engine) now() int64 {
	if e == nil || e.nowFn == nil {
		return time.Now().Unix()
	}
	return e.nowFn()
}

// CreateListing lists znhbAmount ZNHB for sale at the exact rational rate
// rateNumerator/rateDenominator (ZNHB per NHB), escrowing the ZNHB into the
// engine's market-escrow account.
func (e *Engine) CreateListing(seller crypto.Address, znhbAmount *big.Int, rateNumerator, rateDenominator *big.Int, allowPartial bool) (*Listing, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if znhbAmount == nil || znhbAmount.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	if rateNumerator == nil || rateNumerator.Sign() <= 0 {
		return nil, errInvalidRate
	}
	if rateDenominator == nil || rateDenominator.Sign() <= 0 {
		return nil, errInvalidRate
	}

	now := e.now()
	total := new(big.Int).Set(znhbAmount)
	numerator := new(big.Int).Set(rateNumerator)
	denominator := new(big.Int).Set(rateDenominator)
	listingID := newListingID(seller, now, total, numerator, denominator, allowPartial)

	// True idempotency, not just a matching ID: if this exact listing
	// (same seller/amount/rate/partial-flag/CreatedAt-second) already
	// exists, return it as-is WITHOUT mutating any balance a second time.
	// Without this check, a second call that happens to derive the same ID
	// would still re-debit the seller's ZNHB and re-credit the escrow
	// account, while PutListing's second write only ever leaves ONE
	// listing record behind -- stranding the first call's escrowed ZNHB
	// permanently (debited from the seller, credited to escrow, but never
	// attached to any RemainingAmount a buyer or the seller could ever
	// claim). A genuine duplicate submission of the same transaction can't
	// reach here at all (nonce-based replay protection rejects it before
	// state transition), so this only fires for two distinct transactions
	// that deliberately request the exact same listing twice -- treating
	// that as a no-op is the correct, safe behavior either way.
	if existing, err := e.state.GetListing(listingID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing.Clone(), nil
	}

	sellerAcc, err := e.loadAccount(seller)
	if err != nil {
		return nil, err
	}
	if sellerAcc.BalanceZNHB.Cmp(znhbAmount) < 0 {
		return nil, errInsufficientBalance
	}

	escrowAcc, err := e.loadAccount(e.marketEscrowAddress)
	if err != nil {
		return nil, err
	}

	sellerAcc.BalanceZNHB = new(big.Int).Sub(sellerAcc.BalanceZNHB, znhbAmount)
	escrowAcc.BalanceZNHB = new(big.Int).Add(escrowAcc.BalanceZNHB, znhbAmount)

	if err := e.persistAccount(seller, sellerAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(e.marketEscrowAddress, escrowAcc); err != nil {
		return nil, err
	}

	listing := &Listing{
		ID:              listingID,
		Seller:          seller,
		RateNumerator:   numerator,
		RateDenominator: denominator,
		TotalAmount:     total,
		RemainingAmount: new(big.Int).Set(znhbAmount),
		AllowPartial:    allowPartial,
		Status:          ListingOpen,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := e.state.PutListing(listing); err != nil {
		return nil, err
	}
	if err := e.state.AppendOpenListing(listing.ID); err != nil {
		return nil, err
	}

	return listing.Clone(), nil
}

// FillListing executes a full or partial purchase of znhbAmountRequested
// ZNHB from the listing, charging the buyer the exact rational NHB cost
// (rounded up -- see the ceil-division comment below) plus flatFeeWei on
// top, routed to the fee collector. txHash is stamped onto the resulting
// Fill as-is (see Fill.TxHash's doc comment) -- the engine does not compute
// or validate it.
func (e *Engine) FillListing(buyer crypto.Address, listingID [32]byte, znhbAmountRequested *big.Int, flatFeeWei *big.Int, txHash [32]byte) (*Fill, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}

	listing, err := e.state.GetListing(listingID)
	if err != nil {
		return nil, err
	}
	if listing == nil {
		return nil, errListingNotFound
	}
	if listing.Status != ListingOpen {
		return nil, errListingNotOpen
	}

	if znhbAmountRequested == nil || znhbAmountRequested.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	if listing.RemainingAmount == nil || znhbAmountRequested.Cmp(listing.RemainingAmount) > 0 {
		return nil, errInsufficientRemaining
	}
	if !listing.AllowPartial && znhbAmountRequested.Cmp(listing.RemainingAmount) != 0 {
		return nil, errPartialFillNotAllowed
	}

	if flatFeeWei == nil {
		flatFeeWei = big.NewInt(0)
	} else if flatFeeWei.Sign() < 0 {
		return nil, errInvalidAmount
	}

	// nhbCost = ceil(znhbAmountRequested * RateDenominator / RateNumerator).
	// Rounded UP, protocol-favoring: the buyer never pays less than the
	// true rate implies due to integer-division truncation. big.Int only
	// -- never float64 -- per QuoRem's exact remainder check, mirroring
	// native/potso/metrics.go's QuoRem-based rounding.
	costNumerator := new(big.Int).Mul(znhbAmountRequested, listing.RateDenominator)
	nhbCost, remainder := new(big.Int).QuoRem(costNumerator, listing.RateNumerator, new(big.Int))
	if remainder.Sign() != 0 {
		nhbCost.Add(nhbCost, big.NewInt(1))
	}

	totalDebit := new(big.Int).Add(nhbCost, flatFeeWei)

	buyerAcc, err := e.loadAccount(buyer)
	if err != nil {
		return nil, err
	}
	if buyerAcc.BalanceNHB.Cmp(totalDebit) < 0 {
		return nil, errInsufficientBalance
	}

	sellerAcc, err := e.loadAccount(listing.Seller)
	if err != nil {
		return nil, err
	}

	var feeAcc *types.Account
	if flatFeeWei.Sign() > 0 {
		feeAcc, err = e.loadAccount(e.feeCollectorAddress)
		if err != nil {
			return nil, err
		}
	}

	escrowAcc, err := e.loadAccount(e.marketEscrowAddress)
	if err != nil {
		return nil, err
	}
	if escrowAcc.BalanceZNHB.Cmp(znhbAmountRequested) < 0 {
		return nil, errInsufficientBalance
	}

	buyerAcc.BalanceNHB = new(big.Int).Sub(buyerAcc.BalanceNHB, totalDebit)
	sellerAcc.BalanceNHB = new(big.Int).Add(sellerAcc.BalanceNHB, nhbCost)
	if feeAcc != nil {
		feeAcc.BalanceNHB = new(big.Int).Add(feeAcc.BalanceNHB, flatFeeWei)
	}
	escrowAcc.BalanceZNHB = new(big.Int).Sub(escrowAcc.BalanceZNHB, znhbAmountRequested)
	buyerAcc.BalanceZNHB = new(big.Int).Add(buyerAcc.BalanceZNHB, znhbAmountRequested)

	if err := e.persistAccount(buyer, buyerAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(listing.Seller, sellerAcc); err != nil {
		return nil, err
	}
	if feeAcc != nil {
		if err := e.persistAccount(e.feeCollectorAddress, feeAcc); err != nil {
			return nil, err
		}
	}
	if err := e.persistAccount(e.marketEscrowAddress, escrowAcc); err != nil {
		return nil, err
	}

	now := e.now()
	remainingBefore := new(big.Int).Set(listing.RemainingAmount)
	fill := &Fill{
		ID:          newFillID(listing.ID, buyer, now, remainingBefore, znhbAmountRequested),
		ListingID:   listing.ID,
		Buyer:       buyer,
		Seller:      listing.Seller,
		ZNHBAmount:  new(big.Int).Set(znhbAmountRequested),
		NHBAmount:   new(big.Int).Set(nhbCost),
		FeeAmount:   new(big.Int).Set(flatFeeWei),
		TxHash:      txHash,
		CreatedAt:   now,
		BlockHeight: e.blockHeight,
	}

	listing.RemainingAmount = new(big.Int).Sub(remainingBefore, znhbAmountRequested)
	listing.UpdatedAt = now
	if listing.RemainingAmount.Sign() == 0 {
		// Still readable by ID -- just no longer discoverable in the open
		// list.
		listing.Status = ListingFilled
		if err := e.state.RemoveOpenListing(listing.ID); err != nil {
			return nil, err
		}
	}
	if err := e.state.PutListing(listing); err != nil {
		return nil, err
	}

	if err := e.state.AppendFill(fill); err != nil {
		return nil, err
	}

	return fill.Clone(), nil
}

// CancelListing withdraws an open listing, returning its unsold ZNHB to the
// seller.
func (e *Engine) CancelListing(seller crypto.Address, listingID [32]byte) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}

	listing, err := e.state.GetListing(listingID)
	if err != nil {
		return err
	}
	if listing == nil {
		return errListingNotFound
	}
	if listing.Status != ListingOpen {
		return errListingNotOpen
	}
	if !bytes.Equal(listing.Seller.Bytes(), seller.Bytes()) {
		return errNotSeller
	}

	sellerAcc, err := e.loadAccount(seller)
	if err != nil {
		return err
	}
	escrowAcc, err := e.loadAccount(e.marketEscrowAddress)
	if err != nil {
		return err
	}
	if escrowAcc.BalanceZNHB.Cmp(listing.RemainingAmount) < 0 {
		return errInsufficientBalance
	}

	sellerAcc.BalanceZNHB = new(big.Int).Add(sellerAcc.BalanceZNHB, listing.RemainingAmount)
	escrowAcc.BalanceZNHB = new(big.Int).Sub(escrowAcc.BalanceZNHB, listing.RemainingAmount)

	if err := e.persistAccount(seller, sellerAcc); err != nil {
		return err
	}
	if err := e.persistAccount(e.marketEscrowAddress, escrowAcc); err != nil {
		return err
	}

	listing.Status = ListingCancelled
	listing.UpdatedAt = e.now()
	if err := e.state.PutListing(listing); err != nil {
		return err
	}
	return e.state.RemoveOpenListing(listing.ID)
}

// loadAccount fetches an account, defaulting nil balance fields to zero.
// Mirrors native/lending's Engine.loadAccount exactly, including treating a
// missing account the same as an empty one -- a fill/create/cancel against
// an address with no on-chain account can never have a positive balance to
// spend, so errInsufficientBalance is the correct outcome either way.
func (e *Engine) loadAccount(addr crypto.Address) (*types.Account, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	acc, err := e.state.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, errInsufficientBalance
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	return acc, nil
}

func (e *Engine) persistAccount(addr crypto.Address, acc *types.Account) error {
	return e.state.PutAccount(addr, acc)
}
