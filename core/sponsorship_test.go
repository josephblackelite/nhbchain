package core

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newSponsorshipState(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func signTransaction(t *testing.T, tx *types.Transaction, key *crypto.PrivateKey) {
	t.Helper()
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
}

func signPaymaster(t *testing.T, tx *types.Transaction, key *crypto.PrivateKey) {
	t.Helper()
	hash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash transaction: %v", err)
	}
	sig, err := ethcrypto.Sign(hash, key.PrivateKey)
	if err != nil {
		t.Fatalf("sign paymaster: %v", err)
	}
	tx.PaymasterR = new(big.Int).SetBytes(sig[:32])
	tx.PaymasterS = new(big.Int).SetBytes(sig[32:64])
	tx.PaymasterV = new(big.Int).SetUint64(uint64(sig[64]) + 27)
}

func TestEvaluateSponsorship(t *testing.T) {
	sp := newSponsorshipState(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()
	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address().Bytes()

	baseTx := func() *types.Transaction {
		to := make([]byte, 20)
		to[0] = 0x99
		tx := &types.Transaction{
			ChainID:  types.NHBChainID(),
			Type:     types.TxTypeTransfer,
			Nonce:    0,
			To:       to,
			GasLimit: 21000,
			GasPrice: big.NewInt(1_000_000_000),
			Value:    big.NewInt(1),
		}
		return tx
	}

	cases := []struct {
		name     string
		prepare  func(tx *types.Transaction)
		mutateSP func()
		status   SponsorshipStatus
	}{
		{
			name:   "no paymaster",
			status: SponsorshipStatusNone,
		},
		{
			name: "module disabled",
			prepare: func(tx *types.Transaction) {
				tx.Paymaster = append([]byte(nil), paymasterAddr...)
				signPaymaster(t, tx, paymasterKey)
				if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
					t.Fatalf("seed paymaster: %v", err)
				}
			},
			mutateSP: func() { sp.SetPaymasterEnabled(false) },
			status:   SponsorshipStatusModuleDisabled,
		},
		{
			name: "missing signature",
			prepare: func(tx *types.Transaction) {
				tx.Paymaster = append([]byte(nil), paymasterAddr...)
				if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
					t.Fatalf("seed paymaster: %v", err)
				}
			},
			status: SponsorshipStatusSignatureMissing,
		},
		{
			name: "invalid signature",
			prepare: func(tx *types.Transaction) {
				tx.Paymaster = append([]byte(nil), paymasterAddr...)
				otherKey, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Fatalf("generate alt key: %v", err)
				}
				signPaymaster(t, tx, otherKey)
				if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
					t.Fatalf("seed paymaster: %v", err)
				}
			},
			status: SponsorshipStatusSignatureInvalid,
		},
		{
			name: "insufficient balance",
			prepare: func(tx *types.Transaction) {
				tx.Paymaster = append([]byte(nil), paymasterAddr...)
				signPaymaster(t, tx, paymasterKey)
				if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
					t.Fatalf("seed paymaster: %v", err)
				}
			},
			status: SponsorshipStatusInsufficientBalance,
		},
		{
			name: "ready",
			prepare: func(tx *types.Transaction) {
				tx.Paymaster = append([]byte(nil), paymasterAddr...)
				signPaymaster(t, tx, paymasterKey)
				required := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), tx.GasPrice)
				balance := new(big.Int).Mul(required, big.NewInt(2))
				if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: balance, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
					t.Fatalf("seed paymaster: %v", err)
				}
			},
			status: SponsorshipStatusReady,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp.SetPaymasterEnabled(true)
			tx := baseTx()
			if tc.prepare != nil {
				tc.prepare(tx)
			}
			signTransaction(t, tx, senderKey)
			if tc.mutateSP != nil {
				tc.mutateSP()
				t.Cleanup(func() { sp.SetPaymasterEnabled(true) })
			}
			assessment, err := sp.EvaluateSponsorship(tx)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if assessment == nil {
				t.Fatalf("assessment nil")
			}
			if assessment.Status != tc.status {
				t.Fatalf("expected status %s, got %s", tc.status, assessment.Status)
			}
			if tc.status == SponsorshipStatusReady {
				expected := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), tx.GasPrice)
				if assessment.GasCost == nil || assessment.GasCost.Cmp(expected) != 0 {
					t.Fatalf("expected gas cost %s, got %v", expected.String(), assessment.GasCost)
				}
				if assessment.Sponsor != (common.Address{}) {
					// ensure sponsor matches paymaster address
					if !bytes.Equal(assessment.Sponsor.Bytes(), paymasterAddr) {
						t.Fatalf("unexpected sponsor address")
					}
				}
			}
		})
	}
}
