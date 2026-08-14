package loyalty

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

type mockProgramState struct {
	*mockState
	programs           map[[32]byte]*Program
	ownerPrograms      map[[20]byte][]ProgramID
	businesses         map[[20]byte]*Business
	programDaily       map[[32]byte]map[string]map[string]*big.Int
	programDailyTotals map[[32]byte]map[string]*big.Int
	programDailyTxCnts map[[32]byte]map[string]uint64
	programLifetime    map[[32]byte]*big.Int
	programEpochTotals map[[32]byte]map[uint64]*big.Int
	programIssuance    map[[32]byte]map[string]*big.Int
}

func newMockProgramState(cfg *GlobalConfig) *mockProgramState {
	return &mockProgramState{
		mockState:          newMockState(cfg),
		programs:           make(map[[32]byte]*Program),
		ownerPrograms:      make(map[[20]byte][]ProgramID),
		businesses:         make(map[[20]byte]*Business),
		programDaily:       make(map[[32]byte]map[string]map[string]*big.Int),
		programDailyTotals: make(map[[32]byte]map[string]*big.Int),
		programDailyTxCnts: make(map[[32]byte]map[string]uint64),
		programLifetime:    make(map[[32]byte]*big.Int),
		programEpochTotals: make(map[[32]byte]map[uint64]*big.Int),
		programIssuance:    make(map[[32]byte]map[string]*big.Int),
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
	clone.DailyCapProgram = cloneBigInt(p.DailyCapProgram)
	clone.EpochCapProgram = cloneBigInt(p.EpochCapProgram)
	clone.IssuanceCapUser = cloneBigInt(p.IssuanceCapUser)
	clone.FixedRewardWei = cloneBigInt(p.FixedRewardWei)
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
		ID:                  business.ID,
		Owner:               business.Owner,
		Name:                business.Name,
		Paymaster:           business.Paymaster,
		Merchants:           append([][20]byte(nil), business.Merchants...),
		PaymasterReserveMin: cloneBigInt(business.PaymasterReserveMin),
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
			ID:                  business.ID,
			Owner:               business.Owner,
			Name:                business.Name,
			Paymaster:           business.Paymaster,
			Merchants:           append([][20]byte(nil), business.Merchants...),
			PaymasterReserveMin: cloneBigInt(business.PaymasterReserveMin),
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

func (m *mockProgramState) LoyaltyProgramDailyTotalAccrued(programID ProgramID, day string) (*big.Int, error) {
	totals, ok := m.programDailyTotals[programID]
	if !ok {
		return big.NewInt(0), nil
	}
	if amt, exists := totals[day]; exists {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockProgramState) SetLoyaltyProgramDailyTotalAccrued(programID ProgramID, day string, amount *big.Int) error {
	if _, ok := m.programDailyTotals[programID]; !ok {
		m.programDailyTotals[programID] = make(map[string]*big.Int)
	}
	m.programDailyTotals[programID][day] = new(big.Int).Set(amount)
	return nil
}

func (m *mockProgramState) LoyaltyProgramDailyTxCount(programID ProgramID, day string) (uint64, error) {
	counts, ok := m.programDailyTxCnts[programID]
	if !ok {
		return 0, nil
	}
	return counts[day], nil
}

func (m *mockProgramState) SetLoyaltyProgramDailyTxCount(programID ProgramID, day string, count uint64) error {
	if _, ok := m.programDailyTxCnts[programID]; !ok {
		m.programDailyTxCnts[programID] = make(map[string]uint64)
	}
	m.programDailyTxCnts[programID][day] = count
	return nil
}

func (m *mockProgramState) LoyaltyProgramLifetimeAccrued(programID ProgramID) (*big.Int, error) {
	if amt, ok := m.programLifetime[programID]; ok {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockProgramState) SetLoyaltyProgramLifetimeAccrued(programID ProgramID, amount *big.Int) error {
	m.programLifetime[programID] = new(big.Int).Set(amount)
	return nil
}

func (m *mockProgramState) LoyaltyProgramEpochAccrued(programID ProgramID, epoch uint64) (*big.Int, error) {
	totals, ok := m.programEpochTotals[programID]
	if !ok {
		return big.NewInt(0), nil
	}
	if amt, exists := totals[epoch]; exists {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockProgramState) SetLoyaltyProgramEpochAccrued(programID ProgramID, epoch uint64, amount *big.Int) error {
	if _, ok := m.programEpochTotals[programID]; !ok {
		m.programEpochTotals[programID] = make(map[uint64]*big.Int)
	}
	m.programEpochTotals[programID][epoch] = new(big.Int).Set(amount)
	return nil
}

func (m *mockProgramState) LoyaltyProgramIssuanceAccrued(programID ProgramID, addr []byte) (*big.Int, error) {
	totals, ok := m.programIssuance[programID]
	if !ok {
		return big.NewInt(0), nil
	}
	if amt, exists := totals[string(addr)]; exists {
		return new(big.Int).Set(amt), nil
	}
	return big.NewInt(0), nil
}

func (m *mockProgramState) SetLoyaltyProgramIssuanceAccrued(programID ProgramID, addr []byte, amount *big.Int) error {
	if _, ok := m.programIssuance[programID]; !ok {
		m.programIssuance[programID] = make(map[string]*big.Int)
	}
	m.programIssuance[programID][string(addr)] = new(big.Int).Set(amount)
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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}

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

func TestApplyProgramRewardProgramPaused(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[0] = 0x01
	var merchant [20]byte
	merchant[0] = 0x02
	var paymaster [20]byte
	paymaster[0] = 0x03
	var programID ProgramID
	programID[0] = 0x04
	var businessID BusinessID
	businessID[0] = 0x05

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:          programID,
		Owner:       merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  500,
		Active:      false,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: paymaster, Merchants: [][20]byte{merchant}})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	baseCtx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: baseCtx, ProgramHint: &programID}); result != "program_inactive" {
		t.Fatalf("expected program_inactive result, got %q", result)
	}

	if baseCtx.FromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward for paused program")
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if paymasterAcc.BalanceZNHB.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected paymaster balance unchanged, got %s", paymasterAcc.BalanceZNHB.String())
	}
	if len(state.events) != 1 || state.events[0].Type != eventProgramSkipped {
		t.Fatalf("expected program skipped event, got %#v", state.events)
	}
	if reason := state.events[0].Attributes["reason"]; reason != "program_inactive" {
		t.Fatalf("expected program_inactive reason, got %q", reason)
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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != "paymaster_insufficient" {
		t.Fatalf("expected paymaster_insufficient result, got %q", result)
	}

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

func TestApplyProgramRewardPaymasterRotation(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[0] = 0x01
	var merchant [20]byte
	merchant[0] = 0x02
	var oldPaymaster [20]byte
	oldPaymaster[0] = 0x03
	var newPaymaster [20]byte
	newPaymaster[0] = 0x04
	var programID ProgramID
	programID[0] = 0x05
	var businessID BusinessID
	businessID[0] = 0x06

	state.addAccount(oldPaymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	state.addAccount(newPaymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:           programID,
		Owner:        merchant,
		TokenSymbol:  "ZNHB",
		AccrualBps:   500,
		MinSpendWei:  big.NewInt(0),
		DailyCapUser: nil,
		Active:       true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: oldPaymaster, Merchants: [][20]byte{merchant}})

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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx, ProgramHint: &programID}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}

	paymasterAcc, _ := state.GetAccount(oldPaymaster[:])
	if paymasterAcc.BalanceZNHB.String() != "950" {
		t.Fatalf("expected old paymaster balance 950, got %s", paymasterAcc.BalanceZNHB.String())
	}
	if len(state.events) == 0 {
		t.Fatalf("expected program accrued event, got none")
	}
	if got := state.events[len(state.events)-1].Attributes["paymaster"]; got != hex.EncodeToString(oldPaymaster[:]) {
		t.Fatalf("expected paymaster attribute %s, got %s", hex.EncodeToString(oldPaymaster[:]), got)
	}

	// Rotate paymaster and apply reward again.
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: newPaymaster, Merchants: [][20]byte{merchant}})
	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   ctx.Timestamp.Add(24 * time.Hour),
		FromAccount: fromAccount,
	}
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2, ProgramHint: &programID}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}

	newPaymasterAcc, _ := state.GetAccount(newPaymaster[:])
	if newPaymasterAcc.BalanceZNHB.String() != "950" {
		t.Fatalf("expected new paymaster balance 950, got %s", newPaymasterAcc.BalanceZNHB.String())
	}
	paymasterAcc, _ = state.GetAccount(oldPaymaster[:])
	if paymasterAcc.BalanceZNHB.String() != "950" {
		t.Fatalf("expected old paymaster balance unchanged after rotation, got %s", paymasterAcc.BalanceZNHB.String())
	}
	if len(state.events) == 0 {
		t.Fatalf("expected accrued event after rotation")
	}
	if got := state.events[len(state.events)-1].Attributes["paymaster"]; got != hex.EncodeToString(newPaymaster[:]) {
		t.Fatalf("expected paymaster attribute %s, got %s", hex.EncodeToString(newPaymaster[:]), got)
	}
}

