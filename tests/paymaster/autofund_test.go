package paymaster

import (
	"errors"
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

func findEventByType(events []types.Event, eventType string) *types.Event {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func addressBytes(addr crypto.Address) [20]byte {
	var out [20]byte
	copy(out[:], addr.Bytes())
	return out
}

func requireDistinctIdentities(t *testing.T, minter, approver [20]byte) {
	t.Helper()
	if minter == ([20]byte{}) || approver == ([20]byte{}) {
		return
	}
	if minter == approver {
		t.Fatalf("expected distinct minter and approver identities")
	}
}

func putZNHBAccount(t *testing.T, manager *nhbstate.Manager, addr crypto.Address, balance *big.Int) {
	t.Helper()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(balance), Stake: big.NewInt(0)}
	if err := manager.PutAccount(addr.Bytes(), account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}

type autoTopUpActors struct {
	fundingAddr   crypto.Address
	fundingBytes  [20]byte
	minterAddr    crypto.Address
	minterBytes   [20]byte
	approverAddr  crypto.Address
	approverBytes [20]byte
}

func newAutoTopUpActors(t *testing.T) autoTopUpActors {
	t.Helper()
	fundingKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate funding key: %v", err)
	}
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter key: %v", err)
	}
	approverKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate approver key: %v", err)
	}
	actors := autoTopUpActors{
		fundingAddr:  fundingKey.PubKey().Address(),
		minterAddr:   minterKey.PubKey().Address(),
		approverAddr: approverKey.PubKey().Address(),
	}
	actors.fundingBytes = addressBytes(actors.fundingAddr)
	actors.minterBytes = addressBytes(actors.minterAddr)
	actors.approverBytes = addressBytes(actors.approverAddr)
	requireDistinctIdentities(t, actors.minterBytes, actors.approverBytes)
	return actors
}

func TestPaymasterAutoTopUpSuccess(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors := newAutoTopUpActors(t)

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, manager, actors.fundingAddr, initialFunding)

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
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged before apply, got %v", account.BalanceZNHB)
	}
	fundingAccount, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(initialFunding) != 0 {
		t.Fatalf("expected funding balance %v before apply, got %v", initialFunding, fundingAccount)
	}
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	dayRecordBefore, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day before apply: %v", err)
	}
	mintedBefore := big.NewInt(0)
	if dayRecordBefore != nil && dayRecordBefore.MintedWei != nil {
		mintedBefore = new(big.Int).Set(dayRecordBefore.MintedWei)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	account, err = sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("expected balance 2500, got %v", account.BalanceZNHB)
	}

	fundingAccount, err = sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	expectedFunding := new(big.Int).Sub(new(big.Int).Set(initialFunding), policy.TopUpAmountWei)
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(expectedFunding) != 0 {
		t.Fatalf("expected funding balance %v after apply, got %v", expectedFunding, fundingAccount)
	}

	dayRecordAfter, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day after apply: %v", err)
	}
	mintedAfter := big.NewInt(0)
	if dayRecordAfter != nil && dayRecordAfter.MintedWei != nil {
		mintedAfter = new(big.Int).Set(dayRecordAfter.MintedWei)
	}
	if mintedAfter.Cmp(mintedBefore) != 0 {
		t.Fatalf("expected minted totals unchanged (%v), got %v", mintedBefore, mintedAfter)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if status := evt.Attributes["status"]; status != "success" {
		t.Fatalf("expected success status, got %v", status)
	}
}

