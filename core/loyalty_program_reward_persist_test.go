package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// loyaltyRewardEnv wires a real state processor with an active 1200 bps
// program (capped at 400 per transaction), a registered business and a funded
// paymaster.
type loyaltyRewardEnv struct {
	sp        *StateProcessor
	senderKey *crypto.PrivateKey
	sender    [20]byte
	merchant  [20]byte
	paymaster [20]byte
	treasury  [20]byte
	programID loyalty.ProgramID
}

// newLoyaltyRewardEnv builds the environment. paymasterRole "sender" or
// "merchant" puts the paymaster on that address; anything else keeps it
// distinct.
func newLoyaltyRewardEnv(t *testing.T, paymasterRole string) *loyaltyRewardEnv {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
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

	env := &loyaltyRewardEnv{sp: sp}
	env.senderKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	copy(env.sender[:], env.senderKey.PubKey().Address().Bytes())
	env.merchant[19] = 0x30
	env.paymaster[19] = 0x40
	env.treasury[19] = 0x10
	switch paymasterRole {
	case "sender":
		env.paymaster = env.sender
	case "merchant":
		env.paymaster = env.merchant
	}

	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     env.treasury[:],
		BaseBps:      50,
		MinSpend:     big.NewInt(100),
		CapPerTx:     big.NewInt(500),
		DailyCapUser: big.NewInt(1_000),
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	seed := func(addr [20]byte, nhb, zn int64) {
		mustWriteAccount(t, sp, addr, &types.Account{BalanceNHB: big.NewInt(nhb), BalanceZNHB: big.NewInt(zn), Stake: big.NewInt(0)})
	}
	seed(env.treasury, 0, 1000)
	seed(env.sender, 10_000, 0)
	seed(env.merchant, 0, 0)
	// Seeding the paymaster last (and adding to a role address when they
	// coincide) keeps every role at a known ZNHB balance.
	switch paymasterRole {
	case "sender":
		seed(env.sender, 10_000, 600)
	case "merchant":
		seed(env.merchant, 0, 600)
	default:
		seed(env.paymaster, 0, 600)
	}

	registry := loyalty.NewRegistry(manager)
	bizID, err := registry.RegisterBusiness(env.merchant, "merchant")
	if err != nil {
		t.Fatalf("register business: %v", err)
	}
	if err := registry.SetPaymaster(bizID, env.paymaster, env.paymaster); err != nil {
		t.Fatalf("paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(bizID, env.merchant, env.paymaster); err != nil {
		t.Fatalf("set paymaster: %v", err)
	}
	if err := registry.AddMerchantAddress(bizID, env.merchant); err != nil {
		t.Fatalf("add merchant: %v", err)
	}
	env.programID[31] = 0x01
	if err := registry.CreateProgram(env.merchant, &loyalty.Program{
		ID:              env.programID,
		Owner:           env.merchant,
		TokenSymbol:     "ZNHB",
		AccrualBps:      1200,
		MinSpendWei:     big.NewInt(0),
		DailyCapProgram: big.NewInt(100_000),
		CapPerTx:        big.NewInt(400),
		Active:          true,
	}); err != nil {
		t.Fatalf("create program: %v", err)
	}
	return env
}

func (e *loyaltyRewardEnv) balances(t *testing.T, addr [20]byte) (nhb, znhb *big.Int) {
	t.Helper()
	acc, err := e.sp.getAccount(addr[:])
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	return acc.BalanceNHB, acc.BalanceZNHB
}

// totals sums NHB and ZNHB over every distinct touched address.
func (e *loyaltyRewardEnv) totals(t *testing.T) (nhb, znhb *big.Int) {
	t.Helper()
	nhb, znhb = big.NewInt(0), big.NewInt(0)
	seen := map[[20]byte]bool{}
	for _, addr := range [][20]byte{e.sender, e.merchant, e.paymaster, e.treasury} {
		if seen[addr] {
			continue
		}
		seen[addr] = true
		n, z := e.balances(t, addr)
		nhb.Add(nhb, n)
		znhb.Add(znhb, z)
	}
	return nhb, znhb
}

func (e *loyaltyRewardEnv) payMerchant(t *testing.T, amount int64) {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		To:       append([]byte(nil), e.merchant[:]...),
		Value:    big.NewInt(amount),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(e.senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := e.sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}
}

func assertBig(t *testing.T, label string, got *big.Int, want int64) {
	t.Helper()
	if got.Cmp(big.NewInt(want)) != 0 {
		t.Fatalf("%s: want %d, got %s", label, want, got)
	}
}

// TestNativeTransferProgramRewardIsPersistedToCustomer runs a real NHB
// transfer to a merchant with an active program and checks the stored
// balances: the customer receives exactly what the paymaster paid.
func TestNativeTransferProgramRewardIsPersistedToCustomer(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "")
	nhbBefore, znhbBefore := env.totals(t)

	env.payMerchant(t, 2000) // 2000 * 1200 / 10000 = 240, under the 400 cap

	_, senderZ := env.balances(t, env.sender)
	_, paymasterZ := env.balances(t, env.paymaster)
	assertBig(t, "customer ZNHB", senderZ, 240)
	assertBig(t, "paymaster ZNHB", paymasterZ, 360)

	nhbAfter, znhbAfter := env.totals(t)
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", znhbBefore, znhbAfter)
	}
	if nhbAfter.Cmp(nhbBefore) != 0 {
		t.Fatalf("NHB not conserved: before %s after %s", nhbBefore, nhbAfter)
	}
}

// TestNativeTransferProgramRewardWhenPaymasterIsMerchant covers the
// paymaster and the merchant being one address.
func TestNativeTransferProgramRewardWhenPaymasterIsMerchant(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "merchant")
	nhbBefore, znhbBefore := env.totals(t)

	env.payMerchant(t, 2000)

	_, senderZ := env.balances(t, env.sender)
	_, merchantZ := env.balances(t, env.merchant)
	assertBig(t, "customer ZNHB", senderZ, 240)
	assertBig(t, "merchant/paymaster ZNHB", merchantZ, 360)

	nhbAfter, znhbAfter := env.totals(t)
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", znhbBefore, znhbAfter)
	}
	if nhbAfter.Cmp(nhbBefore) != 0 {
		t.Fatalf("NHB not conserved: before %s after %s", nhbBefore, nhbAfter)
	}
}

// TestNativeTransferProgramRewardWhenPaymasterIsCustomer covers the paymaster
// paying itself: the movement nets to zero.
func TestNativeTransferProgramRewardWhenPaymasterIsCustomer(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "sender")
	nhbBefore, znhbBefore := env.totals(t)

	env.payMerchant(t, 2000)

	_, senderZ := env.balances(t, env.sender)
	assertBig(t, "customer/paymaster ZNHB", senderZ, 600)

	nhbAfter, znhbAfter := env.totals(t)
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", znhbBefore, znhbAfter)
	}
	if nhbAfter.Cmp(nhbBefore) != 0 {
		t.Fatalf("NHB not conserved: before %s after %s", nhbBefore, nhbAfter)
	}
}