func TestApplyProgramRewardThrottledLowReserve(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[0] = 0x11
	var merchant [20]byte
	merchant[0] = 0x12
	var paymaster [20]byte
	paymaster[0] = 0x13
	var programID ProgramID
	programID[0] = 0x21

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1200), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:          programID,
		Owner:       merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  2500,
		Active:      true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster, PaymasterReserveMin: big.NewInt(1000)})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 10, 10, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	if reserve := state.businesses[merchant].PaymasterReserveMin; reserve == nil || reserve.String() != "1000" {
		t.Fatalf("expected reserve min 1000, got %v", reserve)
	}

	engine := NewEngine()
	result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx})
	if result != resultThrottledLowReserve {
		t.Fatalf("expected throttled result, got %q; events=%v", result, state.events)
	}
	if fromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward accrual, got %s", fromAccount.BalanceZNHB.String())
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if paymasterAcc.BalanceZNHB.Cmp(big.NewInt(1200)) != 0 {
		t.Fatalf("expected paymaster balance unchanged, got %s", paymasterAcc.BalanceZNHB.String())
	}
	if len(state.events) != 2 {
		t.Fatalf("expected warning and skip events, got %d", len(state.events))
	}
	if state.events[0].Type != eventProgramPaymasterWarn {
		t.Fatalf("expected paymaster warning event, got %s", state.events[0].Type)
	}
	if state.events[0].Attributes["balance"] != "950" {
		t.Fatalf("expected warning balance 950, got %s", state.events[0].Attributes["balance"])
	}
	if state.events[1].Attributes["reason"] != resultThrottledLowReserve {
		t.Fatalf("expected throttled reason, got %s", state.events[1].Attributes["reason"])
	}
}

