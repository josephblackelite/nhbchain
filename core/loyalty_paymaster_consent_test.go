package core

import (
	"errors"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

// TestLoyaltyPaymasterMustConsentToBeNamed replays the drain a business owner
// could run against any account: name the victim as paymaster, create a rich
// program, and pay the merchant address from a second account. The victim
// never signs anything, so the owner's assignment must be refused
// (ErrPaymasterConsentRequired: an owner names only its own wallet), a loyalty
// admin's must be refused too until the victim has signed its own opt-in
// (ErrPaymasterConsent), and the victim ZNHB must stay put.
func TestLoyaltyPaymasterMustConsentToBeNamed(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.RegisterToken("ZNHB", "ZapNHB", 18); err != nil {
		t.Fatalf("register ZNHB: %v", err)
	}

	victimKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	victim := toAddress(victimKey)
	customerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	customer := toAddress(customerKey)
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	merchant := toAddress(merchantKey)
	mustWriteAccount(t, sp, victim, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000_000), Stake: big.NewInt(0)})
	mustWriteAccount(t, sp, customer, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	mustWriteAccount(t, sp, merchant, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	businessID := loyaltyCreateBusinessTx(t, sp, merchant[:], "drain")
	merchantAddr := crypto.MustNewAddress(crypto.NHBPrefix, merchant[:])
	if err := loyaltyAddMerchantTx(t, sp, merchant[:], businessID, merchantAddr); err != nil {
		t.Fatalf("add merchant: %v", err)
	}
	idHex := "0x" + hexString(businessID[:])
	setPaymaster := func(sender []byte, paymaster crypto.Address) error {
		tx := &types.Transaction{Type: types.TxTypeLoyaltySetPaymaster, Data: mustJSON(t, map[string]string{"businessId": idHex, "paymaster": paymaster.String()})}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		return sp.handleNativeTransaction(tx, sender, acc)
	}
	victimAddr := victimKey.PubKey().Address()
	if err := setPaymaster(merchant[:], victimAddr); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("the owner naming another wallet: want ErrPaymasterConsentRequired, got %v", err)
	}
	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	admin := toAddress(adminKey)
	mustWriteAccount(t, sp, admin, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	if err := manager.SetRole(RoleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}
	if err := setPaymaster(admin[:], victimAddr); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("a loyalty admin naming a non-consenting paymaster: want ErrPaymasterConsent, got %v", err)
	}
	business, _, err := sp.LoyaltyBusinessByID(businessID)
	if err != nil {
		t.Fatalf("load business: %v", err)
	}
	var zero [20]byte
	if business.Paymaster != zero {
		t.Fatalf("paymaster must stay unset without consent")
	}

	zeroCap := "0"
	huge := "1000000000000000000000000000000"
	prog := loyaltyProgramPayload{
		BusinessID: idHex, ID: mustProgramIDHex(t, "drain-prog"), Pool: merchantAddr.String(), TokenSymbol: "ZNHB",
		AccrualBps: 100000, DailyCapProgram: &huge, CapPerTx: &zeroCap,
	}
	create := &types.Transaction{Type: types.TxTypeCreateLoyaltyProgram, Data: mustJSON(t, prog)}
	acc, _ := sp.getAccount(merchant[:])
	if err := sp.handleNativeTransaction(create, merchant[:], acc); err != nil {
		t.Fatalf("create program: %v", err)
	}

	pay := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeTransfer, To: merchant[:], Value: big.NewInt(1000), GasLimit: 21000, GasPrice: big.NewInt(1)}
	if err := pay.Sign(customerKey.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := sp.ApplyTransaction(pay); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}
	victimAcc, _ := sp.getAccount(victim[:])
	customerAcc, _ := sp.getAccount(customer[:])
	assertBig(t, "victim ZNHB", victimAcc.BalanceZNHB, 1_000_000)
	assertBig(t, "customer ZNHB", customerAcc.BalanceZNHB, 0)

	// Once the victim signs its own opt-in a loyalty admin may name it; the
	// owner still may not.
	if err := setPaymaster(victim[:], victimAddr); err != nil {
		t.Fatalf("victim opt-in: %v", err)
	}
	if err := setPaymaster(merchant[:], victimAddr); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("the owner naming a consenting wallet: want ErrPaymasterConsentRequired, got %v", err)
	}
	if err := setPaymaster(admin[:], victimAddr); err != nil {
		t.Fatalf("loyalty admin naming a consenting paymaster: %v", err)
	}
}

// TestClassifyProposalErrorPaymasterConsent registers the consent sentinel as
// an ordinary per-transaction skip, so a refused assignment never aborts a
// block build.
func TestClassifyProposalErrorPaymasterConsent(t *testing.T) {
	if got := classifyProposalError(loyalty.ErrPaymasterConsent); got != proposalDispositionSkip {
		t.Fatalf("want skip, got %v", got)
	}
	wrapped := errors.Join(errors.New("apply"), loyalty.ErrPaymasterConsent)
	if got := classifyProposalError(wrapped); got != proposalDispositionSkip {
		t.Fatalf("wrapped: want skip, got %v", got)
	}
}

