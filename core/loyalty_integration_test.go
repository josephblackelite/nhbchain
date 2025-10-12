package core

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	events "nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestLoyaltyEngineAppliesBaseAndProgramRewards(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	manager := nhbstate.NewManager(trie)

	if err := manager.RegisterToken("NHB", "Native", 18); err != nil {
		t.Fatalf("register NHB: %v", err)
	}
	if err := manager.RegisterToken("ZNHB", "ZapNHB", 18); err != nil {
		t.Fatalf("register ZNHB: %v", err)
	}

	var treasury [20]byte
	treasury[19] = 0x10
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		BaseBps:      50,
		MinSpend:     big.NewInt(100),
		CapPerTx:     big.NewInt(500),
		DailyCapUser: big.NewInt(1_000),
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	var from [20]byte
	from[19] = 0x20
	var merchant [20]byte
	merchant[19] = 0x30
	var paymaster [20]byte
	paymaster[19] = 0x40

	mustWriteAccount(t, sp, treasury, &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	mustWriteAccount(t, sp, paymaster, &types.Account{BalanceZNHB: big.NewInt(600), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)})
	mustWriteAccount(t, sp, from, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	mustWriteAccount(t, sp, merchant, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	registry := loyalty.NewRegistry(manager)
	bizID, err := registry.RegisterBusiness(merchant, "merchant")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	if err := registry.SetPaymaster(bizID, merchant, paymaster); err != nil {
		t.Fatalf("set paymaster: %v", err)
	}
	if err := registry.AddMerchantAddress(bizID, merchant); err != nil {
		t.Fatalf("add merchant: %v", err)
	}

	var programID loyalty.ProgramID
	programID[31] = 0x01
	program := &loyalty.Program{
		ID:           programID,
		Owner:        merchant,
		TokenSymbol:  "ZNHB",
		AccrualBps:   1200,
		MinSpendWei:  big.NewInt(0),
		CapPerTx:     big.NewInt(400),
		DailyCapUser: big.NewInt(800),
		StartTime:    uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		Active:       true,
	}
	if err := registry.CreateProgram(merchant, program); err != nil {
		t.Fatalf("create program: %v", err)
	}

	fromAcc, err := sp.getAccount(from[:])
	if err != nil {
		t.Fatalf("load from account: %v", err)
	}
	toAcc, err := sp.getAccount(merchant[:])
	if err != nil {
		t.Fatalf("load merchant account: %v", err)
	}

	amount := big.NewInt(2000)
	timestamp := time.Date(2024, 1, 15, 15, 0, 0, 0, time.UTC)
	ctx := &loyalty.BaseRewardContext{
		From:        append([]byte(nil), from[:]...),
		To:          append([]byte(nil), merchant[:]...),
		Token:       "NHB",
		Amount:      amount,
		Timestamp:   timestamp,
		FromAccount: fromAcc,
		ToAccount:   toAcc,
	}

	testState := &testLoyaltyState{StateProcessor: sp}
	sp.LoyaltyEngine.OnTransactionSuccess(testState, ctx)

	expectedBase := big.NewInt(10)     // 2000 * 50 / 10000
	expectedProgram := big.NewInt(240) // 2000 * 1200 / 10000 capped by 400 -> 240
	if ctx.FromAccount.BalanceZNHB.Cmp(expectedProgram) != 0 {
		t.Fatalf("expected user reward %s, got %s", expectedProgram.String(), ctx.FromAccount.BalanceZNHB.String())
	}

	treasuryAcc, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	if treasuryAcc.BalanceZNHB.String() != "1000" {
		t.Fatalf("expected treasury balance 1000, got %s", treasuryAcc.BalanceZNHB.String())
	}

	paymasterAcc, err := sp.getAccount(paymaster[:])
	if err != nil {
		t.Fatalf("load paymaster: %v", err)
	}
	if paymasterAcc.BalanceZNHB.String() != "360" {
		t.Fatalf("expected paymaster balance 360, got %s", paymasterAcc.BalanceZNHB.String())
	}

	dayKey := timestamp.UTC().Format("2006-01-02")
	baseAccrued, err := sp.LoyaltyBaseDailyAccrued(from[:], dayKey)
	if err != nil {
		t.Fatalf("base daily accrued: %v", err)
	}
	if baseAccrued.String() != expectedBase.String() {
		t.Fatalf("expected base daily %s, got %s", expectedBase.String(), baseAccrued.String())
	}
	programAccrued, err := sp.LoyaltyProgramDailyAccrued(programID, from[:], dayKey)
	if err != nil {
		t.Fatalf("program daily accrued: %v", err)
	}
	if programAccrued.String() != expectedProgram.String() {
		t.Fatalf("expected program daily %s, got %s", expectedProgram.String(), programAccrued.String())
	}

	pending := sp.BlockContext().PendingRewards
	if len(pending) != 1 {
		t.Fatalf("expected one pending reward, got %d", len(pending))
	}
	if pending[0].AmountZNHB.Cmp(expectedBase) != 0 {
		t.Fatalf("expected pending reward %s, got %s", expectedBase.String(), pending[0].AmountZNHB.String())
	}
	if pending[0].Payer != from {
		t.Fatalf("expected pending payer %x, got %x", from, pending[0].Payer)
	}
	if pending[0].Recipient != merchant {
		t.Fatalf("expected pending recipient %x, got %x", merchant, pending[0].Recipient)
	}

	evts := sp.Events()
	if len(evts) != 3 {
		t.Fatalf("expected three events, got %d", len(evts))
	}
	if evts[0].Type != events.TypeLoyaltyRewardProposed {
		t.Fatalf("expected reward proposed event first, got %s", evts[0].Type)
	}
	if evts[1].Type != "loyalty.base.accrued" {
		t.Fatalf("expected base accrued event second, got %s", evts[1].Type)
	}
	if evts[2].Type != "loyalty.program.accrued" {
		t.Fatalf("expected program accrued event third, got %s", evts[2].Type)
	}
}

func mustWriteAccount(t *testing.T, sp *StateProcessor, addr [20]byte, account *types.Account) {
	t.Helper()
	ensureAccountDefaults(account)
	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		t.Fatalf("balance overflow for account")
	}
	stateAcc := &gethtypes.StateAccount{
		Nonce:   account.Nonce,
		Balance: balance,
		Root:    common.BytesToHash(account.StorageRoot),
		CodeHash: func() []byte {
			if len(account.CodeHash) == 0 {
				return gethtypes.EmptyCodeHash.Bytes()
			}
			return common.CopyBytes(account.CodeHash)
		}(),
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := sp.writeStateAccount(addr[:], stateAcc); err != nil {
		t.Fatalf("write state account: %v", err)
	}
	meta := &accountMetadata{
		BalanceZNHB:        new(big.Int).Set(account.BalanceZNHB),
		Stake:              new(big.Int).Set(account.Stake),
		LockedZNHB:         new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:  new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:      new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:       new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex: new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex: new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		DelegatedValidator: func() []byte {
			if len(account.DelegatedValidator) == 0 {
				return nil
			}
			return append([]byte(nil), account.DelegatedValidator...)
		}(),
		Unbonding: func() []stakeUnbond {
			out := make([]stakeUnbond, len(account.PendingUnbonds))
			for i, entry := range account.PendingUnbonds {
				amount := big.NewInt(0)
				if entry.Amount != nil {
					amount = new(big.Int).Set(entry.Amount)
				}
				var validator []byte
				if len(entry.Validator) > 0 {
					validator = append([]byte(nil), entry.Validator...)
				}
				out[i] = stakeUnbond{
					ID:          entry.ID,
					Validator:   validator,
					Amount:      amount,
					ReleaseTime: entry.ReleaseTime,
				}
			}
			return out
		}(),
		UnbondingSeq:              account.NextUnbondingID,
		Username:                  account.Username,
		EngagementScore:           account.EngagementScore,
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
	}
	if err := sp.writeAccountMetadata(addr[:], meta); err != nil {
		t.Fatalf("write account metadata: %v", err)
	}
}

type testLoyaltyState struct {
	*StateProcessor
}

func (t *testLoyaltyState) PutAccount(addr []byte, account *types.Account) error {
	if t.StateProcessor == nil {
		return fmt.Errorf("nil state processor")
	}
	ensureAccountDefaults(account)
	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		return fmt.Errorf("balance overflow")
	}
	stateAcc := &gethtypes.StateAccount{
		Nonce:   account.Nonce,
		Balance: balance,
		Root:    common.BytesToHash(account.StorageRoot),
		CodeHash: func() []byte {
			if len(account.CodeHash) == 0 {
				return gethtypes.EmptyCodeHash.Bytes()
			}
			return common.CopyBytes(account.CodeHash)
		}(),
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := t.writeStateAccount(addr, stateAcc); err != nil {
		return err
	}
	meta := &accountMetadata{
		BalanceZNHB:               new(big.Int).Set(account.BalanceZNHB),
		Stake:                     new(big.Int).Set(account.Stake),
		LockedZNHB:                new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:         new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:             new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:              new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex:        new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex:        new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		Username:                  account.Username,
		EngagementScore:           account.EngagementScore,
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
	}
	return t.writeAccountMetadata(addr, meta)
}

func (t *testLoyaltyState) QueuePendingBaseReward(ctx *loyalty.BaseRewardContext, reward *big.Int) {
	if t.StateProcessor == nil {
		return
	}
	t.StateProcessor.QueuePendingBaseReward(ctx, reward)
}
