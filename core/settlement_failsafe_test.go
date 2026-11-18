package core

import (
	"bytes"
	"log/slog"
	"math/big"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// Tests of the two settlement failsafe rules (PL-EXT-28, PL-EXT-29 item E-1b).
// Each one drives the real epoch-closing step (ProcessBlockLifecycle at the
// epoch's last height) of a state processor, with the setting off, set above the
// closing height, set at it and set below it.

const (
	failsafeOff     = uint64(0) // the shipped value
	failsafeNotYet  = uint64(3) // the epoch closes at height 2
	failsafeAtClose = uint64(2)
	failsafeBefore  = uint64(1)
)

var failsafeSettings = []struct {
	name   string
	height uint64
	on     bool
}{
	{"setting off", failsafeOff, false},
	{"height above the closing block", failsafeNotYet, false},
	{"height equal to the closing block", failsafeAtClose, true},
	{"height below the closing block", failsafeBefore, true},
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func eventsOfType(sp *StateProcessor, eventType string) []types.Event {
	var out []types.Event
	for _, evt := range sp.Events() {
		if evt.Type == eventType {
			out = append(out, evt)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Buyback settlement
// ---------------------------------------------------------------------------

type buybackEpochCase struct {
	name             string
	governedDiscount string // "" leaves the parameter unset
	governedMargin   string
	genesisDiscount  uint32
	genesisMargin    uint32
	// wantRefundReason is the reason the failsafe gives; "" means the price is
	// usable and the epoch settles as it always did.
	wantRefundReason string
	// wantOldError is a part of the error the epoch-closing block returns without
	// the failsafe; "" when the price is usable.
	wantOldError string
	// wantDiscount and wantMargin are the basis-point values the price was
	// computed from, as the refund event reports them.
	wantDiscount, wantMargin string
}

var buybackEpochCases = []buybackEpochCase{
	{name: "governed discount 10000", governedDiscount: "10000", wantRefundReason: "zero_price", wantOldError: "price must be positive", wantDiscount: "10000", wantMargin: "0"},
	{name: "governed safety margin 10000", governedMargin: "10000", wantRefundReason: "zero_price", wantOldError: "price must be positive", wantDiscount: "0", wantMargin: "10000"},
	{name: "both governed values 10000", governedDiscount: "10000", governedMargin: "10000", wantRefundReason: "zero_price", wantOldError: "price must be positive", wantDiscount: "10000", wantMargin: "10000"},
	{name: "genesis discount 10000, nothing governed", genesisDiscount: 10000, wantRefundReason: "zero_price", wantOldError: "price must be positive", wantDiscount: "10000", wantMargin: "0"},
	{name: "genesis safety margin 10000, nothing governed", genesisMargin: 10000, wantRefundReason: "zero_price", wantOldError: "price must be positive", wantDiscount: "0", wantMargin: "10000"},
	{name: "stored discount above 10000", governedDiscount: "20000", wantRefundReason: "unusable_price", wantOldError: "must not exceed", wantDiscount: "20000", wantMargin: "0"},
	{name: "stored safety margin above 10000", governedMargin: "10001", wantRefundReason: "unusable_price", wantOldError: "must not exceed", wantDiscount: "0", wantMargin: "10001"},
	{name: "discount 9999 (controls)", governedDiscount: "9999"},
	{name: "safety margin 9999 (controls)", governedMargin: "9999"},
	{name: "nothing governed, genesis defaults (controls)"},
}

type buybackEpoch struct {
	sp          *StateProcessor
	sellers     [][]byte
	asked       []*big.Int
	accrualAddr [20]byte
	adminAddr   [20]byte
	signerKey   *crypto.PrivateKey
}

// prepareBuybackEpoch seeds two sellers' asks and, when withRef is set, a signed
// reference price, and runs block 1. The caller closes the epoch with block 2.
func prepareBuybackEpoch(t *testing.T, c buybackEpochCase, withRef bool) *buybackEpoch {
	t.Helper()
	sp, adminAddr, accrualAddr, signerKey := newBuybackTestState(t)
	if c.genesisDiscount != 0 || c.genesisMargin != 0 {
		cfg, ok := sp.BuybackConfig()
		if !ok {
			t.Fatalf("no buyback config")
		}
		cfg.DiscountBps = c.genesisDiscount
		cfg.SafetyMarginBps = c.genesisMargin
		if err := sp.SetBuybackConfig(cfg); err != nil {
			t.Fatalf("set genesis config: %v", err)
		}
	}
	ep := &buybackEpoch{sp: sp, accrualAddr: accrualAddr, adminAddr: adminAddr, signerKey: signerKey}
	for _, whole := range []int64{300, 700} {
		sender, _ := seedBuybackSeller(t, sp, weiZNHB(1000))
		ep.sellers = append(ep.sellers, sender)
		ep.asked = append(ep.asked, weiZNHB(whole))
	}
	fundBuybackAccrualNHB(t, sp, accrualAddr, weiZNHB(1000))
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.ZNHBSetCumulativeSaleDistributed(weiZNHB(500)); err != nil {
		t.Fatalf("seed cumulative: %v", err)
	}
	if c.governedDiscount != "" {
		if err := manager.ParamStoreSet(governance.ParamKeyBuybackDiscountBps, []byte(c.governedDiscount)); err != nil {
			t.Fatalf("set governed discount: %v", err)
		}
	}
	if c.governedMargin != "" {
		if err := manager.ParamStoreSet(governance.ParamKeyBuybackSafetyMarginBps, []byte(c.governedMargin)); err != nil {
			t.Fatalf("set governed margin: %v", err)
		}
	}
	ts := time.Unix(rewardBlockTimestamp1, 0).UTC()
	sp.BeginBlock(1, ts)
	for i, sender := range ep.sellers {
		submitBuybackAsk(t, sp, sender, ep.asked[i])
	}
	if withRef {
		submitBuybackRefPrice(t, sp, 1, big.NewInt(1), big.NewInt(1), ts, signerKey)
	}
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("block 1: %v", err)
	}
	return ep
}

func (ep *buybackEpoch) closeEpoch() error {
	return ep.sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2)
}

func mustAccount(t *testing.T, sp *StateProcessor, addr []byte) *types.Account {
	t.Helper()
	acc, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	return acc
}

// assertEveryAskRefunded checks the accounting of an epoch that bought nothing.
func assertEveryAskRefunded(t *testing.T, ep *buybackEpoch) {
	t.Helper()
	for i, sender := range ep.sellers {
		acc := mustAccount(t, ep.sp, sender)
		if acc.BalanceZNHB.Cmp(weiZNHB(1000)) != 0 {
			t.Fatalf("seller %d ZNHB = %s, want the full 1000 back", i, acc.BalanceZNHB)
		}
		if acc.BalanceNHB.Sign() != 0 {
			t.Fatalf("seller %d was paid %s NHB, want none", i, acc.BalanceNHB)
		}
	}
	escrow := mustAccount(t, ep.sp, ep.accrualAddr[:])
	if escrow.BalanceZNHB.Sign() != 0 {
		t.Fatalf("escrow still holds %s ZNHB", escrow.BalanceZNHB)
	}
	if escrow.BalanceNHB.Cmp(weiZNHB(1000)) != 0 {
		t.Fatalf("accrual NHB = %s, want it untouched at 1000", escrow.BalanceNHB)
	}
	manager := nhbstate.NewManager(ep.sp.Trie)
	ledger, err := manager.ZNHBBuybackAccrualBalance()
	if err != nil {
		t.Fatalf("read accrual ledger: %v", err)
	}
	if ledger.Cmp(weiZNHB(1000)) != 0 {
		t.Fatalf("accrual ledger = %s, want 1000", ledger)
	}
	cumulative, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative: %v", err)
	}
	if cumulative.Cmp(weiZNHB(500)) != 0 {
		t.Fatalf("cumulative sale distributed moved to %s without a purchase", cumulative)
	}
	asks, err := manager.BuybackAsksForEpoch(1)
	if err != nil {
		t.Fatalf("read asks: %v", err)
	}
	if len(asks) != 0 {
		t.Fatalf("%d asks left behind", len(asks))
	}
	if _, has, err := manager.BuybackRefPriceForEpoch(1); err != nil || has {
		t.Fatalf("reference price left behind (has=%v err=%v)", has, err)
	}
	if err := ep.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
}

func TestBuybackSettlementFailsafe(t *testing.T) {
	for _, setting := range failsafeSettings {
		for _, c := range buybackEpochCases {
			setting, c := setting, c
			t.Run(setting.name+"/"+c.name, func(t *testing.T) {
				logs := captureSlog(t)
				ep := prepareBuybackEpoch(t, c, true)
				ep.sp.SetEpochSettlementFailsafe(setting.height)
				err := ep.closeEpoch()

				if c.wantRefundReason == "" {
					// the price is usable: the failsafe never changes an epoch
					if err != nil {
						t.Fatalf("a usable price failed the block: %v", err)
					}
					if len(eventsOfType(ep.sp, "buyback.epoch.refunded")) != 0 {
						t.Fatalf("a refund event for a usable price")
					}
					settled := eventsOfType(ep.sp, "buyback.epoch.settled")
					if len(settled) != 1 || settled[0].Attributes["totalZNHBFilledWei"] != weiZNHB(1000).String() {
						t.Fatalf("a usable price did not buy every ask: %v", settled)
					}
					if settled[0].Attributes["referencePriceUsed"] != "true" || settled[0].Attributes["clearingPrice"] == "" {
						t.Fatalf("a usable price is not reported as used: %v", settled[0].Attributes)
					}
					if err := ep.sp.CheckZNHBSupplyInvariant(); err != nil {
						t.Fatalf("supply invariant: %v", err)
					}
					return
				}

				if !setting.on {
					if err == nil {
						t.Fatalf("the epoch-closing block succeeded without the failsafe")
					}
					if !strings.Contains(err.Error(), c.wantOldError) {
						t.Fatalf("error %q does not say %q", err, c.wantOldError)
					}
					if len(eventsOfType(ep.sp, "buyback.epoch.refunded")) != 0 {
						t.Fatalf("a refund event with the failsafe off")
					}
					return
				}

				if err != nil {
					t.Fatalf("the epoch-closing block failed with the failsafe on: %v", err)
				}
				assertEveryAskRefunded(t, ep)

				settled := eventsOfType(ep.sp, "buyback.epoch.settled")
				if len(settled) != 1 {
					t.Fatalf("%d settled events, want 1", len(settled))
				}
				got := settled[0].Attributes
				if got["epoch"] != "1" || got["asksFilled"] != "2" || got["totalZNHBFilledWei"] != "0" || got["totalNHBPaidWei"] != "0" || got["referencePriceUsed"] != "false" {
					t.Fatalf("settled event = %v", got)
				}
				if _, has := got["clearingPrice"]; has {
					t.Fatalf("settled event carries a clearing price: %v", got)
				}
				refunded := eventsOfType(ep.sp, "buyback.epoch.refunded")
				if len(refunded) != 1 {
					t.Fatalf("%d refund events, want 1", len(refunded))
				}
				attrs := refunded[0].Attributes
				if attrs["epoch"] != "1" || attrs["asksRefunded"] != "2" || attrs["totalZNHBRefundedWei"] != weiZNHB(1000).String() || attrs["reason"] != c.wantRefundReason {
					t.Fatalf("refund event = %v, want reason %s", attrs, c.wantRefundReason)
				}
				if attrs["discountBps"] != c.wantDiscount || attrs["safetyMarginBps"] != c.wantMargin {
					t.Fatalf("refund event reports discount %s margin %s, want %s and %s", attrs["discountBps"], attrs["safetyMarginBps"], c.wantDiscount, c.wantMargin)
				}
				out := logs.String()
				if !strings.Contains(out, "no purchase this epoch") || !strings.Contains(out, "refunded in full") || !strings.Contains(out, c.wantRefundReason) {
					t.Fatalf("log line missing or incomplete: %q", out)
				}
			})
		}
	}
}

// With a price that is usable the failsafe changes nothing at all: the same state
// at the closing block gives the same state root with the setting off and on.
func TestBuybackSettlementFailsafeLeavesAHealthyEpochByteIdentical(t *testing.T) {
	for _, withRef := range []bool{true, false} {
		ep := prepareBuybackEpoch(t, buybackEpochCase{name: "healthy", governedDiscount: "100"}, withRef)
		on, err := ep.sp.Copy()
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		on.SetEpochSettlementFailsafe(failsafeAtClose)
		off, err := ep.sp.Copy()
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		off.SetEpochSettlementFailsafe(failsafeOff)
		if err := on.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
			t.Fatalf("on: %v", err)
		}
		if err := off.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
			t.Fatalf("off: %v", err)
		}
		if on.Trie.Hash() != off.Trie.Hash() {
			t.Fatalf("withRef=%v: the state differs with the failsafe on", withRef)
		}
		if len(on.Events()) != len(off.Events()) {
			t.Fatalf("withRef=%v: %d events with the failsafe on, %d off", withRef, len(on.Events()), len(off.Events()))
		}
	}
}

