package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/amm"
)

// The native ZNHB/NHB constant-product pool (ledger PL-BC-B). One pool, one pair, protocol-owned liquidity: only the
// configured liquidity provider adds or removes it, anyone may swap once swaps are opened.
//
// What it must never do, and how each is held:
//   - Create ZNHB: the pool only holds what was moved into its module account. There is no mint path. The admin wallet
//     can neither provide liquidity nor swap, so ZNHB reaches the pool only after it has left the admin wallet through
//     the curve or the swap-voucher path, and CheckZNHBSupplyInvariant is unaffected by construction.
//   - Feed a price anywhere: the pool's price is read only by its own swaps and by the read RPC. Nothing in the curve,
//     the buyback reference price or lending collateral valuation reads it.
//   - Be drained by one swap: a swap's amount in is capped as a share of the reserve, a reserve never falls below its
//     floor, and the arithmetic rounds in the pool's favour so the product of the reserves only grows.
//   - Trade by default: swaps stay closed until SwapStartHeight, which is separate from the activation height.
//   - Be reachable before its height: from AmmActivationHeight only; before it the three bytes are unknown types.

var (
	ErrAmmInvalidPayload      = errors.New("amm: invalid payload")
	ErrAmmUnauthorized        = errors.New("amm: only the liquidity provider may add or remove liquidity")
	ErrAmmAdminWallet         = errors.New("amm: the admin wallet cannot use the pool")
	ErrAmmSwapsClosed         = errors.New("amm: swaps are not open")
	ErrAmmInsufficientBalance = errors.New("amm: the sender holds less than the amount")
	ErrAmmSlippage            = errors.New("amm: the result is below the minimum the sender asked for")
	ErrAmmTooLarge            = errors.New("amm: the swap is larger than the pool allows for one swap")
	ErrAmmReserveFloor        = errors.New("amm: the operation would take a reserve below its floor")
	ErrAmmEmptyPool           = errors.New("amm: the pool has no liquidity")
	ErrAmmZeroOutput          = errors.New("amm: the swap would pay nothing out")
)

// Swap directions.
const (
	AmmDirZnhbToNhb uint8 = 0
	AmmDirNhbToZnhb uint8 = 1
)

type ammPoolState struct {
	ReserveZnhb *big.Int
	ReserveNhb  *big.Int
	TotalShares *big.Int
}

type ammAddPayload struct {
	Znhb      *big.Int
	Nhb       *big.Int
	MinShares *big.Int
}

type ammRemovePayload struct {
	Shares  *big.Int
	MinZnhb *big.Int
	MinNhb  *big.Int
}

type ammSwapPayload struct {
	Dir      uint8
	AmountIn *big.Int
	MinOut   *big.Int
}

type ammTradeRecord struct {
	Height uint64
	Trader []byte
	Dir    uint8
	In     *big.Int
	Out    *big.Int
	Fee    *big.Int
}

func ammPoolAddress() []byte {
	return deriveModuleAddress("module/amm/pool", crypto.ZNHBPrefix).Bytes()
}

func ammSharesKey(owner []byte) []byte { return []byte("amm/shares/" + hex.EncodeToString(owner)) }
func ammTradeKey(seq uint64) []byte    { return []byte("amm/trade/" + strconv.FormatUint(seq, 10)) }

func loadAmmPool(m *nhbstate.Manager) (ammPoolState, error) {
	var p ammPoolState
	found, err := m.KVGet([]byte("amm/pool"), &p)
	if err != nil {
		return p, err
	}
	if !found || p.ReserveZnhb == nil || p.ReserveNhb == nil || p.TotalShares == nil {
		return ammPoolState{ReserveZnhb: new(big.Int), ReserveNhb: new(big.Int), TotalShares: new(big.Int)}, nil
	}
	return p, nil
}

func nhbBalanceOf(account *types.Account) *big.Int {
	if account == nil || account.BalanceNHB == nil {
		return new(big.Int)
	}
	return account.BalanceNHB
}

func (sp *StateProcessor) ammIsProvider(sender []byte) bool {
	return sp.amm.HasProvider && bytes.Equal(sender, sp.amm.Provider[:])
}

