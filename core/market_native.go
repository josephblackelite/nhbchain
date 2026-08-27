package core

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/market"
)

// defaultMarketFlatFeeWei is 0.1 NHB (18 decimals) -- the buyer-side flat
// fee charged on every fill until a governance param.update proposal sets
// governance.ParamKeyMarketFlatFeeWei to something else. Matches the rate
// the user asked to start at ("for now we will set our fee to buyer at
// 0.1nhb for each transaction").
const defaultMarketFlatFeeWei = "100000000000000000"

// marketStateAdapter implements native/market's engineState interface
// against nhbstate.Manager, mirroring lendingStateAdapter's shape exactly
// (core/lending_native.go) -- GetAccount/PutAccount go straight through to
// the manager's raw-byte-address account accessors, while listing/fill
// persistence is bridged to the market-specific manager methods added in
// core/state/manager.go.
type marketStateAdapter struct {
	manager *nhbstate.Manager
}

func (sp *StateProcessor) marketStateAdapter() *marketStateAdapter {
	return &marketStateAdapter{manager: nhbstate.NewManager(sp.Trie)}
}

func (a *marketStateAdapter) GetAccount(addr crypto.Address) (*types.Account, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("market: state manager unavailable")
	}
	return a.manager.GetAccount(addr.Bytes())
}

func (a *marketStateAdapter) PutAccount(addr crypto.Address, account *types.Account) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("market: state manager unavailable")
	}
	if account == nil {
		return fmt.Errorf("market: account must not be nil")
	}
	return a.manager.PutAccount(addr.Bytes(), account)
}

// GetListing bridges Manager.GetMarketListing's 3-value
// (*market.Listing, bool, error) return to the engineState interface's
// 2-value (*Listing, error) contract, where a nil listing with a nil error
// means "not found" (see native/market/engine.go's engineState doc
// comment).
func (a *marketStateAdapter) GetListing(id [32]byte) (*market.Listing, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("market: state manager unavailable")
	}
	listing, ok, err := a.manager.GetMarketListing(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return listing, nil
}

func (a *marketStateAdapter) PutListing(listing *market.Listing) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("market: state manager unavailable")
	}
	return a.manager.PutMarketListing(listing)
}

func (a *marketStateAdapter) AppendOpenListing(id [32]byte) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("market: state manager unavailable")
	}
	return a.manager.AppendOpenMarketListing(id)
}

func (a *marketStateAdapter) RemoveOpenListing(id [32]byte) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("market: state manager unavailable")
	}
	return a.manager.RemoveOpenMarketListing(id)
}

func (a *marketStateAdapter) AppendFill(fill *market.Fill) error {
	if a == nil || a.manager == nil {
		return fmt.Errorf("market: state manager unavailable")
	}
	return a.manager.AppendMarketFill(fill)
}

func (a *marketStateAdapter) ListFillsByBuyer(addr crypto.Address) ([]*market.Fill, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("market: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	return a.manager.ListMarketFillsByBuyer(raw)
}

func (a *marketStateAdapter) ListFillsBySeller(addr crypto.Address) ([]*market.Fill, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("market: state manager unavailable")
	}
	var raw [20]byte
	copy(raw[:], addr.Bytes())
	return a.manager.ListMarketFillsBySeller(raw)
}

// marketEngine constructs a fully configured market engine for a single
// transaction's apply call, mirroring sp.lendingEngine()'s shape. Critically,
// SetNowFunc is wired to sp.blockTimestamp() -- the deterministic value
// BeginBlock(height, timestamp) stamps into sp.execContext before any of a
// block's transactions are applied -- NOT sp.now(), which resolves to real
// wall-clock time.Now() in production (sp.now() does not consult
// execContext at all). Listing/fill ID derivation and CreatedAt/UpdatedAt
// (native/market/types.go) get hashed into consensus state, so every
// validator must derive the identical value for the identical transaction;
// wiring this to wall-clock time would make that depend on the exact
// instant each validator happens to execute the tx, guaranteeing a
// state-root mismatch the first time more than one validator (or a
// replaying/syncing node) processes the same market transaction. This
// mirrors an existing bug in EscrowEngine/TradeEngine's own SetNowFunc
// wiring (sp.now(), not sp.blockTimestamp()) -- not fixed here, out of
// scope for this change, but the same fix should eventually apply there.
func (sp *StateProcessor) marketEngine() *market.Engine {
	engine := market.NewEngine(cloneAddress(sp.marketEscrowAddr), cloneAddress(sp.marketFeeCollectorAddr))
	engine.SetPauses(sp.pauses)
	engine.SetState(sp.marketStateAdapter())
	engine.SetBlockHeight(sp.blockHeight())
	engine.SetNowFunc(func() int64 { return sp.blockTimestamp().Unix() })
	return engine
}

