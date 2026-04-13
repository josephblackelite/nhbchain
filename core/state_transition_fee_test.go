package core

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/native/fees"
)

func TestApplyTransactionFeeDefaultsToSender(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
	}
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
	sender[19] = 0xAA

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(500))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	expectedSender := new(big.Int).Sub(initialSender, expectedFee)
	if fromAcc.BalanceNHB.Cmp(expectedSender) != 0 {
		t.Fatalf("sender balance mismatch: got %s want %s", fromAcc.BalanceNHB, expectedSender)
	}
	if toAcc.BalanceNHB.Cmp(initialRecipient) != 0 {
		t.Fatalf("recipient balance mutated: got %s want %s", toAcc.BalanceNHB, initialRecipient)
	}

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedFee) != 0 {
		t.Fatalf("route wallet balance mismatch: got %v want %v", routeAcc.BalanceNHB, expectedFee)
	}
}

func TestApplyTransactionFeeSenderInsufficient(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 1
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
	sender[18] = 0xBB

	fromAcc := &types.Account{BalanceNHB: big.NewInt(400)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(25_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc)
	if err == nil {
		t.Fatalf("expected error when sender lacks funds")
	}
	if err.Error() != "fees: insufficient balance to route fee" {
		t.Fatalf("unexpected error: %v", err)
	}

	if fromAcc.BalanceNHB.Cmp(initialSender) != 0 {
		t.Fatalf("sender balance mutated on error: got %s want %s", fromAcc.BalanceNHB, initialSender)
	}
	if toAcc.BalanceNHB.Cmp(initialRecipient) != 0 {
		t.Fatalf("recipient balance mutated on error: got %s want %s", toAcc.BalanceNHB, initialRecipient)
	}
}

func TestApplyTransactionFeeRecipientOptIn(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var owner [20]byte
	owner[0] = 2
	domain := "pos"
	sp.SetFeePolicy(fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			domain: {
				MDRBasisPoints:        300,
				OwnerWallet:           owner,
				FeePayer:              fees.FeePayerRecipient,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB: {MDRBasisPoints: 300, OwnerWallet: owner},
				},
			},
		},
	})

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(8_000)}
	sender := make([]byte, 20)
	sender[17] = 0xCC

	fromAcc := &types.Account{BalanceNHB: big.NewInt(75_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(60_000)}

	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(300))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	if fromAcc.BalanceNHB.Cmp(initialSender) != 0 {
		t.Fatalf("sender balance mutated under recipient policy: got %s want %s", fromAcc.BalanceNHB, initialSender)
	}

	expectedRecipient := new(big.Int).Sub(initialRecipient, expectedFee)
	if toAcc.BalanceNHB.Cmp(expectedRecipient) != 0 {
		t.Fatalf("recipient balance mismatch: got %s want %s", toAcc.BalanceNHB, expectedRecipient)
	}

	routeAcc, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route account: %v", err)
	}
	if routeAcc.BalanceNHB == nil || routeAcc.BalanceNHB.Cmp(expectedFee) != 0 {
		t.Fatalf("route wallet balance mismatch: got %v want %v", routeAcc.BalanceNHB, expectedFee)
	}
}