func (sp *StateProcessor) ammSwapsOpen() bool {
	return sp.amm.SwapStartHeight != 0 && sp.blockHeight() >= sp.amm.SwapStartHeight
}

func positive(n *big.Int) bool { return n != nil && n.Sign() > 0 }

func (sp *StateProcessor) ammEvent(kind string, attrs map[string]string) {
	sp.AppendEvent(&types.Event{Type: kind, Attributes: attrs})
}

func ammAddrString(raw []byte) string {
	return crypto.MustNewAddress(crypto.NHBPrefix, append([]byte(nil), raw...)).String()
}

// movePair debits the sender and credits the pool by the two amounts (any may be zero). Balances were checked.
func (sp *StateProcessor) ammMoveIn(sender []byte, znhb, nhb *big.Int) error {
	pool := ammPoolAddress()
	from, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	to, err := sp.getAccount(pool)
	if err != nil {
		return err
	}
	from.BalanceZNHB = new(big.Int).Sub(znhbBalanceOf(from), znhb)
	from.BalanceNHB = new(big.Int).Sub(nhbBalanceOf(from), nhb)
	to.BalanceZNHB = new(big.Int).Add(znhbBalanceOf(to), znhb)
	to.BalanceNHB = new(big.Int).Add(nhbBalanceOf(to), nhb)
	if err := sp.setAccount(sender, from); err != nil {
		return err
	}
	return sp.setAccount(pool, to)
}

// ammMoveOut debits the pool and credits the receiver by the two amounts.
func (sp *StateProcessor) ammMoveOut(receiver []byte, znhb, nhb *big.Int) error {
	pool := ammPoolAddress()
	from, err := sp.getAccount(pool)
	if err != nil {
		return err
	}
	to, err := sp.getAccount(receiver)
	if err != nil {
		return err
	}
	from.BalanceZNHB = new(big.Int).Sub(znhbBalanceOf(from), znhb)
	from.BalanceNHB = new(big.Int).Sub(nhbBalanceOf(from), nhb)
	to.BalanceZNHB = new(big.Int).Add(znhbBalanceOf(to), znhb)
	to.BalanceNHB = new(big.Int).Add(nhbBalanceOf(to), nhb)
	if err := sp.setAccount(pool, from); err != nil {
		return err
	}
	return sp.setAccount(receiver, to)
}

func (sp *StateProcessor) ammRequireProvider(sender []byte) error {
	if !sp.ammIsProvider(sender) {
		return ErrAmmUnauthorized
	}
	if sp.bridgeIsAdminWallet(sender) {
		return ErrAmmAdminWallet
	}
	return nil
}

