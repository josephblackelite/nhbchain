package core

import (
	"testing"
	"time"
)

// R14 integration: two chain rules refuse to let an unpriceable buyback epoch stop the block that closes it.
// BuybackUnpriceableEpochRefundActivationHeight (live since r13) settles it like an epoch with no reference
// price; EpochSettlementFailsafeActivationHeight (W8) does the same and also reports why in its own event.
// Both heights can be set at once, so what each combination does is pinned here, so that adding the second
// rule never changes what the first, already live, does on its own.
func TestBuybackTwoRefundRulesCombine(t *testing.T) {
	zeroPrice := buybackEpochCase{name: "governed discount 10000", governedDiscount: "10000", wantRefundReason: "zero_price", wantDiscount: "10000", wantMargin: "0"}
	unusable := buybackEpochCase{name: "stored discount above 10000", governedDiscount: "20000", wantRefundReason: "unusable_price", wantDiscount: "20000", wantMargin: "0"}

	// The epoch is closed by the block at height 2, executed in its own block context as the node does it: the
	// older rule reads the height from that context, the failsafe from the closing block's own height.
	cases := []struct {
		name         string
		c            buybackEpochCase
		unpriceable  uint64 // BuybackUnpriceableEpochRefund height (0 = off)
		failsafe     uint64 // EpochSettlementFailsafe height (0 = off)
		wantFailsafe bool   // the failsafe's own refund event is reported
		wantRefund   bool   // every ask is refunded and the block succeeds
	}{
		{"neither rule: the block fails", zeroPrice, 0, 0, false, false},
		{"the older rule alone refunds without the failsafe's event", zeroPrice, 2, 0, false, true},
		{"the failsafe alone refunds and reports why", zeroPrice, 0, 2, true, true},
		{"both: the failsafe's report wins", zeroPrice, 2, 2, true, true},
		{"both set, only the older reached", zeroPrice, 2, 3, false, true},
		{"both set, only the failsafe reached", zeroPrice, 3, 2, true, true},
		{"both set, neither reached: the block fails", zeroPrice, 3, 3, false, false},
		{"unusable price, older rule alone", unusable, 2, 0, false, true},
		{"unusable price, both", unusable, 2, 2, true, true},
		{"unusable price, neither: the block fails", unusable, 0, 0, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ep := prepareBuybackEpoch(t, tc.c, true)
			ep.sp.SetBuybackUnpriceableEpochRefund(tc.unpriceable)
			ep.sp.SetEpochSettlementFailsafe(tc.failsafe)
			ep.sp.BeginBlock(2, time.Unix(rewardBlockTimestamp2, 0).UTC())
			err := ep.closeEpoch()
			if !tc.wantRefund {
				if err == nil {
					t.Fatalf("the epoch-closing block succeeded with no rule in force")
				}
				return
			}
			if err != nil {
				t.Fatalf("the epoch-closing block failed: %v", err)
			}
			assertEveryAskRefunded(t, ep)
			settled := eventsOfType(ep.sp, "buyback.epoch.settled")
			if len(settled) != 1 || settled[0].Attributes["referencePriceUsed"] != "false" || settled[0].Attributes["asksFilled"] != "2" {
				t.Fatalf("settled event = %v", settled)
			}
			if _, has := settled[0].Attributes["clearingPrice"]; has {
				t.Fatalf("a refunded epoch carries a clearing price: %v", settled[0].Attributes)
			}
			refunded := eventsOfType(ep.sp, "buyback.epoch.refunded")
			if tc.wantFailsafe {
				if len(refunded) != 1 || refunded[0].Attributes["reason"] != tc.c.wantRefundReason {
					t.Fatalf("refund event = %v, want one with reason %s", refunded, tc.c.wantRefundReason)
				}
			} else if len(refunded) != 0 {
				t.Fatalf("the older rule alone must not emit the failsafe's refund event: %v", refunded)
			}
		})
	}
}