func TestApplyProgramRewardDailyProgramCap(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[5] = 0x21
	var merchant [20]byte
	merchant[5] = 0x22
	var paymaster [20]byte
	paymaster[5] = 0x23
	var programID ProgramID
	programID[5] = 0x24

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		AccrualBps:      1000,
		DailyCapProgram: big.NewInt(150),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 11, 8, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); res != resultAccrued {
		t.Fatalf("first accrual expected %q, got %q", resultAccrued, res)
	}
	if fromAccount.BalanceZNHB.String() != "100" {
		t.Fatalf("expected first reward 100, got %s", fromAccount.BalanceZNHB.String())
	}

	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 11, 9, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); res != resultAccrued {
		t.Fatalf("second accrual expected %q, got %q", resultAccrued, res)
	}
	if fromAccount.BalanceZNHB.String() != "150" {
		t.Fatalf("expected cumulative reward 150, got %s", fromAccount.BalanceZNHB.String())
	}
	total, err := state.LoyaltyProgramDailyTotalAccrued(programID, "2024-01-11")
	if err != nil {
		t.Fatalf("daily total error: %v", err)
	}
	if total.String() != "150" {
		t.Fatalf("expected program daily total 150, got %s", total.String())
	}
}

// TestApplyProgramRewardUncappedProgramStillMetersTotals guards the
// loyalty_programStats fix: a program with neither DailyCapProgram nor
// EpochCapProgram configured must still accumulate a real per-day total and
// tx count (previously these meters were only ever written when a cap was
// configured, so an uncapped program's daily total silently stayed at zero
// forever regardless of actual reward volume).
func TestApplyProgramRewardUncappedProgramStillMetersTotals(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[6] = 0x51
	var merchant [20]byte
	merchant[6] = 0x52
	var paymaster [20]byte
	paymaster[6] = 0x53
	var programID ProgramID
	programID[6] = 0x54

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	// Deliberately no DailyCapProgram/EpochCapProgram -- the anti-sybil
	// requirement enforced at CreateProgram/UpdateProgram time doesn't apply
	// here since this test drives the engine directly, and legacy programs
	// created before that guardrail existed can still be in this state.
	program := &Program{
		ID:          programID,
		Owner:       merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  1000,
		Active:      true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	day := time.Date(2024, 2, 1, 8, 0, 0, 0, time.UTC)
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   day,
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); res != resultAccrued {
		t.Fatalf("first accrual expected %q, got %q", resultAccrued, res)
	}
	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   day.Add(time.Hour),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); res != resultAccrued {
		t.Fatalf("second accrual expected %q, got %q", resultAccrued, res)
	}

	total, err := state.LoyaltyProgramDailyTotalAccrued(programID, "2024-02-01")
	if err != nil {
		t.Fatalf("daily total error: %v", err)
	}
	if total.String() != "200" {
		t.Fatalf("expected uncapped program daily total 200, got %s", total.String())
	}
	txCount, err := state.LoyaltyProgramDailyTxCount(programID, "2024-02-01")
	if err != nil {
		t.Fatalf("tx count error: %v", err)
	}
	if txCount != 2 {
		t.Fatalf("expected tx count 2, got %d", txCount)
	}
}

