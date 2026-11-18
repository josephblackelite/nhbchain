package core

import (
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

// PL-EXT-28. A governed buyback discount or safety margin of exactly 10,000 basis points
// leaves no positive highest price. The settlement returned "price must be positive" and
// the block that closes the epoch was rejected by every validator that applied it. These
// tests close an epoch with one funded ask and a signed reference price under different
// governed values, with the rule off and on.

type buybackEpochRun struct {
	settleErr  error
	sellerZNHB *big.Int
	sellerNHB  *big.Int
	escrowZNHB *big.Int
	accrualNHB *big.Int
	asksLeft   int
	refLeft    bool
	settled    map[string]string // attributes of the settlement event, nil if none
	invariant  error
}

func runBuybackEpoch(t *testing.T, activation uint64, discountBps, marginBps string) buybackEpochRun {
	t.Helper()
	sp, _, accrualAddr, signerKey := newBuybackTestState(t)
	sp.SetBuybackUnpriceableEpochRefund(activation)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1000))
	fundBuybackAccrualNHB(t, sp, accrualAddr, weiZNHB(1000))
	manager := nhbstate.NewManager(sp.Trie)
	if discountBps != "" {
		if err := manager.ParamStoreSet(governance.ParamKeyBuybackDiscountBps, []byte(discountBps)); err != nil {
			t.Fatalf("set governed discount: %v", err)
		}
	}
	if marginBps != "" {
		if err := manager.ParamStoreSet(governance.ParamKeyBuybackSafetyMarginBps, []byte(marginBps)); err != nil {
			t.Fatalf("set governed safety margin: %v", err)
		}
	}

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	submitBuybackAsk(t, sp, sender, weiZNHB(100))
	submitBuybackRefPrice(t, sp, 1, big.NewInt(1), big.NewInt(1), time.Unix(rewardBlockTimestamp1, 0).UTC(), signerKey)
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	out := buybackEpochRun{}
	// The block that closes the epoch is executed inside its own block context, as the
	// node does it: the rule reads the height from it.
	sp.BeginBlock(2, time.Unix(rewardBlockTimestamp2, 0).UTC())
	out.settleErr = sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2)

	seller, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	out.sellerZNHB, out.sellerNHB = new(big.Int).Set(seller.BalanceZNHB), new(big.Int).Set(seller.BalanceNHB)
	escrow, err := sp.getAccount(accrualAddr[:])
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	out.escrowZNHB, out.accrualNHB = new(big.Int).Set(escrow.BalanceZNHB), new(big.Int).Set(escrow.BalanceNHB)
	asks, err := manager.BuybackAsksForEpoch(1)
	if err != nil {
		t.Fatalf("read asks: %v", err)
	}
	out.asksLeft = len(asks)
	_, out.refLeft, err = manager.BuybackRefPriceForEpoch(1)
	if err != nil {
		t.Fatalf("read reference price: %v", err)
	}
	for _, evt := range sp.Events() {
		if evt.Type == events.TypeBuybackEpochSettled {
			out.settled = evt.Attributes
		}
	}
	out.invariant = sp.CheckZNHBSupplyInvariant()
	return out
}

// The weakness, shown: with the rule off the epoch cannot close.
func TestBuybackEpochWithNoPositivePriceFailsTheBlockWithTheRuleOff(t *testing.T) {
	// Off; set one block above the closing block (3 > 2); and far above the chain tip.
	for _, activation := range []uint64{0, 3, 100} {
		for _, governed := range [][2]string{{"10000", ""}, {"", "10000"}, {"10000", "10000"}} {
			out := runBuybackEpoch(t, activation, governed[0], governed[1])
			if out.settleErr == nil || !strings.Contains(out.settleErr.Error(), "price must be positive") {
				t.Fatalf("activation %d, discount %q margin %q: the epoch closed (%v); below the height it must fail as it always did", activation, governed[0], governed[1], out.settleErr)
			}
		}
	}
}

func TestBuybackEpochWithNoPositivePriceRefundsItsAsksFromTheHeight(t *testing.T) {
	// The epoch closes in block 2: a height of 2 is the first block the rule governs, 1 is past it.
	for _, tc := range []struct {
		activation uint64
		governed   [2]string
	}{
		{1, [2]string{"10000", ""}}, {1, [2]string{"", "10000"}}, {1, [2]string{"10000", "10000"}},
		{2, [2]string{"10000", ""}}, {2, [2]string{"", "10000"}},
	} {
		governed := tc.governed
		out := runBuybackEpoch(t, tc.activation, governed[0], governed[1])
		name := "activation " + strconv.FormatUint(tc.activation, 10) + " discount " + governed[0] + " margin " + governed[1]
		if out.settleErr != nil {
			t.Fatalf("%s: the epoch still fails with the rule on: %v", name, out.settleErr)
		}
		if out.sellerZNHB.Cmp(weiZNHB(1000)) != 0 || out.sellerNHB.Sign() != 0 {
			t.Fatalf("%s: seller holds %s ZNHB and %s NHB, want a full refund (1000 ZNHB, 0 NHB)", name, out.sellerZNHB, out.sellerNHB)
		}
		if out.escrowZNHB.Sign() != 0 || out.accrualNHB.Cmp(weiZNHB(1000)) != 0 {
			t.Fatalf("%s: escrow holds %s ZNHB and the accrual %s NHB, want 0 and an untouched 1000", name, out.escrowZNHB, out.accrualNHB)
		}
		if out.asksLeft != 0 || out.refLeft {
			t.Fatalf("%s: %d asks and reference price present=%v are left behind, want none: the epoch's records are cleared", name, out.asksLeft, out.refLeft)
		}
		if out.invariant != nil {
			t.Fatalf("%s: the supply invariant fails after the refund: %v", name, out.invariant)
		}
		if out.settled == nil {
			t.Fatalf("%s: no settlement event was recorded", name)
		}
		if out.settled["referencePriceUsed"] != "false" || out.settled["totalNHBPaidWei"] != "0" || out.settled["totalZNHBFilledWei"] != "0" {
			t.Fatalf("%s: the settlement event says %v, want no reference price used and nothing bought", name, out.settled)
		}
		if _, ok := out.settled["clearingPrice"]; ok {
			t.Fatalf("%s: the settlement event names a clearing price (%s) though nothing was priced", name, out.settled["clearingPrice"])
		}
	}
}

