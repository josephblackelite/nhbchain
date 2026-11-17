package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// tryBuybackAsk applies an ask and returns the error instead of failing the
// test.
func tryBuybackAsk(t *testing.T, sp *StateProcessor, sender []byte, amount *big.Int) error {
	t.Helper()
	data, err := rlp.EncodeToBytes(buybackAskPayload{ZNHBAmount: amount})
	if err != nil {
		t.Fatalf("encode ask payload: %v", err)
	}
	senderAccount, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller account: %v", err)
	}
	return sp.applyBuybackAsk(&types.Transaction{Type: types.TxTypeBuybackAsk, Data: data}, sender, senderAccount)
}

func pendingAsks(t *testing.T, sp *StateProcessor, epoch uint64) int {
	t.Helper()
	asks, err := nhbstate.NewManager(sp.Trie).BuybackAsksForEpoch(epoch)
	if err != nil {
		t.Fatalf("load asks: %v", err)
	}
	return len(asks)
}

func znhbBalance(t *testing.T, sp *StateProcessor, addr []byte) *big.Int {
	t.Helper()
	account, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	return account.BalanceZNHB
}

// An ask of less than one whole ZNHB is refused, and nothing moves.
func TestBuybackAskBelowTheMinimumIsRefused(t *testing.T) {
	sp, _, accrualAddr, _ := newBuybackTestState(t)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(10))
	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	defer sp.EndBlock()

	dust := new(big.Int).Sub(weiZNHB(1), big.NewInt(1))
	if err := tryBuybackAsk(t, sp, sender, dust); !errors.Is(err, ErrBuybackAskTooSmall) {
		t.Fatalf("an ask of %s wei: got %v, want ErrBuybackAskTooSmall", dust, err)
	}
	if got := znhbBalance(t, sp, sender); got.Cmp(weiZNHB(10)) != 0 {
		t.Fatalf("a refused ask moved the seller's ZNHB to %s", got)
	}
	if got := znhbBalance(t, sp, accrualAddr[:]); got.Sign() != 0 {
		t.Fatalf("a refused ask reached the escrow: %s", got)
	}
	if n := pendingAsks(t, sp, 1); n != 0 {
		t.Fatalf("a refused ask was recorded: %d pending", n)
	}
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
		t.Fatalf("an ask of exactly one whole ZNHB: %v", err)
	}
}

// One address may have only so many asks pending in an epoch; the count starts
// again with the next epoch and other sellers are unaffected.
func TestBuybackAskLimitPerSellerPerEpoch(t *testing.T) {
	sp, _, accrualAddr, _ := newBuybackTestState(t)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(100))
	other, _ := seedBuybackSeller(t, sp, weiZNHB(100))

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	for i := 0; i < maxBuybackAsksPerSeller; i++ {
		if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
			t.Fatalf("ask %d of %d: %v", i+1, maxBuybackAsksPerSeller, err)
		}
	}
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); !errors.Is(err, ErrBuybackAskLimit) {
		t.Fatalf("ask over the per-seller limit: got %v, want ErrBuybackAskLimit", err)
	}
	if got := znhbBalance(t, sp, accrualAddr[:]); got.Cmp(weiZNHB(int64(maxBuybackAsksPerSeller))) != 0 {
		t.Fatalf("escrow holds %s after a refused ask, want exactly the %d accepted asks", got, maxBuybackAsksPerSeller)
	}
	if n := pendingAsks(t, sp, 1); n != maxBuybackAsksPerSeller {
		t.Fatalf("%d asks pending, want %d", n, maxBuybackAsksPerSeller)
	}
	if err := tryBuybackAsk(t, sp, other, weiZNHB(1)); err != nil {
		t.Fatalf("another seller's first ask: %v", err)
	}
	sp.EndBlock()

	// Heights 3 and 4 are the next epoch.
	sp.BeginBlock(3, time.Unix(rewardBlockTimestamp1+10, 0).UTC())
	defer sp.EndBlock()
	if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
		t.Fatalf("the seller's first ask of the next epoch: %v", err)
	}
}

// An epoch holds only so many asks in all, however many sellers make them.
func TestBuybackAskLimitPerEpoch(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	for i := 0; i < maxBuybackAsksPerEpoch; i++ {
		sender, _ := seedBuybackSeller(t, sp, weiZNHB(2))
		if err := tryBuybackAsk(t, sp, sender, weiZNHB(1)); err != nil {
			t.Fatalf("ask %d of %d: %v", i+1, maxBuybackAsksPerEpoch, err)
		}
	}
	late, _ := seedBuybackSeller(t, sp, weiZNHB(2))
	if err := tryBuybackAsk(t, sp, late, weiZNHB(1)); !errors.Is(err, ErrBuybackAskLimit) {
		t.Fatalf("ask into a full epoch: got %v, want ErrBuybackAskLimit", err)
	}
	if got := znhbBalance(t, sp, late); got.Cmp(weiZNHB(2)) != 0 {
		t.Fatalf("a refused ask moved the seller's ZNHB to %s", got)
	}
	if n := pendingAsks(t, sp, 1); n != maxBuybackAsksPerEpoch {
		t.Fatalf("%d asks pending, want %d", n, maxBuybackAsksPerEpoch)
	}
	sp.EndBlock()

	sp.BeginBlock(3, time.Unix(rewardBlockTimestamp1+10, 0).UTC())
	defer sp.EndBlock()
	if err := tryBuybackAsk(t, sp, late, weiZNHB(1)); err != nil {
		t.Fatalf("the same ask in the next epoch: %v", err)
	}
}
