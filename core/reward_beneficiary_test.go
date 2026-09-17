package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func setRewardBeneficiaryTx(t *testing.T, nonce uint64, beneficiary string) *types.Transaction {
	t.Helper()
	payload := struct {
		Beneficiary string `json:"beneficiary"`
	}{Beneficiary: beneficiary}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSetRewardBeneficiary,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
}

func TestApplySetRewardBeneficiary_SetAndClear(t *testing.T) {
	sp := newStakingStateProcessor(t)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(validatorAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed validator: %v", err)
	}

	beneficiaryKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate beneficiary key: %v", err)
	}
	beneficiaryAddrStr := beneficiaryKey.PubKey().Address().String()

	tx := setRewardBeneficiaryTx(t, 0, beneficiaryAddrStr)
	if err := tx.Sign(validatorKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	validator, err := sp.getAccount(validatorAddr)
	if err != nil {
		t.Fatalf("load validator: %v", err)
	}
	if len(validator.RewardBeneficiary) == 0 {
		t.Fatalf("expected reward beneficiary to be set")
	}

	clearTx := setRewardBeneficiaryTx(t, 1, "")
	if err := clearTx.Sign(validatorKey.PrivateKey); err != nil {
		t.Fatalf("sign clear transaction: %v", err)
	}
	if err := sp.ApplyTransaction(clearTx); err != nil {
		t.Fatalf("apply clear transaction: %v", err)
	}
	validator, err = sp.getAccount(validatorAddr)
	if err != nil {
		t.Fatalf("reload validator: %v", err)
	}
	if len(validator.RewardBeneficiary) != 0 {
		t.Fatalf("expected reward beneficiary to be cleared, got %x", validator.RewardBeneficiary)
	}
}

func TestApplySetRewardBeneficiary_RejectsSelf(t *testing.T) {
	sp := newStakingStateProcessor(t)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address()
	if err := sp.setAccount(validatorAddr.Bytes(), &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed validator: %v", err)
	}

	tx := setRewardBeneficiaryTx(t, 0, validatorAddr.String())
	if err := tx.Sign(validatorKey.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected rejection when beneficiary equals the validator's own address")
	}
}

func TestApplyAccountRewards_RedirectsToBeneficiary(t *testing.T) {
	sp := newStakingStateProcessor(t)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()

	beneficiaryKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate beneficiary key: %v", err)
	}
	beneficiaryAddr := beneficiaryKey.PubKey().Address().Bytes()

	if err := sp.setAccount(validatorAddr, &types.Account{
		BalanceNHB:        big.NewInt(0),
		BalanceZNHB:       big.NewInt(0),
		Stake:             big.NewInt(10_000),
		RewardBeneficiary: append([]byte(nil), beneficiaryAddr...),
	}); err != nil {
		t.Fatalf("seed validator: %v", err)
	}
	if err := sp.setAccount(beneficiaryAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(500),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed beneficiary: %v", err)
	}

	rewardMap := map[string]*accountReward{
		string(validatorAddr): {
			addr:       append([]byte(nil), validatorAddr...),
			total:      big.NewInt(1_000),
			validators: big.NewInt(1_000),
			stakers:    big.NewInt(0),
			engagement: big.NewInt(0),
		},
	}

	payouts, err := sp.applyAccountRewards(1, rewardMap)
	if err != nil {
		t.Fatalf("apply account rewards: %v", err)
	}
	if len(payouts) != 1 {
		t.Fatalf("expected 1 payout, got %d", len(payouts))
	}

	validator, err := sp.getAccount(validatorAddr)
	if err != nil {
		t.Fatalf("load validator: %v", err)
	}
	if validator.BalanceZNHB.Sign() != 0 {
		t.Fatalf("validator's own balance must be untouched when redirected, got %s", validator.BalanceZNHB)
	}

	beneficiary, err := sp.getAccount(beneficiaryAddr)
	if err != nil {
		t.Fatalf("load beneficiary: %v", err)
	}
	if beneficiary.BalanceZNHB.Cmp(big.NewInt(1_500)) != 0 {
		t.Fatalf("expected beneficiary ZNHB 1500 (500 seed + 1000 reward), got %s", beneficiary.BalanceZNHB)
	}
}

func TestApplyAccountRewards_NoBeneficiaryPaysValidatorDirectly(t *testing.T) {
	sp := newStakingStateProcessor(t)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorAddr := validatorKey.PubKey().Address().Bytes()
	if err := sp.setAccount(validatorAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(10_000),
	}); err != nil {
		t.Fatalf("seed validator: %v", err)
	}

	rewardMap := map[string]*accountReward{
		string(validatorAddr): {
			addr:       append([]byte(nil), validatorAddr...),
			total:      big.NewInt(1_000),
			validators: big.NewInt(1_000),
			stakers:    big.NewInt(0),
			engagement: big.NewInt(0),
		},
	}

	if _, err := sp.applyAccountRewards(1, rewardMap); err != nil {
		t.Fatalf("apply account rewards: %v", err)
	}

	validator, err := sp.getAccount(validatorAddr)
	if err != nil {
		t.Fatalf("load validator: %v", err)
	}
	if validator.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected validator ZNHB 1000 with no beneficiary set, got %s", validator.BalanceZNHB)
	}
}
