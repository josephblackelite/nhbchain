package pos

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

type memoryLifecycleState struct {
	kv       map[string][]byte
	accounts map[string]*types.Account
}

func newMemoryLifecycleState() *memoryLifecycleState {
	return &memoryLifecycleState{
		kv:       make(map[string][]byte),
		accounts: make(map[string]*types.Account),
	}
}

func (m *memoryLifecycleState) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := m.kv[string(key)]
	if !ok {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(encoded, out); err != nil {
		return false, err
	}
	return true, nil
}

func (m *memoryLifecycleState) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.kv[string(key)] = encoded
	return nil
}

func (m *memoryLifecycleState) KVDelete(key []byte) error {
	delete(m.kv, string(key))
	return nil
}

func (m *memoryLifecycleState) GetAccount(addr []byte) (*types.Account, error) {
	if acc, ok := m.accounts[string(addr)]; ok {
		return cloneAccount(acc), nil
	}
	return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}, nil
}

func (m *memoryLifecycleState) PutAccount(addr []byte, account *types.Account) error {
	m.accounts[string(addr)] = cloneAccount(account)
	return nil
}

func TestLifecyclePartialCapture(t *testing.T) {
	state := newMemoryLifecycleState()
	var payer, merchant [20]byte
	payer[1] = 0x01
	merchant[2] = 0x02
	state.accounts[string(payer[:])] = &types.Account{BalanceZNHB: big.NewInt(1_000), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	state.accounts[string(merchant[:])] = &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}

	engine := NewLifecycle(state)
	base := time.Unix(1_700_000_000, 0)
	engine.SetNowFunc(func() time.Time { return base })

	auth, err := engine.Authorize(payer, merchant, big.NewInt(600), uint64(base.Add(time.Hour).Unix()), []byte("intent-001"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	payerAcc, _ := state.GetAccount(payer[:])
	if payerAcc.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("payer balance after auth: got %s want 400", payerAcc.BalanceZNHB)
	}
	if payerAcc.LockedZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("payer locked after auth: got %s want 600", payerAcc.LockedZNHB)
	}

	engine.SetNowFunc(func() time.Time { return base.Add(10 * time.Minute) })
	updated, err := engine.Capture(auth.ID, big.NewInt(250))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if updated.Status != AuthorizationStatusCaptured {
		t.Fatalf("status after capture: got %v want captured", updated.Status)
	}
	if updated.CapturedAmount.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("captured amount: got %s want 250", updated.CapturedAmount)
	}
	if updated.RefundedAmount.Cmp(big.NewInt(350)) != 0 {
		t.Fatalf("refunded amount: got %s want 350", updated.RefundedAmount)
	}
	payerAcc, _ = state.GetAccount(payer[:])
	if payerAcc.LockedZNHB.Sign() != 0 {
		t.Fatalf("payer locked after capture: got %s want 0", payerAcc.LockedZNHB)
	}
	if payerAcc.BalanceZNHB.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("payer balance after capture: got %s want 750", payerAcc.BalanceZNHB)
	}
	merchantAcc, _ := state.GetAccount(merchant[:])
	if merchantAcc.BalanceZNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("merchant balance after capture: got %s want 250", merchantAcc.BalanceZNHB)
	}
}

func TestLifecycleDoubleCaptureRejected(t *testing.T) {
	state := newMemoryLifecycleState()
	var payer, merchant [20]byte
	payer[3] = 0x44
	merchant[4] = 0x55
	state.accounts[string(payer[:])] = &types.Account{BalanceZNHB: big.NewInt(500), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	state.accounts[string(merchant[:])] = &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	engine := NewLifecycle(state)
	now := time.Unix(1_800_000_000, 0)
	engine.SetNowFunc(func() time.Time { return now })

	auth, err := engine.Authorize(payer, merchant, big.NewInt(300), uint64(now.Add(time.Hour).Unix()), nil)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	engine.SetNowFunc(func() time.Time { return now.Add(5 * time.Minute) })
	if _, err := engine.Capture(auth.ID, big.NewInt(300)); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if _, err := engine.Capture(auth.ID, big.NewInt(10)); !errors.Is(err, errAuthorizationConsumed) {
		t.Fatalf("double capture error: got %v want %v", err, errAuthorizationConsumed)
	}
}

func TestLifecycleAutoVoidOnExpiry(t *testing.T) {
	state := newMemoryLifecycleState()
	var payer, merchant [20]byte
	payer[5] = 0x99
	merchant[6] = 0x77
	state.accounts[string(payer[:])] = &types.Account{BalanceZNHB: big.NewInt(800), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	state.accounts[string(merchant[:])] = &types.Account{BalanceZNHB: big.NewInt(0), BalanceNHB: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	engine := NewLifecycle(state)
	base := time.Unix(1_900_000_000, 0)
	engine.SetNowFunc(func() time.Time { return base })

	auth, err := engine.Authorize(payer, merchant, big.NewInt(500), uint64(base.Add(15*time.Minute).Unix()), []byte("intent-exp"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	engine.SetNowFunc(func() time.Time { return base.Add(20 * time.Minute) })
	updated, err := engine.Capture(auth.ID, big.NewInt(200))
	if !errors.Is(err, errAuthorizationExpired) {
		t.Fatalf("capture after expiry error: got %v want %v", err, errAuthorizationExpired)
	}
	if updated.Status != AuthorizationStatusExpired {
		t.Fatalf("status after auto-void: got %v want expired", updated.Status)
	}
	if updated.RefundedAmount.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("refunded after auto-void: got %s want 500", updated.RefundedAmount)
	}
	if updated.VoidReason != "expired" {
		t.Fatalf("void reason: got %q want %q", updated.VoidReason, "expired")
	}
	payerAcc, _ := state.GetAccount(payer[:])
	if payerAcc.LockedZNHB.Sign() != 0 {
		t.Fatalf("payer locked after auto-void: got %s want 0", payerAcc.LockedZNHB)
	}
	if payerAcc.BalanceZNHB.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("payer balance after auto-void: got %s want 800", payerAcc.BalanceZNHB)
	}
}
