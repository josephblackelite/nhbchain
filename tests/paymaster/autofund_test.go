package paymaster

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newStateProcessor(t *testing.T) *core.StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := core.NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func registerZNHB(t *testing.T, manager *nhbstate.Manager) {
	t.Helper()
	if err := manager.RegisterToken("ZNHB", "ZapNHB", 18); err != nil {
		t.Fatalf("register token: %v", err)
	}
}

func paymasterStorageKey(addr crypto.Address) string {
	return strings.ToLower(common.BytesToAddress(addr.Bytes()).Hex())
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

func TestPaymasterAutoTopUpSuccess(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	operatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate operator: %v", err)
	}
	operatorAddr := operatorKey.PubKey().Address()
	var operatorBytes [20]byte
	copy(operatorBytes[:], operatorAddr.Bytes())

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		Operator:       operatorBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	start := time.Unix(1_700_000_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{0xAA}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: paymasterAddr.Bytes(),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, paymasterKey)

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment.Status != core.SponsorshipStatusReady {
		t.Fatalf("expected ready status, got %s", assessment.Status)
	}

	account, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("expected balance 2500, got %v", account.BalanceZNHB)
	}

	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord == nil || dayRecord.MintedWei.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("expected minted 2500, got %#v", dayRecord)
	}

	eventsList := sp.Events()
	if len(eventsList) == 0 || eventsList[len(eventsList)-1].Type != events.TypePaymasterAutoTopUp {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	attrs := eventsList[len(eventsList)-1].Attributes
	if attrs["status"] != "success" {
		t.Fatalf("expected success status, got %v", attrs["status"])
	}
}

func TestPaymasterAutoTopUpRespectsCooldown(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	operatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate operator: %v", err)
	}
	operatorAddr := operatorKey.PubKey().Address()
	var operatorBytes [20]byte
	copy(operatorBytes[:], operatorAddr.Bytes())

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		Operator:       operatorBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	start := time.Unix(1_700_100_000, 0).UTC()
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	if err := manager.PaymasterPutTopUpStatus(&nhbstate.PaymasterTopUpStatus{Paymaster: paymasterStorageKey(paymasterAddr), LastUnix: uint64(start.Add(-time.Minute).Unix())}); err != nil {
		t.Fatalf("seed cooldown status: %v", err)
	}

	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{0xBB}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: paymasterAddr.Bytes(),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, paymasterKey)

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment.Status != core.SponsorshipStatusReady {
		t.Fatalf("expected ready status, got %s", assessment.Status)
	}

	account, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
		t.Fatalf("expected no minted amount, got %#v", dayRecord)
	}

	eventsList := sp.Events()
	if len(eventsList) == 0 || eventsList[len(eventsList)-1].Type != events.TypePaymasterAutoTopUp {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	attrs := eventsList[len(eventsList)-1].Attributes
	if attrs["status"] != "failure" || attrs["reason"] != "cooldown_active" {
		t.Fatalf("expected cooldown failure, got %#v", attrs)
	}
}