func TestApplyProgramRewardEpochCap(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[9] = 0x31
	var merchant [20]byte
	merchant[9] = 0x32
	var paymaster [20]byte
	paymaster[9] = 0x33
	var programID ProgramID
	programID[9] = 0x34

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:                 programID,
		Owner:              merchant,
		TokenSymbol:        "ZNHB",
		AccrualBps:         1000,
		EpochCapProgram:    big.NewInt(180),
		EpochLengthSeconds: 1000,
		Active:             true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	timestamp := time.Unix(1_000, 0).UTC()
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp,
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); res != resultAccrued {
		t.Fatalf("first accrual expected %q, got %q", resultAccrued, res)
	}

	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp.Add(10 * time.Second),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); res != resultAccrued {
		t.Fatalf("second accrual expected %q, got %q", resultAccrued, res)
	}
	if fromAccount.BalanceZNHB.String() != "180" {
		t.Fatalf("expected cumulative epoch reward 180, got %s", fromAccount.BalanceZNHB.String())
	}

	ctx3 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp.Add(20 * time.Second),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx3}); res != "epoch_cap_reached" {
		t.Fatalf("expected epoch cap reached result, got %q", res)
	}
	epochKey := uint64(timestamp.UTC().Unix()) / program.EpochLengthSeconds
	total, err := state.LoyaltyProgramEpochAccrued(programID, epochKey)
	if err != nil {
		t.Fatalf("epoch meter error: %v", err)
	}
	if total.String() != "180" {
		t.Fatalf("expected epoch total 180, got %s", total.String())
	}
}