// The chain keeps producing: after the refunded epoch a corrected parameter and a
// new ask settle at the next epoch boundary.
func TestBuybackSettlementFailsafeNextEpochSettlesAfterTheCorrection(t *testing.T) {
	ep := prepareBuybackEpoch(t, buybackEpochCase{name: "bad", governedDiscount: "10000"}, true)
	ep.sp.SetEpochSettlementFailsafe(failsafeAtClose)
	if err := ep.closeEpoch(); err != nil {
		t.Fatalf("epoch 1: %v", err)
	}
	// the corrective proposal executes in a later block
	manager := nhbstate.NewManager(ep.sp.Trie)
	if err := manager.ParamStoreSet(governance.ParamKeyBuybackDiscountBps, []byte("750")); err != nil {
		t.Fatalf("correct the discount: %v", err)
	}
	// epoch 2 covers heights 3 and 4; the signer key is the one of the test state
	signerKey := ep.signerKey
	ts3 := time.Unix(rewardBlockTimestamp2+1, 0).UTC()
	ep.sp.BeginBlock(3, ts3)
	submitBuybackAsk(t, ep.sp, ep.sellers[0], weiZNHB(100))
	submitBuybackRefPrice(t, ep.sp, 2, big.NewInt(1), big.NewInt(1), ts3, signerKey)
	ep.sp.EndBlock()
	if err := ep.sp.ProcessBlockLifecycle(3, rewardBlockTimestamp2+1); err != nil {
		t.Fatalf("block 3: %v", err)
	}
	if err := ep.sp.ProcessBlockLifecycle(4, rewardBlockTimestamp2+2); err != nil {
		t.Fatalf("epoch 2: %v", err)
	}
	seller := mustAccount(t, ep.sp, ep.sellers[0])
	if seller.BalanceNHB.Sign() <= 0 {
		t.Fatalf("the seller was not paid in the epoch after the correction")
	}
	if err := ep.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reward settlement
// ---------------------------------------------------------------------------

// setAdminLiquid moves what the admin wallet holds above liquid into its locked
// balance, the way staking does: the sum the supply invariant counts is unchanged.
func setAdminLiquid(t *testing.T, sp *StateProcessor, adminAddr [20]byte, liquid *big.Int) {
	t.Helper()
	acc := mustAccount(t, sp, adminAddr[:])
	if liquid.Cmp(acc.BalanceZNHB) > 0 {
		t.Fatalf("cannot raise the liquid balance to %s", liquid)
	}
	moved := new(big.Int).Sub(acc.BalanceZNHB, liquid)
	locked := big.NewInt(0)
	if acc.LockedZNHB != nil {
		locked = acc.LockedZNHB
	}
	acc.LockedZNHB = new(big.Int).Add(locked, moved)
	acc.BalanceZNHB = new(big.Int).Set(liquid)
	if err := sp.setAccount(adminAddr[:], acc); err != nil {
		t.Fatalf("persist admin: %v", err)
	}
}

type rewardsEpoch struct {
	sp         *StateProcessor
	adminAddr  [20]byte
	validators [][]byte
}

// prepareRewardsEpoch seeds two validators, optionally lowers the reward pool to
// pool and the admin wallet's liquid balance to liquid, and runs block 1. The
// caller closes the epoch with block 2.
func prepareRewardsEpoch(t *testing.T, pool, liquid *big.Int) *rewardsEpoch {
	t.Helper()
	sp := newRewardTestState(t)
	adminAddr := withRewardPoolAdminWallet(t, sp)
	if pool != nil {
		drainRewardPool(t, sp, adminAddr, pool)
	}
	re := &rewardsEpoch{sp: sp, adminAddr: adminAddr}
	re.validators = append(re.validators, seedEligibleValidator(t, sp, 6000, 10), seedEligibleValidator(t, sp, 4000, 5))
	if liquid != nil {
		setAdminLiquid(t, sp, adminAddr, liquid)
	}
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("block 1: %v", err)
	}
	return re
}

