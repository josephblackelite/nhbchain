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

// TestApplyTransactionFeeRouteWalletIsSenderNotLost is the NHB-asset
// regression for PL-R1-FEEROUTE: when a domain's configured fee OwnerWallet
// is the transfer's own sender, the credit must land on the same *types.Account
// object the caller already loaded for the sender (fromAcc) and will persist
// after this call returns -- not on a second copy fetched fresh from state,
// which the caller's later sp.setAccount(sender, fromAcc) would silently
// overwrite, destroying the fee. Mirrors the ZNHB fix for the identical
// aliasing case (applyTransferZNHB's route-wallet-is-a-party handling).
func TestApplyTransactionFeeRouteWalletIsSenderNotLost(t *testing.T) {
	sp := newStakingStateProcessor(t)

	sender := make([]byte, 20)
	sender[19] = 0xAA
	var owner [20]byte
	copy(owner[:], sender) // the route wallet IS the sender

	recipient := make([]byte, 20)
	recipient[19] = 0xBB

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

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000), To: recipient}

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}
	initialSender := new(big.Int).Set(fromAcc.BalanceNHB)

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	// Mirror what every real call site does immediately after
	// applyTransactionFee returns: persist the sender's (and recipient's)
	// account object. On the pre-fix code this is exactly the write that
	// silently erases the fee credit a separately-loaded route account had
	// just received.
	if err := sp.setAccount(sender, fromAcc); err != nil {
		t.Fatalf("persist sender: %v", err)
	}
	if err := sp.setAccount(recipient, toAcc); err != nil {
		t.Fatalf("persist recipient: %v", err)
	}

	// The sender paid the fee to themselves: the debit and the credit are
	// the same object, so the balance nets back to its starting value. Any
	// other result means the fee was either lost (debited only) or
	// double-applied.
	final, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route/sender account: %v", err)
	}
	if final.BalanceNHB == nil || final.BalanceNHB.Cmp(initialSender) != 0 {
		t.Fatalf("fee paid to self was lost or mutated: got %v want %v (unchanged)", final.BalanceNHB, initialSender)
	}
}

// TestApplyTransactionFeeRouteWalletIsRecipientNotLost is the NHB-asset
// regression for PL-R1-FEEROUTE's other aliasing case: the domain's fee
// OwnerWallet is the transfer's own recipient. The fee credit must land on
// the caller's already-loaded recipient object (toAcc), not a second copy
// the caller's later sp.setAccount(recipient, toAcc) would overwrite.
func TestApplyTransactionFeeRouteWalletIsRecipientNotLost(t *testing.T) {
	sp := newStakingStateProcessor(t)

	sender := make([]byte, 20)
	sender[19] = 0xCC

	recipient := make([]byte, 20)
	recipient[19] = 0xDD
	var owner [20]byte
	copy(owner[:], recipient) // the route wallet IS the recipient

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

	tx := &types.Transaction{Type: types.TxTypeTransfer, MerchantAddress: domain, Value: big.NewInt(10_000), To: recipient}

	fromAcc := &types.Account{BalanceNHB: big.NewInt(100_000)}
	toAcc := &types.Account{BalanceNHB: big.NewInt(50_000)}
	initialRecipient := new(big.Int).Set(toAcc.BalanceNHB)

	expectedFee := new(big.Int).Mul(tx.Value, big.NewInt(500))
	expectedFee.Div(expectedFee, big.NewInt(10_000))

	if err := sp.applyTransactionFee(tx, sender, fromAcc, toAcc); err != nil {
		t.Fatalf("apply fee: %v", err)
	}

	// Mirror the real call sites' post-call persistence, same as above.
	if err := sp.setAccount(sender, fromAcc); err != nil {
		t.Fatalf("persist sender: %v", err)
	}
	if err := sp.setAccount(recipient, toAcc); err != nil {
		t.Fatalf("persist recipient: %v", err)
	}

	expectedRecipient := new(big.Int).Add(initialRecipient, expectedFee)
	final, err := sp.getAccount(owner[:])
	if err != nil {
		t.Fatalf("load route/recipient account: %v", err)
	}
	if final.BalanceNHB == nil || final.BalanceNHB.Cmp(expectedRecipient) != 0 {
		t.Fatalf("fee routed to recipient was lost: got %v want %v", final.BalanceNHB, expectedRecipient)
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
