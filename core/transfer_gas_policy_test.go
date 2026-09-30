package core

import (
	"math/big"
	"path/filepath"
	"testing"

	"nhbchain/config"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestTransferGasPolicyFreeTierAndThreshold(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()
	var collector [20]byte
	collector[19] = 0x55

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(1000),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
		FeeBps:            1_000, // 10%, chosen for clean test arithmetic
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(50_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	first := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(400),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := first.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign first transfer: %v", err)
	}
	if err := sp.ApplyTransaction(first); err != nil {
		t.Fatalf("apply first transfer: %v", err)
	}

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender after first transfer: %v", err)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(49_600)) != 0 {
		t.Fatalf("expected sender balance 49600 after free-tier transfer, got %s", updatedSender.BalanceNHB)
	}
	collectorAcc, err := sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector after first transfer: %v", err)
	}
	if collectorAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected collector to remain 0 during free tier, got %s", collectorAcc.BalanceNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var senderWallet [20]byte
	copy(senderWallet[:], senderAddr)
	status, err := manager.TransferGasSpendStatus(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(1000), "NHB")
	if err != nil {
		t.Fatalf("load spend status after first transfer: %v", err)
	}
	if status.Spent.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected recorded spend 400, got %s", status.Spent)
	}
	if !status.Eligible {
		t.Fatalf("expected sender to remain eligible below threshold")
	}

	if _, err := manager.TransferGasSpendAdd(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(600), big.NewInt(1000), "NHB"); err != nil {
		t.Fatalf("prime sender spend to threshold: %v", err)
	}

	second := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    1,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(200),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := second.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign second transfer: %v", err)
	}
	if err := sp.ApplyTransaction(second); err != nil {
		t.Fatalf("apply second transfer: %v", err)
	}

	updatedSender, err = sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender after second transfer: %v", err)
	}
	// The charge is 10% of the transfer value (20), not GasLimit*GasPrice
	// (21_000*1) -- see docs/issue30.md item 7b. The sender's self-declared
	// GasLimit/GasPrice on this transaction must not influence what's
	// actually charged.
	expectedSender := big.NewInt(49_600 - 200 - 20)
	if updatedSender.BalanceNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("expected sender balance %s after paid transfer, got %s", expectedSender, updatedSender.BalanceNHB)
	}
	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient after second transfer: %v", err)
	}
	if updatedRecipient.BalanceNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected recipient balance 600, got %s", updatedRecipient.BalanceNHB)
	}
	collectorAcc, err = sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector after second transfer: %v", err)
	}
	if collectorAcc.BalanceNHB.Cmp(big.NewInt(20)) != 0 {
		t.Fatalf("expected collector balance 20 (10%% of the 200 transfer), got %s", collectorAcc.BalanceNHB)
	}
}

func TestTransferGasPolicyThresholdCrossingTransferRemainsFree(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()
	var collector [20]byte
	collector[19] = 0x77

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(1000),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
		// Nonzero on purpose: proves the free-tier exemption actually
		// suppresses the fee below, rather than the transfer merely looking
		// free because no fee was configured at all.
		FeeBps: 1_000,
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(5_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	var senderWallet [20]byte
	copy(senderWallet[:], senderAddr)
	if _, err := manager.TransferGasSpendAdd(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(900), big.NewInt(1000), "NHB"); err != nil {
		t.Fatalf("prime spend status: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(200),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if updatedSender.BalanceNHB.Cmp(big.NewInt(4_800)) != 0 {
		t.Fatalf("expected threshold-crossing transfer to stay free, got %s", updatedSender.BalanceNHB)
	}
	status, err := manager.TransferGasSpendStatus(senderWallet, nhbstate.TransferGasWindowLifetime, sp.blockTimestamp(), big.NewInt(1000), "NHB")
	if err != nil {
		t.Fatalf("load final spend status: %v", err)
	}
	if status.Spent.Cmp(big.NewInt(1_100)) != 0 {
		t.Fatalf("expected recorded spend 1100, got %s", status.Spent)
	}
	if status.Eligible {
		t.Fatalf("expected sender to become ineligible after crossing threshold")
	}
}

// TestTransferGasPolicyDisabledStillCreditsFee is the regression test for
// audit PL-DC-08: applyEvmTransaction's NHB transfer path debited gasCost
// from the sender whenever `sponsorshipCtx==nil && !freeTransferGas`, but
// only credited it to the collector inside a branch additionally gated on
// `transferGasPolicy.Enabled` -- so a policy with Enabled=false and
// FeeBps>0 (exactly what buildTransferGasPolicyFromConfig produces whenever
// TransferFreeTierSpendWei<=0, independently of FeeBps) destroyed the fee
// instead of collecting it. The ZNHB transfer path never had this bug (it
// credits the collector whenever gasCost.Sign() > 0, regardless of
// Enabled); this test proves the NHB path now matches that same guarantee:
// the chain must never lose money it already took from a sender.
func TestTransferGasPolicyDisabledStillCreditsFee(t *testing.T) {
	sp := newStakingStateProcessor(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	senderAddr := senderKey.PubKey().Address().Bytes()
	recipientAddr := recipientKey.PubKey().Address().Bytes()
	var collector [20]byte
	collector[19] = 0x99

	// The exact config combination the code should never lose money under:
	// Enabled=false (as buildTransferGasPolicyFromConfig forces whenever
	// TransferFreeTierSpendWei<=0) with FeeBps>0 left configured.
	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           false,
		FreeSpendLimitWei: big.NewInt(0),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
		FeeBps:            1_000, // 10%, chosen for clean test arithmetic
	})

	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(50_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if err := sp.setAccount(recipientAddr, &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	if err := sp.setAccount(collector[:], &types.Account{BalanceNHB: big.NewInt(0)}); err != nil {
		t.Fatalf("seed collector: %v", err)
	}

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientAddr...),
		Value:    big.NewInt(1_000),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}

	// 10% of 1000 = 100. With Enabled=false, ComputeFee still returns this
	// (ComputeFee ignores Enabled) and freeTransferGas can never become
	// true (it is only evaluated when Enabled), so the debit path always
	// fires here -- the fee this test cares about is genuinely deducted.
	wantFee := big.NewInt(100)

	updatedSender, err := sp.getAccount(senderAddr)
	if err != nil {
		t.Fatalf("load sender: %v", err)
	}
	wantSender := new(big.Int).Sub(big.NewInt(50_000), new(big.Int).Add(big.NewInt(1_000), wantFee))
	if updatedSender.BalanceNHB.Cmp(wantSender) != 0 {
		t.Fatalf("expected sender balance %s (transfer + fee debited), got %s", wantSender, updatedSender.BalanceNHB)
	}

	collectorAcc, err := sp.getAccount(collector[:])
	if err != nil {
		t.Fatalf("load collector: %v", err)
	}
	// This is the assertion that catches PL-DC-08: before the fix, the
	// collector stayed at 0 here even though the sender was charged --
	// the fee vanished instead of being collected.
	if collectorAcc.BalanceNHB.Cmp(wantFee) != 0 {
		t.Fatalf("PL-DC-08 regression: expected collector to receive the exact fee debited from the sender (%s), got %s -- fee was lost instead of credited", wantFee, collectorAcc.BalanceNHB)
	}

	updatedRecipient, err := sp.getAccount(recipientAddr)
	if err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if updatedRecipient.BalanceNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected recipient to receive the full transfer value 1000, got %s", updatedRecipient.BalanceNHB)
	}
}

