package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"

	"github.com/ethereum/go-ethereum/rlp"
)

// buybackAskPayload mirrors applyBuybackAsk's unexported payload shape --
// RLP encodes by field order/type, not by name, so this only needs to match
// structurally.
type buybackAskPayload struct {
	ZNHBAmount *big.Int
}

// buybackRefPricePayload (mirroring applyBuybackRefPrice's unexported
// payload shape) is defined once in node.go, since SubmitBuybackRefPrice
// needs the same encode-side struct in a non-test file -- reused here
// rather than redeclared to avoid two structurally-identical types drifting
// apart.

func newBuybackTestState(t *testing.T) (sp *StateProcessor, adminAddr, accrualAddr [20]byte, signerKey *crypto.PrivateKey) {
	t.Helper()
	sp = newRewardTestState(t)
	adminAddr = withRewardPoolAdminWallet(t, sp)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate signer key: %v", err)
	}
	var signerAddr [20]byte
	copy(signerAddr[:], key.PubKey().Address().Bytes())
	buybackCfg := buyback.Config{
		FeeShareBps:     2000,
		DiscountBps:     0,
		SafetyMarginBps: 0,
		SignerThreshold: 1,
		Signers:         [][20]byte{signerAddr},
	}
	if err := sp.SetBuybackConfig(buybackCfg); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	moduleAddr := deriveModuleAddress("module/tokenomics/buybackAccrual", crypto.NHBPrefix)
	sp.SetBuybackAccrualAddress(moduleAddr)
	copy(accrualAddr[:], moduleAddr.Bytes())
	return sp, adminAddr, accrualAddr, key
}

func fundBuybackAccrualNHB(t *testing.T, sp *StateProcessor, accrualAddr [20]byte, amount *big.Int) {
	t.Helper()
	acc, err := sp.getAccount(accrualAddr[:])
	if err != nil {
		t.Fatalf("load accrual account: %v", err)
	}
	acc.BalanceNHB = new(big.Int).Set(amount)
	if err := sp.setAccount(accrualAddr[:], acc); err != nil {
		t.Fatalf("fund accrual account: %v", err)
	}
	if err := nhbstate.NewManager(sp.Trie).ZNHBSetBuybackAccrualBalance(new(big.Int).Set(amount)); err != nil {
		t.Fatalf("set accrual ledger: %v", err)
	}
}

func seedBuybackSeller(t *testing.T, sp *StateProcessor, znhbBalance *big.Int) ([]byte, [20]byte) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sender := key.PubKey().Address().Bytes()
	var addr [20]byte
	copy(addr[:], sender)
	if err := sp.setAccount(sender, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: new(big.Int).Set(znhbBalance),
	}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}
	return sender, addr
}

func submitBuybackAsk(t *testing.T, sp *StateProcessor, sender []byte, amount *big.Int) {
	t.Helper()
	data, err := rlp.EncodeToBytes(buybackAskPayload{ZNHBAmount: amount})
	if err != nil {
		t.Fatalf("encode ask payload: %v", err)
	}
	tx := &types.Transaction{Type: types.TxTypeBuybackAsk, Data: data}
	senderAccount, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller account: %v", err)
	}
	if err := sp.applyBuybackAsk(tx, sender, senderAccount); err != nil {
		t.Fatalf("apply buyback ask: %v", err)
	}
}

func signBuybackRefPrice(t *testing.T, rp *buyback.ReferencePrice, key *crypto.PrivateKey) []byte {
	t.Helper()
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash reference price: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign reference price: %v", err)
	}
	return sig
}

func submitBuybackRefPrice(t *testing.T, sp *StateProcessor, epochNumber uint64, rateNum, rateDenom *big.Int, ts time.Time, key *crypto.PrivateKey) {
	t.Helper()
	rp := &buyback.ReferencePrice{Rate: new(big.Rat).SetFrac(rateNum, rateDenom), Epoch: epochNumber, Timestamp: ts}
	sig := signBuybackRefPrice(t, rp, key)
	data, err := rlp.EncodeToBytes(buybackRefPricePayload{
		RateNum:    rateNum,
		RateDenom:  rateDenom,
		Epoch:      epochNumber,
		Timestamp:  uint64(ts.Unix()),
		Signatures: [][]byte{sig},
	})
	if err != nil {
		t.Fatalf("encode ref price payload: %v", err)
	}
	tx := &types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: data}
	if err := sp.applyBuybackRefPrice(tx); err != nil {
		t.Fatalf("apply buyback ref price: %v", err)
	}
}

