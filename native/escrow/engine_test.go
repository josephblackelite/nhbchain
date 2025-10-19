package escrow

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

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

func testRealmMetadata() *EscrowRealmMetadata {
	return &EscrowRealmMetadata{
		Scope:             EscrowRealmScopePlatform,
		ProviderProfile:   "core-team",
		ArbitrationFeeBps: 0,
	}
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

func (m *mockState) EscrowBalance(id [32]byte, token string) (*big.Int, error) {
	normalized, err := NormalizeToken(token)
	if err != nil {
		return nil, err
	}
	if balances, ok := m.vaultBalances[normalized]; ok {
		if existing, exists := balances[id]; exists && existing != nil {
			return new(big.Int).Set(existing), nil
		}
	}
	return big.NewInt(0), nil
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

var arbitratorKeySeed byte = 1

func mustGenerateArbitrator(t *testing.T) (*ecdsa.PrivateKey, [20]byte) {
	t.Helper()
	seed := bytes.Repeat([]byte{arbitratorKeySeed}, 32)
	arbitratorKeySeed++
	key, err := ethcrypto.ToECDSA(seed)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(key.PublicKey)
	var out [20]byte
	copy(out[:], addr[:])
	return key, out
}

func buildDecisionPayload(t *testing.T, id [32]byte, nonce uint64, outcome string, meta [32]byte) []byte {
	t.Helper()
	payload := map[string]interface{}{
		"escrowId":    hex.EncodeToString(id[:]),
		"outcome":     outcome,
		"policyNonce": nonce,
	}
	if meta != ([32]byte{}) {
		payload["metadata"] = hex.EncodeToString(meta[:])
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func signDecisionPayload(t *testing.T, payload []byte, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	digest := ethcrypto.Keccak256Hash(payload)
	sig, err := ethcrypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	return sig
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
		nonce    uint64
		wantErr  bool
	}{
		{"ok", "NHB", big.NewInt(100), 100, 1_700_000_500, 1, false},
		{"invalid token", "DOGE", big.NewInt(100), 0, 1_700_000_500, 2, true},
		{"zero amount", "NHB", big.NewInt(0), 0, 1_700_000_500, 3, true},
		{"fee too high", "ZNHB", big.NewInt(100), 10_001, 1_700_000_500, 4, true},
		{"deadline before now", "NHB", big.NewInt(100), 0, 1_600_000_000, 5, true},
		{"zero nonce", "NHB", big.NewInt(100), 0, 1_700_000_500, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.Create(payer, payee, tc.token, tc.amount, tc.fee, tc.deadline, tc.nonce, nil, meta, "")
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

	nonce := uint64(10)
	first, err := engine.Create(payer, payee, "NHB", big.NewInt(500), 50, 1_700_000_500, nonce, nil, meta, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := engine.Create(payer, payee, "nhb", big.NewInt(500), 50, 1_700_000_500, nonce, nil, meta, "")
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

func TestCreateWithDifferentNonceYieldsDistinctIDs(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x20)
	payee := newTestAddress(0x21)
	meta := [32]byte{}
	meta[0] = 0xAB

	first, err := engine.Create(payer, payee, "NHB", big.NewInt(500), 0, 1_700_001_000, 11, nil, meta, "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := engine.Create(payer, payee, "NHB", big.NewInt(500), 0, 1_700_001_000, 12, nil, meta, "")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected differing ids when nonce changes")
	}
	if first.Nonce == second.Nonce {
		t.Fatalf("expected nonce values to differ")
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
		Metadata: testRealmMetadata(),
	}
	if err := state.EscrowRealmPut(baseRealm); err != nil {
		t.Fatalf("put realm: %v", err)
	}
	payer := newTestAddress(0x31)
	payee := newTestAddress(0x32)
	meta := [32]byte{0xAB}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(200), 0, 1_700_000_800, 21, nil, meta, "core")
	if err != nil {
		t.Fatalf("create with realm: %v", err)
	}
	if esc.RealmID != "core" {
		t.Fatalf("expected realm id preserved, got %q", esc.RealmID)
	}
	if esc.FrozenArb == nil {
		t.Fatalf("expected frozen policy on escrow")
	}
	if esc.FrozenArb.Metadata == nil {
		t.Fatalf("expected frozen metadata captured")
	}
	if esc.FrozenArb.Metadata.ProviderProfile != "core-team" {
		t.Fatalf("unexpected frozen provider profile: %s", esc.FrozenArb.Metadata.ProviderProfile)
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
	if _, err := engine.Create(payer, payee, "NHB", big.NewInt(150), 0, 1_700_000_900, 22, nil, meta, "missing"); err == nil || !errors.Is(err, errRealmNotFound) {
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
		Metadata: testRealmMetadata(),
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
		Metadata: &EscrowRealmMetadata{Scope: EscrowRealmScopeMarketplace, ProviderProfile: "alpha-ops", ArbitrationFeeBps: 0},
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
	if stored.Metadata.ProviderProfile != "alpha-ops" {
		t.Fatalf("expected updated provider profile, got %s", stored.Metadata.ProviderProfile)
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
		Metadata: testRealmMetadata(),
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
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(300), 0, 1_700_001_000, 30, nil, meta, "")
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
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(100), 0, 1_700_001_000, 31, nil, meta, "")
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
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(1_000), 250, 1_700_002_000, 32, &mediator, meta, "")
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
			esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(1_000), tc.fee, 1_700_003_000, 40, nil, meta, "")
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

func TestReleaseZeroFeeWithoutTreasury(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() int64 { return 1_700_000_000 })

	payer := newTestAddress(0x5A)
	payee := newTestAddress(0x5B)
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(1_200), 0, 1_700_004_000, 41, nil, [32]byte{}, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(2_000)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Release(esc.ID, payee); err != nil {
		t.Fatalf("release: %v", err)
	}

	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowReleased {
		t.Fatalf("expected released status, got %d", stored.Status)
	}
	payeeAcc := state.account(payee)
	if got := payeeAcc.BalanceNHB.String(); got != "1200" {
		t.Fatalf("expected payee balance 1200, got %s", got)
	}
	var zeroTreasury [20]byte
	treasuryAcc := state.account(zeroTreasury)
	if got := treasuryAcc.BalanceNHB.String(); got != "0" {
		t.Fatalf("expected zero treasury balance, got %s", got)
	}
}

func TestRefundHonorsDeadlineAndCaller(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	payer := newTestAddress(0x61)
	payee := newTestAddress(0x62)
	meta := [32]byte{}
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(400), 0, 1_700_000_500, 42, nil, meta, "")
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
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(100), 0, 1_600_000_000, 43, nil, meta, "")
	if err == nil {
		t.Fatalf("expected create error for deadline before now")
	}
	engine.SetNowFunc(func() int64 { return 1_600_000_000 })
	esc, err = engine.Create(payer, payee, "NHB", big.NewInt(100), 0, 1_600_000_500, 44, nil, meta, "")
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
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(200), 0, 1_700_000_500, 45, nil, meta, "")
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

func TestResolveWithSignaturesRelease(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	emitter := &capturingEmitter{}
	engine.SetEmitter(emitter)

	keyA, addrA := mustGenerateArbitrator(t)
	keyB, addrB := mustGenerateArbitrator(t)
	_, addrC := mustGenerateArbitrator(t)

	arbRecipient := newTestAddress(0xAD)
	realm := &EscrowRealm{
		ID: "realm-alpha",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{addrA, addrB, addrC},
		},
		FeeSchedule: &RealmFeeSchedule{FeeBps: 120, Recipient: arbRecipient},
		Metadata:    testRealmMetadata(),
	}
	if _, err := engine.CreateRealm(realm); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	payer := newTestAddress(0x91)
	payee := newTestAddress(0x92)
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(600), 500, 1_700_001_000, 46, nil, [32]byte{}, realm.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if esc.FrozenArb == nil || esc.FrozenArb.FeeSchedule == nil {
		t.Fatalf("expected frozen fee schedule")
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(1_000)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payee, "counterfeit goods"); err != nil {
		t.Fatalf("dispute: %v", err)
	}

	var decisionMeta [32]byte
	copy(decisionMeta[:], bytes.Repeat([]byte{0xAB}, 32))
	payload := buildDecisionPayload(t, esc.ID, esc.FrozenArb.PolicyNonce, "release", decisionMeta)
	sigs := [][]byte{
		signDecisionPayload(t, payload, keyA),
		signDecisionPayload(t, payload, keyB),
	}
	if err := engine.ResolveWithSignatures(esc.ID, payload, sigs); err != nil {
		t.Fatalf("resolve with signatures: %v", err)
	}
	digest := ethcrypto.Keccak256Hash(payload)
	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowReleased {
		t.Fatalf("expected released status, got %d", stored.Status)
	}
	if stored.ResolutionHash != digest {
		t.Fatalf("expected resolution hash %x, got %x", digest[:], stored.ResolutionHash[:])
	}
	if stored.DisputeReason != "counterfeit goods" {
		t.Fatalf("expected stored dispute reason, got %q", stored.DisputeReason)
	}
	payeeAcc := state.account(payee)
	if got := payeeAcc.BalanceNHB.String(); got != "563" {
		t.Fatalf("expected payee 563, got %s", got)
	}
	treasuryAcc := state.account(engine.feeTreasury)
	if got := treasuryAcc.BalanceNHB.String(); got != "30" {
		t.Fatalf("expected treasury 30, got %s", got)
	}
	arbAcc := state.account(arbRecipient)
	if got := arbAcc.BalanceNHB.String(); got != "7" {
		t.Fatalf("expected arbitrator recipient 7, got %s", got)
	}
	events := emitter.typesEvents()
	if len(events) == 0 {
		t.Fatalf("expected events emitted")
	}
	var disputeEvt *types.Event
	for _, evt := range events {
		if evt.Type == EventTypeEscrowDisputed {
			disputeEvt = evt
			break
		}
	}
	if disputeEvt == nil {
		t.Fatalf("expected disputed event to be emitted")
	}
	if disputeEvt.Attributes["disputeReason"] != "counterfeit goods" {
		t.Fatalf("expected dispute reason attribute, got %q", disputeEvt.Attributes["disputeReason"])
	}
	last := events[len(events)-1]
	if last.Type != EventTypeEscrowResolved {
		t.Fatalf("expected resolved event, got %s", last.Type)
	}
	if last.Attributes["decision"] != "release" {
		t.Fatalf("expected decision release, got %s", last.Attributes["decision"])
	}
	if last.Attributes["decisionMetadata"] != hex.EncodeToString(decisionMeta[:]) {
		t.Fatalf("metadata mismatch: %s", last.Attributes["decisionMetadata"])
	}
	expectedSigners := strings.Join([]string{
		hex.EncodeToString(addrA[:]),
		hex.EncodeToString(addrB[:]),
	}, ",")
	if last.Attributes["decisionSigners"] != expectedSigners {
		t.Fatalf("unexpected signers: %s", last.Attributes["decisionSigners"])
	}
}

