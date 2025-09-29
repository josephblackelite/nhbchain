package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestApplyTransactionRejectsNativeNonceReplay(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, sp *StateProcessor, addr []byte)
		build func(t *testing.T, sp *StateProcessor, priv *crypto.PrivateKey) *types.Transaction
	}{
		{
			name: "register identity",
			setup: func(t *testing.T, sp *StateProcessor, addr []byte) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(0),
					BalanceZNHB: big.NewInt(0),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(addr, account); err != nil {
					t.Fatalf("seed account: %v", err)
				}
			},
			build: func(t *testing.T, _ *StateProcessor, priv *crypto.PrivateKey) *types.Transaction {
				t.Helper()
				tx := &types.Transaction{
					ChainID:  types.NHBChainID(),
					Type:     types.TxTypeRegisterIdentity,
					Nonce:    0,
					Data:     []byte("alice"),
					GasLimit: 21000,
					GasPrice: big.NewInt(1),
				}
				if err := tx.Sign(priv.PrivateKey); err != nil {
					t.Fatalf("sign tx: %v", err)
				}
				return tx
			},
		},
		{
			name: "create escrow",
			setup: func(t *testing.T, sp *StateProcessor, addr []byte) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(1_000),
					BalanceZNHB: big.NewInt(0),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(addr, account); err != nil {
					t.Fatalf("seed escrow account: %v", err)
				}
			},
			build: func(t *testing.T, sp *StateProcessor, priv *crypto.PrivateKey) *types.Transaction {
				t.Helper()
				payee := priv.PubKey().Address().Bytes()
				payload := struct {
					Payee    []byte   `json:"payee"`
					Token    string   `json:"token"`
					Amount   *big.Int `json:"amount"`
					FeeBps   uint32   `json:"feeBps"`
					Deadline int64    `json:"deadline"`
					Nonce    uint64   `json:"nonce"`
				}{
					Payee:    payee,
					Token:    "NHB",
					Amount:   big.NewInt(100),
					FeeBps:   0,
					Deadline: time.Now().Add(time.Hour).Unix(),
					Nonce:    1,
				}
				data, err := json.Marshal(payload)
				if err != nil {
					t.Fatalf("marshal payload: %v", err)
				}
				tx := &types.Transaction{
					ChainID:  types.NHBChainID(),
					Type:     types.TxTypeCreateEscrow,
					Nonce:    0,
					Data:     data,
					GasLimit: 21000,
					GasPrice: big.NewInt(1),
				}
				if err := tx.Sign(priv.PrivateKey); err != nil {
					t.Fatalf("sign tx: %v", err)
				}
				return tx
			},
		},
		{
			name: "heartbeat",
			setup: func(t *testing.T, sp *StateProcessor, addr []byte) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(0),
					BalanceZNHB: big.NewInt(0),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(addr, account); err != nil {
					t.Fatalf("seed heartbeat account: %v", err)
				}
			},
			build: func(t *testing.T, _ *StateProcessor, priv *crypto.PrivateKey) *types.Transaction {
				t.Helper()
				payload := types.HeartbeatPayload{Timestamp: time.Now().UTC().Unix()}
				data, err := json.Marshal(payload)
				if err != nil {
					t.Fatalf("marshal heartbeat: %v", err)
				}
				tx := &types.Transaction{
					ChainID:  types.NHBChainID(),
					Type:     types.TxTypeHeartbeat,
					Nonce:    0,
					Data:     data,
					GasLimit: 21000,
					GasPrice: big.NewInt(1),
				}
				if err := tx.Sign(priv.PrivateKey); err != nil {
					t.Fatalf("sign tx: %v", err)
				}
				return tx
			},
		},
		{
			name: "stake",
			setup: func(t *testing.T, sp *StateProcessor, addr []byte) {
				t.Helper()
				account := &types.Account{
					BalanceNHB:  big.NewInt(0),
					BalanceZNHB: big.NewInt(2_000),
					Stake:       big.NewInt(0),
				}
				if err := sp.setAccount(addr, account); err != nil {
					t.Fatalf("seed staking account: %v", err)
				}
			},
			build: func(t *testing.T, _ *StateProcessor, priv *crypto.PrivateKey) *types.Transaction {
				t.Helper()
				tx := &types.Transaction{
					ChainID:  types.NHBChainID(),
					Type:     types.TxTypeStake,
					Nonce:    0,
					Value:    big.NewInt(500),
					GasLimit: 21000,
					GasPrice: big.NewInt(1),
				}
				if err := tx.Sign(priv.PrivateKey); err != nil {
					t.Fatalf("sign tx: %v", err)
				}
				return tx
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			priv, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			addr := priv.PubKey().Address().Bytes()
			tc.setup(t, sp, addr)
			tx := tc.build(t, sp, priv)

			if err := sp.ApplyTransaction(tx); err != nil {
				t.Fatalf("apply transaction: %v", err)
			}
			if err := sp.ApplyTransaction(tx); err == nil {
				t.Fatalf("expected nonce replay to be rejected")
			} else if !errors.Is(err, ErrNonceMismatch) {
				t.Fatalf("expected ErrNonceMismatch, got %v", err)
			}
		})
	}
}