func (sp *StateProcessor) applyAmmAddLiquidityTransaction(tx *types.Transaction, sender []byte) error {
	if err := sp.ammRequireProvider(sender); err != nil {
		return err
	}
	var payload ammAddPayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return err
	}
	// amm.AddLiquidity refuses a zero or missing amount; the decoder never yields a negative or missing minimum.
	deposit, err := amm.AddLiquidity(pool.ReserveZnhb, pool.ReserveNhb, pool.TotalShares, payload.Znhb, payload.Nhb)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	if deposit.Shares.Cmp(payload.MinShares) < 0 {
		return ErrAmmSlippage
	}
	account, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	if znhbBalanceOf(account).Cmp(deposit.Znhb) < 0 || nhbBalanceOf(account).Cmp(deposit.Nhb) < 0 {
		return ErrAmmInsufficientBalance
	}
	if err := sp.ammMoveIn(sender, deposit.Znhb, deposit.Nhb); err != nil {
		return err
	}
	pool.ReserveZnhb = new(big.Int).Add(pool.ReserveZnhb, deposit.Znhb)
	pool.ReserveNhb = new(big.Int).Add(pool.ReserveNhb, deposit.Nhb)
	pool.TotalShares = new(big.Int).Add(pool.TotalShares, deposit.Shares)
	if err := manager.KVPut([]byte("amm/pool"), pool); err != nil {
		return err
	}
	owned := new(big.Int)
	if _, err := manager.KVGet(ammSharesKey(sender), &owned); err != nil {
		return err
	}
	if owned == nil {
		owned = new(big.Int)
	}
	if err := manager.KVPut(ammSharesKey(sender), new(big.Int).Add(owned, deposit.Shares)); err != nil {
		return err
	}
	sp.ammEvent("amm.liquidity_added", map[string]string{
		"provider": ammAddrString(sender), "znhbWei": deposit.Znhb.String(), "nhbWei": deposit.Nhb.String(), "shares": deposit.Shares.String(),
	})
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyAmmRemoveLiquidityTransaction(tx *types.Transaction, sender []byte) error {
	if err := sp.ammRequireProvider(sender); err != nil {
		return err
	}
	var payload ammRemovePayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	// A pure function of the transaction's own bytes, so it is judged (and pruned) before the pool is looked at.
	if !positive(payload.Shares) {
		return fmt.Errorf("%w: shares must be positive", ErrAmmInvalidPayload)
	}
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return err
	}
	if pool.TotalShares.Sign() == 0 {
		return ErrAmmEmptyPool
	}
	owned := new(big.Int)
	if _, err := manager.KVGet(ammSharesKey(sender), &owned); err != nil {
		return err
	}
	if owned == nil || owned.Cmp(payload.Shares) < 0 {
		return fmt.Errorf("%w: the provider holds fewer shares", ErrAmmInvalidPayload)
	}
	znhb, nhb, err := amm.RemoveLiquidity(pool.ReserveZnhb, pool.ReserveNhb, pool.TotalShares, payload.Shares)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	if znhb.Cmp(payload.MinZnhb) < 0 || nhb.Cmp(payload.MinNhb) < 0 {
		return ErrAmmSlippage
	}
	newZnhb := new(big.Int).Sub(pool.ReserveZnhb, znhb)
	newNhb := new(big.Int).Sub(pool.ReserveNhb, nhb)
	newShares := new(big.Int).Sub(pool.TotalShares, payload.Shares)
	if newShares.Sign() > 0 {
		// A partial removal may not leave a reserve under its floor. Removing every share empties the pool, which is allowed.
		if newZnhb.Cmp(sp.amm.MinReserveZNHB) < 0 || newNhb.Cmp(sp.amm.MinReserveNHB) < 0 {
			return ErrAmmReserveFloor
		}
	}
	if err := sp.ammMoveOut(sender, znhb, nhb); err != nil {
		return err
	}
	pool.ReserveZnhb, pool.ReserveNhb, pool.TotalShares = newZnhb, newNhb, newShares
	if err := manager.KVPut([]byte("amm/pool"), pool); err != nil {
		return err
	}
	if err := manager.KVPut(ammSharesKey(sender), new(big.Int).Sub(owned, payload.Shares)); err != nil {
		return err
	}
	sp.ammEvent("amm.liquidity_removed", map[string]string{
		"provider": ammAddrString(sender), "znhbWei": znhb.String(), "nhbWei": nhb.String(), "shares": payload.Shares.String(),
	})
	return sp.incrementNativeAccountNonce(sender)
}