func weiZNHB(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

func TestBuybackAsk_EscrowsZNHBImmediately(t *testing.T) {
	sp, _, accrualAddr, _ := newBuybackTestState(t)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1000))

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	submitBuybackAsk(t, sp, sender, weiZNHB(100))
	sp.EndBlock()

	sellerAcc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if sellerAcc.BalanceZNHB.Cmp(weiZNHB(900)) != 0 {
		t.Fatalf("seller ZNHB balance = %s, want %s", sellerAcc.BalanceZNHB, weiZNHB(900))
	}
	escrowAcc, err := sp.getAccount(accrualAddr[:])
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	if escrowAcc.BalanceZNHB.Cmp(weiZNHB(100)) != 0 {
		t.Fatalf("escrow ZNHB balance = %s, want %s", escrowAcc.BalanceZNHB, weiZNHB(100))
	}
}

func TestBuybackSettlement_FullyFundedFillRecyclesIntoSalePool(t *testing.T) {
	sp, adminAddr, accrualAddr, signerKey := newBuybackTestState(t)
	sender, sellerAddr := seedBuybackSeller(t, sp, weiZNHB(1000))
	fundBuybackAccrualNHB(t, sp, accrualAddr, weiZNHB(1000)) // NHB budget, plenty

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.ZNHBSetCumulativeSaleDistributed(weiZNHB(500)); err != nil {
		t.Fatalf("seed cumulative sale distributed: %v", err)
	}
	salePoolBefore, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool: %v", err)
	}
	adminBefore, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("read admin account: %v", err)
	}

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	submitBuybackAsk(t, sp, sender, weiZNHB(100))
	// Curve tranche 0 spot price is exactly $0.05/ZNHB; a 1.00 reference
	// price with zero discount/safety-margin bps means the curve price
	// (the lesser of the two) is what actually clears.
	submitBuybackRefPrice(t, sp, 1, big.NewInt(1), big.NewInt(1), time.Unix(rewardBlockTimestamp1, 0).UTC(), signerKey)
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
		t.Fatalf("process block 2 (epoch finalize): %v", err)
	}

	wantPaid := weiZNHB(5) // 100 ZNHB * 0.05 NHB/ZNHB
	sellerAcc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if sellerAcc.BalanceZNHB.Cmp(weiZNHB(900)) != 0 {
		t.Fatalf("seller ZNHB balance = %s, want %s (no refund on a full fill)", sellerAcc.BalanceZNHB, weiZNHB(900))
	}
	if sellerAcc.BalanceNHB.Cmp(wantPaid) != 0 {
		t.Fatalf("seller NHB balance = %s, want %s", sellerAcc.BalanceNHB, wantPaid)
	}

	escrowAcc, err := sp.getAccount(accrualAddr[:])
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	if escrowAcc.BalanceZNHB.Sign() != 0 {
		t.Fatalf("escrow ZNHB balance should be zero after a full fill, got %s", escrowAcc.BalanceZNHB)
	}
	wantAccrualNHB := new(big.Int).Sub(weiZNHB(1000), wantPaid)
	if escrowAcc.BalanceNHB.Cmp(wantAccrualNHB) != 0 {
		t.Fatalf("accrual NHB balance = %s, want %s", escrowAcc.BalanceNHB, wantAccrualNHB)
	}

	adminAfter, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("read admin account after: %v", err)
	}
	wantAdminZNHB := new(big.Int).Add(adminBefore.BalanceZNHB, weiZNHB(100))
	if adminAfter.BalanceZNHB.Cmp(wantAdminZNHB) != 0 {
		t.Fatalf("admin ZNHB balance = %s, want %s (recycled fill should credit the admin wallet)", adminAfter.BalanceZNHB, wantAdminZNHB)
	}

	salePoolAfter, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		t.Fatalf("read sale pool after: %v", err)
	}
	wantSalePool := new(big.Int).Add(salePoolBefore, weiZNHB(100))
	if salePoolAfter.Cmp(wantSalePool) != 0 {
		t.Fatalf("sale pool balance = %s, want %s", salePoolAfter, wantSalePool)
	}

	cumulativeAfter, err := manager.ZNHBCumulativeSaleDistributed()
	if err != nil {
		t.Fatalf("read cumulative after: %v", err)
	}
	wantCumulative := weiZNHB(400) // 500 - 100
	if cumulativeAfter.Cmp(wantCumulative) != 0 {
		t.Fatalf("cumulative sale distributed = %s, want %s", cumulativeAfter, wantCumulative)
	}

	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant violated after settlement: %v", err)
	}

	asksLeft, err := manager.BuybackAsksForEpoch(1)
	if err != nil {
		t.Fatalf("read remaining asks: %v", err)
	}
	if len(asksLeft) != 0 {
		t.Fatalf("expected settled asks to be cleared, got %d remaining", len(asksLeft))
	}

	_ = sellerAddr
}

