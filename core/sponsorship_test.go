package core

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
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

func commitState(t *testing.T, sp *StateProcessor) {
	t.Helper()
	if sp == nil {
		return
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit state: %v", err)
	}
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

type accountSnapshot struct {
	nonce      uint64
	balanceNHB *big.Int
}

func captureAccountSnapshot(t *testing.T, sp *StateProcessor, addr []byte) accountSnapshot {
	t.Helper()
	acc, err := sp.getAccount(addr)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	snapshot := accountSnapshot{}
	if acc != nil {
		snapshot.nonce = acc.Nonce
		if acc.BalanceNHB != nil {
			snapshot.balanceNHB = new(big.Int).Set(acc.BalanceNHB)
		}
	}
	return snapshot
}

func (s accountSnapshot) equal(other accountSnapshot) bool {
	if s.nonce != other.nonce {
		return false
	}
	switch {
	case s.balanceNHB == nil && other.balanceNHB == nil:
		return true
	case s.balanceNHB == nil || other.balanceNHB == nil:
		return false
	default:
		return s.balanceNHB.Cmp(other.balanceNHB) == 0
	}
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

func TestEvaluateSponsorshipPOSRegistry(t *testing.T) {
	t.Run("pause blocks sponsored tx only", func(t *testing.T) {
		sp := newSponsorshipState(t)

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		merchant := "merchant-pos-1"
		device := "terminal-pos-1"

		baseTx := func() *types.Transaction {
			return &types.Transaction{
				ChainID:         types.NHBChainID(),
				Type:            types.TxTypeTransfer,
				Nonce:           0,
				To:              make([]byte, 20),
				GasLimit:        21000,
				GasPrice:        big.NewInt(1),
				MerchantAddress: merchant,
				DeviceID:        device,
			}
		}

		if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed paymaster: %v", err)
		}
		if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed sender: %v", err)
		}

		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.POSPutMerchant(&pos.Merchant{Address: merchant, Paused: true}); err != nil {
			t.Fatalf("seed merchant pause: %v", err)
		}

		raw := baseTx()
		signTransaction(t, raw, senderKey)
		assessment, err := sp.EvaluateSponsorship(raw)
		if err != nil {
			t.Fatalf("evaluate raw: %v", err)
		}
		if assessment == nil || assessment.Status != SponsorshipStatusNone {
			t.Fatalf("expected no sponsorship status, got %+v", assessment)
		}

		sponsored := baseTx()
		sponsored.Paymaster = append([]byte(nil), paymasterAddr...)
		signPaymaster(t, sponsored, paymasterKey)
		signTransaction(t, sponsored, senderKey)

		assessment, err = sp.EvaluateSponsorship(sponsored)
		if err != nil {
			t.Fatalf("evaluate sponsored: %v", err)
		}
		if assessment == nil || assessment.Status != SponsorshipStatusThrottled {
			t.Fatalf("expected throttled assessment, got %+v", assessment)
		}
		if !strings.Contains(assessment.Reason, "merchant") {
			t.Fatalf("expected merchant pause reason, got %q", assessment.Reason)
		}
	})

	t.Run("revoked device blocked", func(t *testing.T) {
		sp := newSponsorshipState(t)

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		merchant := "merchant-pos-2"
		device := "terminal-pos-2"

		tx := &types.Transaction{
			ChainID:         types.NHBChainID(),
			Type:            types.TxTypeTransfer,
			Nonce:           0,
			To:              make([]byte, 20),
			GasLimit:        21000,
			GasPrice:        big.NewInt(1),
			MerchantAddress: merchant,
			DeviceID:        device,
		}

		tx.Paymaster = append([]byte(nil), paymasterAddr...)
		signPaymaster(t, tx, paymasterKey)
		signTransaction(t, tx, senderKey)

		if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed paymaster: %v", err)
		}
		if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed sender: %v", err)
		}

		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.POSPutMerchant(&pos.Merchant{Address: merchant}); err != nil {
			t.Fatalf("seed merchant: %v", err)
		}
		if err := manager.POSPutDevice(&pos.Device{DeviceID: device, Merchant: merchant, Revoked: true}); err != nil {
			t.Fatalf("seed device: %v", err)
		}

		assessment, err := sp.EvaluateSponsorship(tx)
		if err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if assessment == nil || assessment.Status != SponsorshipStatusThrottled {
			t.Fatalf("expected throttled assessment, got %+v", assessment)
		}
		if !strings.Contains(assessment.Reason, "device") {
			t.Fatalf("expected device revocation reason, got %q", assessment.Reason)
		}
	})
}