func TestApplyProgramRewardIssuanceCap(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[10] = 0x41
	var merchant [20]byte
	merchant[10] = 0x42
	var paymaster [20]byte
	paymaster[10] = 0x43
	var programID ProgramID
	programID[10] = 0x44

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		AccrualBps:      1200,
		IssuanceCapUser: big.NewInt(150),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	timestamp := time.Date(2024, 1, 12, 12, 0, 0, 0, time.UTC)
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp,
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); res != resultAccrued {
		t.Fatalf("expected first accrual %q, got %q", resultAccrued, res)
	}
	if fromAccount.BalanceZNHB.String() != "120" {
		t.Fatalf("expected first reward 120, got %s", fromAccount.BalanceZNHB.String())
	}

	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp.Add(time.Hour),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); res != resultAccrued {
		t.Fatalf("expected second accrual %q, got %q", resultAccrued, res)
	}
	if fromAccount.BalanceZNHB.String() != "150" {
		t.Fatalf("expected capped total 150, got %s", fromAccount.BalanceZNHB.String())
	}

	ctx3 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   timestamp.Add(2 * time.Hour),
		FromAccount: fromAccount,
	}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx3}); res != "issuance_cap_reached" {
		t.Fatalf("expected issuance cap reached, got %q", res)
	}
	issuance, err := state.LoyaltyProgramIssuanceAccrued(programID, toBytes(from))
	if err != nil {
		t.Fatalf("issuance meter error: %v", err)
	}
	if issuance.String() != "150" {
		t.Fatalf("expected issuance total 150, got %s", issuance.String())
	}
}

func TestApplyProgramRewardFixedModeHappyPath(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[19] = 0x61
	var merchant [20]byte
	merchant[19] = 0x62
	var paymaster [20]byte
	paymaster[19] = 0x63
	var programID ProgramID
	programID[31] = 0xCC
	var businessID BusinessID
	businessID[31] = 0xDD

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		FixedRewardWei:  big.NewInt(10),
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{ID: businessID, Owner: merchant, Paymaster: paymaster, Merchants: [][20]byte{merchant}})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1_000_000), // spend size must not affect a fixed reward
		Timestamp:   time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "10" {
		t.Fatalf("expected flat reward 10 regardless of spend size, got %s", got)
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if got := paymasterAcc.BalanceZNHB.String(); got != "990" {
		t.Fatalf("expected paymaster balance 990, got %s", got)
	}
	if state.events[0].Attributes["rewardMode"] != "fixed" {
		t.Fatalf("expected rewardMode=fixed event attribute, got %q", state.events[0].Attributes["rewardMode"])
	}
	if state.events[0].Attributes["fixedRewardWei"] != "10" {
		t.Fatalf("expected fixedRewardWei=10 event attribute, got %q", state.events[0].Attributes["fixedRewardWei"])
	}

	// A second purchase with a very different spend size must pay the exact
	// same flat amount -- this is the whole point of fixed mode.
	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1),
		Timestamp:   ctx.Timestamp.Add(time.Hour),
		FromAccount: fromAccount,
	}
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); result != resultAccrued {
		t.Fatalf("expected second result %q, got %q", resultAccrued, result)
	}
	if got := ctx2.FromAccount.BalanceZNHB.String(); got != "20" {
		t.Fatalf("expected cumulative reward 20 after two identical flat payouts, got %s", got)
	}
}

func TestApplyProgramRewardFixedModeCapPerTxClamps(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[18] = 0x71
	var merchant [20]byte
	merchant[18] = 0x72
	var paymaster [20]byte
	paymaster[18] = 0x73
	var programID ProgramID
	programID[30] = 0xCC

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		FixedRewardWei:  big.NewInt(100),
		CapPerTx:        big.NewInt(5), // deliberately below FixedRewardWei
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected result %q, got %q", resultAccrued, result)
	}
	if got := ctx.FromAccount.BalanceZNHB.String(); got != "5" {
		t.Fatalf("expected CapPerTx to clamp the fixed reward down to 5, got %s", got)
	}
}