// A stored value above the whole price (a proposal cannot carry one, but the parameter
// store does not look) makes the price computation itself fail. The same rule applies.
func TestBuybackEpochWhosePriceComputationFailsFollowsTheSameRule(t *testing.T) {
	for _, governed := range [][2]string{{"10001", ""}, {"", "10001"}} {
		off := runBuybackEpoch(t, 0, governed[0], governed[1])
		if off.settleErr == nil || !strings.Contains(off.settleErr.Error(), "compute max price") {
			t.Fatalf("discount %q margin %q with the rule off: %v, want the block to fail in the price computation", governed[0], governed[1], off.settleErr)
		}
		on := runBuybackEpoch(t, 1, governed[0], governed[1])
		if on.settleErr != nil || on.sellerZNHB.Cmp(weiZNHB(1000)) != 0 || on.sellerNHB.Sign() != 0 || on.settled["referencePriceUsed"] != "false" {
			t.Fatalf("discount %q margin %q with the rule on: error %v, seller %s ZNHB %s NHB, event %v; want a full refund", governed[0], governed[1], on.settleErr, on.sellerZNHB, on.sellerNHB, on.settled)
		}
	}
}

// An epoch whose price can be computed settles exactly as before, whatever the setting.
func TestBuybackEpochThatCanBePricedIsUnchangedByTheRule(t *testing.T) {
	off := runBuybackEpoch(t, 0, "100", "100")
	on := runBuybackEpoch(t, 1, "100", "100")
	if off.settleErr != nil || on.settleErr != nil {
		t.Fatalf("a priceable epoch failed: off %v, on %v", off.settleErr, on.settleErr)
	}
	if off.sellerNHB.Sign() <= 0 {
		t.Fatalf("the priceable epoch bought nothing (seller NHB %s): the comparison would prove nothing", off.sellerNHB)
	}
	if off.sellerNHB.Cmp(on.sellerNHB) != 0 || off.sellerZNHB.Cmp(on.sellerZNHB) != 0 || off.accrualNHB.Cmp(on.accrualNHB) != 0 {
		t.Fatalf("the rule changed a priceable epoch: off %s NHB / %s ZNHB, on %s NHB / %s ZNHB", off.sellerNHB, off.sellerZNHB, on.sellerNHB, on.sellerZNHB)
	}
	for _, key := range []string{"referencePriceUsed", "clearingPrice", "totalNHBPaidWei", "totalZNHBFilledWei", "asksFilled"} {
		if off.settled[key] != on.settled[key] || off.settled[key] == "" {
			t.Fatalf("the settlement event differs on %q: off %q, on %q", key, off.settled[key], on.settled[key])
		}
	}
	if off.settled["referencePriceUsed"] != "true" {
		t.Fatalf("a priced epoch reports referencePriceUsed=%s, want true", off.settled["referencePriceUsed"])
	}
	// The highest value that still leaves a positive price is unchanged too.
	if out := runBuybackEpoch(t, 1, "9999", ""); out.settleErr != nil || out.settled["referencePriceUsed"] != "true" {
		t.Fatalf("a discount of 9,999 basis points still leaves a positive price and must be priced: %v %v", out.settleErr, out.settled)
	}
}

func TestBuybackUnpriceableEpochRefundPredicateTable(t *testing.T) {
	sp := newStakingStateProcessor(t)
	at := func(height uint64) { sp.BeginBlock(height, time.Unix(1_800_000_000, 0)) }
	sp.SetBuybackUnpriceableEpochRefund(50)
	at(49)
	if sp.buybackUnpriceableEpochRefunds() {
		t.Fatalf("below the height the epoch fails as before")
	}
	at(50)
	if !sp.buybackUnpriceableEpochRefunds() {
		t.Fatalf("at the height the epoch is refunded")
	}
	sp.SetBuybackUnpriceableEpochRefund(0)
	at(1000)
	if sp.buybackUnpriceableEpochRefunds() {
		t.Fatalf("with the setting at zero the rule is off at every height")
	}
	var nilSP *StateProcessor
	nilSP.SetBuybackUnpriceableEpochRefund(1)
	if nilSP.buybackUnpriceableEpochRefunds() {
		t.Fatalf("a nil state processor is never active")
	}
}

func TestBuybackUnpriceableEpochRefundSettingIsCopied(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetBuybackUnpriceableEpochRefund(444)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.buybackUnpriceableRefundFromHeight != 444 {
		t.Fatalf("the copy holds %d, want 444", cp.buybackUnpriceableRefundFromHeight)
	}
}

func TestNodeBuybackUnpriceableEpochRefund_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetBuybackUnpriceableEpochRefund(9)
	node.stateMu.Lock()
	gotH := node.state.buybackUnpriceableRefundFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.buybackUnpriceableRefundFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuiltH)
	}
}