func TestBuybackSettlement_OversubscribedScalesDownProportionally(t *testing.T) {
	sp, _, accrualAddr, signerKey := newBuybackTestState(t)
	senderA, _ := seedBuybackSeller(t, sp, weiZNHB(3000))
	senderB, _ := seedBuybackSeller(t, sp, weiZNHB(1000))
	// Budget only covers 2,000 ZNHB at $0.05/ZNHB (100 NHB), against 4,000
	// ZNHB of total demand -- every seller should be filled at exactly half.
	fundBuybackAccrualNHB(t, sp, accrualAddr, weiZNHB(100))

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	submitBuybackAsk(t, sp, senderA, weiZNHB(3000))
	submitBuybackAsk(t, sp, senderB, weiZNHB(1000))
	submitBuybackRefPrice(t, sp, 1, big.NewInt(1), big.NewInt(1), time.Unix(rewardBlockTimestamp1, 0).UTC(), signerKey)
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
		t.Fatalf("process block 2 (epoch finalize): %v", err)
	}

	accA, err := sp.getAccount(senderA)
	if err != nil {
		t.Fatalf("load seller A: %v", err)
	}
	accB, err := sp.getAccount(senderB)
	if err != nil {
		t.Fatalf("load seller B: %v", err)
	}
	// Seller A started with 3000, escrowed all 3000, gets half (1500) back
	// as a refund plus payment for the other 1500 filled.
	if accA.BalanceZNHB.Cmp(weiZNHB(1500)) != 0 {
		t.Fatalf("seller A ZNHB balance = %s, want %s", accA.BalanceZNHB, weiZNHB(1500))
	}
	if accB.BalanceZNHB.Cmp(weiZNHB(500)) != 0 {
		t.Fatalf("seller B ZNHB balance = %s, want %s", accB.BalanceZNHB, weiZNHB(500))
	}
	totalPaid := new(big.Int).Add(accA.BalanceNHB, accB.BalanceNHB)
	if totalPaid.Cmp(weiZNHB(100)) > 0 {
		t.Fatalf("total NHB paid %s exceeds the 100 NHB budget", totalPaid)
	}
	if totalPaid.Sign() <= 0 {
		t.Fatalf("expected a positive total paid, got %s", totalPaid)
	}
}

func TestBuybackSettlement_NoReferencePriceRefundsAsksInFull(t *testing.T) {
	sp, _, accrualAddr, _ := newBuybackTestState(t)
	sender, _ := seedBuybackSeller(t, sp, weiZNHB(1000))
	fundBuybackAccrualNHB(t, sp, accrualAddr, weiZNHB(1000))

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	submitBuybackAsk(t, sp, sender, weiZNHB(100))
	// Deliberately no reference-price submission this epoch.
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, rewardBlockTimestamp1); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, rewardBlockTimestamp2); err != nil {
		t.Fatalf("process block 2 (epoch finalize): %v", err)
	}

	sellerAcc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if sellerAcc.BalanceZNHB.Cmp(weiZNHB(1000)) != 0 {
		t.Fatalf("seller ZNHB balance = %s, want %s (fully refunded, no reference price available)", sellerAcc.BalanceZNHB, weiZNHB(1000))
	}
	if sellerAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("seller should not have been paid anything, got %s NHB", sellerAcc.BalanceNHB)
	}

	escrowAcc, err := sp.getAccount(accrualAddr[:])
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	if escrowAcc.BalanceZNHB.Sign() != 0 {
		t.Fatalf("escrow ZNHB balance should be zero after a full refund, got %s", escrowAcc.BalanceZNHB)
	}
	if escrowAcc.BalanceNHB.Cmp(weiZNHB(1000)) != 0 {
		t.Fatalf("accrual NHB balance should be untouched, got %s want %s", escrowAcc.BalanceNHB, weiZNHB(1000))
	}
}

