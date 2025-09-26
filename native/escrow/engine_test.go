package escrow

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

type mockState struct {
	escrows       map[[32]byte]*Escrow
	accounts      map[[20]byte]*types.Account
	vaultBalances map[string]map[[32]byte]*big.Int
	vaultAddrs    map[string][20]byte
	trades        map[[32]byte]*Trade
	tradeByEscrow map[[32]byte][32]byte
	realms        map[string]*EscrowRealm
	frozen        map[[32]byte]*FrozenArb
	params        map[string][]byte
}

func newMockState() *mockState {
	return &mockState{
		escrows:       make(map[[32]byte]*Escrow),
		accounts:      make(map[[20]byte]*types.Account),
		vaultBalances: make(map[string]map[[32]byte]*big.Int),
		trades:        make(map[[32]byte]*Trade),
		tradeByEscrow: make(map[[32]byte][32]byte),
		vaultAddrs: map[string][20]byte{
			"NHB":  newTestAddress(0xAA),
			"ZNHB": newTestAddress(0xBB),
		},
		realms: make(map[string]*EscrowRealm),
		frozen: make(map[[32]byte]*FrozenArb),
		params: make(map[string][]byte),
	}
}

func newTestAddress(fill byte) [20]byte {
	var addr [20]byte
	copy(addr[:], bytes.Repeat([]byte{fill}, 20))
	return addr
}

func cloneAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	clone := &types.Account{
		Nonce:           acc.Nonce,
		BalanceNHB:      big.NewInt(0),
		BalanceZNHB:     big.NewInt(0),
		Stake:           big.NewInt(0),
		Username:        acc.Username,
		EngagementScore: acc.EngagementScore,
		CodeHash:        append([]byte(nil), acc.CodeHash...),
		StorageRoot:     append([]byte(nil), acc.StorageRoot...),
	}
	if acc.BalanceNHB != nil {
		clone.BalanceNHB = new(big.Int).Set(acc.BalanceNHB)
	}
	if acc.BalanceZNHB != nil {
		clone.BalanceZNHB = new(big.Int).Set(acc.BalanceZNHB)
	}
	if acc.Stake != nil {
		clone.Stake = new(big.Int).Set(acc.Stake)
	}
	return clone
}

func (m *mockState) EscrowPut(e *Escrow) error {
	if e == nil {
		return fmt.Errorf("nil escrow")
	}
	sanitized, err := SanitizeEscrow(e)
	if err != nil {
		return err
	}
	m.escrows[sanitized.ID] = sanitized.Clone()
	return nil
}

func (m *mockState) EscrowGet(id [32]byte) (*Escrow, bool) {
	esc, ok := m.escrows[id]
	if !ok {
		return nil, false
	}
	return esc.Clone(), true
}

func (m *mockState) EscrowRealmPut(realm *EscrowRealm) error {
	sanitized, err := SanitizeEscrowRealm(realm)
	if err != nil {
		return err
	}
	m.realms[strings.TrimSpace(sanitized.ID)] = sanitized.Clone()
	return nil
}

func (m *mockState) EscrowRealmGet(id string) (*EscrowRealm, bool, error) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, false, fmt.Errorf("realm id required")
	}
	realm, ok := m.realms[trimmed]
	if !ok {
		return nil, false, nil
	}
	return realm.Clone(), true, nil
}

func (m *mockState) EscrowFrozenPolicyPut(id [32]byte, policy *FrozenArb) error {
	sanitized, err := SanitizeFrozenArb(policy)
	if err != nil {
		return err
	}
	m.frozen[id] = sanitized.Clone()
	return nil
}

func (m *mockState) EscrowFrozenPolicyGet(id [32]byte) (*FrozenArb, bool, error) {
	policy, ok := m.frozen[id]
	if !ok {
		return nil, false, nil
	}
	return policy.Clone(), true, nil
}

func (m *mockState) ParamStoreGet(name string) ([]byte, bool, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, false, fmt.Errorf("params key must not be empty")
	}
	val, ok := m.params[trimmed]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), val...), true, nil
}