func TestApplyProgramRewardFixedModeNoRewardRate(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[17] = 0x81
	var merchant [20]byte
	merchant[17] = 0x82
	var paymaster [20]byte
	paymaster[17] = 0x83
	var programID ProgramID
	programID[29] = 0xCC

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	// RewardMode=Fixed but FixedRewardWei was never set (e.g. a program
	// mistakenly saved with mode=fixed and amount=0) must skip cleanly, not
	// silently fall back to a percentage or pay zero-as-success.
	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != "no_reward_rate" {
		t.Fatalf("expected no_reward_rate, got %q", result)
	}
	if fromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no reward paid, got %s", fromAccount.BalanceZNHB.String())
	}
}

func TestApplyProgramRewardFixedModePaymasterInsufficientSkipsCleanly(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[16] = 0x91
	var merchant [20]byte
	merchant[16] = 0x92
	var paymaster [20]byte
	paymaster[16] = 0x93
	var programID ProgramID
	programID[28] = 0xCC

	// Paymaster only has 5 ZNHB but the program promises a flat 10.
	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(5), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		FixedRewardWei:  big.NewInt(10),
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != "paymaster_insufficient" {
		t.Fatalf("expected paymaster_insufficient, got %q", result)
	}
	if fromAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected no partial payout, got %s", fromAccount.BalanceZNHB.String())
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if paymasterAcc.BalanceZNHB.Cmp(big.NewInt(5)) != 0 {
		t.Fatalf("expected paymaster balance untouched at 5, got %s", paymasterAcc.BalanceZNHB.String())
	}
}

func TestApplyProgramRewardFixedModeDailyCapUserClamps(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[15] = 0xA1
	var merchant [20]byte
	merchant[15] = 0xA2
	var paymaster [20]byte
	paymaster[15] = 0xA3
	var programID ProgramID
	programID[27] = 0xCC

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:              programID,
		Owner:           merchant,
		TokenSymbol:     "ZNHB",
		RewardMode:      RewardModeFixed,
		FixedRewardWei:  big.NewInt(10),
		DailyCapUser:    big.NewInt(15), // less than two full flat rewards (20)
		DailyCapProgram: big.NewInt(1000),
		Active:          true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

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
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); result != resultAccrued {
		t.Fatalf("expected first result %q, got %q", resultAccrued, result)
	}
	if fromAccount.BalanceZNHB.String() != "10" {
		t.Fatalf("expected first flat reward 10, got %s", fromAccount.BalanceZNHB.String())
	}

	ctx2 := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   ctx.Timestamp.Add(time.Hour),
		FromAccount: fromAccount,
	}
	if result := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); result != resultAccrued {
		t.Fatalf("expected second result %q, got %q", resultAccrued, result)
	}
	// Second flat reward of 10 would bring the daily total to 20, above the
	// 15 cap -- must be clamped down to the remaining 5, not paid in full
	// and not skipped outright.
	if fromAccount.BalanceZNHB.String() != "15" {
		t.Fatalf("expected daily cap to clamp cumulative reward to 15, got %s", fromAccount.BalanceZNHB.String())
	}
}

