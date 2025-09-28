package trade_test

import (
	"fmt"
	"math/big"
	"testing"

	"nhbchain/core/types"
	escrowpkg "nhbchain/native/escrow"
)

type testState struct {
	escrows       map[[32]byte]*escrowpkg.Escrow
	trades        map[[32]byte]*escrowpkg.Trade
	tradeByEscrow map[[32]byte][32]byte
	accounts      map[[20]byte]*types.Account
	vaultAddrs    map[string][20]byte
	vaultBalances map[string]map[[32]byte]*big.Int
}

func newTestState() *testState {
	return &testState{
		escrows:       make(map[[32]byte]*escrowpkg.Escrow),
		trades:        make(map[[32]byte]*escrowpkg.Trade),
		tradeByEscrow: make(map[[32]byte][32]byte),
		accounts:      make(map[[20]byte]*types.Account),
		vaultAddrs:    make(map[string][20]byte),
		vaultBalances: make(map[string]map[[32]byte]*big.Int),
	}
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	clone := &types.Account{Stake: big.NewInt(0)}
	if acc.BalanceNHB != nil {
		clone.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	} else {
		clone.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB != nil {
		clone.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	} else {
		clone.BalanceZNHB = big.NewInt(0)
	}
	if acc.Stake != nil {
		clone.Stake = new(big.Int).Set(acc.Stake)
	}
	return clone
}

func (s *testState) EscrowPut(e *escrowpkg.Escrow) error {
	sanitized, err := escrowpkg.SanitizeEscrow(e)
	if err != nil {
		return err
	}
	s.escrows[sanitized.ID] = sanitized.Clone()
	return nil
}

func (s *testState) EscrowGet(id [32]byte) (*escrowpkg.Escrow, bool) {
	esc, ok := s.escrows[id]
	if !ok {
		return nil, false
	}
	return esc.Clone(), true
}

func (s *testState) EscrowCredit(id [32]byte, token string, amt *big.Int) error {
	normalized, err := escrowpkg.NormalizeToken(token)
	if err != nil {
		return err
	}
	if _, ok := s.escrows[id]; !ok {
		return fmt.Errorf("escrow not found")
	}
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() == 0 {
		return nil
	}
	if _, ok := s.vaultBalances[normalized]; !ok {
		s.vaultBalances[normalized] = make(map[[32]byte]*big.Int)
	}
	current := big.NewInt(0)
	if existing, ok := s.vaultBalances[normalized][id]; ok && existing != nil {
		current = new(big.Int).Set(existing)
	}
	current.Add(current, amt)
	s.vaultBalances[normalized][id] = current
	return nil
}

func (s *testState) EscrowDebit(id [32]byte, token string, amt *big.Int) error {
	normalized, err := escrowpkg.NormalizeToken(token)
	if err != nil {
		return err
	}
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("negative debit")
	}
	current := big.NewInt(0)
	if balances, ok := s.vaultBalances[normalized]; ok {
		if existing, exists := balances[id]; exists && existing != nil {
			current = new(big.Int).Set(existing)
		}
	}
	if current.Cmp(amt) < 0 {
		return fmt.Errorf("insufficient balance")
	}
	if amt.Sign() == 0 {
		return nil
	}
	current.Sub(current, amt)
	if current.Sign() == 0 {
		delete(s.vaultBalances[normalized], id)
	} else {
		s.vaultBalances[normalized][id] = current
	}
	return nil
}

func (s *testState) EscrowBalance(id [32]byte, token string) (*big.Int, error) {
	normalized, err := escrowpkg.NormalizeToken(token)
	if err != nil {
		return nil, err
	}
	if balances, ok := s.vaultBalances[normalized]; ok {
		if existing, exists := balances[id]; exists && existing != nil {
			return new(big.Int).Set(existing), nil
		}
	}
	return big.NewInt(0), nil
}