func (m *mockState) EscrowCredit(id [32]byte, token string, amt *big.Int) error {
	normalized, err := NormalizeToken(token)
	if err != nil {
		return err
	}
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("negative credit")
	}
	if _, ok := m.escrows[id]; !ok {
		return fmt.Errorf("escrow not found")
	}
	if amt.Sign() == 0 {
		return nil
	}
	if _, ok := m.vaultBalances[normalized]; !ok {
		m.vaultBalances[normalized] = make(map[[32]byte]*big.Int)
	}
	current := big.NewInt(0)
	if existing, ok := m.vaultBalances[normalized][id]; ok && existing != nil {
		current = new(big.Int).Set(existing)
	}
	current.Add(current, amt)
	m.vaultBalances[normalized][id] = current
	return nil
}

func (m *mockState) EscrowDebit(id [32]byte, token string, amt *big.Int) error {
	normalized, err := NormalizeToken(token)
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
	if balances, ok := m.vaultBalances[normalized]; ok {
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
		delete(m.vaultBalances[normalized], id)
	} else {
		if _, ok := m.vaultBalances[normalized]; !ok {
			m.vaultBalances[normalized] = make(map[[32]byte]*big.Int)
		}
		m.vaultBalances[normalized][id] = current
	}
	return nil
}

func (m *mockState) EscrowVaultAddress(token string) ([20]byte, error) {
	normalized, err := NormalizeToken(token)
	if err != nil {
		return [20]byte{}, err
	}
	if addr, ok := m.vaultAddrs[normalized]; ok {
		return addr, nil
	}
	addr := newTestAddress(byte(len(m.vaultAddrs) + 1))
	m.vaultAddrs[normalized] = addr
	return addr, nil
}

func (m *mockState) GetAccount(addr []byte) (*types.Account, error) {
	var key [20]byte
	copy(key[:], addr)
	if acc, ok := m.accounts[key]; ok {
		return cloneAccount(acc), nil
	}
	return cloneAccount(nil), nil
}

func (m *mockState) PutAccount(addr []byte, account *types.Account) error {
	var key [20]byte
	copy(key[:], addr)
	m.accounts[key] = cloneAccount(account)
	return nil
}

func (m *mockState) setAccount(addr [20]byte, acc *types.Account) {
	m.accounts[addr] = cloneAccount(acc)
}

func (m *mockState) account(addr [20]byte) *types.Account {
	if acc, ok := m.accounts[addr]; ok {
		return cloneAccount(acc)
	}
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
}

func (m *mockState) TradePut(t *Trade) error {
	if t == nil {
		return fmt.Errorf("nil trade")
	}
	sanitized, err := SanitizeTrade(t)
	if err != nil {
		return err
	}
	m.trades[sanitized.ID] = sanitized.Clone()
	return nil
}

func (m *mockState) TradeGet(id [32]byte) (*Trade, bool) {
	tr, ok := m.trades[id]
	if !ok {
		return nil, false
	}
	return tr.Clone(), true
}

func (m *mockState) TradeSetStatus(id [32]byte, status TradeStatus) error {
	if !status.Valid() {
		return fmt.Errorf("invalid status")
	}
	trade, ok := m.trades[id]
	if !ok {
		return fmt.Errorf("trade not found")
	}
	if trade.Status == status {
		return nil
	}
	clone := trade.Clone()
	clone.Status = status
	m.trades[id] = clone
	return nil
}

func (m *mockState) TradeIndexEscrow(escrowID [32]byte, tradeID [32]byte) error {
	m.tradeByEscrow[escrowID] = tradeID
	return nil
}

func (m *mockState) TradeLookupByEscrow(escrowID [32]byte) ([32]byte, bool, error) {
	tradeID, ok := m.tradeByEscrow[escrowID]
	return tradeID, ok, nil
}

func (m *mockState) TradeRemoveByEscrow(escrowID [32]byte) error {
	delete(m.tradeByEscrow, escrowID)
	return nil
}

type capturingEmitter struct {
	events []events.Event
}