func (sp *StateProcessor) applyAmmSwapTransaction(tx *types.Transaction, sender []byte) error {
	var payload ammSwapPayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	if payload.Dir > AmmDirNhbToZnhb || !positive(payload.AmountIn) {
		return fmt.Errorf("%w: the direction or the amount in is not valid", ErrAmmInvalidPayload)
	}
	if sp.bridgeIsAdminWallet(sender) || bytes.Equal(sender, ammPoolAddress()) {
		return ErrAmmAdminWallet
	}
	if !sp.ammSwapsOpen() {
		return ErrAmmSwapsClosed
	}
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return err
	}
	if pool.TotalShares.Sign() == 0 || !positive(pool.ReserveZnhb) || !positive(pool.ReserveNhb) {
		return ErrAmmEmptyPool
	}
	reserveIn, reserveOut := pool.ReserveZnhb, pool.ReserveNhb
	minReserveOut := sp.amm.MinReserveNHB
	if payload.Dir == AmmDirNhbToZnhb {
		reserveIn, reserveOut = pool.ReserveNhb, pool.ReserveZnhb
		minReserveOut = sp.amm.MinReserveZNHB
	}
	// One swap may move at most MaxSwapBps of the reserve it adds to.
	limit := new(big.Int).Div(new(big.Int).Mul(reserveIn, big.NewInt(int64(sp.amm.MaxSwapBps))), big.NewInt(amm.FeeDenominator))
	if payload.AmountIn.Cmp(limit) > 0 {
		return ErrAmmTooLarge
	}
	out, err := amm.AmountOut(reserveIn, reserveOut, payload.AmountIn, sp.amm.FeeBps)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAmmInvalidPayload, err)
	}
	if out.Sign() == 0 {
		return ErrAmmZeroOutput
	}
	if out.Cmp(payload.MinOut) < 0 {
		return ErrAmmSlippage
	}
	if new(big.Int).Sub(reserveOut, out).Cmp(minReserveOut) < 0 {
		return ErrAmmReserveFloor
	}
	account, err := sp.getAccount(sender)
	if err != nil {
		return err
	}
	held := znhbBalanceOf(account)
	if payload.Dir == AmmDirNhbToZnhb {
		held = nhbBalanceOf(account)
	}
	if held.Cmp(payload.AmountIn) < 0 {
		return ErrAmmInsufficientBalance
	}
	// The product of the reserves may only grow (the fee stays in the pool).
	before := new(big.Int).Mul(reserveIn, reserveOut)
	after := new(big.Int).Mul(new(big.Int).Add(reserveIn, payload.AmountIn), new(big.Int).Sub(reserveOut, out))
	if after.Cmp(before) < 0 {
		return fmt.Errorf("amm: internal error: the swap would lower the reserve product")
	}

	zero := new(big.Int)
	if payload.Dir == AmmDirZnhbToNhb {
		if err := sp.ammMoveIn(sender, payload.AmountIn, zero); err != nil {
			return err
		}
		if err := sp.ammMoveOut(sender, zero, out); err != nil {
			return err
		}
		pool.ReserveZnhb = new(big.Int).Add(pool.ReserveZnhb, payload.AmountIn)
		pool.ReserveNhb = new(big.Int).Sub(pool.ReserveNhb, out)
	} else {
		if err := sp.ammMoveIn(sender, zero, payload.AmountIn); err != nil {
			return err
		}
		if err := sp.ammMoveOut(sender, out, zero); err != nil {
			return err
		}
		pool.ReserveNhb = new(big.Int).Add(pool.ReserveNhb, payload.AmountIn)
		pool.ReserveZnhb = new(big.Int).Sub(pool.ReserveZnhb, out)
	}
	if err := manager.KVPut([]byte("amm/pool"), pool); err != nil {
		return err
	}
	var seq uint64
	if _, err := manager.KVGet([]byte("amm/tradeSeq"), &seq); err != nil {
		return err
	}
	seq++
	fee := new(big.Int).Div(new(big.Int).Mul(payload.AmountIn, big.NewInt(int64(sp.amm.FeeBps))), big.NewInt(amm.FeeDenominator))
	trade := ammTradeRecord{Height: sp.blockHeight(), Trader: append([]byte(nil), sender...), Dir: payload.Dir, In: new(big.Int).Set(payload.AmountIn), Out: new(big.Int).Set(out), Fee: fee}
	if err := manager.KVPut(ammTradeKey(seq), trade); err != nil {
		return err
	}
	if err := manager.KVPut([]byte("amm/tradeSeq"), seq); err != nil {
		return err
	}
	sp.ammEvent("amm.swap", map[string]string{
		"id": strconv.FormatUint(seq, 10), "trader": ammAddrString(sender), "dir": strconv.Itoa(int(payload.Dir)),
		"amountIn": payload.AmountIn.String(), "amountOut": out.String(), "feeWei": fee.String(),
	})
	return sp.incrementNativeAccountNonce(sender)
}

// AmmPoolView is what the read-only pool calls report.
type AmmPoolView struct {
	Active         bool
	SwapsOpen      bool
	PoolAddress    string
	ReserveZnhbWei *big.Int
	ReserveNhbWei  *big.Int
	TotalShares    *big.Int
	FeeBps         uint32
	MaxSwapBps     uint32
	TradeCount     uint64
	// PriceNhbPerZnhb is reserveNhb/reserveZnhb as a decimal string with 18 digits ("" for an empty pool). It is
	// information only: nothing on the chain reads it.
	PriceNhbPerZnhb string
}