func (s *testState) EscrowVaultAddress(token string) ([20]byte, error) {
	normalized, err := escrowpkg.NormalizeToken(token)
	if err != nil {
		return [20]byte{}, err
	}
	if addr, ok := s.vaultAddrs[normalized]; ok {
		return addr, nil
	}
	addr := newAddress(byte(len(s.vaultAddrs) + 1))
	s.vaultAddrs[normalized] = addr
	return addr, nil
}

func (s *testState) GetAccount(addr []byte) (*types.Account, error) {
	var key [20]byte
	copy(key[:], addr)
	if acc, ok := s.accounts[key]; ok {
		return cloneAccount(acc), nil
	}
	return cloneAccount(nil), nil
}

func (s *testState) PutAccount(addr []byte, account *types.Account) error {
	var key [20]byte
	copy(key[:], addr)
	s.accounts[key] = cloneAccount(account)
	return nil
}

func (s *testState) TradePut(t *escrowpkg.Trade) error {
	sanitized, err := escrowpkg.SanitizeTrade(t)
	if err != nil {
		return err
	}
	s.trades[sanitized.ID] = sanitized.Clone()
	return nil
}

func (s *testState) TradeGet(id [32]byte) (*escrowpkg.Trade, bool) {
	tr, ok := s.trades[id]
	if !ok {
		return nil, false
	}
	return tr.Clone(), true
}

func (s *testState) TradeSetStatus(id [32]byte, status escrowpkg.TradeStatus) error {
	trade, ok := s.trades[id]
	if !ok {
		return fmt.Errorf("trade not found")
	}
	clone := trade.Clone()
	clone.Status = status
	s.trades[id] = clone
	return nil
}

func (s *testState) TradeIndexEscrow(escrowID [32]byte, tradeID [32]byte) error {
	s.tradeByEscrow[escrowID] = tradeID
	return nil
}

func (s *testState) TradeLookupByEscrow(escrowID [32]byte) ([32]byte, bool, error) {
	tradeID, ok := s.tradeByEscrow[escrowID]
	return tradeID, ok, nil
}

func (s *testState) TradeRemoveByEscrow(escrowID [32]byte) error {
	delete(s.tradeByEscrow, escrowID)
	return nil
}

func (s *testState) EscrowRealmPut(*escrowpkg.EscrowRealm) error { return nil }
func (s *testState) EscrowRealmGet(string) (*escrowpkg.EscrowRealm, bool, error) {
	return nil, false, nil
}
func (s *testState) EscrowFrozenPolicyPut([32]byte, *escrowpkg.FrozenArb) error { return nil }
func (s *testState) EscrowFrozenPolicyGet([32]byte) (*escrowpkg.FrozenArb, bool, error) {
	return nil, false, nil
}
func (s *testState) ParamStoreGet(string) ([]byte, bool, error) { return nil, false, nil }

func newAddress(seed byte) [20]byte {
	var addr [20]byte
	for i := range addr {
		addr[i] = seed
	}
	return addr
}

func setupEngines(t *testing.T) (*escrowpkg.TradeEngine, *escrowpkg.Engine, *testState) {
	t.Helper()
	state := newTestState()
	esc := escrowpkg.NewEngine()
	esc.SetState(state)
	esc.SetFeeTreasury(newAddress(0xF1))
	esc.SetNowFunc(func() int64 { return 1000 })
	trade := escrowpkg.NewTradeEngine(esc)
	trade.SetState(state)
	trade.SetNowFunc(func() int64 { return 1000 })
	return trade, esc, state
}

