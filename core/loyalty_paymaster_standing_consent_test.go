package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

// PL-R5T-15 through the real transfer path: a reward is paid out of the business's
// paymaster only if that wallet is the owner's own or has a standing record, from
// the configured height.

const consentHeight = 100

func (e *loyaltyRewardEnv) payAt(t *testing.T, height uint64, nonce uint64, amount int64) {
	t.Helper()
	e.sp.BeginBlock(height, time.Unix(1_700_000_000, 0).UTC())
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       append([]byte(nil), e.merchant[:]...),
		Value:    big.NewInt(amount),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(e.senderKey.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := e.sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}
}

func (e *loyaltyRewardEnv) znhb(t *testing.T, addr [20]byte) int64 {
	t.Helper()
	_, z := e.balances(t, addr)
	return z.Int64()
}

func TestLoyaltyStandingConsentOffPaysALegacyAssignmentAtEveryHeight(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "")
	// the setting is 0
	env.payAt(t, 1_000_000, 0, 2000)
	if got := env.znhb(t, env.sender); got != 240 {
		t.Fatalf("setting off: customer ZNHB %d, want 240", got)
	}
}

func TestLoyaltyStandingConsentOnSkipsALegacyAssignmentFromTheHeight(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "")
	env.sp.SetLoyaltyPaymasterConsent(consentHeight)

	// Below the height the reward is paid as before.
	env.payAt(t, consentHeight-1, 0, 2000)
	if got := env.znhb(t, env.sender); got != 240 {
		t.Fatalf("below the height: customer ZNHB %d, want 240", got)
	}
	paymasterBefore := env.znhb(t, env.paymaster)

	// At the height the assignment made without a record stops paying; the transfer
	// itself still succeeds.
	env.payAt(t, consentHeight, 1, 2000)
	if got := env.znhb(t, env.sender); got != 240 {
		t.Fatalf("at the height: customer ZNHB %d, want it unchanged at 240", got)
	}
	if got := env.znhb(t, env.paymaster); got != paymasterBefore {
		t.Fatalf("at the height: paymaster ZNHB %d, want it unchanged at %d", got, paymasterBefore)
	}
	senderNHB, _ := env.balances(t, env.sender)
	if senderNHB.Cmp(big.NewInt(10_000-2000-2000)) > 0 {
		t.Fatalf("the second transfer did not move the customer's NHB: %s", senderNHB)
	}
}

func TestLoyaltyStandingConsentOnPaysTheOwnersOwnWalletWithoutARecord(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "merchant") // the paymaster is the owner's own wallet
	env.sp.SetLoyaltyPaymasterConsent(consentHeight)
	env.payAt(t, consentHeight+10, 0, 2000)
	if got := env.znhb(t, env.sender); got != 240 {
		t.Fatalf("owner's own wallet: customer ZNHB %d, want 240", got)
	}
}

func TestLoyaltyStandingConsentOnPaysAgainOnceTheAssignmentIsReaffirmedWithConsent(t *testing.T) {
	env := newLoyaltyRewardEnv(t, "")
	env.sp.SetLoyaltyPaymasterConsent(consentHeight)
	env.sp.BeginBlock(consentHeight, time.Unix(1_700_000_000, 0).UTC())

	manager := nhbstate.NewManager(env.sp.Trie)
	var admin [20]byte
	admin[19] = 0x50 // the loyalty admin newLoyaltyRewardEnv created
	registry := env.sp.loyaltyRegistry()
	businessID := mustBusinessOf(t, env)

	// The consent the first assignment used is gone; the admin cannot re-affirm.
	if err := registry.SetPaymaster(businessID, admin, env.paymaster); !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("re-affirm without new consent: err = %v, want ErrPaymasterConsent", err)
	}
	if ok, err := registry.PaymasterAssignmentRecorded(businessID, env.paymaster); err != nil || ok {
		t.Fatalf("a refused re-affirmation recorded consent (ok=%v err=%v)", ok, err)
	}
	if err := registry.SetPaymaster(businessID, env.paymaster, env.paymaster); err != nil {
		t.Fatalf("new opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, admin, env.paymaster); err != nil {
		t.Fatalf("re-affirm: %v", err)
	}
	_ = manager

	env.payAt(t, consentHeight+1, 0, 2000)
	if got := env.znhb(t, env.sender); got != 240 {
		t.Fatalf("after re-affirmation: customer ZNHB %d, want 240", got)
	}
}

// The treasury case of the ledger item: a legacy assignment naming the admin
// wallet debits the Reward Pool with it. From the height it does not.
func TestLoyaltyStandingConsentOnStopsAnAssignmentThatNamesTheAdminWallet(t *testing.T) {
	env := adminRewardEnv(t, "", "paymaster", true)
	env.sp.SetLoyaltyPaymasterConsent(consentHeight)
	poolBefore := env.rewardPool(t)
	_, znhbBefore := env.totals(t)

	env.payAt(t, consentHeight, 0, 2000)

	if err := env.sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
	assertBig(t, "customer ZNHB", mustZ(t, env, env.sender), 0)
	assertBig(t, "admin ZNHB", mustZ(t, env, env.paymaster), 10_000)
	if env.rewardPool(t).Cmp(poolBefore) != 0 {
		t.Fatalf("the Reward Pool moved: before %s after %s", poolBefore, env.rewardPool(t))
	}
	_, znhbAfter := env.totals(t)
	if znhbAfter.Cmp(znhbBefore) != 0 {
		t.Fatalf("ZNHB not conserved")
	}
}

