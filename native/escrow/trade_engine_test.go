package escrow

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
)

func setupTradeEnvironment(t *testing.T) (*TradeEngine, *Engine, *mockState, *capturingEmitter) {
	t.Helper()
	state := newMockState()
	escEngine := NewEngine()
	escEngine.SetState(state)
	escEngine.SetFeeTreasury(newTestAddress(0xFE))
	emitter := &capturingEmitter{}
	escEngine.SetEmitter(emitter)
	tradeEngine := NewTradeEngine(escEngine)
	tradeEngine.SetState(state)
	tradeEngine.SetEmitter(emitter)
	tradeEngine.SetNowFunc(func() int64 { return 1000 })
	escEngine.SetNowFunc(func() int64 { return 1000 })
	return tradeEngine, escEngine, state, emitter
}

func ensureBalances(state *mockState, buyer, seller [20]byte) {
	state.setAccount(seller, &types.Account{BalanceNHB: big.NewInt(1000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	state.setAccount(buyer, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1000), Stake: big.NewInt(0)})
}

func eventSeen(emitter *capturingEmitter, eventType string) bool {
	if emitter == nil {
		return false
	}
	for _, evt := range emitter.events {
		if evt.EventType() == eventType {
			return true
		}
	}
	return false
}

func TestTradeCreateAndFundingProgress(t *testing.T) {
	tradeEngine, escEngine, state, emitter := setupTradeEnvironment(t)
	buyer := newTestAddress(0x01)
	seller := newTestAddress(0x02)
	ensureBalances(state, buyer, seller)
	nonce := [32]byte{0xAA}
        trade, err := tradeEngine.CreateTrade("offer-1", buyer, seller, "ZNHB", big.NewInt(200), "NHB", big.NewInt(300), 2000, 0, nonce)
	if err != nil {
		t.Fatalf("CreateTrade error: %v", err)
	}
	if trade.Status != TradeInit {
		t.Fatalf("expected TradeInit, got %v", trade.Status)
	}
	if !eventSeen(emitter, EventTypeTradeCreated) {
		t.Fatalf("expected trade created event")
	}
	if _, ok := state.TradeGet(trade.ID); !ok {
		t.Fatalf("trade not stored")
	}
	if err := escEngine.Fund(trade.EscrowBase, seller); err != nil {
		t.Fatalf("fund base leg: %v", err)
	}
	if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
		t.Fatalf("on funding progress: %v", err)
	}
	stored, ok := state.TradeGet(trade.ID)
	if !ok || stored.Status != TradePartialFunded {
		t.Fatalf("expected partial funded status, got %v", stored)
	}
	if !eventSeen(emitter, EventTypeTradePartialFunded) {
		t.Fatalf("expected partial funded event")
	}
	if err := escEngine.Fund(trade.EscrowQuote, buyer); err != nil {
		t.Fatalf("fund quote leg: %v", err)
	}
	if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
		t.Fatalf("on funding progress: %v", err)
	}
	stored, _ = state.TradeGet(trade.ID)
	if stored.Status != TradeFunded {
		t.Fatalf("expected funded status, got %v", stored.Status)
	}
	if !eventSeen(emitter, EventTypeTradeFunded) {
		t.Fatalf("expected trade funded event")
	}
}