func setAccountBalances(state *testState, buyer, seller [20]byte) {
	state.accounts[buyer] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000_000), Stake: big.NewInt(0)}
	state.accounts[seller] = &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func createFundedTrade(t *testing.T, tradeEngine *escrowpkg.TradeEngine, escEngine *escrowpkg.Engine, state *testState, slippage uint32) *escrowpkg.Trade {
	buyer := newAddress(0x10)
	seller := newAddress(0x20)
	setAccountBalances(state, buyer, seller)
	trade, err := tradeEngine.CreateTrade("offer-live", buyer, seller, "ZNHB", big.NewInt(200), "NHB", big.NewInt(100), 5000, slippage, [32]byte{0xAB})
	if err != nil {
		t.Fatalf("CreateTrade: %v", err)
	}
	if err := escEngine.Fund(trade.EscrowBase, seller); err != nil {
		t.Fatalf("fund base: %v", err)
	}
	if err := escEngine.Fund(trade.EscrowQuote, buyer); err != nil {
		t.Fatalf("fund quote: %v", err)
	}
	if err := tradeEngine.OnFundingProgress(trade.ID); err != nil {
		t.Fatalf("fund progress: %v", err)
	}
	stored, ok := state.TradeGet(trade.ID)
	if !ok {
		t.Fatalf("trade not stored")
	}
	return stored
}

func TestAutoRefundAfterIdle(t *testing.T) {
	tradeEngine, escEngine, state := setupEngines(t)
	trade := createFundedTrade(t, tradeEngine, escEngine, state, 0)
	expireAt := int64(1000 + 900)
	if err := tradeEngine.TradeTryExpire(trade.ID, expireAt); err != nil {
		t.Fatalf("TradeTryExpire: %v", err)
	}
	updated, ok := state.TradeGet(trade.ID)
	if !ok {
		t.Fatalf("trade missing")
	}
	if updated.Status != escrowpkg.TradeExpired {
		t.Fatalf("expected TradeExpired, got %v", updated.Status)
	}
	baseEscrow, _ := state.EscrowGet(trade.EscrowBase)
	quoteEscrow, _ := state.EscrowGet(trade.EscrowQuote)
	if baseEscrow.Status != escrowpkg.EscrowRefunded {
		t.Fatalf("base leg status %v", baseEscrow.Status)
	}
	if quoteEscrow.Status != escrowpkg.EscrowRefunded {
		t.Fatalf("quote leg status %v", quoteEscrow.Status)
	}
}

func TestSettleAtomicSlippageViolation(t *testing.T) {
	tradeEngine, escEngine, state := setupEngines(t)
	trade := createFundedTrade(t, tradeEngine, escEngine, state, 50)
	if err := state.EscrowDebit(trade.EscrowQuote, "ZNHB", big.NewInt(199)); err != nil {
		t.Fatalf("EscrowDebit: %v", err)
	}
	if err := tradeEngine.SettleAtomic(trade.ID); err == nil {
		t.Fatalf("expected slippage error")
	}
}

func TestPartialSettlementAdjustsEscrowAmounts(t *testing.T) {
	tradeEngine, escEngine, state := setupEngines(t)
	trade := createFundedTrade(t, tradeEngine, escEngine, state, 100)
	if err := state.EscrowDebit(trade.EscrowBase, "NHB", big.NewInt(10)); err != nil {
		t.Fatalf("debit base: %v", err)
	}
	if err := state.EscrowDebit(trade.EscrowQuote, "ZNHB", big.NewInt(20)); err != nil {
		t.Fatalf("debit quote: %v", err)
	}
	if err := tradeEngine.SettleAtomic(trade.ID); err != nil {
		t.Fatalf("settle atomic: %v", err)
	}
	stored, _ := state.TradeGet(trade.ID)
	if stored.Status != escrowpkg.TradeSettled {
		t.Fatalf("expected TradeSettled, got %v", stored.Status)
	}
	baseEscrow, _ := state.EscrowGet(trade.EscrowBase)
	if baseEscrow.Amount.Cmp(big.NewInt(90)) != 0 {
		t.Fatalf("expected base amount 90 got %s", baseEscrow.Amount)
	}
	quoteEscrow, _ := state.EscrowGet(trade.EscrowQuote)
	if quoteEscrow.Amount.Cmp(big.NewInt(180)) != 0 {
		t.Fatalf("expected quote amount 180 got %s", quoteEscrow.Amount)
	}
}