func mustZ(t *testing.T, e *loyaltyRewardEnv, addr [20]byte) *big.Int {
	t.Helper()
	_, z := e.balances(t, addr)
	return z
}

func mustBusinessOf(t *testing.T, e *loyaltyRewardEnv) loyalty.BusinessID {
	t.Helper()
	ids, err := e.sp.LoyaltyBusinessesByOwner(e.merchant)
	if err != nil || len(ids) != 1 {
		t.Fatalf("business lookup: %v %v", ids, err)
	}
	return ids[0]
}

// Through the transaction: the paymaster leaves, and a merchant address removes
// itself, only from the height.
func TestLoyaltyStandingConsentWithdrawalAndMerchantSelfRemovalNeedTheHeight(t *testing.T) {
	for _, active := range []bool{false, true} {
		sp := newLoyaltyTestProcessor(t)
		if active {
			sp.SetLoyaltyPaymasterConsent(consentHeight)
		}
		sp.BeginBlock(consentHeight, time.Unix(1_700_000_000, 0).UTC())
		manager := nhbstate.NewManager(sp.Trie)
		_, owner := newLoyaltyTestKey(t, sp)
		adminAddr, admin := newLoyaltyTestKey(t, sp)
		walletAddr, wallet := newLoyaltyTestKey(t, sp)
		merchantAddr, merchant := newLoyaltyTestKey(t, sp)
		if err := manager.SetRole(RoleLoyaltyAdmin, adminAddr.Bytes()); err != nil {
			t.Fatalf("grant admin: %v", err)
		}
		businessID := loyaltyCreateBusinessTx(t, sp, owner, "Acme")
		idHex := "0x" + hexString(businessID[:])
		send := func(txType types.TxType, sender []byte, payload map[string]string) error {
			tx := &types.Transaction{Type: txType, Data: mustJSON(t, payload)}
			acc, _ := sp.getAccount(sender)
			return sp.handleNativeTransaction(tx, sender, acc)
		}
		if err := send(types.TxTypeLoyaltySetPaymaster, wallet, map[string]string{"businessId": idHex, "paymaster": walletAddr.String()}); err != nil {
			t.Fatalf("wallet opt-in: %v", err)
		}
		if err := send(types.TxTypeLoyaltySetPaymaster, admin, map[string]string{"businessId": idHex, "paymaster": walletAddr.String()}); err != nil {
			t.Fatalf("admin assigns: %v", err)
		}
		if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
			t.Fatalf("add merchant: %v", err)
		}

		// The merchant address removes itself.
		err := send(types.TxTypeLoyaltyRemoveMerchant, merchant, map[string]string{"businessId": idHex, "merchant": merchantAddr.String()})
		if active && err != nil {
			t.Fatalf("active: merchant self-removal: %v", err)
		}
		if !active && err == nil {
			t.Fatalf("off: merchant self-removal must stay refused")
		}
		// Another stranger still cannot remove it.
		strangerAddr, stranger := newLoyaltyTestKey(t, sp)
		_ = strangerAddr
		if err := loyaltyAddMerchantTx(t, sp, owner, businessID, merchantAddr); err != nil {
			t.Fatalf("re-add merchant: %v", err)
		}
		if err := send(types.TxTypeLoyaltyRemoveMerchant, stranger, map[string]string{"businessId": idHex, "merchant": merchantAddr.String()}); err == nil {
			t.Fatalf("a stranger removed a merchant")
		}

		// The paymaster withdraws itself.
		err = send(types.TxTypeLoyaltySetPaymaster, wallet, map[string]string{"businessId": idHex})
		if active && err != nil {
			t.Fatalf("active: paymaster withdrawal: %v", err)
		}
		if !active && !errors.Is(err, loyalty.ErrUnauthorized) {
			t.Fatalf("off: paymaster withdrawal err = %v, want ErrUnauthorized", err)
		}
	}
}

func TestLoyaltyStandingConsentIsCarriedByCopy(t *testing.T) {
	sp := newLoyaltyTestProcessor(t)
	sp.SetLoyaltyPaymasterConsent(consentHeight)
	dup, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.loyaltyPaymasterConsentFromHeight != consentHeight {
		t.Fatalf("Copy dropped the setting: %d", dup.loyaltyPaymasterConsentFromHeight)
	}
}

func TestNodeLoyaltyStandingConsent_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetLoyaltyPaymasterConsent(9)
	node.stateMu.Lock()
	gotH := node.state.loyaltyPaymasterConsentFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.loyaltyPaymasterConsentFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt processor at %d, want 9 and 9", gotH, rebuiltH)
	}
}

var _ = crypto.GeneratePrivateKey
