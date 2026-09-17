package core

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestApplyTransactionChainIDValidation(t *testing.T) {
	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sender := priv.PubKey().Address().Bytes()

	tests := []struct {
		name    string
		chainID *big.Int
		prepare func(t *testing.T, sp *StateProcessor)
		wantErr bool
		sign    bool
	}{
		{
			name:    "valid chain id",
			chainID: types.NHBChainID(),
			prepare: func(t *testing.T, sp *StateProcessor) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(0),
					BalanceZNHB: big.NewInt(0),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(sender, account); err != nil {
					t.Fatalf("seed account: %v", err)
				}
			},
			sign: true,
		},
		{
			name:    "nil chain id",
			chainID: nil,
			prepare: func(t *testing.T, sp *StateProcessor) {},
			wantErr: true,
			sign:    false,
		},
		{
			name:    "wrong chain id",
			chainID: big.NewInt(12345),
			prepare: func(t *testing.T, sp *StateProcessor) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(0),
					BalanceZNHB: big.NewInt(0),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(sender, account); err != nil {
					t.Fatalf("seed account: %v", err)
				}
			},
			wantErr: true,
			sign:    true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			tc.prepare(t, sp)

			tx := &types.Transaction{
				ChainID:  tc.chainID,
				Type:     types.TxTypeRegisterIdentity,
				Nonce:    0,
				Data:     []byte("alice"),
				GasLimit: 21_000,
				GasPrice: big.NewInt(1),
			}
			if tc.sign {
				if err := tx.Sign(priv.PrivateKey); err != nil {
					t.Fatalf("sign tx: %v", err)
				}
			}

			err := sp.ApplyTransaction(tx)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if err != nil && !errors.Is(err, ErrInvalidChainID) {
					t.Fatalf("expected ErrInvalidChainID, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply transaction: %v", err)
			}
		})
	}
}