func TestPaymasterAutoTopUpRoleValidation(t *testing.T) {
	cases := []struct {
		name           string
		assignMinter   bool
		assignApprover bool
		expectedReason string
	}{
		{
			name:           "missing-minter-role",
			assignApprover: true,
			expectedReason: "minter_role_missing",
		},
		{
			name:           "missing-approver-role",
			assignMinter:   true,
			expectedReason: "approver_role_missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStateProcessor(t)
			manager := nhbstate.NewManager(sp.Trie)
			registerZNHB(t, manager)

			operatorKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate operator: %v", err)
			}
			operatorAddr := operatorKey.PubKey().Address()
			var operatorBytes [20]byte
			copy(operatorBytes[:], operatorAddr.Bytes())

			policy := core.PaymasterAutoTopUpPolicy{
				Enabled:        true,
				Token:          "ZNHB",
				MinBalanceWei:  big.NewInt(1_000),
				TopUpAmountWei: big.NewInt(2_500),
				DailyCapWei:    big.NewInt(10_000),
				Cooldown:       time.Hour,
				Operator:       operatorBytes,
				ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
				MinterRole:     "MINTER_ZNHB",
			}
			sp.SetPaymasterAutoTopUpPolicy(policy)

			if tc.assignMinter {
				if err := manager.SetRole(policy.MinterRole, operatorAddr.Bytes()); err != nil {
					t.Fatalf("assign minter role: %v", err)
				}
			}
			if tc.assignApprover {
				if err := manager.SetRole(policy.ApproverRole, operatorAddr.Bytes()); err != nil {
					t.Fatalf("assign approver role: %v", err)
				}
			}

			paymasterKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate paymaster key: %v", err)
			}
			paymasterAddr := paymasterKey.PubKey().Address()

			sp.SetPaymasterEnabled(true)

			senderKey, err := crypto.GeneratePrivateKey()
			if err != nil {
				t.Fatalf("generate sender: %v", err)
			}
			senderAddr := senderKey.PubKey().Address()
			if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
				t.Fatalf("seed sender metadata: %v", err)
			}

			start := time.Unix(1_700_200_000, 0).UTC()
			dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)

			sp.BeginBlock(1, start)
			defer sp.EndBlock()

			tx := &types.Transaction{
				ChainID:   types.NHBChainID(),
				Type:      types.TxTypeTransfer,
				Nonce:     0,
				To:        common.Address{0xCC}.Bytes(),
				Value:     big.NewInt(1),
				GasLimit:  21000,
				GasPrice:  big.NewInt(0),
				Paymaster: paymasterAddr.Bytes(),
			}
			if err := tx.Sign(senderKey.PrivateKey); err != nil {
				t.Fatalf("sign sender: %v", err)
			}
			signPaymaster(t, tx, paymasterKey)

			assessment, err := sp.EvaluateSponsorship(tx)
			if err != nil {
				t.Fatalf("evaluate sponsorship: %v", err)
			}
			if assessment.Status != core.SponsorshipStatusReady {
				t.Fatalf("expected ready status, got %s", assessment.Status)
			}

			account, err := sp.GetAccount(paymasterAddr.Bytes())
			if err != nil {
				t.Fatalf("get paymaster: %v", err)
			}
			if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
				t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
			}

			dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
			if err != nil {
				t.Fatalf("get top-up day: %v", err)
			}
			if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
				t.Fatalf("expected no minted amount, got %#v", dayRecord)
			}

			eventsList := sp.Events()
			if len(eventsList) == 0 || eventsList[len(eventsList)-1].Type != events.TypePaymasterAutoTopUp {
				t.Fatalf("expected auto top-up event, got %#v", eventsList)
			}
			attrs := eventsList[len(eventsList)-1].Attributes
			if attrs["status"] != "failure" || attrs["reason"] != tc.expectedReason {
				t.Fatalf("expected %s failure, got %#v", tc.expectedReason, attrs)
			}
		})
	}
}

func TestPaymasterAutoTopUpDailyCap(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	operatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate operator: %v", err)
	}
	operatorAddr := operatorKey.PubKey().Address()
	var operatorBytes [20]byte
	copy(operatorBytes[:], operatorAddr.Bytes())

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		Operator:       operatorBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, operatorAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	start := time.Unix(1_700_300_000, 0).UTC()
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	if err := manager.PaymasterPutTopUpDay(&nhbstate.PaymasterTopUpDay{
		Paymaster: paymasterStorageKey(paymasterAddr),
		Day:       dayKey,
		MintedWei: big.NewInt(9_500),
	}); err != nil {
		t.Fatalf("seed day record: %v", err)
	}

	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{0xDD}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: paymasterAddr.Bytes(),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, paymasterKey)

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment.Status != core.SponsorshipStatusReady {
		t.Fatalf("expected ready status, got %s", assessment.Status)
	}

	account, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord == nil || dayRecord.MintedWei.Cmp(big.NewInt(9_500)) != 0 {
		t.Fatalf("expected minted amount 9500, got %#v", dayRecord)
	}

	eventsList := sp.Events()
	if len(eventsList) == 0 || eventsList[len(eventsList)-1].Type != events.TypePaymasterAutoTopUp {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	attrs := eventsList[len(eventsList)-1].Attributes
	if attrs["status"] != "failure" || attrs["reason"] != "daily_cap_exceeded" {
		t.Fatalf("expected daily cap failure, got %#v", attrs)
	}
}
