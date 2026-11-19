package modules

// The public lending reads (lending_getMarket, lend_getPools,
// lending_getUserAccount) rebuild a pool's state from the committed lending
// transactions whenever the stored market has nothing in it -- which is always,
// on a chain where nobody has supplied or borrowed -- and, for an account, when
// nothing is stored for the address. The rebuild used to read the blocks that
// hold lending transactions, decode each transaction, recover its signer and add
// it up, on every call and inside the node's exclusive state lock: on a chain
// with many lending transactions that is a second or more of CPU per call, with
// block production and every state read waiting for it.
//
// Blocks never change once committed, so what the rebuild needs from them is
// worked out once, here. The first time a block is examined, each committed
// lending transaction in it is decoded into a small event (pool, kind, signer,
// value), and the events are kept per pool in chain order. A pool's rebuild is
// the sum of its events, and the sum is kept too and only brought forward over
// the events that arrived after the last read, so a read costs time in the number
// of blocks committed since the previous one, not in the length of the chain, and
// needs no block read, no JSON decoding and no signature recovery. A pool no
// committed lending transaction names has no entry at all, so a read for an
// arbitrary pool name answers at once and keeps nothing.
//
// A read gets its answer from the index before it takes the state lock (see
// replayPool), so the rebuild itself does not run inside the lock. What a read
// still does inside it is the state read and, as before, the pass over the
// accounts that migrates positions recorded the old way, for a pool that has not
// completed that pass yet; this index does not change that pass.
//
// A read never spends more than lendingScanBudget examining blocks: the
// background WarmReplayIndex brings the index up to the chain tip after start-up,
// gently, and a read that arrives before it has finished helps for that long and,
// if the index is still behind the tip and the read needs it, is answered with a
// retry-later error, as an answer built from part of the chain would be wrong. A
// read that does not need it (the state already holds the market or the account)
// is not affected.
//
// A block that cannot be read when the index looks at it is not one that holds no
// lending transaction: the read may have failed once (a store error) and succeed
// the next time, and remembering the failure as an answer would hide that block
// from every later replay until the process restarts. Such heights are kept apart
// and read again, all of them together, once lendingUnreadRetryAfter has passed,
// when a read next asks; until then a call does not touch them, so a block that
// stays unreadable costs one read every few seconds and not one per call. The
// events of a block found late belong in the middle of a pool's list, so the
// sums of the pools it names are started again from the events.

import (
	"context"
	"math/big"
	"sync"
	"time"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/native/lending"
)

// lendingUnreadRetryAfter is how long heights that could not be read are left
// alone before they are read again.
const lendingUnreadRetryAfter = 5 * time.Second

// lendingScanSlice is the most blocks one step of the background warm-up
// examines.
const lendingScanSlice = 1024

// lendingScanBudget is the longest one step of catching up runs, in the
// background or for a read; a step ends after the block that takes it past this.
// It is a variable so that tests can shorten it.
var lendingScanBudget = 100 * time.Millisecond

// lendingEvent is one committed lending transaction as the rebuild uses it.
type lendingEvent struct {
	height uint64
	kind   types.TxType
	from   [20]byte
	value  *big.Int
}

// lendingPoolEvent is an event together with the pool it is for.
type lendingPoolEvent struct {
	pool string
	lendingEvent
}

// lendingPoolLog is every event of one pool, ascending by height and in block
// order within a height, and the sum of a leading part of it.
type lendingPoolLog struct {
	events []lendingEvent
	// fold is the sum of events[:fold.n]; nil when none has been made yet or when
	// events were added in the middle of the list.
	fold *lendingFold
}

// lendingFold is the state a run of events adds up to.
type lendingFold struct {
	n      int
	market *lending.Market
	users  map[[20]byte]*lending.UserAccount
}

func newLendingFold() *lendingFold {
	return &lendingFold{
		market: &lending.Market{
			TotalSupplyShares: big.NewInt(0),
			TotalNHBSupplied:  big.NewInt(0),
			TotalNHBBorrowed:  big.NewInt(0),
		},
		users: make(map[[20]byte]*lending.UserAccount),
	}
}

