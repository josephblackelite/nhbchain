package core

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

// PL-R5T-33: the smallest buyback ask is configurable from a height; the fixed
// minimum of one ZNHB stays the floor.

const minAskHeight = 50

func TestBuybackMinAskOffKeepsTheFixedMinimum(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1_000))
	sp.BeginBlock(1_000_000, time.Unix(rewardBlockTimestamp1, 0).UTC()) // the setting is 0
	defer sp.EndBlock()
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
		t.Fatalf("one ZNHB with the setting off: %v", err)
	}
}

func TestBuybackMinAskOnRefusesAnAskBelowItFromTheHeight(t *testing.T) {
	sp, _, accrualAddr, _ := newBuybackTestState(t)
	sp.SetBuybackMinAsk(minAskHeight, weiZNHB(100))
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1_000))

	// Below the height the old minimum applies.
	sp.BeginBlock(minAskHeight-1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
		t.Fatalf("below the height, one ZNHB: %v", err)
	}
	sp.EndBlock()

	escrowBefore := new(big.Int).Set(znhbBalance(t, sp, accrualAddr[:]))
	sp.BeginBlock(minAskHeight, time.Unix(rewardBlockTimestamp1+10, 0).UTC())
	defer sp.EndBlock()
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(99)); !errors.Is(err, ErrBuybackAskTooSmall) {
		t.Fatalf("99 ZNHB at the height: got %v, want ErrBuybackAskTooSmall", err)
	}
	if got := znhbBalance(t, sp, accrualAddr[:]); got.Cmp(escrowBefore) != 0 {
		t.Fatalf("a refused ask reached the escrow: %s", got)
	}
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(100)); err != nil {
		t.Fatalf("exactly the minimum at the height: %v", err)
	}
}

// A setting at or below the fixed minimum never lowers it.
func TestBuybackMinAskNeverLowersTheFixedMinimum(t *testing.T) {
	for _, amount := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1), big.NewInt(1), weiZNHB(1)} {
		sp, _, _, _ := newBuybackTestState(t)
		sp.SetBuybackMinAsk(minAskHeight, amount)
		if (amount == nil || amount.Sign() <= 0) && sp.buybackMinAskWei != nil {
			t.Fatalf("amount %v: the processor keeps a non-positive amount %s", amount, sp.buybackMinAskWei)
		}
		sender, _ := seedBuybackSeller(t, sp, weiZNHB(10))
		sp.BeginBlock(minAskHeight, time.Unix(rewardBlockTimestamp1, 0).UTC())
		dust := new(big.Int).Sub(weiZNHB(1), big.NewInt(1))
		if err := tryBuybackAsk(t, sp, sender, dust); !errors.Is(err, ErrBuybackAskTooSmall) {
			t.Fatalf("amount %v: an ask below one ZNHB: got %v, want ErrBuybackAskTooSmall", amount, err)
		}
		if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
			t.Fatalf("amount %v: one ZNHB: %v", amount, err)
		}
		sp.EndBlock()
	}
}

func TestBuybackMinAskZeroHeightIsOff(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	sp.SetBuybackMinAsk(0, weiZNHB(100))
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(10))
	sp.BeginBlock(1_000_000, time.Unix(rewardBlockTimestamp1, 0).UTC())
	defer sp.EndBlock()
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
		t.Fatalf("an amount with no height: %v", err)
	}
}

// The slot limits are untouched by the setting.
func TestBuybackMinAskLeavesTheSlotLimitsAlone(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	sp.SetBuybackMinAsk(minAskHeight, weiZNHB(2))
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1_000))
	sp.BeginBlock(minAskHeight, time.Unix(rewardBlockTimestamp1, 0).UTC())
	defer sp.EndBlock()
	for i := 0; i < maxBuybackAsksPerSeller; i++ {
		if err := tryBuybackAsk(t, sp, sender, weiZNHB(2)); err != nil {
			t.Fatalf("ask %d: %v", i+1, err)
		}
	}
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(2)); !errors.Is(err, ErrBuybackAskLimit) {
		t.Fatalf("over the per-seller limit: got %v, want ErrBuybackAskLimit", err)
	}
}

func TestBuybackMinAskIsCarriedByCopyAndOwnsItsAmount(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	amount := weiZNHB(5)
	sp.SetBuybackMinAsk(77, amount)
	amount.SetInt64(1)
	dup, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.buybackMinAskFromHeight != 77 || dup.buybackMinAskWei == nil || dup.buybackMinAskWei.Cmp(weiZNHB(5)) != 0 {
		t.Fatalf("Copy dropped the setting or the setter shared the caller's amount: %d %v", dup.buybackMinAskFromHeight, dup.buybackMinAskWei)
	}
	dup.buybackMinAskWei.SetInt64(1)
	if sp.buybackMinAskWei.Cmp(weiZNHB(5)) != 0 {
		t.Fatalf("the copy shares the amount with the original")
	}
}

func TestNodeBuybackMinAsk_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetBuybackMinAsk(9, weiZNHB(7))
	node.stateMu.Lock()
	gotH, gotW := node.state.buybackMinAskFromHeight, node.state.buybackMinAskWei
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH, rebuiltW := node.state.buybackMinAskFromHeight, node.state.buybackMinAskWei
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || gotW == nil || gotW.Cmp(weiZNHB(7)) != 0 {
		t.Fatalf("setter reached the processor as %d %v", gotH, gotW)
	}
	if rebuiltH != 9 || rebuiltW == nil || rebuiltW.Cmp(weiZNHB(7)) != 0 {
		t.Fatalf("a rebuilt processor lost the setting: %d %v", rebuiltH, rebuiltW)
	}
}