func TestResolveWithSignaturesRefundRoutesFees(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)

	keyA, addrA := mustGenerateArbitrator(t)
	keyB, addrB := mustGenerateArbitrator(t)

	arbRecipient := newTestAddress(0xAE)
	realm := &EscrowRealm{
		ID: "realm-delta",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{addrA, addrB},
		},
		FeeSchedule: &RealmFeeSchedule{FeeBps: 150, Recipient: arbRecipient},
	}
	if _, err := engine.CreateRealm(realm); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	payer := newTestAddress(0x93)
	payee := newTestAddress(0x94)
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(500), 400, 1_700_001_100, 47, nil, [32]byte{}, realm.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(1_000)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer, "quality issue"); err != nil {
		t.Fatalf("dispute: %v", err)
	}

	payload := buildDecisionPayload(t, esc.ID, esc.FrozenArb.PolicyNonce, "refund", [32]byte{})
	sigs := [][]byte{
		signDecisionPayload(t, payload, keyA),
		signDecisionPayload(t, payload, keyB),
	}
	if err := engine.ResolveWithSignatures(esc.ID, payload, sigs); err != nil {
		t.Fatalf("resolve with signatures: %v", err)
	}

	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowRefunded {
		t.Fatalf("expected refunded status, got %d", stored.Status)
	}

	payerAcc := state.account(payer)
	if got := payerAcc.BalanceNHB.String(); got != "973" {
		t.Fatalf("expected payer balance 973, got %s", got)
	}
	treasuryAcc := state.account(engine.feeTreasury)
	if got := treasuryAcc.BalanceNHB.String(); got != "20" {
		t.Fatalf("expected treasury 20, got %s", got)
	}
	arbAcc := state.account(arbRecipient)
	if got := arbAcc.BalanceNHB.String(); got != "7" {
		t.Fatalf("expected arbitrator recipient 7, got %s", got)
	}
}