// apply adds one event to the sum.
func (f *lendingFold) apply(ev lendingEvent) {
	market := f.market
	user := f.users[ev.from]
	if user == nil {
		user = &lending.UserAccount{
			Address:        toCryptoAddress(ev.from),
			CollateralZNHB: big.NewInt(0),
			SupplyShares:   big.NewInt(0),
			DebtNHB:        big.NewInt(0),
			ScaledDebt:     big.NewInt(0),
		}
		f.users[ev.from] = user
	}
	amount := ev.value
	switch ev.kind {
	case types.TxTypeLendingSupplyNHB:
		user.SupplyShares = sumBigInt(user.SupplyShares, amount)
		market.TotalSupplyShares = sumBigInt(market.TotalSupplyShares, amount)
		market.TotalNHBSupplied = sumBigInt(market.TotalNHBSupplied, amount)
	case types.TxTypeLendingWithdrawNHB:
		if user.SupplyShares.Cmp(amount) < 0 {
			user.SupplyShares = big.NewInt(0)
		} else {
			user.SupplyShares = new(big.Int).Sub(user.SupplyShares, amount)
		}
		if market.TotalSupplyShares.Cmp(amount) < 0 {
			market.TotalSupplyShares = big.NewInt(0)
		} else {
			market.TotalSupplyShares = new(big.Int).Sub(market.TotalSupplyShares, amount)
		}
		if market.TotalNHBSupplied.Cmp(amount) < 0 {
			market.TotalNHBSupplied = big.NewInt(0)
		} else {
			market.TotalNHBSupplied = new(big.Int).Sub(market.TotalNHBSupplied, amount)
		}
	case types.TxTypeLendingDepositZNHB:
		user.CollateralZNHB = sumBigInt(user.CollateralZNHB, amount)
	case types.TxTypeLendingWithdrawZNHB:
		if user.CollateralZNHB.Cmp(amount) < 0 {
			user.CollateralZNHB = big.NewInt(0)
		} else {
			user.CollateralZNHB = new(big.Int).Sub(user.CollateralZNHB, amount)
		}
	case types.TxTypeLendingBorrowNHB:
		user.DebtNHB = sumBigInt(user.DebtNHB, amount)
		user.ScaledDebt = sumBigInt(user.ScaledDebt, amount)
		market.TotalNHBBorrowed = sumBigInt(market.TotalNHBBorrowed, amount)
	case types.TxTypeLendingRepayNHB:
		repayAmount := amount
		if user.DebtNHB.Cmp(repayAmount) < 0 {
			repayAmount = new(big.Int).Set(user.DebtNHB)
		}
		if repayAmount.Sign() > 0 {
			user.DebtNHB = new(big.Int).Sub(user.DebtNHB, repayAmount)
			if user.ScaledDebt.Cmp(repayAmount) < 0 {
				user.ScaledDebt = big.NewInt(0)
			} else {
				user.ScaledDebt = new(big.Int).Sub(user.ScaledDebt, repayAmount)
			}
			if market.TotalNHBBorrowed.Cmp(repayAmount) < 0 {
				market.TotalNHBBorrowed = big.NewInt(0)
			} else {
				market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, repayAmount)
			}
		}
	}
	market.LastUpdateBlock = ev.height
}

// lendingPoolReplay is what a pool's committed lending transactions add up to.
// It is a private copy, so the caller may keep it and write through it.
type lendingPoolReplay struct {
	supplyShares    *big.Int
	supplied        *big.Int
	borrowed        *big.Int
	lastUpdateBlock uint64
	// users holds the accounts that were asked for and that have events.
	users map[[20]byte]*lending.UserAccount
}

func cloneLendingUser(user *lending.UserAccount) *lending.UserAccount {
	out := *user
	out.CollateralZNHB = cloneBigInt(user.CollateralZNHB)
	out.SupplyShares = cloneBigInt(user.SupplyShares)
	out.DebtNHB = cloneBigInt(user.DebtNHB)
	out.ScaledDebt = cloneBigInt(user.ScaledDebt)
	return &out
}