func TestTradeAtomicSettlement(t *testing.T) {
	tradeEngine, escEngine, state, emitter := setupTradeEnvironment(t)
	buyer := newTestAddress(0x11)
	seller := newTestAddress(0x22)
	ensureBalances(state, buyer, seller)
	nonce := [32]byte{0xBB}
        trade, err := tradeEngine.CreateTrade("offer-2", buyer, seller, "ZNHB", big.NewInt(100), "NHB", big.NewInt(150), 5000, 0, nonce)
	if err != nil {
		t.Fatalf("CreateTrade error: %v", err)
	}
	if err := escEngine.Fund(trade.EscrowBase, seller); err != nil {
		t.Fatalf("fund base leg: %v", err)
	}
	if err := escEngine.Fund(trade.EscrowQuote, buyer); err != nil {
		t.Fatalf("fund quote leg: %v", err)
	}
	if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
		t.Fatalf("funding progress: %v", err)
	}
	if err := tradeEngine.SettleAtomic(trade.ID); err != nil {
		t.Fatalf("settle atomic: %v", err)
	}
	stored, ok := state.TradeGet(trade.ID)
	if !ok || stored.Status != TradeSettled {
		t.Fatalf("expected TradeSettled, got %#v", stored)
	}
	baseEscrow, _ := state.EscrowGet(trade.EscrowBase)
	if baseEscrow.Status != EscrowReleased {
		t.Fatalf("expected base escrow released, got %v", baseEscrow.Status)
	}
	quoteEscrow, _ := state.EscrowGet(trade.EscrowQuote)
	if quoteEscrow.Status != EscrowReleased {
		t.Fatalf("expected quote escrow released, got %v", quoteEscrow.Status)
	}
	buyerAcc := state.account(buyer)
	if buyerAcc.BalanceNHB.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("buyer NHB mismatch, got %s", buyerAcc.BalanceNHB)
	}
	if buyerAcc.BalanceZNHB.Cmp(big.NewInt(900)) != 0 {
		t.Fatalf("buyer ZNHB mismatch, got %s", buyerAcc.BalanceZNHB)
	}
	sellerAcc := state.account(seller)
	if sellerAcc.BalanceNHB.Cmp(big.NewInt(850)) != 0 {
		t.Fatalf("seller NHB mismatch, got %s", sellerAcc.BalanceNHB)
	}
	if sellerAcc.BalanceZNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("seller ZNHB mismatch, got %s", sellerAcc.BalanceZNHB)
	}
	foundSettled := false
	for _, evt := range emitter.events {
		if evt.EventType() == EventTypeTradeSettled {
			foundSettled = true
			break
		}
	}
	if !foundSettled {
		t.Fatalf("expected trade settled event")
	}
}

func TestTradeDisputeAndResolve(t *testing.T) {
	outcomes := []struct {
		name       string
		outcome    string
		buyerNHB   int64
		buyerZNHB  int64
		sellerNHB  int64
		sellerZNHB int64
	}{
		{"release_both", "release_both", 150, 900, 850, 100},
		{"refund_both", "refund_both", 0, 1000, 1000, 0},
		{"release_base_refund_quote", "release_base_refund_quote", 150, 1000, 850, 0},
		{"release_quote_refund_base", "release_quote_refund_base", 0, 900, 1000, 100},
	}
	for _, tc := range outcomes {
		t.Run(tc.name, func(t *testing.T) {
			tradeEngine, escEngine, state, emitter := setupTradeEnvironment(t)
			buyer := newTestAddress(0x31)
			seller := newTestAddress(0x41)
			ensureBalances(state, buyer, seller)
                        trade, err := tradeEngine.CreateTrade("offer-dispute", buyer, seller, "ZNHB", big.NewInt(100), "NHB", big.NewInt(150), 4000, 0, [32]byte{0xCC})
			if err != nil {
				t.Fatalf("CreateTrade error: %v", err)
			}
			if err := escEngine.Fund(trade.EscrowBase, seller); err != nil {
				t.Fatalf("fund base: %v", err)
			}
			if err := escEngine.Fund(trade.EscrowQuote, buyer); err != nil {
				t.Fatalf("fund quote: %v", err)
			}
			if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
				t.Fatalf("funding progress: %v", err)
			}
			if err := tradeEngine.TradeDispute(trade.ID, buyer); err != nil {
				t.Fatalf("trade dispute: %v", err)
			}
			if err := tradeEngine.TradeResolve(trade.ID, tc.outcome); err != nil {
				t.Fatalf("trade resolve: %v", err)
			}
			stored, ok := state.TradeGet(trade.ID)
			if !ok || stored.Status != TradeSettled {
				t.Fatalf("expected TradeSettled, got %#v", stored)
			}
			buyerAcc := state.account(buyer)
			if buyerAcc.BalanceNHB.Cmp(big.NewInt(tc.buyerNHB)) != 0 {
				t.Fatalf("buyer NHB expected %d got %s", tc.buyerNHB, buyerAcc.BalanceNHB)
			}
			if buyerAcc.BalanceZNHB.Cmp(big.NewInt(tc.buyerZNHB)) != 0 {
				t.Fatalf("buyer ZNHB expected %d got %s", tc.buyerZNHB, buyerAcc.BalanceZNHB)
			}
			sellerAcc := state.account(seller)
			if sellerAcc.BalanceNHB.Cmp(big.NewInt(tc.sellerNHB)) != 0 {
				t.Fatalf("seller NHB expected %d got %s", tc.sellerNHB, sellerAcc.BalanceNHB)
			}
			if sellerAcc.BalanceZNHB.Cmp(big.NewInt(tc.sellerZNHB)) != 0 {
				t.Fatalf("seller ZNHB expected %d got %s", tc.sellerZNHB, sellerAcc.BalanceZNHB)
			}
			foundResolved := false
			for _, evt := range emitter.events {
				if evt.EventType() == EventTypeTradeResolved {
					foundResolved = true
					break
				}
			}
			if !foundResolved {
				t.Fatalf("expected trade resolved event")
			}
		})
	}
}