func TestArbitratedReleaseZeroFeeWithoutTreasury(t *testing.T) {
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	engine.SetNowFunc(func() int64 { return 1_700_000_000 })

	keyA, addrA := mustGenerateArbitrator(t)
	keyB, addrB := mustGenerateArbitrator(t)
	realm := &EscrowRealm{
		ID: "realm-zero-fee",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{addrA, addrB},
		},
		Metadata: testRealmMetadata(),
	}
	if _, err := engine.CreateRealm(realm); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	payer := newTestAddress(0xA5)
	payee := newTestAddress(0xA6)
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(900), 0, 1_700_005_000, 47, nil, [32]byte{}, realm.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceZNHB: big.NewInt(1_500)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payee, "late delivery"); err != nil {
		t.Fatalf("dispute: %v", err)
	}

	payload := buildDecisionPayload(t, esc.ID, esc.FrozenArb.PolicyNonce, "release", [32]byte{})
	sigs := [][]byte{
		signDecisionPayload(t, payload, keyA),
		signDecisionPayload(t, payload, keyB),
	}
	if err := engine.ResolveWithSignatures(esc.ID, payload, sigs); err != nil {
		t.Fatalf("resolve with signatures: %v", err)
	}

	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowReleased {
		t.Fatalf("expected released status, got %d", stored.Status)
	}
	payeeAcc := state.account(payee)
	if got := payeeAcc.BalanceZNHB.String(); got != "900" {
		t.Fatalf("expected payee balance 900, got %s", got)
	}
	var zeroTreasury [20]byte
	treasuryAcc := state.account(zeroTreasury)
	if got := treasuryAcc.BalanceZNHB.String(); got != "0" {
		t.Fatalf("expected zero treasury balance, got %s", got)
	}
}