// readGovernedMarketFlatFeeWei resolves the buyer-side flat fee from the
// generic governance param store (native/governance's ProposalKindParamUpdate
// applied via applyParamUpdates -- no dedicated proposal kind needed for
// this key), falling back to defaultMarketFlatFeeWei when never set. Reuses
// readGovernedSwapRiskWei (core/swap_risk_params.go) as-is: it is already a
// generic "governed wei amount with a string default" reader, not specific
// to swap risk despite its name and file location.
func readGovernedMarketFlatFeeWei(manager *nhbstate.Manager) (*big.Int, error) {
	return readGovernedSwapRiskWei(manager, governance.ParamKeyMarketFlatFeeWei, defaultMarketFlatFeeWei)
}

func decodeMarketListingID(raw []byte) ([32]byte, error) {
	var id [32]byte
	if len(raw) != len(id) {
		return id, fmt.Errorf("market: listingId must be exactly 32 bytes, got %d", len(raw))
	}
	copy(id[:], raw)
	return id, nil
}

// applyMarketCreateListing handles TxTypeMarketCreateListing. tx.Value is
// the ZNHB amount to escrow; tx.Data is
// RLP([rateNumerator, rateDenominator, allowPartial]) per the payload shape
// documented on TxTypeMarketCreateListing in core/types/transaction.go.
func (sp *StateProcessor) applyMarketCreateListing(tx *types.Transaction, sender []byte) error {
	if tx.Value == nil || tx.Value.Sign() <= 0 {
		return fmt.Errorf("marketCreateListing: znhbAmount must be positive")
	}
	var payload struct {
		RateNumerator   *big.Int
		RateDenominator *big.Int
		AllowPartial    bool
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("marketCreateListing: decode payload: %w", err)
	}
	seller := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...))
	if _, err := sp.marketEngine().CreateListing(seller, tx.Value, payload.RateNumerator, payload.RateDenominator, payload.AllowPartial); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

// applyMarketFillListing handles TxTypeMarketFillListing. tx.Value is
// unused; tx.Data is RLP([listingID, znhbAmountRequested]) -- the NHB cost
// is computed on-chain from the listing's own rate, never client-declared.
// The buyer-side flat fee is likewise always read fresh from the governance
// param store here, never accepted from the caller.
func (sp *StateProcessor) applyMarketFillListing(tx *types.Transaction, sender []byte) error {
	var payload struct {
		ListingID  []byte
		ZNHBAmount *big.Int
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("marketFillListing: decode payload: %w", err)
	}
	listingID, err := decodeMarketListingID(payload.ListingID)
	if err != nil {
		return fmt.Errorf("marketFillListing: %w", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	flatFeeWei, err := readGovernedMarketFlatFeeWei(manager)
	if err != nil {
		return fmt.Errorf("marketFillListing: %w", err)
	}
	rawTxHash, err := tx.Hash()
	if err != nil {
		return fmt.Errorf("marketFillListing: compute tx hash: %w", err)
	}
	var txHash [32]byte
	copy(txHash[:], rawTxHash)
	buyer := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...))
	if _, err := sp.marketEngine().FillListing(buyer, listingID, payload.ZNHBAmount, flatFeeWei, txHash); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}

// applyMarketCancelListing handles TxTypeMarketCancelListing. tx.Value is
// unused; tx.Data is RLP([listingID]).
func (sp *StateProcessor) applyMarketCancelListing(tx *types.Transaction, sender []byte) error {
	var payload struct {
		ListingID []byte
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("marketCancelListing: decode payload: %w", err)
	}
	listingID, err := decodeMarketListingID(payload.ListingID)
	if err != nil {
		return fmt.Errorf("marketCancelListing: %w", err)
	}
	seller := crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), sender...))
	if err := sp.marketEngine().CancelListing(seller, listingID); err != nil {
		return err
	}
	return sp.incrementNativeAccountNonce(sender)
}