func (re *rewardsEpoch) closeEpoch() error {
	return re.sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2)
}

func (re *rewardsEpoch) validatorsTotal(t *testing.T) *big.Int {
	t.Helper()
	total := big.NewInt(0)
	for _, v := range re.validators {
		acc := mustAccount(t, re.sp, v)
		total.Add(total, acc.BalanceZNHB)
	}
	return total
}

// scaledPayout is what the test schedule pays when limited to limit: the three
// categories (20, 50 and 30 percent of the full payout) each scaled and rounded
// down.
func scaledPayout(full, limit *big.Int) *big.Int {
	if limit.Cmp(full) >= 0 {
		return new(big.Int).Set(full)
	}
	total := big.NewInt(0)
	for _, share := range []int64{20, 50, 30} {
		category := new(big.Int).Mul(full, big.NewInt(share))
		category.Quo(category, big.NewInt(100))
		scaled := new(big.Int).Mul(category, limit)
		scaled.Quo(scaled, full)
		total.Add(total, scaled)
	}
	return total
}

// fullPayout is what a healthy epoch pays in this fixture.
func fullPayout(t *testing.T) *big.Int {
	t.Helper()
	re := prepareRewardsEpoch(t, nil, nil)
	if err := re.closeEpoch(); err != nil {
		t.Fatalf("baseline epoch: %v", err)
	}
	settlement, ok := re.sp.LatestRewardEpochSettlement()
	if !ok || settlement.PaidTotal.Sign() <= 0 {
		t.Fatalf("baseline epoch paid nothing")
	}
	return new(big.Int).Set(settlement.PaidTotal)
}