type lendingHeightIndex struct {
	mu      sync.Mutex
	scanned uint64 // every height 1..scanned has been examined
	// pools holds the events of every pool a committed lending transaction names.
	pools map[string]*lendingPoolLog
	// unread holds, ascending, the heights up to scanned whose block could not be
	// read when it was examined, and retryAt is when they are next read again.
	unread  []uint64
	retryAt time.Time
	now     func() time.Time // the clock; nil means time.Now
}

func (x *lendingHeightIndex) clock() time.Time {
	if x.now != nil {
		return x.now()
	}
	return time.Now()
}

// readLendingBlock reads the block at height h and decodes the committed lending
// transactions in it that the rebuild would use: those whose data names a pool
// (or none) and whose signer can be recovered. readable is false when the block
// could not be read.
func readLendingBlock(node *core.Node, h uint64) (events []lendingPoolEvent, readable bool) {
	block, err := node.GetBlockByHeight(h)
	if err != nil || block == nil {
		return nil, false
	}
	for _, tx := range block.Transactions {
		if tx == nil || !isCommittedLendingTxType(tx.Type) {
			continue
		}
		pool, err := lendingPoolIDFromTxData(tx.Data)
		if err != nil {
			continue
		}
		from, err := tx.From()
		if err != nil || len(from) == 0 {
			continue
		}
		ev := lendingPoolEvent{pool: pool, lendingEvent: lendingEvent{height: h, kind: tx.Type, value: cloneBigInt(tx.Value)}}
		copy(ev.from[:], from)
		events = append(events, ev)
	}
	return events, true
}

// logLocked returns the log of the pool, making it if it is not there yet.
func (x *lendingHeightIndex) logLocked(pool string) *lendingPoolLog {
	entry := x.pools[pool]
	if entry == nil {
		if x.pools == nil {
			x.pools = make(map[string]*lendingPoolLog)
		}
		entry = &lendingPoolLog{}
		x.pools[pool] = entry
	}
	return entry
}

// appendLocked adds the events of a block above every height already in the
// index.
func (x *lendingHeightIndex) appendLocked(events []lendingPoolEvent) {
	for _, ev := range events {
		entry := x.logLocked(ev.pool)
		entry.events = append(entry.events, ev.lendingEvent)
	}
}

// scanLocked examines the blocks above what has been scanned, up to height and,
// when limit is not zero, at most limit of them and, when budget is not zero,
// stopping after the block that takes it past budget. A block that cannot be
// read is passed over for now, as the full walk passed it over, and is read
// again later (see retryUnreadLocked).
func (x *lendingHeightIndex) scanLocked(node *core.Node, height, limit uint64, budget time.Duration) {
	end := height
	if limit > 0 && x.scanned+limit < end {
		end = x.scanned + limit
	}
	var started time.Time
	if budget > 0 {
		started = x.clock()
	}
	hadUnread := len(x.unread) > 0
	for h := x.scanned + 1; h <= end; h++ {
		events, readable := readLendingBlock(node, h)
		if readable {
			x.appendLocked(events)
		} else {
			x.unread = append(x.unread, h)
		}
		x.scanned = h
		if budget > 0 && x.clock().Sub(started) >= budget {
			break
		}
	}
	if !hadUnread && len(x.unread) > 0 {
		x.retryAt = x.clock().Add(lendingUnreadRetryAfter)
	}
}