func (c *capturingEmitter) Emit(evt events.Event) {
	c.events = append(c.events, evt)
}

func (c *capturingEmitter) typesEvents() []*types.Event {
	out := make([]*types.Event, 0, len(c.events))
	for _, evt := range c.events {
		if wrapper, ok := evt.(escrowEvent); ok && wrapper.evt != nil {
			clone := &types.Event{Type: wrapper.evt.Type, Attributes: map[string]string{}}
			keys := make([]string, 0, len(wrapper.evt.Attributes))
			for k := range wrapper.evt.Attributes {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				clone.Attributes[k] = wrapper.evt.Attributes[k]
			}
			out = append(out, clone)
		}
	}
	return out
}

func newTestEngine(state *mockState) *Engine {
	engine := NewEngine()
	engine.SetState(state)
	engine.SetFeeTreasury(newTestAddress(0xCC))
	engine.SetNowFunc(func() int64 { return 1_700_000_000 })
	return engine
}

func TestCreateValidations(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x01)
	payee := newTestAddress(0x02)
	meta := [32]byte{}
	meta[0] = 0xFF

	cases := []struct {
		name     string
		token    string
		amount   *big.Int
		fee      uint32
		deadline int64
		wantErr  bool
	}{
		{"ok", "NHB", big.NewInt(100), 100, 1_700_000_500, false},
		{"invalid token", "DOGE", big.NewInt(100), 0, 1_700_000_500, true},
		{"zero amount", "NHB", big.NewInt(0), 0, 1_700_000_500, true},
		{"fee too high", "ZNHB", big.NewInt(100), 10_001, 1_700_000_500, true},
		{"deadline before now", "NHB", big.NewInt(100), 0, 1_600_000_000, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.Create(payer, payee, tc.token, tc.amount, tc.fee, tc.deadline, nil, meta, "")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCreateIsIdempotent(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x10)
	payee := newTestAddress(0x11)
	meta := [32]byte{}
	meta[0] = 0x01

	first, err := engine.Create(payer, payee, "NHB", big.NewInt(500), 50, 1_700_000_500, nil, meta, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := engine.Create(payer, payee, "nhb", big.NewInt(500), 50, 1_700_000_500, nil, meta, "")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected same escrow id on idempotent create")
	}
	if second.Token != "NHB" {
		t.Fatalf("expected token normalized")
	}
}

func TestCreateWithRealmFreezesPolicy(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	arbitrator := newTestAddress(0x99)
	baseRealm := &EscrowRealm{
		ID:              "core",
		Version:         1,
		NextPolicyNonce: 1,
		CreatedAt:       1_699_999_000,
		UpdatedAt:       1_699_999_000,
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeSingle,
			Threshold: 1,
			Members:   [][20]byte{arbitrator},
		},
	}
	if err := state.EscrowRealmPut(baseRealm); err != nil {
		t.Fatalf("put realm: %v", err)
	}
	payer := newTestAddress(0x31)
	payee := newTestAddress(0x32)
	meta := [32]byte{0xAB}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(200), 0, 1_700_000_800, nil, meta, "core")
	if err != nil {
		t.Fatalf("create with realm: %v", err)
	}
	if esc.RealmID != "core" {
		t.Fatalf("expected realm id preserved, got %q", esc.RealmID)
	}
	if esc.FrozenArb == nil {
		t.Fatalf("expected frozen policy on escrow")
	}
	if esc.FrozenArb.PolicyNonce != 1 {
		t.Fatalf("expected frozen policy nonce 1, got %d", esc.FrozenArb.PolicyNonce)
	}
	if esc.FrozenArb.Scheme != ArbitrationSchemeSingle {
		t.Fatalf("unexpected scheme: %d", esc.FrozenArb.Scheme)
	}
	if esc.FrozenArb.Threshold != 1 {
		t.Fatalf("unexpected threshold: %d", esc.FrozenArb.Threshold)
	}
	if len(esc.FrozenArb.Members) != 1 || esc.FrozenArb.Members[0] != arbitrator {
		t.Fatalf("unexpected frozen members: %+v", esc.FrozenArb.Members)
	}
	storedRealm, ok, err := state.EscrowRealmGet("core")
	if err != nil {
		t.Fatalf("realm get: %v", err)
	}
	if !ok {
		t.Fatalf("realm not found")
	}
	if storedRealm.NextPolicyNonce != 2 {
		t.Fatalf("expected nonce incremented to 2, got %d", storedRealm.NextPolicyNonce)
	}
	policy, ok, err := state.EscrowFrozenPolicyGet(esc.ID)
	if err != nil {
		t.Fatalf("frozen get: %v", err)
	}
	if !ok {
		t.Fatalf("expected frozen policy persisted")
	}
	if policy.PolicyNonce != esc.FrozenArb.PolicyNonce {
		t.Fatalf("policy nonce mismatch: got %d want %d", policy.PolicyNonce, esc.FrozenArb.PolicyNonce)
	}
}