func TestApplyProgramRewardPaymasterWarning(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[15] = 0x51
	var merchant [20]byte
	merchant[15] = 0x52
	var paymaster [20]byte
	paymaster[15] = 0x53
	var programID ProgramID
	programID[15] = 0x54

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(1_500), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	program := &Program{
		ID:          programID,
		Owner:       merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  4000,
		Active:      true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster, PaymasterReserveMin: big.NewInt(1000)})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	ctx := &BaseRewardContext{
		From:        toBytes(from),
		To:          toBytes(merchant),
		Token:       "NHB",
		Amount:      big.NewInt(1000),
		Timestamp:   time.Date(2024, 1, 13, 15, 0, 0, 0, time.UTC),
		FromAccount: fromAccount,
	}

	engine := NewEngine()
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx}); res != resultAccrued {
		t.Fatalf("expected accrued result, got %q", res)
	}
	if len(state.events) < 2 {
		t.Fatalf("expected warning and accrued events, got %d", len(state.events))
	}
	if state.events[0].Type != eventProgramPaymasterWarn {
		t.Fatalf("expected first event warning, got %s", state.events[0].Type)
	}
	if state.events[0].Attributes["balance"] != "1100" {
		t.Fatalf("expected warning balance 1100, got %s", state.events[0].Attributes["balance"])
	}
	if state.events[1].Type != eventProgramAccrued {
		t.Fatalf("expected accrued event second, got %s", state.events[1].Type)
	}
	paymasterAcc, _ := state.GetAccount(paymaster[:])
	if paymasterAcc.BalanceZNHB.String() != "1100" {
		t.Fatalf("expected paymaster balance 1100, got %s", paymasterAcc.BalanceZNHB.String())
	}
}

// TestApplyProgramRewardLifetimeAccruedZeroWithNoHistory guards case (b) of
// the lifetime-meter contract: a program that has never accrued anything must
// read back a real, non-nil zero -- not panic and not return an error --
// exactly like the pre-existing daily/epoch/issuance meters already do for a
// fresh key.
func TestApplyProgramRewardLifetimeAccruedZeroWithNoHistory(t *testing.T) {
	state := newMockProgramState(newConfig(0, 0, 0, 0, []byte("treasury")))
	var programID ProgramID
	programID[0] = 0x77

	lifetime, err := state.LoyaltyProgramLifetimeAccrued(programID)
	if err != nil {
		t.Fatalf("unexpected error reading lifetime meter with no history: %v", err)
	}
	if lifetime == nil {
		t.Fatalf("expected a real zero value, got nil")
	}
	if lifetime.Sign() != 0 {
		t.Fatalf("expected zero lifetime accrued for untouched program, got %s", lifetime.String())
	}
}

