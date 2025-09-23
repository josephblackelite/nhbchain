package loyalty

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

type mockProgramState struct {
	*mockState
	programs      map[[32]byte]*Program
	ownerPrograms map[[20]byte][]ProgramID
	businesses    map[[20]byte]*Business
	programDaily  map[[32]byte]map[string]map[string]*big.Int
}

func newMockProgramState(cfg *GlobalConfig) *mockProgramState {
	return &mockProgramState{
		mockState:     newMockState(cfg),
		programs:      make(map[[32]byte]*Program),
		ownerPrograms: make(map[[20]byte][]ProgramID),
		businesses:    make(map[[20]byte]*Business),
		programDaily:  make(map[[32]byte]map[string]map[string]*big.Int),
	}
}

func cloneProgram(p *Program) *Program {
	if p == nil {
		return nil
	}
	clone := *p
	clone.MinSpendWei = cloneBigInt(p.MinSpendWei)
	clone.CapPerTx = cloneBigInt(p.CapPerTx)
	clone.DailyCapUser = cloneBigInt(p.DailyCapUser)
	return &clone
}

func (m *mockProgramState) addProgram(p *Program) {
	if p == nil {
		return
	}
	key := p.ID
	m.programs[key] = cloneProgram(p)
	ownerKey := p.Owner
	existing := append([]ProgramID{}, m.ownerPrograms[ownerKey]...)
	existing = append(existing, p.ID)
	m.ownerPrograms[ownerKey] = existing
}

func (m *mockProgramState) addBusinessMapping(merchant [20]byte, business *Business) {
	if business == nil {
		return
	}
	m.businesses[merchant] = &Business{
		ID:        business.ID,
		Owner:     business.Owner,
		Name:      business.Name,
		Paymaster: business.Paymaster,
		Merchants: append([][20]byte(nil), business.Merchants...),
	}
}

func (m *mockProgramState) LoyaltyProgramByID(id ProgramID) (*Program, bool, error) {
	if program, ok := m.programs[id]; ok {
		return cloneProgram(program), true, nil
	}
	return nil, false, nil
}

func (m *mockProgramState) LoyaltyProgramsByOwner(owner [20]byte) ([]ProgramID, error) {
	programs := m.ownerPrograms[owner]
	out := make([]ProgramID, len(programs))
	copy(out, programs)
	return out, nil
}

func (m *mockProgramState) LoyaltyBusinessByMerchant(merchant [20]byte) (*Business, bool, error) {
	if business, ok := m.businesses[merchant]; ok {
		return &Business{
			ID:        business.ID,
			Owner:     business.Owner,
			Name:      business.Name,
			Paymaster: business.Paymaster,
			Merchants: append([][20]byte(nil), business.Merchants...),
		}, true, nil
	}
	return nil, false, nil
}

func (m *mockProgramState) LoyaltyProgramDailyAccrued(programID ProgramID, addr []byte, day string) (*big.Int, error) {
	programKey := programID
	dayMeters, ok := m.programDaily[programKey]
	if !ok {
		return big.NewInt(0), nil
	}
	userMeters, ok := dayMeters[day]
	if !ok {
		return big.NewInt(0), nil
	}
	if amt, exists := userMeters[string(addr)]; exists {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockProgramState) SetLoyaltyProgramDailyAccrued(programID ProgramID, addr []byte, day string, amount *big.Int) error {
	programKey := programID
	if _, ok := m.programDaily[programKey]; !ok {
		m.programDaily[programKey] = make(map[string]map[string]*big.Int)
	}
	if _, ok := m.programDaily[programKey][day]; !ok {
		m.programDaily[programKey][day] = make(map[string]*big.Int)
	}
	m.programDaily[programKey][day][string(addr)] = new(big.Int).Set(amount)
	return nil
}

func toBytes(addr [20]byte) []byte {
	return append([]byte(nil), addr[:]...)
}

func TestApplyProgramRewardHappyPath(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[19] = 0x01
	var merchant [20]byte
	merchant[19] = 0x02
	var paymaster [20]byte
	paymaster[19] = 0x03
	var programID ProgramID
	programID[31] = 0xAA
	var businessID BusinessID
	businessID[31] = 0xBB

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:           programID,
		Owner:        merchant,
		TokenSymbol:  "ZNHB",
		AccrualBps:   500,
		MinSpendWei:  big.NewInt(100),
		CapPerTx:     big.NewInt(500),
		DailyCapUser: big.NewInt(1000),
		StartTime:    uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		Active:       true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: paymaster, Merchants: [][20]byte{merchant}})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx})

	if got := ctx.FromAccount.BalanceZNHB.String(); got != "50" {
		t.Fatalf("expected reward 50, got %s", got)
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if got := paymasterAcc.BalanceZNHB.String(); got != "950" {
		t.Fatalf("expected paymaster balance 950, got %s", got)
	}
	accrued, err := state.LoyaltyProgramDailyAccrued(programID, toBytes(from), "2024-01-10")
	if err != nil {
		t.Fatalf("daily accrued error: %v", err)
	}
	if accrued.String() != "50" {
		t.Fatalf("expected daily accrued 50, got %s", accrued.String())
	}
	if len(state.events) != 1 || state.events[0].Type != eventProgramAccrued {
		t.Fatalf("expected program accrued event, got %#v", state.events)
	}
	if state.events[0].Attributes["programId"] != "00000000000000000000000000000000000000000000000000000000000000aa" {
		t.Fatalf("expected program id attribute, got %s", state.events[0].Attributes["programId"])
	}
	if state.events[0].Attributes["paymaster"] != "0000000000000000000000000000000000000003" {
		t.Fatalf("expected paymaster attribute, got %s", state.events[0].Attributes["paymaster"])
	}
}

func TestApplyProgramRewardInsufficientPaymaster(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[10] = 0x11
	var merchant [20]byte
	merchant[9] = 0x22
	var paymaster [20]byte
	paymaster[8] = 0x33
	var programID ProgramID
	programID[31] = 0x01

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	state.addProgram(&Program{
		ID:           programID,
		Owner:        merchant,
		TokenSymbol:  "ZNHB",
		AccrualBps:   1000,
		MinSpendWei:  big.NewInt(0),
		CapPerTx:     big.NewInt(1000),
		DailyCapUser: big.NewInt(1000),
		StartTime:    uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		Active:       true,
	})
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx})

	if ctx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward, got %s", ctx.FromAccount.BalanceZNHB.String())
	}
	if len(state.events) != 1 || state.events[0].Type != eventProgramSkipped {
		t.Fatalf("expected program skipped event, got %#v", state.events)
	}
	if state.events[0].Attributes["reason"] != "paymaster_insufficient" {
		t.Fatalf("unexpected skip reason %s", state.events[0].Attributes["reason"])
	}
}