func TestCreateWithUnknownRealmFails(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x41)
	payee := newTestAddress(0x42)
	meta := [32]byte{0xCC}
	if _, err := engine.Create(payer, payee, "NHB", big.NewInt(150), 0, 1_700_000_900, nil, meta, "missing"); err == nil || !errors.Is(err, errRealmNotFound) {
		t.Fatalf("expected realm not found error, got %v", err)
	}
}

func TestRealmLifecycleEvents(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	engine.SetNowFunc(func() int64 { return 1_700_000_500 })
	emitter := &capturingEmitter{}
	engine.SetEmitter(emitter)
	realmInput := &EscrowRealm{
		ID: "alpha",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{newTestAddress(0xA1), newTestAddress(0xA2), newTestAddress(0xA3)},
		},
	}
	created, err := engine.CreateRealm(realmInput)
	if err != nil {
		t.Fatalf("CreateRealm: %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("expected version 1, got %d", created.Version)
	}
	if created.NextPolicyNonce != 1 {
		t.Fatalf("expected nonce 1, got %d", created.NextPolicyNonce)
	}
	events := emitter.typesEvents()
	if len(events) == 0 || events[len(events)-1].Type != EventTypeRealmCreated {
		t.Fatalf("expected realm created event, events=%v", events)
	}
	update := &EscrowRealm{
		ID: "alpha",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 3,
			Members:   [][20]byte{newTestAddress(0xA1), newTestAddress(0xA2), newTestAddress(0xA3), newTestAddress(0xA4)},
		},
	}
	updated, err := engine.UpdateRealm(update)
	if err != nil {
		t.Fatalf("UpdateRealm: %v", err)
	}
	if updated.Version != 2 {
		t.Fatalf("expected version 2, got %d", updated.Version)
	}
	if updated.NextPolicyNonce != created.NextPolicyNonce {
		t.Fatalf("expected nonce unchanged, got %d", updated.NextPolicyNonce)
	}
	events = emitter.typesEvents()
	if len(events) == 0 || events[len(events)-1].Type != EventTypeRealmUpdated {
		t.Fatalf("expected realm updated event, events=%v", events)
	}
	stored, ok, err := state.EscrowRealmGet("alpha")
	if err != nil {
		t.Fatalf("realm get: %v", err)
	}
	if !ok {
		t.Fatalf("expected realm stored")
	}
	if stored.Version != 2 {
		t.Fatalf("stored version mismatch: %d", stored.Version)
	}
	if stored.NextPolicyNonce != updated.NextPolicyNonce {
		t.Fatalf("stored nonce mismatch: %d", stored.NextPolicyNonce)
	}
}

func TestCreateRealmRespectsBounds(t *testing.T) {
	state := newMockState()
	state.params[ParamKeyRealmMinThreshold] = []byte("2")
	engine := newTestEngine(state)
	_, err := engine.CreateRealm(&EscrowRealm{
		ID: "beta",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeSingle,
			Threshold: 1,
			Members:   [][20]byte{newTestAddress(0xB1), newTestAddress(0xB2)},
		},
	})
	if err == nil {
		t.Fatalf("expected error due to threshold below governance minimum")
	}
}