func TestEvaluateSponsorshipThrottling(t *testing.T) {
	baseTime := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address().Bytes()

	type expect struct {
		status SponsorshipStatus
		scope  PaymasterThrottleScope
	}

	cases := []struct {
		name   string
		limits PaymasterLimits
		setup  func(t *testing.T, sp *StateProcessor, tx *types.Transaction)
		want   expect
	}{
		{
			name: "within merchant cap",
			limits: PaymasterLimits{
				MerchantDailyCapWei: big.NewInt(100),
				DeviceDailyTxCap:    5,
				GlobalDailyCapWei:   big.NewInt(500),
			},
			setup: func(t *testing.T, sp *StateProcessor, tx *types.Transaction) {
				manager := nhbstate.NewManager(sp.Trie)
				day := sp.currentPaymasterDay()
				merchant := nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress)
				if err := manager.PaymasterPutMerchantDay(&nhbstate.PaymasterMerchantDay{
					Merchant:   merchant,
					Day:        day,
					TxCount:    1,
					BudgetWei:  big.NewInt(90),
					ChargedWei: big.NewInt(80),
				}); err != nil {
					t.Fatalf("seed merchant meter: %v", err)
				}
				if err := manager.PaymasterPutGlobalDay(&nhbstate.PaymasterGlobalDay{
					Day:        day,
					TxCount:    1,
					BudgetWei:  big.NewInt(90),
					ChargedWei: big.NewInt(80),
				}); err != nil {
					t.Fatalf("seed global meter: %v", err)
				}
			},
			want: expect{status: SponsorshipStatusReady},
		},
		{
			name: "merchant cap exceeded",
			limits: PaymasterLimits{
				MerchantDailyCapWei: big.NewInt(100),
				DeviceDailyTxCap:    10,
				GlobalDailyCapWei:   big.NewInt(1000),
			},
			setup: func(t *testing.T, sp *StateProcessor, tx *types.Transaction) {
				manager := nhbstate.NewManager(sp.Trie)
				day := sp.currentPaymasterDay()
				merchant := nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress)
				if err := manager.PaymasterPutMerchantDay(&nhbstate.PaymasterMerchantDay{
					Merchant:   merchant,
					Day:        day,
					TxCount:    3,
					BudgetWei:  big.NewInt(95),
					ChargedWei: big.NewInt(90),
				}); err != nil {
					t.Fatalf("seed merchant meter: %v", err)
				}
			},
			want: expect{status: SponsorshipStatusThrottled, scope: PaymasterThrottleScopeMerchant},
		},
		{
			name: "global cap exceeded",
			limits: PaymasterLimits{
				MerchantDailyCapWei: big.NewInt(1000),
				DeviceDailyTxCap:    10,
				GlobalDailyCapWei:   big.NewInt(100),
			},
			setup: func(t *testing.T, sp *StateProcessor, tx *types.Transaction) {
				manager := nhbstate.NewManager(sp.Trie)
				day := sp.currentPaymasterDay()
				if err := manager.PaymasterPutGlobalDay(&nhbstate.PaymasterGlobalDay{
					Day:        day,
					TxCount:    5,
					BudgetWei:  big.NewInt(95),
					ChargedWei: big.NewInt(90),
				}); err != nil {
					t.Fatalf("seed global meter: %v", err)
				}
			},
			want: expect{status: SponsorshipStatusThrottled, scope: PaymasterThrottleScopeGlobal},
		},
		{
			name: "device cap exceeded",
			limits: PaymasterLimits{
				MerchantDailyCapWei: big.NewInt(1000),
				DeviceDailyTxCap:    1,
				GlobalDailyCapWei:   big.NewInt(1000),
			},
			setup: func(t *testing.T, sp *StateProcessor, tx *types.Transaction) {
				manager := nhbstate.NewManager(sp.Trie)
				day := sp.currentPaymasterDay()
				merchant := nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress)
				device := nhbstate.NormalizePaymasterDevice(tx.DeviceID)
				if err := manager.PaymasterPutDeviceDay(&nhbstate.PaymasterDeviceDay{
					Merchant:   merchant,
					DeviceID:   device,
					Day:        day,
					TxCount:    1,
					BudgetWei:  big.NewInt(10),
					ChargedWei: big.NewInt(10),
				}); err != nil {
					t.Fatalf("seed device meter: %v", err)
				}
			},
			want: expect{status: SponsorshipStatusThrottled, scope: PaymasterThrottleScopeDevice},
		},
		{
			name: "day rollover clears usage",
			limits: PaymasterLimits{
				MerchantDailyCapWei: big.NewInt(100),
				DeviceDailyTxCap:    5,
				GlobalDailyCapWei:   big.NewInt(100),
			},
			setup: func(t *testing.T, sp *StateProcessor, tx *types.Transaction) {
				manager := nhbstate.NewManager(sp.Trie)
				prior := baseTime.Add(-24 * time.Hour)
				day := nhbstate.NormalizePaymasterDay(prior.Format(nhbstate.PaymasterDayFormat))
				merchant := nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress)
				if err := manager.PaymasterPutMerchantDay(&nhbstate.PaymasterMerchantDay{
					Merchant:   merchant,
					Day:        day,
					TxCount:    5,
					BudgetWei:  big.NewInt(100),
					ChargedWei: big.NewInt(100),
				}); err != nil {
					t.Fatalf("seed prior merchant meter: %v", err)
				}
				if err := manager.PaymasterPutGlobalDay(&nhbstate.PaymasterGlobalDay{
					Day:        day,
					TxCount:    5,
					BudgetWei:  big.NewInt(100),
					ChargedWei: big.NewInt(100),
				}); err != nil {
					t.Fatalf("seed prior global meter: %v", err)
				}
				sp.BeginBlock(1, baseTime.Add(24*time.Hour))
			},
			want: expect{status: SponsorshipStatusReady},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newSponsorshipState(t)
			sp.BeginBlock(1, baseTime)
			sp.SetPaymasterLimits(tc.limits)

			tx := &types.Transaction{
				ChainID:         types.NHBChainID(),
				Type:            types.TxTypeTransfer,
				Nonce:           0,
				To:              make([]byte, 20),
				GasLimit:        10,
				GasPrice:        big.NewInt(1),
				MerchantAddress: "merchant-1",
				DeviceID:        "device-1",
			}

			if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000)}); err != nil {
				t.Fatalf("seed paymaster: %v", err)
			}

			tx.Paymaster = append([]byte(nil), paymasterAddr...)
			signPaymaster(t, tx, paymasterKey)

			if tc.setup != nil {
				tc.setup(t, sp, tx)
			}

			assessment, err := sp.EvaluateSponsorship(tx)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if assessment.Status != tc.want.status {
				t.Fatalf("expected status %s, got %s", tc.want.status, assessment.Status)
			}
			if tc.want.status == SponsorshipStatusThrottled {
				if assessment.Throttle == nil {
					t.Fatalf("expected throttle metadata")
				}
				if assessment.Throttle.Scope != tc.want.scope {
					t.Fatalf("expected scope %s, got %s", tc.want.scope, assessment.Throttle.Scope)
				}
			} else if assessment.Throttle != nil {
				t.Fatalf("unexpected throttle metadata for status %s", assessment.Status)
			}
		})
	}
}