func TestResolveWithSignaturesRejectsUnderQuorum(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)

	keyA, addrA := mustGenerateArbitrator(t)
	_, addrB := mustGenerateArbitrator(t)

	realm := &EscrowRealm{
		ID: "realm-beta",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{addrA, addrB},
		},
		Metadata: testRealmMetadata(),
	}
	if _, err := engine.CreateRealm(realm); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	payer := newTestAddress(0xA1)
	payee := newTestAddress(0xA2)
	esc, err := engine.Create(payer, payee, "ZNHB", big.NewInt(300), 0, 1_700_001_500, 48, nil, [32]byte{}, realm.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceZNHB: big.NewInt(600)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer, "duplicate request"); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	payload := buildDecisionPayload(t, esc.ID, esc.FrozenArb.PolicyNonce, "refund", [32]byte{})
	sig := signDecisionPayload(t, payload, keyA)
	if err := engine.ResolveWithSignatures(esc.ID, payload, [][]byte{sig}); err == nil {
		t.Fatalf("expected quorum failure")
	}
	if err := engine.ResolveWithSignatures(esc.ID, payload, [][]byte{sig, sig}); err == nil {
		t.Fatalf("expected duplicate signer failure")
	}
	stored, _ := state.EscrowGet(esc.ID)
	if stored.Status != EscrowDisputed {
		t.Fatalf("expected disputed status, got %d", stored.Status)
	}
}

func TestResolveWithSignaturesReplay(t *testing.T) {
	state := newMockState()
	engine := newTestEngine(state)
	emitter := &capturingEmitter{}
	engine.SetEmitter(emitter)

	keyA, addrA := mustGenerateArbitrator(t)
	keyB, addrB := mustGenerateArbitrator(t)

	realm := &EscrowRealm{
		ID: "realm-gamma",
		Arbitrators: &ArbitratorSet{
			Scheme:    ArbitrationSchemeCommittee,
			Threshold: 2,
			Members:   [][20]byte{addrA, addrB},
		},
		Metadata: testRealmMetadata(),
	}
	if _, err := engine.CreateRealm(realm); err != nil {
		t.Fatalf("create realm: %v", err)
	}

	payer := newTestAddress(0xB1)
	payee := newTestAddress(0xB2)
	esc, err := engine.Create(payer, payee, "NHB", big.NewInt(400), 0, 1_700_002_000, 49, nil, [32]byte{}, realm.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	state.setAccount(payer, &types.Account{BalanceNHB: big.NewInt(800)})
	if err := engine.Fund(esc.ID, payer); err != nil {
		t.Fatalf("fund: %v", err)
	}
	if err := engine.Dispute(esc.ID, payer, ""); err != nil {
		t.Fatalf("dispute: %v", err)
	}
	payload := buildDecisionPayload(t, esc.ID, esc.FrozenArb.PolicyNonce, "refund", [32]byte{})
	sigs := [][]byte{
		signDecisionPayload(t, payload, keyA),
		signDecisionPayload(t, payload, keyB),
	}
	if err := engine.ResolveWithSignatures(esc.ID, payload, sigs); err != nil {
		t.Fatalf("initial resolve: %v", err)
	}
	firstEvents := len(emitter.events)
	if err := engine.ResolveWithSignatures(esc.ID, payload, sigs); err != nil {
		t.Fatalf("replay should be ignored: %v", err)
	}
	if len(emitter.events) != firstEvents {
		t.Fatalf("expected no additional events on replay")
	}
}