type rewardsCase struct {
	name string
	// pool and liquid are given as functions of the full payout P; nil leaves the
	// fixture's own (very large) value.
	pool   func(p *big.Int) *big.Int
	liquid func(p *big.Int) *big.Int
	// shortOnLiquid: with the failsafe on the admin wallet's balance is what limits
	// the payout and the epoch is recorded short. limit is the most the epoch may
	// pay with the failsafe on (nil: the full payout).
	shortOnLiquid bool
	limit         func(p *big.Int) *big.Int
	// failsWithoutFailsafe: the old code returns the payout error.
	failsWithoutFailsafe bool
}

func minus(n int64) func(p *big.Int) *big.Int {
	return func(p *big.Int) *big.Int { return new(big.Int).Sub(p, big.NewInt(n)) }
}
func halfOf(p *big.Int) *big.Int  { return new(big.Int).Rsh(p, 1) }
func tenthOf(p *big.Int) *big.Int { return new(big.Int).Quo(p, big.NewInt(10)) }
func exactly(p *big.Int) *big.Int { return new(big.Int).Set(p) }
func zero(*big.Int) *big.Int      { return big.NewInt(0) }

var rewardsCases = []rewardsCase{
	{name: "ample balance (controls)"},
	{name: "liquid exactly the payout", liquid: exactly},
	{name: "liquid one below the payout", liquid: minus(1), shortOnLiquid: true, limit: minus(1), failsWithoutFailsafe: true},
	{name: "liquid half the payout", liquid: halfOf, shortOnLiquid: true, limit: halfOf, failsWithoutFailsafe: true},
	{name: "liquid zero (all staked)", liquid: zero, shortOnLiquid: true, limit: zero, failsWithoutFailsafe: true},
	// the pool is the smaller limit: the old pool scaling alone applies
	{name: "pool below the payout, liquid above the pool", pool: minus(40), liquid: minus(10), limit: minus(40)},
	{name: "pool and liquid equal, both below the payout", pool: minus(40), liquid: minus(40), limit: minus(40)},
	// the balance is the smaller limit
	{name: "liquid below the pool, pool below the payout", pool: minus(10), liquid: minus(40), shortOnLiquid: true, limit: minus(40), failsWithoutFailsafe: true},
}