func TestPaymasterThrottledEventEmitted(t *testing.T) {
	sp := newSponsorshipState(t)
	blockTime := time.Date(2024, time.January, 2, 8, 0, 0, 0, time.UTC)
	sp.BeginBlock(1, blockTime)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address().Bytes()

	if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000)}); err != nil {
		t.Fatalf("seed paymaster: %v", err)
	}
	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeTransfer,
		Nonce:           0,
		To:              make([]byte, 20),
		GasLimit:        21000,
		GasPrice:        big.NewInt(1),
		MerchantAddress: "merchant-2",
		DeviceID:        "device-2",
	}
	tx.Paymaster = append([]byte(nil), paymasterAddr...)
	signPaymaster(t, tx, paymasterKey)
	signTransaction(t, tx, senderKey)

	requiredBudget := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), tx.GasPrice)
	globalLimit := new(big.Int).Sub(requiredBudget, big.NewInt(1))
	sp.SetPaymasterLimits(PaymasterLimits{
		MerchantDailyCapWei: big.NewInt(1_000_000),
		DeviceDailyTxCap:    10,
		GlobalDailyCapWei:   globalLimit,
	})

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if assessment == nil || assessment.Status != SponsorshipStatusThrottled {
		t.Fatalf("expected throttled assessment, got %+v", assessment)
	}
	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash transaction: %v", err)
	}
	sp.emitSponsorshipFailureEvent(common.BytesToAddress(senderAddr), assessment, bytesToHash32(txHash))

	eventsList := sp.Events()
	found := false
	for _, evt := range eventsList {
		if evt.Type == events.TypePaymasterThrottled {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected paymaster throttled event in %+v", eventsList)
	}
}