// AmmPool reads the pool. It writes nothing.
func (sp *StateProcessor) AmmPool() (*AmmPoolView, error) {
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return nil, err
	}
	var seq uint64
	if _, err := manager.KVGet([]byte("amm/tradeSeq"), &seq); err != nil {
		return nil, err
	}
	view := &AmmPoolView{
		Active: sp.amm.ActivationHeight != 0, SwapsOpen: sp.ammSwapsOpen(),
		PoolAddress:    crypto.MustNewAddress(crypto.ZNHBPrefix, ammPoolAddress()).String(),
		ReserveZnhbWei: pool.ReserveZnhb, ReserveNhbWei: pool.ReserveNhb, TotalShares: pool.TotalShares,
		FeeBps: sp.amm.FeeBps, MaxSwapBps: sp.amm.MaxSwapBps, TradeCount: seq,
	}
	if pool.ReserveZnhb.Sign() > 0 {
		price := new(big.Rat).SetFrac(pool.ReserveNhb, pool.ReserveZnhb)
		view.PriceNhbPerZnhb = price.FloatString(18)
	}
	return view, nil
}

// AmmQuote is what a swap of amountIn would pay out now, without writing anything.
func (sp *StateProcessor) AmmQuote(dir uint8, amountIn *big.Int) (*big.Int, error) {
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return nil, err
	}
	if dir > AmmDirNhbToZnhb {
		return nil, ErrAmmInvalidPayload
	}
	in, out := pool.ReserveZnhb, pool.ReserveNhb
	if dir == AmmDirNhbToZnhb {
		in, out = pool.ReserveNhb, pool.ReserveZnhb
	}
	return amm.AmountOut(in, out, amountIn, sp.amm.FeeBps)
}

// AmmTradeView is one swap as the read call reports it.
type AmmTradeView struct {
	ID        uint64
	Height    uint64
	Trader    string
	Direction uint8
	AmountIn  *big.Int
	AmountOut *big.Int
	FeeWei    *big.Int
	// Time is the timestamp of the block the swap was included in, filled by the node's read (0 when unknown).
	Time int64
}

// AmmTrades returns the newest swaps first, at most limit (and at most 200).
func (sp *StateProcessor) AmmTrades(limit int) ([]AmmTradeView, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	manager := nhbstate.NewManager(sp.Trie)
	var seq uint64
	if _, err := manager.KVGet([]byte("amm/tradeSeq"), &seq); err != nil {
		return nil, err
	}
	out := make([]AmmTradeView, 0, limit)
	for id := seq; id >= 1 && len(out) < limit; id-- {
		var t ammTradeRecord
		found, err := manager.KVGet(ammTradeKey(id), &t)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, AmmTradeView{ID: id, Height: t.Height, Trader: ammAddrString(t.Trader), Direction: t.Dir, AmountIn: t.In, AmountOut: t.Out, FeeWei: t.Fee})
	}
	return out, nil
}

// CheckAmmInvariant reports an error when the pool's module account holds less of either asset than the reserves
// recorded in state, or when the shares or reserves are inconsistent. (It may hold more: a plain transfer to the pool
// address is a gift that no swap can reach.) It reads state and writes nothing.
func (sp *StateProcessor) CheckAmmInvariant() error {
	manager := nhbstate.NewManager(sp.Trie)
	pool, err := loadAmmPool(manager)
	if err != nil {
		return err
	}
	account, err := sp.getAccount(ammPoolAddress())
	if err != nil {
		return err
	}
	if znhbBalanceOf(account).Cmp(pool.ReserveZnhb) < 0 || nhbBalanceOf(account).Cmp(pool.ReserveNhb) < 0 {
		return fmt.Errorf("amm invariant: the pool account holds %s ZNHB and %s NHB, the reserves are %s and %s",
			znhbBalanceOf(account), nhbBalanceOf(account), pool.ReserveZnhb, pool.ReserveNhb)
	}
	if (pool.TotalShares.Sign() == 0) != (pool.ReserveZnhb.Sign() == 0 && pool.ReserveNhb.Sign() == 0) {
		return fmt.Errorf("amm invariant: shares %s and reserves %s/%s disagree about whether the pool is empty", pool.TotalShares, pool.ReserveZnhb, pool.ReserveNhb)
	}
	return nil
}