func (rc rewardsCase) build(t *testing.T, p *big.Int) *rewardsEpoch {
	var pool, liquid *big.Int
	if rc.pool != nil {
		pool = rc.pool(p)
	}
	if rc.liquid != nil {
		liquid = rc.liquid(p)
	}
	return prepareRewardsEpoch(t, pool, liquid)
}

func TestRewardSettlementFailsafe(t *testing.T) {
	p := fullPayout(t)
	for _, setting := range failsafeSettings {
		for _, rc := range rewardsCases {
			setting, rc := setting, rc
			t.Run(setting.name+"/"+rc.name, func(t *testing.T) {
				logs := captureSlog(t)
				re := rc.build(t, p)
				re.sp.SetEpochSettlementFailsafe(setting.height)
				poolBefore := rewardPoolBalance(t, re.sp)
				adminBefore := mustAccount(t, re.sp, re.adminAddr[:])
				err := re.closeEpoch()

				if rc.failsWithoutFailsafe && !setting.on {
					if err == nil {
						t.Fatalf("the epoch-closing block succeeded without the failsafe")
					}
					if !strings.Contains(err.Error(), "less than this epoch's payout total") {
						t.Fatalf("unexpected error: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("the epoch-closing block failed: %v", err)
				}
				settlement, ok := re.sp.LatestRewardEpochSettlement()
				if !ok {
					t.Fatalf("no settlement recorded")
				}
				wantMax := new(big.Int).Set(p)
				if rc.limit != nil {
					wantMax = rc.limit(p)
				}
				// the test schedule plans 100: 20 to the validators, 50 to the
				// stakers and 30 to the engaged accounts, each scaled down by the
				// limit and rounded down
				want := scaledPayout(p, wantMax)
				if settlement.PlannedTotal.Cmp(want) != 0 || settlement.PaidTotal.Cmp(want) != 0 {
					t.Fatalf("limit %s: planned %s paid %s, want %s", wantMax, settlement.PlannedTotal, settlement.PaidTotal, want)
				}
				// every unit the settlement says it paid reached a validator, and left
				// the admin wallet and the reward pool
				if got := re.validatorsTotal(t); got.Cmp(settlement.PaidTotal) != 0 {
					t.Fatalf("validators hold %s, settlement paid %s", got, settlement.PaidTotal)
				}
				adminAfter := mustAccount(t, re.sp, re.adminAddr[:])
				if got := new(big.Int).Sub(adminBefore.BalanceZNHB, adminAfter.BalanceZNHB); got.Cmp(settlement.PaidTotal) != 0 {
					t.Fatalf("admin liquid fell by %s, settlement paid %s", got, settlement.PaidTotal)
				}
				if adminAfter.BalanceZNHB.Sign() < 0 {
					t.Fatalf("admin liquid went negative")
				}
				if got := new(big.Int).Sub(poolBefore, rewardPoolBalance(t, re.sp)); got.Cmp(settlement.PaidTotal) != 0 {
					t.Fatalf("reward pool fell by %s, settlement paid %s", got, settlement.PaidTotal)
				}
				if err := re.sp.CheckZNHBSupplyInvariant(); err != nil {
					t.Fatalf("supply invariant: %v", err)
				}
				closed := eventsOfType(re.sp, "rewards.epoch_closed")
				if len(closed) != 1 {
					t.Fatalf("%d epoch_closed events", len(closed))
				}
				reason := closed[0].Attributes["short_reason"]
				logged := strings.Contains(logs.String(), "recorded short")
				if rc.shortOnLiquid && setting.on {
					if reason != "admin_liquid_balance" {
						t.Fatalf("short_reason = %q, want admin_liquid_balance", reason)
					}
					if !logged || !strings.Contains(logs.String(), "epoch=1") {
						t.Fatalf("no log line for a short epoch: %q", logs.String())
					}
				} else {
					if reason != "" {
						t.Fatalf("short_reason = %q on an epoch the balance did not limit", reason)
					}
					if logged {
						t.Fatalf("a short-epoch log line on an epoch the balance did not limit: %q", logs.String())
					}
				}
			})
		}
	}
}

// When the failsafe is on but the admin wallet can pay, nothing changes: the same
// state at the closing block gives the same state root and the same events.
func TestRewardSettlementFailsafeLeavesAHealthyEpochByteIdentical(t *testing.T) {
	p := fullPayout(t)
	for _, rc := range rewardsCases {
		if rc.shortOnLiquid {
			continue
		}
		re := rc.build(t, p)
		on, err := re.sp.Copy()
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		on.SetEpochSettlementFailsafe(failsafeAtClose)
		off, err := re.sp.Copy()
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		if err := on.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
			t.Fatalf("%s: on: %v", rc.name, err)
		}
		if err := off.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
			t.Fatalf("%s: off: %v", rc.name, err)
		}
		if on.Trie.Hash() != off.Trie.Hash() {
			t.Fatalf("%s: the state differs with the failsafe on", rc.name)
		}
		onEvents, offEvents := on.Events(), off.Events()
		if len(onEvents) != len(offEvents) {
			t.Fatalf("%s: %d events on, %d off", rc.name, len(onEvents), len(offEvents))
		}
		for i := range onEvents {
			if onEvents[i].Type != offEvents[i].Type || len(onEvents[i].Attributes) != len(offEvents[i].Attributes) {
				t.Fatalf("%s: event %d differs: %v vs %v", rc.name, i, onEvents[i], offEvents[i])
			}
		}
	}
}

