package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/fees"
)

func TestApplyTransactionFeeSweepsBuybackShareWhenConfigured(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 0x10
	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        500,
				OwnerWallet:           owner,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB: {MDRBasisPoints: 500, OwnerWallet: owner},
				},
			},
		},
	})

	var signer [20]byte
	signer[0] = 0x99
	buybackCfg := buyback.Config{
		FeeShareBps:     2000, // 20%
		DiscountBps:     500,
		SafetyMarginBps: 500,
		SignerThreshold: 1,
		Signers:         [][20]byte{signer},
	}
	if err := sp.SetBuybackConfig(buybackCfg); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	accrualAddr := deriveModuleAddress("module/tokenomics/buybackAccrual", crypto.NHBPrefix)
	sp.SetBuybackAccrualAddress(accrualAddr)

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000)}
	sender := make([]byte, 20)
	sender[19] = 0xAA

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(500))
	expectedFee.Div(expectedFee, big.NewInt(10_000)) // 500 total fee

	expectedBuybackShare := new(big.Int).Mul(expectedFee, big.NewInt(2000))
	expectedBuybackShare.Div(expectedBuybackShare, big.NewInt(10_000)) // 100
	expectedOwnerShare := new(big.Int).Sub(expectedFee, expectedBuybackShare)

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedOwnerShare) != 0 {
		t.Fatalf("owner wallet balance mismatch: got %v want %v", routeAcc.BalanceNHB, expectedOwnerShare)
	}

	accrualAcc, err := sp.getAccount(accrualAddr.Bytes())
	if err != nil {
		t.Fatalf("load buyback accrual account: %v", err)
	}
	if accrualAcc.BalanceNHB == nil || accrualAcc.BalanceNHB.Cmp(expectedBuybackShare) != 0 {
		t.Fatalf("buyback accrual balance mismatch: got %v want %v", accrualAcc.BalanceNHB, expectedBuybackShare)
	}

	ledgerBalance, err := nhbstate.NewManager(sp.Trie).ZNHBBuybackAccrualBalance()
	if err != nil {
		t.Fatalf("read buyback accrual ledger: %v", err)
	}
	if ledgerBalance.Cmp(expectedBuybackShare) != 0 {
		t.Fatalf("buyback accrual ledger mismatch: got %v want %v", ledgerBalance, expectedBuybackShare)
	}

	// Sender pays the whole fee regardless of the internal split.
	expectedSender := new(big.Int).Sub(big.NewInt(100_000), expectedFee)
	if fromAcc.BalanceNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("sender balance mismatch: got %s want %s", fromAcc.BalanceNHB, expectedSender)
	}
}

func TestApplyTransactionFeeUnaffectedWhenBuybackNotConfigured(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 0x11
	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        500,
				OwnerWallet:           owner,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB: {MDRBasisPoints: 500, OwnerWallet: owner},
				},
			},
		},
	})

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000)}
	sender := make([]byte, 20)
	sender[19] = 0xBB

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(500))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedFee) != 0 {
		t.Fatalf("owner wallet should receive the full fee when buyback isn't configured: got %v want %v", routeAcc.BalanceNHB, expectedFee)
	}
}