func TestFundTransfersToVaultAndIsIdempotent(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x21)
	payee := newTestAddress(0x22)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(300), 0, 1_700_001_000, nil, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	vault, _ := state.EscrowVaultAddress("NHB")

	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund #1: %v", err)
	}
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund #2: %v", err)
	}

	payerAcc := state.account(payer)
	if got := payerAcc.BalanceNHB.String(); got != "700" {
		t.Fatalf("unexpected payer balance: %s", got)
	}
	vaultAcc := state.account(vault)
	if got := vaultAcc.BalanceNHB.String(); got != "300" {
		t.Fatalf("unexpected vault balance: %s", got)
	}
	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowFunded {
		t.Fatalf("expected funded status, got %d", stored.Status)
	}
}

func TestFundRejectsWrongCaller(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x31)
	payee := newTestAddress(0x32)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(100), 0, 1_700_001_000, nil, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(200), Stake: big.NewInt(0)})

	if err := engine.Fund(esc.ID, payee); err == nil {
		t.Fatalf("expected unauthorized error")
	}
}

func TestReleaseDistributesFees(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x41)
	payee := newTestAddress(0x42)
	mediator := newTestAddress(0x43)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(1_000), 250, 1_700_002_000, &mediator, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(5_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}

	emitter := &capturingEmitter{}
	engine.SetEmitter(emitter)
	if err := engine.Release(esc.ID, payee); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := engine.Release(esc.ID, mediator); err != nil { // idempotent
		t.Fatalf("release idempotent: %v", err)
	}

	payeeAcc := state.account(payee)
	if got := payeeAcc.BalanceNHB.String(); got != "975" {
		t.Fatalf("expected payee 975, got %s", got)
	}
	treasury := engine.feeTreasury
	treasuryAcc := state.account(treasury)
	if got := treasuryAcc.BalanceNHB.String(); got != "25" {
		t.Fatalf("expected treasury 25, got %s", got)
	}

	events := emitter.typesEvents()
	if len(events) == 0 {
		t.Fatalf("expected events emitted")
	}
	foundRelease := false
	for _, evt := range events {
		if evt.Type == EventTypeEscrowReleased {
			foundRelease = true
		}
	}
	if !foundRelease {
		t.Fatalf("expected release event in %v", events)
	}
}

func TestReleaseHandlesFeeEdgeCases(t *testing.T) {
	cases := []struct {
		name         string
		fee          uint32
		wantPayee    string
		wantTreasury string
	}{
		{"zero", 0, "1000", "0"},
		{"full", 10_000, "0", "1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := newMockState()
			engine := newTestEngine(state)
			payer := newTestAddress(0x51)
			payee := newTestAddress(0x52)
			meta := [32]byte{}
			esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(1_000), tc.fee, 1_700_003_000, nil, meta, "")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			state.setAccount(payer, &types.Account{BalanceZNHB: big.NewInt(2_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
			if err := engine.Fund(esc.ID, payer); err != nil {
				t.Fatalf("fund: %v", err)
			}
			if err := engine.Release(esc.ID, payee); err != nil {
				t.Fatalf("release: %v", err)
			}
			payeeAcc := state.account(payee)
			if got := payeeAcc.BalanceZNHB.String(); got != tc.wantPayee {
				t.Fatalf("expected payee %s, got %s", tc.wantPayee, got)
			}
			treasuryAcc := state.account(engine.feeTreasury)
			if got := treasuryAcc.BalanceZNHB.String(); got != tc.wantTreasury {
				t.Fatalf("expected treasury %s, got %s", tc.wantTreasury, got)
			}
		})
	}
}

func TestRefundHonorsDeadlineAndCaller(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x61)
	payee := newTestAddress(0x62)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(400), 0, 1_700_000_500, nil, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(1_000)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}

	if err := engine.Refund(esc.ID, payee); err == nil {
		t.Fatalf("expected unauthorized refund")
	}
	if err := engine.Refund(esc.ID, payer); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if err := engine.Refund(esc.ID, payer); err != nil {
		t.Fatalf("refund idempotent: %v", err)
	}
	payerAcc := state.account(payer)
	if got := payerAcc.BalanceNHB.String(); got != "1000" {
		t.Fatalf("expected payer restored balance, got %s", got)
	}
}