func TestBuybackAsk_RejectsAdminWalletAndValidatorBondedSellers(t *testing.T) {
	sp, adminAddr, _, _ := newBuybackTestState(t)

	sp.BeginBlock(1, time.Unix(rewardBlockTimestamp1, 0).UTC())
	data, err := rlp.EncodeToBytes(buybackAskPayload{ZNHBAmount: weiZNHB(10)})
	if err != nil {
		t.Fatalf("encode ask payload: %v", err)
	}
	tx := &types.Transaction{Type: types.TxTypeBuybackAsk, Data: data}

	adminAccount, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("load admin account: %v", err)
	}
	if err := sp.applyBuybackAsk(tx, adminAddr[:], adminAccount); err == nil {
		t.Fatalf("expected the admin wallet to be rejected as a buyback seller")
	}

	validatorSender, _ := seedBuybackSeller(t, sp, weiZNHB(1000))
	validatorAccount, err := sp.getAccount(validatorSender)
	if err != nil {
		t.Fatalf("load validator account: %v", err)
	}
	validatorAccount.Stake = weiZNHB(5000)
	if err := sp.setAccount(validatorSender, validatorAccount); err != nil {
		t.Fatalf("persist validator stake: %v", err)
	}
	validatorAccount, err = sp.getAccount(validatorSender)
	if err != nil {
		t.Fatalf("reload validator account: %v", err)
	}
	if err := sp.applyBuybackAsk(tx, validatorSender, validatorAccount); err == nil {
		t.Fatalf("expected a validator-bonded (staked) seller to be rejected")
	}
	sp.EndBlock()
}

func TestEffectiveBuybackConfig_GovernanceOverridesGenesisDefaults(t *testing.T) {
	sp, _, _, _ := newBuybackTestState(t)
	manager := nhbstate.NewManager(sp.Trie)

	genesisCfg, ok := sp.BuybackConfig()
	if !ok {
		t.Fatalf("expected a genesis buyback config")
	}
	if got := sp.effectiveBuybackConfig(manager); got.FeeShareBps != genesisCfg.FeeShareBps {
		t.Fatalf("expected effective config to match the genesis default before any governance vote: got %d want %d", got.FeeShareBps, genesisCfg.FeeShareBps)
	}

	// Simulate a passed policy.buybackParams proposal writing directly into
	// the param store, exactly like native/governance's applyBuybackParams
	// does at execution time.
	if err := manager.ParamStoreSet(governance.ParamKeyBuybackFeeShareBps, []byte("3500")); err != nil {
		t.Fatalf("set governed fee share: %v", err)
	}
	if err := manager.ParamStoreSet(governance.ParamKeyBuybackDiscountBps, []byte("100")); err != nil {
		t.Fatalf("set governed discount: %v", err)
	}

	got := sp.effectiveBuybackConfig(manager)
	if got.FeeShareBps != 3500 {
		t.Fatalf("effective FeeShareBps = %d, want 3500 (governed override)", got.FeeShareBps)
	}
	if got.DiscountBps != 100 {
		t.Fatalf("effective DiscountBps = %d, want 100 (governed override)", got.DiscountBps)
	}
	// SafetyMarginBps was never governed -- must still fall back to the
	// genesis default.
	if got.SafetyMarginBps != genesisCfg.SafetyMarginBps {
		t.Fatalf("effective SafetyMarginBps = %d, want ungoverned genesis default %d", got.SafetyMarginBps, genesisCfg.SafetyMarginBps)
	}
	// The signer quorum is never governable -- must be identical to genesis
	// regardless of any param store content.
	if got.SignerThreshold != genesisCfg.SignerThreshold || len(got.Signers) != len(genesisCfg.Signers) {
		t.Fatalf("effective signer quorum diverged from genesis: got threshold=%d signers=%d, want threshold=%d signers=%d", got.SignerThreshold, len(got.Signers), genesisCfg.SignerThreshold, len(genesisCfg.Signers))
	}
}