// adminRewardEnv is the loyalty reward environment with one of its role
// addresses installed as the admin/treasury wallet (holding 10000 ZNHB), and
// optionally the ZNHB pools bootstrapped from that balance.
func adminRewardEnv(t *testing.T, paymasterRole, adminRole string, bootstrap bool) *loyaltyRewardEnv {
	t.Helper()
	env := newLoyaltyRewardEnv(t, paymasterRole)
	var admin [20]byte
	switch adminRole {
	case "sender":
		admin = env.sender
	case "merchant":
		admin = env.merchant
	default:
		admin = env.paymaster
	}
	acc, err := env.sp.getAccount(admin[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	acc.BalanceZNHB = big.NewInt(10_000)
	mustWriteAccount(t, env.sp, admin, acc)
	env.sp.SetAdminWallet(admin, true)
	if bootstrap {
		if err := env.sp.EnsureZNHBPoolsBootstrapped(); err != nil {
			t.Fatalf("bootstrap pools: %v", err)
		}
		if err := env.sp.CheckZNHBSupplyInvariant(); err != nil {
			t.Fatalf("invariant before transfer: %v", err)
		}
	}
	return env
}

func (e *loyaltyRewardEnv) rewardPool(t *testing.T) *big.Int {
	t.Helper()
	pool, err := nhbstate.NewManager(e.sp.Trie).ZNHBRewardPoolBalance()
	if err != nil {
		t.Fatalf("reward pool: %v", err)
	}
	return pool
}

// TestProgramRewardKeepsZNHBSupplyInvariantWithAdminWallet pays a program
// reward with the admin/treasury wallet on every side of it. Each case checks
// the block-level supply invariant, that ZNHB and NHB are conserved across the
// touched accounts, and that the Reward Pool moves exactly with the admin
// wallet.
func TestProgramRewardKeepsZNHBSupplyInvariantWithAdminWallet(t *testing.T) {
	cases := []struct {
		name          string
		paymasterRole string
		adminRole     string
		wantPoolDelta int64
	}{
		{"admin is paymaster", "", "paymaster", -240},
		{"admin is customer", "", "sender", 240},
		{"admin is merchant and paymaster", "merchant", "merchant", -240},
		{"admin is customer and paymaster", "sender", "sender", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := adminRewardEnv(t, tc.paymasterRole, tc.adminRole, true)
			nhbBefore, znhbBefore := env.totals(t)
			poolBefore := env.rewardPool(t)

			env.payMerchant(t, 2000)

			if err := env.sp.CheckZNHBSupplyInvariant(); err != nil {
				t.Fatalf("supply invariant broken by the reward: %v", err)
			}
			nhbAfter, znhbAfter := env.totals(t)
			if znhbAfter.Cmp(znhbBefore) != 0 {
				t.Fatalf("ZNHB not conserved: before %s after %s", znhbBefore, znhbAfter)
			}
			if nhbAfter.Cmp(nhbBefore) != 0 {
				t.Fatalf("NHB not conserved: before %s after %s", nhbBefore, nhbAfter)
			}
			poolDelta := new(big.Int).Sub(env.rewardPool(t), poolBefore)
			assertBig(t, "reward pool delta", poolDelta, tc.wantPoolDelta)
		})
	}
}

// TestProgramRewardFromAdminWalletSkipsWhenRewardPoolIsShort covers a paymaster
// that is the admin wallet while the Reward Pool cannot cover the reward: the
// reward is skipped whole, no balance or pool moves, and the transfer itself
// still succeeds.
func TestProgramRewardFromAdminWalletSkipsWhenRewardPoolIsShort(t *testing.T) {
	env := adminRewardEnv(t, "", "paymaster", true)
	manager := nhbstate.NewManager(env.sp.Trie)
	// Keep sale + reward equal to the admin balance, but leave the reward pool
	// below the 240 reward.
	if err := manager.ZNHBSetRewardPoolBalance(big.NewInt(100)); err != nil {
		t.Fatalf("set reward pool: %v", err)
	}
	if err := manager.ZNHBSetSalePoolBalance(big.NewInt(9_900)); err != nil {
		t.Fatalf("set sale pool: %v", err)
	}
	if err := env.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant before transfer: %v", err)
	}
	_, znhbBefore := env.totals(t)

	env.payMerchant(t, 2000)

	if err := env.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant broken: %v", err)
	}
	_, senderZ := env.balances(t, env.sender)
	_, adminZ := env.balances(t, env.paymaster)
	assertBig(t, "customer ZNHB", senderZ, 0)
	assertBig(t, "admin ZNHB", adminZ, 10_000)
	assertBig(t, "reward pool", env.rewardPool(t), 100)
	_, znhbAfter := env.totals(t)
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", znhbBefore, znhbAfter)
	}
}

// TestProgramRewardLeavesPoolsAloneBeforeBootstrap checks that a chain whose
// pools are not bootstrapped yet is not given a Reward Pool figure by the
// reward: bootstrap splits the live admin balance later.
func TestProgramRewardLeavesPoolsAloneBeforeBootstrap(t *testing.T) {
	env := adminRewardEnv(t, "", "paymaster", false)
	env.payMerchant(t, 2000)

	_, senderZ := env.balances(t, env.sender)
	_, adminZ := env.balances(t, env.paymaster)
	assertBig(t, "customer ZNHB", senderZ, 240)
	assertBig(t, "admin ZNHB", adminZ, 9_760)
	assertBig(t, "reward pool", env.rewardPool(t), 0)
}