func TestRefundAfterDeadlineFails(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x71)
	payee := newTestAddress(0x72)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(100), 0, 1_600_000_000, nil, meta, "")
	if err == nil {
		t.Fatalf("expected create error for deadline before now")
	}
	engine.SetNowFunc(func() int64 { return 1_600_000_000 })
	esc, err = engine.Create(payer, payee, "NHB", big.NewInt(100), 0, 1_600_000_500, nil, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(200)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	engine.SetNowFunc(func() int64 { return 1_600_000_600 })
	if err := engine.Refund(esc.ID, payer); err == nil {
		t.Fatalf("expected refund after deadline to fail")
	}
}

func TestExpireRefundsAfterDeadline(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x81)
	payee := newTestAddress(0x82)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(200), 0, 1_700_000_500, nil, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceZNHB: big.NewInt(500)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Expire(esc.ID, 1_700_000_400); err == nil {
		t.Fatalf("expected deadline not reached")
	}
	if err := engine.Expire(esc.ID, 1_700_000_600); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := engine.Expire(esc.ID, 1_700_000_700); err != nil {
		t.Fatalf("expire idempotent: %v", err)
	}
	payerAcc := state.account(payer)
	if got := payerAcc.BalanceZNHB.String(); got != "500" {
		t.Fatalf("expected payer refunded, got %s", got)
	}
	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowExpired {
		t.Fatalf("expected expired status, got %d", stored.Status)
	}
}

func TestDisputeAndResolveFlow(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x91)
	payee := newTestAddress(0x92)
	mediator := newTestAddress(0x93)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(600), 0, 1_700_001_000, &mediator, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(1_000)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payee); err != nil {
		t.Fatalf("dispute by payee: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer); err != nil {
		t.Fatalf("dispute idempotent: %v", err)
	}
	if err := engine.Resolve(esc.ID, mediator, "refund"); err != nil {
		t.Fatalf("resolve refund: %v", err)
	}
	payerAcc := state.account(payer)
	if got := payerAcc.BalanceNHB.String(); got != "1000" {
		t.Fatalf("expected payer refunded, got %s", got)
	}
	if err := engine.Resolve(esc.ID, mediator, "refund"); err != nil {
		t.Fatalf("resolve idempotent: %v", err)
	}
}

func TestResolveReleaseOutcome(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0xA1)
	payee := newTestAddress(0xA2)
	mediator := newTestAddress(0xA3)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(300), 500, 1_700_001_500, &mediator, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceZNHB: big.NewInt(600)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	if err := engine.Resolve(esc.ID, mediator, "release"); err != nil {
		t.Fatalf("resolve release: %v", err)
	}
	payeeAcc := state.account(payee)
	if got := payeeAcc.BalanceZNHB.String(); got != "285" {
		t.Fatalf("expected payee 285, got %s", got)
	}
	treasuryAcc := state.account(engine.feeTreasury)
	if got := treasuryAcc.BalanceZNHB.String(); got != "15" {
		t.Fatalf("expected treasury 15, got %s", got)
	}
	if err := engine.Resolve(esc.ID, mediator, "release"); err != nil {
		t.Fatalf("resolve idempotent: %v", err)
	}
}

func TestResolveValidatesOutcomeAndCaller(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0xB1)
	payee := newTestAddress(0xB2)
	mediator := newTestAddress(0xB3)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(100), 0, 1_700_002_000, &mediator, meta, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(200)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	if err := engine.Resolve(esc.ID, payee, "refund"); err == nil {
		t.Fatalf("expected unauthorized resolver")
	}
	if err := engine.Resolve(esc.ID, mediator, "invalid"); err == nil {
		t.Fatalf("expected invalid outcome")
	}
}