func TestPaymasterCountersAccumulate(t *testing.T) {
	sp := newSponsorshipState(t)
	blockTime := time.Date(2024, time.January, 3, 9, 0, 0, 0, time.UTC)
	sp.BeginBlock(1, blockTime)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	senderAddr := senderKey.PubKey().Address().Bytes()

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address().Bytes()

	gasPrice := big.NewInt(2)
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeTransfer,
		Nonce:           0,
		To:              make([]byte, 20),
		GasLimit:        21000,
		GasPrice:        gasPrice,
		MerchantAddress: "merchant-3",
		DeviceID:        "device-3",
	}
	tx.Paymaster = append([]byte(nil), paymasterAddr...)
	signTransaction(t, tx, senderKey)
	signPaymaster(t, tx, paymasterKey)

	if err := sp.setAccount(paymasterAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000)}); err != nil {
		t.Fatalf("seed paymaster: %v", err)
	}
	if err := sp.setAccount(senderAddr, &types.Account{BalanceNHB: big.NewInt(1_000_000)}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	sp.SetPaymasterLimits(PaymasterLimits{
		MerchantDailyCapWei: big.NewInt(1_000_000_000),
		DeviceDailyTxCap:    10,
		GlobalDailyCapWei:   big.NewInt(1_000_000_000),
	})

	day := sp.currentPaymasterDay()
	ctx := &sponsorshipRuntime{
		merchant: nhbstate.NormalizePaymasterMerchant(tx.MerchantAddress),
		device:   nhbstate.NormalizePaymasterDevice(tx.DeviceID),
		day:      day,
		gasPrice: new(big.Int).Set(gasPrice),
		budget:   new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), gasPrice),
	}
	hash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash transaction: %v", err)
	}
	ctx.txHash = bytesToHash32(hash)
	ctx.sponsor = common.BytesToAddress(paymasterAddr)
	ctx.sender = common.BytesToAddress(senderAddr)

	charged := new(big.Int).Set(ctx.budget)
	if err := sp.recordPaymasterUsage(ctx, charged); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	snapshot, err := sp.PaymasterCounters(tx.MerchantAddress, tx.DeviceID, day)
	if err != nil {
		t.Fatalf("counters: %v", err)
	}

	expectedBudget := new(big.Int).Mul(new(big.Int).SetUint64(tx.GasLimit), gasPrice)
	if snapshot.GlobalTxCount != 1 {
		t.Fatalf("expected global tx count 1, got %d", snapshot.GlobalTxCount)
	}
	if snapshot.MerchantTxCount != 1 {
		t.Fatalf("expected merchant tx count 1, got %d", snapshot.MerchantTxCount)
	}
	if snapshot.DeviceTxCount != 1 {
		t.Fatalf("expected device tx count 1, got %d", snapshot.DeviceTxCount)
	}
	if snapshot.GlobalBudgetWei.Cmp(expectedBudget) != 0 {
		t.Fatalf("expected global budget %s, got %s", expectedBudget, snapshot.GlobalBudgetWei)
	}
	if snapshot.MerchantBudgetWei.Cmp(expectedBudget) != 0 {
		t.Fatalf("expected merchant budget %s, got %s", expectedBudget, snapshot.MerchantBudgetWei)
	}
	if snapshot.DeviceBudgetWei.Cmp(expectedBudget) != 0 {
		t.Fatalf("expected device budget %s, got %s", expectedBudget, snapshot.DeviceBudgetWei)
	}
}