// retryUnreadLocked reads again the heights that could not be read, if it is
// time to, and adds the events of those that hold lending transactions to the
// index. Heights that still cannot be read wait another lendingUnreadRetryAfter.
func (x *lendingHeightIndex) retryUnreadLocked(node *core.Node) {
	if len(x.unread) == 0 || x.clock().Before(x.retryAt) {
		return
	}
	late := make(map[string][]lendingEvent)
	still := x.unread[:0]
	for _, h := range x.unread {
		events, readable := readLendingBlock(node, h)
		if !readable {
			still = append(still, h)
			continue
		}
		for _, ev := range events {
			late[ev.pool] = append(late[ev.pool], ev.lendingEvent)
		}
	}
	x.unread = still
	x.retryAt = x.clock().Add(lendingUnreadRetryAfter)
	for pool, found := range late {
		entry := x.logLocked(pool)
		// The events found sit among the ones already there, so they are merged
		// in height order (found is in order: unread is ascending and a block's
		// events are in block order) and the pool's sum is made again from the
		// start. No height is in both lists, as a block is in only one of them.
		merged := make([]lendingEvent, 0, len(entry.events)+len(found))
		i, j := 0, 0
		for i < len(entry.events) || j < len(found) {
			if j == len(found) || i < len(entry.events) && entry.events[i].height < found[j].height {
				merged = append(merged, entry.events[i])
				i++
			} else {
				merged = append(merged, found[j])
				j++
			}
		}
		entry.events = merged
		entry.fold = nil
	}
}

// catchUp examines blocks the index has not looked at yet, up to height, for no
// longer than lendingScanBudget, and reports whether the index has now looked at
// every block up to height.
func (x *lendingHeightIndex) catchUp(node *core.Node, height uint64) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.scanLocked(node, height, 0, lendingScanBudget)
	x.retryUnreadLocked(node)
	return x.scanned >= height
}

// poolReplay returns what the committed lending transactions of the pool add
// up to over every block the index has examined, or nil when there are none; it
// does not examine anything itself. users in the result holds the accounts
// named in want that have events. The sum is brought forward over the events
// added since the last call, so the cost is that of those events and of the
// copies handed out.
func (x *lendingHeightIndex) poolReplay(poolID string, want ...[20]byte) *lendingPoolReplay {
	x.mu.Lock()
	defer x.mu.Unlock()
	entry := x.pools[poolID]
	if entry == nil || len(entry.events) == 0 {
		return nil
	}
	f := foldLocked(entry)
	out := &lendingPoolReplay{
		supplyShares:    cloneBigInt(f.market.TotalSupplyShares),
		supplied:        cloneBigInt(f.market.TotalNHBSupplied),
		borrowed:        cloneBigInt(f.market.TotalNHBBorrowed),
		lastUpdateBlock: f.market.LastUpdateBlock,
		users:           make(map[[20]byte]*lending.UserAccount, len(want)),
	}
	for _, addr := range want {
		if user := f.users[addr]; user != nil {
			out.users[addr] = cloneLendingUser(user)
		}
	}
	return out
}

// foldLocked brings the pool's sum up to its last event.
func foldLocked(entry *lendingPoolLog) *lendingFold {
	if entry.fold == nil {
		entry.fold = newLendingFold()
	}
	f := entry.fold
	for ; f.n < len(entry.events); f.n++ {
		f.apply(entry.events[f.n])
	}
	return f
}

// replayPool returns what the committed lending transactions of the pool add up
// to (nil when there are none), for the accounts in want too, after bringing the
// index up to the chain tip as far as lendingScanBudget allows. When the index is
// still behind the tip it answers with an error that asks the caller to retry
// shortly: a sum over part of the chain would be wrong. It takes no lock but the
// index's own, so the lending reads call it before they take the state lock.
func (m *LendingModule) replayPool(poolID string, want ...[20]byte) (*lendingPoolReplay, *ModuleError) {
	if !m.replayHeights.catchUp(m.node, m.node.GetHeight()) {
		return nil, m.indexBehind()
	}
	return m.replayHeights.poolReplay(poolID, want...), nil
}

// WarmReplayIndex brings the index up to the chain tip a slice of blocks at a
// time, resting between slices for three times as long as the slice took so as
// to use at most a quarter of one CPU, and returns when it has caught up or ctx
// ends. It is meant to run once in the background after start-up.
func (m *LendingModule) WarmReplayIndex(ctx context.Context) {
	if m == nil || m.node == nil {
		return
	}
	x := &m.replayHeights
	for {
		started := time.Now()
		x.mu.Lock()
		height := m.node.GetHeight()
		x.scanLocked(m.node, height, lendingScanSlice, lendingScanBudget)
		done := x.scanned >= height
		x.mu.Unlock()
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Since(started)):
		}
	}
}