// The admin wallet can also be one of the recipients; what it pays itself comes
// back to its liquid balance. The scale-down is to the balance it held before the
// epoch, which is always enough.
func TestRewardSettlementFailsafeWhenTheAdminWalletIsARecipient(t *testing.T) {
	p := fullPayout(t)
	for _, on := range []bool{false, true} {
		sp := newRewardTestState(t)
		adminAddr := withRewardPoolAdminWallet(t, sp)
		validator := seedEligibleValidator(t, sp, 4000, 5)
		_ = validator
		admin := mustAccount(t, sp, adminAddr[:])
		admin.Stake = big.NewInt(6000)
		admin.EngagementScore = 10
		admin.EngagementLastHeartbeat = uint64(rewardBlockTimestamp1)
		admin.ValidatorRegistered = true
		if err := sp.setAccount(adminAddr[:], admin); err != nil {
			t.Fatalf("persist admin: %v", err)
		}
		sp.EligibleValidators[string(adminAddr[:])] = big.NewInt(6000)
		setAdminLiquid(t, sp, adminAddr, tenthOf(p))
		if on {
			sp.SetEpochSettlementFailsafe(failsafeAtClose)
		}
		if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
			t.Fatalf("block 1: %v", err)
		}
		err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2)
		if !on {
			if err == nil {
				t.Fatalf("an admin wallet that cannot pay did not fail the block without the failsafe")
			}
			continue
		}
		if err != nil {
			t.Fatalf("failsafe on: %v", err)
		}
		if err := sp.CheckZNHBSupplyInvariant(); err != nil {
			t.Fatalf("supply invariant: %v", err)
		}
		settlement, _ := sp.LatestRewardEpochSettlement()
		if settlement.PaidTotal.Cmp(tenthOf(p)) > 0 {
			t.Fatalf("paid %s, more than the %s the wallet held", settlement.PaidTotal, tenthOf(p))
		}
	}
}