// TestBuildTransferGasPolicyFromConfigDefaultStaysEnabled proves the
// shipped default config (generated fresh by config.Load the same way
// cmd/nhb does for a brand-new node, real TransferFreeTierSpendWei > 0)
// never lands in the Enabled=false+FeeBps>0 combination that PL-DC-08 was
// about -- i.e. buildTransferGasPolicyFromConfig leaves Enabled=true for
// the config NHB validators actually ship with, with FeeBps still at its
// configured nonzero default.
func TestBuildTransferGasPolicyFromConfigDefaultStaysEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg, err := config.Load(path, config.WithKeystorePassphrase("regression-test-passphrase"))
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}

	var collector [20]byte
	collector[19] = 0xAA
	policy, err := buildTransferGasPolicyFromConfig(cfg.Global.Fees, collector)
	if err != nil {
		t.Fatalf("build transfer gas policy from default config: %v", err)
	}
	if !policy.Enabled {
		t.Fatalf("expected the shipped default config (TransferFreeTierSpendWei=%q) to leave the transfer gas policy Enabled, got Enabled=false", cfg.Global.Fees.TransferFreeTierSpendWei)
	}
	if policy.FeeBps == 0 {
		t.Fatalf("expected the shipped default config to configure a nonzero TransferFeeBps, got 0")
	}
	if policy.FreeSpendLimitWei == nil || policy.FreeSpendLimitWei.Sign() <= 0 {
		t.Fatalf("expected the shipped default config to configure a positive free-tier spend limit, got %v", policy.FreeSpendLimitWei)
	}
}

// TestComputeFeePerAssetRates proves ComputeFee selects each asset's own
// configured rate -- FeeBps for NHB, FeeBpsZNHB for ZNHB -- rather than
// sharing a single rate between them, and that asset matching is
// case-insensitive with an NHB fallback for anything else. See
// docs/issue30.md item 7b's NHB/ZNHB fee split.
func TestComputeFeePerAssetRates(t *testing.T) {
	policy := TransferGasPolicy{FeeBps: 20, FeeBpsZNHB: 10}
	amount := big.NewInt(250_000)

	nhbFee := policy.ComputeFee("NHB", amount)
	if nhbFee.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected NHB fee 500 (20bps of 250000), got %s", nhbFee)
	}
	znhbFee := policy.ComputeFee("ZNHB", amount)
	if znhbFee.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected ZNHB fee 250 (10bps of 250000), got %s", znhbFee)
	}
	if got := policy.ComputeFee("znhb", amount); got.Cmp(znhbFee) != 0 {
		t.Fatalf("expected lowercase \"znhb\" to match the ZNHB rate, got %s", got)
	}
	if got := policy.ComputeFee("", amount); got.Cmp(nhbFee) != 0 {
		t.Fatalf("expected empty asset to fall back to the NHB rate, got %s", got)
	}
	if got := policy.ComputeFee("USD", amount); got.Cmp(nhbFee) != 0 {
		t.Fatalf("expected unrecognized asset to fall back to the NHB rate, got %s", got)
	}
}

// TestFeeBpsForAsset proves the raw basis-points lookup used by
// fees_getTransferQuote (FeeBpsForAsset) resolves the same per-asset rate as
// ComputeFee.
func TestFeeBpsForAsset(t *testing.T) {
	policy := TransferGasPolicy{FeeBps: 20, FeeBpsZNHB: 10}
	if got := policy.FeeBpsForAsset("NHB"); got != 20 {
		t.Fatalf("expected 20 for NHB, got %d", got)
	}
	if got := policy.FeeBpsForAsset("ZNHB"); got != 10 {
		t.Fatalf("expected 10 for ZNHB, got %d", got)
	}
	if got := policy.FeeBpsForAsset("znhb"); got != 10 {
		t.Fatalf("expected case-insensitive ZNHB match, got %d", got)
	}
	if got := policy.FeeBpsForAsset("unknown"); got != 20 {
		t.Fatalf("expected fallback to the NHB rate, got %d", got)
	}
}