// TestApplyProgramRewardLifetimeAccruedAccumulatesAcrossDays guards case (a)
// of the lifetime-meter contract: LoyaltyProgramLifetimeAccrued must keep
// summing across every successful accrual for a program regardless of which
// UTC day each one lands on -- unlike LoyaltyProgramDailyTotalAccrued, it is
// never reset when the day rolls over. It must also increment for an
// uncapped program (no DailyCapProgram/EpochCapProgram), since it is written
// unconditionally, not gated behind cap enforcement the way
// LoyaltyProgramIssuanceAccrued historically was.
func TestApplyProgramRewardLifetimeAccruedAccumulatesAcrossDays(t *testing.T) {
	treasury := []byte("treasury")
	cfg := newConfig(0, 0, 0, 0, treasury)
	state := newMockProgramState(cfg)

	var from [20]byte
	from[7] = 0x61
	var merchant [20]byte
	merchant[7] = 0x62
	var paymaster [20]byte
	paymaster[7] = 0x63
	var programID ProgramID
	programID[7] = 0x64

	state.addAccount(paymaster[:], &types.Account{BalanceZNHB: big.NewInt(10_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})

	// Deliberately uncapped (no DailyCapProgram/EpochCapProgram) to prove the
	// lifetime meter is unconditional, unlike the historically cap-gated
	// issuance meter.
	program := &Program{
		ID:          programID,
		Owner:       merchant,
		TokenSymbol: "ZNHB",
		AccrualBps:  1000,
		Active:      true,
	}
	state.addProgram(program)
	state.addBusinessMapping(merchant, &Business{Paymaster: paymaster})

	fromAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	engine := NewEngine()

	// Day 1: two accruals of 100 each (reward = 1000 * 1000bps / 10000 = 100).
	day1 := time.Date(2024, 3, 1, 8, 0, 0, 0, time.UTC)
	ctx1 := &BaseRewardContext{From: toBytes(from), To: toBytes(merchant), Token: "NHB", Amount: big.NewInt(1000), Timestamp: day1, FromAccount: fromAccount}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx1}); res != resultAccrued {
		t.Fatalf("day1 accrual 1 expected %q, got %q", resultAccrued, res)
	}
	ctx2 := &BaseRewardContext{From: toBytes(from), To: toBytes(merchant), Token: "NHB", Amount: big.NewInt(1000), Timestamp: day1.Add(time.Hour), FromAccount: fromAccount}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx2}); res != resultAccrued {
		t.Fatalf("day1 accrual 2 expected %q, got %q", resultAccrued, res)
	}

	lifetimeAfterDay1, err := state.LoyaltyProgramLifetimeAccrued(programID)
	if err != nil {
		t.Fatalf("lifetime meter error after day1: %v", err)
	}
	if lifetimeAfterDay1.String() != "200" {
		t.Fatalf("expected lifetime total 200 after day1, got %s", lifetimeAfterDay1.String())
	}
	// The day-scoped meter must independently show the same total for day1,
	// since both accruals happened on the same day.
	dayTotal1, err := state.LoyaltyProgramDailyTotalAccrued(programID, "2024-03-01")
	if err != nil {
		t.Fatalf("daily total error for day1: %v", err)
	}
	if dayTotal1.String() != "200" {
		t.Fatalf("expected day1 total 200, got %s", dayTotal1.String())
	}

	// Day 2 (a full day later, so the daily meter resets its own bucket, but
	// lifetime must keep accumulating on top of day1's total): one accrual of
	// 100.
	day2 := day1.Add(24 * time.Hour)
	ctx3 := &BaseRewardContext{From: toBytes(from), To: toBytes(merchant), Token: "NHB", Amount: big.NewInt(1000), Timestamp: day2, FromAccount: fromAccount}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx3}); res != resultAccrued {
		t.Fatalf("day2 accrual expected %q, got %q", resultAccrued, res)
	}

	lifetimeAfterDay2, err := state.LoyaltyProgramLifetimeAccrued(programID)
	if err != nil {
		t.Fatalf("lifetime meter error after day2: %v", err)
	}
	if lifetimeAfterDay2.String() != "300" {
		t.Fatalf("expected cumulative lifetime total 300 (200 + 100) after day2, got %s", lifetimeAfterDay2.String())
	}
	// The day2-scoped meter must show only day2's own 100, proving the daily
	// meter reset for the new day while lifetime did not.
	dayTotal2, err := state.LoyaltyProgramDailyTotalAccrued(programID, "2024-03-02")
	if err != nil {
		t.Fatalf("daily total error for day2: %v", err)
	}
	if dayTotal2.String() != "100" {
		t.Fatalf("expected day2 total 100 (not cumulative with day1), got %s", dayTotal2.String())
	}

	// Day 3: a third day of accrual to prove this isn't a two-sample fluke.
	day3 := day2.Add(24 * time.Hour)
	ctx4 := &BaseRewardContext{From: toBytes(from), To: toBytes(merchant), Token: "NHB", Amount: big.NewInt(1000), Timestamp: day3, FromAccount: fromAccount}
	if res := engine.ApplyProgramReward(state, &ProgramRewardContext{BaseRewardContext: ctx4}); res != resultAccrued {
		t.Fatalf("day3 accrual expected %q, got %q", resultAccrued, res)
	}
	lifetimeAfterDay3, err := state.LoyaltyProgramLifetimeAccrued(programID)
	if err != nil {
		t.Fatalf("lifetime meter error after day3: %v", err)
	}
	if lifetimeAfterDay3.String() != "400" {
		t.Fatalf("expected cumulative lifetime total 400 (300 + 100) after day3, got %s", lifetimeAfterDay3.String())
	}
}