func TestApplyTransactionSponsorshipRejection(t *testing.T) {
	t.Run("invalid signature leaves state untouched", func(t *testing.T) {
		sp := newSponsorshipState(t)
		sp.SetPaymasterEnabled(true)
		sp.BeginBlock(1, time.Unix(1, 0).UTC())

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		senderAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}
		paymasterAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.setAccount(paymasterAddr, paymasterAccount); err != nil {
			t.Fatalf("seed paymaster: %v", err)
		}
		commitState(t, sp)

		tx := &types.Transaction{
			ChainID:   types.NHBChainID(),
			Type:      types.TxTypeTransfer,
			Nonce:     0,
			To:        append([]byte(nil), common.Address{0xAA}.Bytes()...),
			Value:     big.NewInt(1),
			GasLimit:  21_000,
			GasPrice:  big.NewInt(1),
			Paymaster: append([]byte(nil), paymasterAddr...),
		}
		signTransaction(t, tx, senderKey)

		rootBefore := sp.Trie.Hash()
		senderBefore := captureAccountSnapshot(t, sp, senderAddr)
		paymasterBefore := captureAccountSnapshot(t, sp, paymasterAddr)

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected sponsorship rejection error")
		}
		if !errors.Is(err, ErrSponsorshipRejected) {
			t.Fatalf("expected ErrSponsorshipRejected, got %v", err)
		}

		if rootAfter := sp.Trie.Hash(); rootAfter != rootBefore {
			t.Fatalf("expected trie root unchanged, got %s vs %s", rootAfter.Hex(), rootBefore.Hex())
		}
		if senderAfter := captureAccountSnapshot(t, sp, senderAddr); !senderAfter.equal(senderBefore) {
			t.Fatalf("sender account mutated: before=%+v after=%+v", senderBefore, senderAfter)
		}
		if paymasterAfter := captureAccountSnapshot(t, sp, paymasterAddr); !paymasterAfter.equal(paymasterBefore) {
			t.Fatalf("paymaster account mutated: before=%+v after=%+v", paymasterBefore, paymasterAfter)
		}

		eventsList := sp.Events()
		if len(eventsList) == 0 {
			t.Fatalf("expected sponsorship failure event")
		}
		last := eventsList[len(eventsList)-1]
		if last.Type != events.TypeTxSponsorshipFailed {
			t.Fatalf("expected failure event, got %s", last.Type)
		}
		if status := last.Attributes["status"]; status != string(SponsorshipStatusSignatureMissing) {
			t.Fatalf("expected status %s, got %s", SponsorshipStatusSignatureMissing, status)
		}
	})

	t.Run("throttled sponsorship emits events", func(t *testing.T) {
		sp := newSponsorshipState(t)
		sp.SetPaymasterEnabled(true)
		sp.SetPaymasterLimits(PaymasterLimits{DeviceDailyTxCap: 1})
		sp.BeginBlock(1, time.Unix(2, 0).UTC())

		senderKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate sender key: %v", err)
		}
		senderAddr := senderKey.PubKey().Address().Bytes()
		paymasterKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate paymaster key: %v", err)
		}
		paymasterAddr := paymasterKey.PubKey().Address().Bytes()

		senderAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.setAccount(senderAddr, senderAccount); err != nil {
			t.Fatalf("seed sender: %v", err)
		}
		paymasterAccount := &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
		if err := sp.setAccount(paymasterAddr, paymasterAccount); err != nil {
			t.Fatalf("seed paymaster: %v", err)
		}
		commitState(t, sp)

		tx := &types.Transaction{
			ChainID:         types.NHBChainID(),
			Type:            types.TxTypeTransfer,
			Nonce:           0,
			To:              append([]byte(nil), common.Address{0xBB}.Bytes()...),
			Value:           big.NewInt(1),
			GasLimit:        21_000,
			GasPrice:        big.NewInt(1),
			Paymaster:       append([]byte(nil), paymasterAddr...),
			MerchantAddress: "merchant-1",
		}
		signTransaction(t, tx, senderKey)
		signPaymaster(t, tx, paymasterKey)

		rootBefore := sp.Trie.Hash()
		senderBefore := captureAccountSnapshot(t, sp, senderAddr)
		paymasterBefore := captureAccountSnapshot(t, sp, paymasterAddr)

		err = sp.ApplyTransaction(tx)
		if err == nil {
			t.Fatalf("expected sponsorship rejection error")
		}
		if !errors.Is(err, ErrSponsorshipRejected) {
			t.Fatalf("expected ErrSponsorshipRejected, got %v", err)
		}

		if rootAfter := sp.Trie.Hash(); rootAfter != rootBefore {
			t.Fatalf("expected trie root unchanged, got %s vs %s", rootAfter.Hex(), rootBefore.Hex())
		}
		if senderAfter := captureAccountSnapshot(t, sp, senderAddr); !senderAfter.equal(senderBefore) {
			t.Fatalf("sender account mutated: before=%+v after=%+v", senderBefore, senderAfter)
		}
		if paymasterAfter := captureAccountSnapshot(t, sp, paymasterAddr); !paymasterAfter.equal(paymasterBefore) {
			t.Fatalf("paymaster account mutated: before=%+v after=%+v", paymasterBefore, paymasterAfter)
		}

		eventsList := sp.Events()
		if len(eventsList) < 2 {
			t.Fatalf("expected failure and throttle events, got %#v", eventsList)
		}
		failure := eventsList[len(eventsList)-2]
		throttle := eventsList[len(eventsList)-1]
		if failure.Type != events.TypeTxSponsorshipFailed {
			t.Fatalf("expected failure event, got %s", failure.Type)
		}
		if status := failure.Attributes["status"]; status != string(SponsorshipStatusThrottled) {
			t.Fatalf("expected throttled status, got %s", status)
		}
		if throttle.Type != events.TypePaymasterThrottled {
			t.Fatalf("expected throttle event, got %s", throttle.Type)
		}
		if _, ok := throttle.Attributes["scope"]; !ok {
			t.Fatalf("expected throttle scope attribute, got %#v", throttle.Attributes)
		}
	})
}