func TestPaymasterAutoTopUpRespectsCooldown(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors := newAutoTopUpActors(t)

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, manager, actors.fundingAddr, big.NewInt(50_000))

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
		t.Fatalf("expected balance unchanged before apply, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
		t.Fatalf("expected no minted amount before apply, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	account, err = sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err = manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
		t.Fatalf("expected no minted amount after apply, got %#v", dayRecord)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "cooldown_active" {
		t.Fatalf("expected cooldown failure, got %#v", evt.Attributes)
	}
}

func TestPaymasterAutoTopUpRoleValidation(t *testing.T) {
	cases := []struct {
		name                 string
		withMinterIdentity   bool
		withApproverIdentity bool
		assignMinterRole     bool
		assignApproverRole   bool
		expectedReason       string
	}{
		{
			name:                 "missing-minter-identity",
			withMinterIdentity:   false,
			withApproverIdentity: true,
			assignApproverRole:   true,
			expectedReason:       "minter_missing",
		},
		{
			name:                 "missing-approver-identity",
			withMinterIdentity:   true,
			withApproverIdentity: false,
			assignMinterRole:     true,
			expectedReason:       "approver_missing",
		},
		{
			name:                 "missing-minter-role",
			withMinterIdentity:   true,
			withApproverIdentity: true,
			assignApproverRole:   true,
			expectedReason:       "minter_role_missing",
		},
		{
			name:                 "missing-approver-role",
			withMinterIdentity:   true,
			withApproverIdentity: true,
			assignMinterRole:     true,
			expectedReason:       "approver_role_missing",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStateProcessor(t)
			manager := nhbstate.NewManager(sp.Trie)
			registerZNHB(t, manager)

			actors := newAutoTopUpActors(t)

			policy := core.PaymasterAutoTopUpPolicy{
				Enabled:        true,
				Token:          "ZNHB",
				MinBalanceWei:  big.NewInt(1_000),
				TopUpAmountWei: big.NewInt(2_500),
				DailyCapWei:    big.NewInt(10_000),
				Cooldown:       time.Hour,
				FundingAccount: actors.fundingBytes,
				Minter:         actors.minterBytes,
				Approver:       actors.approverBytes,
				ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
				MinterRole:     "MINTER_ZNHB",
			}
			if !tc.withMinterIdentity {
				policy.Minter = [20]byte{}
			}
			if !tc.withApproverIdentity {
				policy.Approver = [20]byte{}
			}
			sp.SetPaymasterAutoTopUpPolicy(policy)

			if tc.assignMinterRole && tc.withMinterIdentity {
				if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
					t.Fatalf("assign minter role: %v", err)
				}
			}
			if tc.assignApproverRole && tc.withApproverIdentity {
				if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
					t.Fatalf("assign approver role: %v", err)
				}
			}

			putZNHBAccount(t, manager, actors.fundingAddr, big.NewInt(50_000))

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
				t.Fatalf("expected balance unchanged before apply, got %v", account.BalanceZNHB)
			}

			dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
			if err != nil {
				t.Fatalf("get top-up day: %v", err)
			}
			if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
				t.Fatalf("expected no minted amount before apply, got %#v", dayRecord)
			}
			if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
				t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
			}

			if err := sp.ApplyTransaction(tx); err != nil {
				t.Fatalf("apply transaction: %v", err)
			}

			account, err = sp.GetAccount(paymasterAddr.Bytes())
			if err != nil {
				t.Fatalf("get paymaster: %v", err)
			}
			if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
				t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
			}

			dayRecord, _, err = manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
			if err != nil {
				t.Fatalf("get top-up day: %v", err)
			}
			if dayRecord != nil && dayRecord.MintedWei.Sign() != 0 {
				t.Fatalf("expected no minted amount after apply, got %#v", dayRecord)
			}

			eventsList := sp.Events()
			evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
			if evt == nil {
				t.Fatalf("expected auto top-up event, got %#v", eventsList)
			}
			if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != tc.expectedReason {
				t.Fatalf("expected %s failure, got %#v", tc.expectedReason, evt.Attributes)
			}
		})
	}
}

func TestPaymasterAutoTopUpDailyCap(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors := newAutoTopUpActors(t)

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, manager, actors.fundingAddr, big.NewInt(50_000))

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
		t.Fatalf("expected balance unchanged before apply, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord == nil || dayRecord.MintedWei.Cmp(big.NewInt(9_500)) != 0 {
		t.Fatalf("expected minted amount 9500 before apply, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	account, err = sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}

	dayRecord, _, err = manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord == nil || dayRecord.MintedWei.Cmp(big.NewInt(9_500)) != 0 {
		t.Fatalf("expected minted amount 9500 after apply, got %#v", dayRecord)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "daily_cap_exceeded" {
		t.Fatalf("expected daily cap failure, got %#v", evt.Attributes)
	}
}

func TestPaymasterAutoTopUpNoMutationWhenThrottled(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors := newAutoTopUpActors(t)

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, manager, actors.fundingAddr, big.NewInt(50_000))

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)
	sp.SetPaymasterLimits(core.PaymasterLimits{GlobalDailyCapWei: big.NewInt(1)})

	if err := manager.SetBalance(paymasterAddr.Bytes(), "NHB", big.NewInt(1_000_000_000)); err != nil {
		t.Fatalf("seed paymaster balance: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	start := time.Unix(1_700_400_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{0xEE}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(1),
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
	if assessment.Status != core.SponsorshipStatusThrottled {
		t.Fatalf("expected throttled status, got %s", assessment.Status)
	}

	account, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil {
		t.Fatalf("expected no day record, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	err = sp.ApplyTransaction(tx)
	if !errors.Is(err, core.ErrSponsorshipRejected) {
		t.Fatalf("expected sponsorship rejection, got %v", err)
	}

	account, err = sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged after apply, got %v", account.BalanceZNHB)
	}
	dayRecord, _, err = manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil {
		t.Fatalf("expected no day record after apply, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event after apply: %#v", evt)
	}
}

func TestPaymasterAutoTopUpNoMutationOnFailure(t *testing.T) {
	sp := newStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors := newAutoTopUpActors(t)

	policy := core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: big.NewInt(2_500),
		DailyCapWei:    big.NewInt(10_000),
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "MINTER_ZNHB",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, manager, actors.fundingAddr, big.NewInt(50_000))

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

	start := time.Unix(1_700_500_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{0xEF}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(1),
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
	if assessment.Status != core.SponsorshipStatusInsufficientBalance {
		t.Fatalf("expected insufficient balance status, got %s", assessment.Status)
	}

	account, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged, got %v", account.BalanceZNHB)
	}
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	dayRecord, _, err := manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil {
		t.Fatalf("expected no day record, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	err = sp.ApplyTransaction(tx)
	if !errors.Is(err, core.ErrSponsorshipRejected) {
		t.Fatalf("expected sponsorship rejection, got %v", err)
	}

	account, err = sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if account.BalanceZNHB == nil || account.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected balance unchanged after apply, got %v", account.BalanceZNHB)
	}
	dayRecord, _, err = manager.PaymasterGetTopUpDay(paymasterStorageKey(paymasterAddr), dayKey)
	if err != nil {
		t.Fatalf("get top-up day: %v", err)
	}
	if dayRecord != nil {
		t.Fatalf("expected no day record after apply, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event after apply: %#v", evt)
	}
}