func TestTradeTryExpire(t *testing.T) {
	t.Run("base leg funded", func(t *testing.T) {
		tradeEngine, escEngine, state, emitter := setupTradeEnvironment(t)
		buyer := newTestAddress(0x51)
		seller := newTestAddress(0x61)
		ensureBalances(state, buyer, seller)
            trade, err := tradeEngine.CreateTrade("offer-expire-base", buyer, seller, "ZNHB", big.NewInt(100), "NHB", big.NewInt(150), 1200, 0, [32]byte{0xDD})
		if err != nil {
			t.Fatalf("CreateTrade error: %v", err)
		}
		if err := escEngine.Fund(trade.EscrowBase, seller); err != nil {
			t.Fatalf("fund base: %v", err)
		}
		if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
			t.Fatalf("funding progress: %v", err)
		}
		if err := tradeEngine.TradeTryExpire(trade.ID, 1300); err != nil {
			t.Fatalf("TradeTryExpire error: %v", err)
		}
		stored, ok := state.TradeGet(trade.ID)
		if !ok || stored.Status != TradeExpired {
			t.Fatalf("expected TradeExpired, got %#v", stored)
		}
		baseEscrow, _ := state.EscrowGet(trade.EscrowBase)
		if baseEscrow.Status != EscrowRefunded {
			t.Fatalf("expected base escrow refunded, got %v", baseEscrow.Status)
		}
		sellerAcc := state.account(seller)
		if sellerAcc.BalanceNHB.Cmp(big.NewInt(1000)) != 0 {
			t.Fatalf("seller NHB expected refund, got %s", sellerAcc.BalanceNHB)
		}
		foundExpired := false
		for _, evt := range emitter.events {
			if evt.EventType() == EventTypeTradeExpired {
				foundExpired = true
				break
			}
		}
		if !foundExpired {
			t.Fatalf("expected trade expired event")
		}
	})

	t.Run("quote leg funded", func(t *testing.T) {
		tradeEngine, escEngine, state, _ := setupTradeEnvironment(t)
		buyer := newTestAddress(0x52)
		seller := newTestAddress(0x62)
		ensureBalances(state, buyer, seller)
            trade, err := tradeEngine.CreateTrade("offer-expire-quote", buyer, seller, "ZNHB", big.NewInt(100), "NHB", big.NewInt(150), 1100, 0, [32]byte{0xDE})
		if err != nil {
			t.Fatalf("CreateTrade error: %v", err)
		}
		if err := escEngine.Fund(trade.EscrowQuote, buyer); err != nil {
			t.Fatalf("fund quote: %v", err)
		}
		if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
			t.Fatalf("funding progress: %v", err)
		}
		if err := tradeEngine.TradeTryExpire(trade.ID, 1200); err != nil {
			t.Fatalf("TradeTryExpire error: %v", err)
		}
		stored, ok := state.TradeGet(trade.ID)
		if !ok || stored.Status != TradeExpired {
			t.Fatalf("expected TradeExpired, got %#v", stored)
		}
		quoteEscrow, _ := state.EscrowGet(trade.EscrowQuote)
		if quoteEscrow.Status != EscrowRefunded {
			t.Fatalf("expected quote escrow refunded, got %v", quoteEscrow.Status)
		}
		buyerAcc := state.account(buyer)
		if buyerAcc.BalanceZNHB.Cmp(big.NewInt(1000)) != 0 {
			t.Fatalf("buyer ZNHB expected refund, got %s", buyerAcc.BalanceZNHB)
		}
	})

	t.Run("no funds", func(t *testing.T) {
		tradeEngine, _, state, _ := setupTradeEnvironment(t)
		buyer := newTestAddress(0x53)
		seller := newTestAddress(0x63)
		ensureBalances(state, buyer, seller)
            trade, err := tradeEngine.CreateTrade("offer-expire-none", buyer, seller, "ZNHB", big.NewInt(100), "NHB", big.NewInt(150), 1000, 0, [32]byte{0xDF})
		if err != nil {
			t.Fatalf("CreateTrade error: %v", err)
		}
		if err := tradeEngine.TradeTryExpire(trade.ID, 1500); err != nil {
			t.Fatalf("TradeTryExpire error: %v", err)
		}
		stored, ok := state.TradeGet(trade.ID)
		if !ok || stored.Status != TradeCancelled {
			t.Fatalf("expected TradeCancelled, got %#v", stored)
		}
	})
}
