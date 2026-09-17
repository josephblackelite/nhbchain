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
	"nhbchain/native/governance"
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
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.RegisterToken("NHB", "NHB", 18); err != nil {
		t.Fatalf("register NHB: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit initial state: %v", err)
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

type autoTopUpActors struct {
	fundingAddr   crypto.Address
	minterAddr    crypto.Address
	approverAddr  crypto.Address
	fundingBytes  [20]byte
	minterBytes   [20]byte
	approverBytes [20]byte
}

func newAutoTopUpActors(t *testing.T) autoTopUpActors {
	t.Helper()
	fundingKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate funding: %v", err)
	}
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate minter: %v", err)
	}
	approverKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate approver: %v", err)
	}
	fundingAddr := fundingKey.PubKey().Address()
	minterAddr := minterKey.PubKey().Address()
	approverAddr := approverKey.PubKey().Address()
	var fundingBytes, minterBytes, approverBytes [20]byte
	copy(fundingBytes[:], fundingAddr.Bytes())
	copy(minterBytes[:], minterAddr.Bytes())
	copy(approverBytes[:], approverAddr.Bytes())
	return autoTopUpActors{
		fundingAddr:   fundingAddr,
		minterAddr:    minterAddr,
		approverAddr:  approverAddr,
		fundingBytes:  fundingBytes,
		minterBytes:   minterBytes,
		approverBytes: approverBytes,
	}
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

func putZNHBAccount(t *testing.T, sp *core.StateProcessor, manager *nhbstate.Manager, addr crypto.Address, balance *big.Int) {
	t.Helper()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: new(big.Int).Set(balance), Stake: big.NewInt(0)}
	if err := manager.PutAccount(addr.Bytes(), account); err != nil {
		t.Fatalf("put account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit account %x: %v", addr.Bytes()[:4], err)
	}
}

func putAccountBalances(t *testing.T, sp *core.StateProcessor, addr crypto.Address, nhb, znhb *big.Int) {
	t.Helper()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if nhb != nil {
		account.BalanceNHB = new(big.Int).Set(nhb)
	}
	if znhb != nil {
		account.BalanceZNHB = new(big.Int).Set(znhb)
	}
	if err := sp.PutAccount(addr.Bytes(), account); err != nil {
		t.Fatalf("put account %x: %v", addr.Bytes()[:4], err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit account %x: %v", addr.Bytes()[:4], err)
	}
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
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
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
	putAccountBalances(t, sp, senderAddr, big.NewInt(1), big.NewInt(0))
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
	if dayRecordAfter == nil || dayRecordAfter.DebitedWei == nil || dayRecordAfter.DebitedWei.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("expected debited 2500, got %#v", dayRecordAfter)
	}

	fundingAccount, err = sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	if fundingAccount.BalanceZNHB == nil || fundingAccount.BalanceZNHB.Cmp(expectedFunding) != 0 {
		t.Fatalf("expected funding balance %v, got %v", expectedFunding, fundingAccount.BalanceZNHB)
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
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(50_000))
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
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
	putAccountBalances(t, sp, senderAddr, big.NewInt(1), big.NewInt(0))
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
	if dayRecord != nil && dayRecord.DebitedWei.Sign() != 0 {
		t.Fatalf("expected no debited amount before apply, got %#v", dayRecord)
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
	if dayRecord != nil && dayRecord.DebitedWei.Sign() != 0 {
		t.Fatalf("expected no debited amount after apply, got %#v", dayRecord)
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
			expectedReason:       "operator_missing",
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
			expectedReason:       "operator_role_missing",
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
				MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
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

			putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(50_000))
			if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
				t.Fatalf("seed funding balance: %v", err)
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
			putAccountBalances(t, sp, senderAddr, big.NewInt(1), big.NewInt(0))
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
			if dayRecord != nil && dayRecord.DebitedWei.Sign() != 0 {
				t.Fatalf("expected no debited amount before apply, got %#v", dayRecord)
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
			if dayRecord != nil && dayRecord.DebitedWei.Sign() != 0 {
				t.Fatalf("expected no debited amount after apply, got %#v", dayRecord)
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
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(50_000))
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)
	paymasterAccount := &types.Account{BalanceNHB: big.NewInt(1_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.PutAccount(paymasterAddr.Bytes(), paymasterAccount); err != nil {
		t.Fatalf("put paymaster account: %v", err)
	}
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	senderAccount := &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.PutAccount(senderAddr.Bytes(), senderAccount); err != nil {
		t.Fatalf("put sender account: %v", err)
	}
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	start := time.Unix(1_700_300_000, 0).UTC()
	dayKey := start.UTC().Format(nhbstate.PaymasterDayFormat)
	if err := manager.PaymasterPutTopUpDay(&nhbstate.PaymasterTopUpDay{
		Paymaster:  paymasterStorageKey(paymasterAddr),
		Day:        dayKey,
		DebitedWei: big.NewInt(9_500),
	}); err != nil {
		t.Fatalf("seed day record: %v", err)
	}

	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit initial state: %v", err)
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
	if dayRecord == nil || dayRecord.DebitedWei.Cmp(big.NewInt(9_500)) != 0 {
		t.Fatalf("expected debited amount 9500 before apply, got %#v", dayRecord)
	}
	if evt := findEventByType(sp.Events(), events.TypePaymasterAutoTopUp); evt != nil {
		t.Fatalf("unexpected auto top-up event before apply: %#v", evt)
	}

	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit before apply: %v", err)
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
	if dayRecord == nil || dayRecord.DebitedWei.Cmp(big.NewInt(9_500)) != 0 {
		t.Fatalf("expected debited amount 9500 after apply, got %#v", dayRecord)
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
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(50_000))
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)
	sp.SetPaymasterLimits(core.PaymasterLimits{GlobalDailyCapWei: big.NewInt(1)})

	paymasterAccount := &types.Account{BalanceNHB: big.NewInt(1_000_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.PutAccount(paymasterAddr.Bytes(), paymasterAccount); err != nil {
		t.Fatalf("put paymaster account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit initial state: %v", err)
	}
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit initial state: %v", err)
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

	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit before apply: %v", err)
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

// newFeeTestSetup builds a StateProcessor, funding/minter/approver actors,
// and a paymaster auto-top-up policy identical across the fee tests below,
// returning everything a caller needs to seed balances and drive a
// sponsored transaction. amount and dailyCap let each test pick values that
// exercise the fee arithmetic differently (e.g. a cap that fits amount
// alone but not amount+fee).
func newFeeTestSetup(t *testing.T, amount, dailyCap *big.Int) (sp *core.StateProcessor, manager *nhbstate.Manager, actors autoTopUpActors, policy core.PaymasterAutoTopUpPolicy, treasuryBytes [20]byte) {
	t.Helper()
	sp = newStateProcessor(t)
	manager = nhbstate.NewManager(sp.Trie)
	registerZNHB(t, manager)

	actors = newAutoTopUpActors(t)

	treasuryBytes[19] = 0x77
	sp.SetEscrowFeeTreasury(treasuryBytes)

	policy = core.PaymasterAutoTopUpPolicy{
		Enabled:        true,
		Token:          "ZNHB",
		MinBalanceWei:  big.NewInt(1_000),
		TopUpAmountWei: amount,
		DailyCapWei:    dailyCap,
		Cooldown:       time.Hour,
		FundingAccount: actors.fundingBytes,
		Minter:         actors.minterBytes,
		Approver:       actors.approverBytes,
		ApproverRole:   "ROLE_PAYMASTER_AUTOFUND",
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}
	return sp, manager, actors, policy, treasuryBytes
}

// buildFeeTestTx creates and signs a sponsored transfer, wiring up a fresh
// sender/paymaster pair, and returns the paymaster address alongside the
// signed transaction so callers can assert on the paymaster's own balance
// afterwards.
func buildFeeTestTx(t *testing.T, sp *core.StateProcessor, manager *nhbstate.Manager, to byte) (paymasterAddr crypto.Address, tx *types.Transaction) {
	t.Helper()
	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr = paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	putAccountBalances(t, sp, senderAddr, big.NewInt(1), big.NewInt(0))
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}

	tx = &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{to}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: paymasterAddr.Bytes(),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, paymasterKey)
	return paymasterAddr, tx
}

// TestPaymasterAutoTopUpFeeCreditsTreasury is the direct regression test for
// the user-mandated fee mechanism: governance.ParamKeyPaymasterTopUpFeeWei
// (core/sponsorship.go's readGovernedPaymasterTopUpFeeWei), which mirrors
// native/market/engine.go's FillListing flatFeeWei pattern -- the funding
// account pays amount+fee, the paymaster still receives exactly amount, and
// the escrow fee treasury (core/node.go's escrowTreasury, wired in here via
// SetEscrowFeeTreasury exactly as node.go wires the live one) receives
// exactly fee. Mirrors state/bank/slash_test.go's conservation-check style:
// what leaves the funding account must exactly equal what lands in the
// paymaster's and treasury's credits combined -- nothing created or
// destroyed.
func TestPaymasterAutoTopUpFeeCreditsTreasury(t *testing.T) {
	amount := big.NewInt(2_500)
	feeWei := big.NewInt(100)
	sp, manager, actors, _, treasuryBytes := newFeeTestSetup(t, amount, big.NewInt(10_000))

	if err := manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte(feeWei.String())); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterAddr, tx := buildFeeTestTx(t, sp, manager, 0x11)

	start := time.Unix(1_700_600_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate sponsorship: %v", err)
	}
	if assessment.Status != core.SponsorshipStatusReady {
		t.Fatalf("expected ready status, got %s", assessment.Status)
	}

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	paymasterAccount, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if paymasterAccount.BalanceZNHB == nil || paymasterAccount.BalanceZNHB.Cmp(amount) != 0 {
		t.Fatalf("expected paymaster balance %s (fee must not reduce what the paymaster receives), got %v", amount, paymasterAccount.BalanceZNHB)
	}

	fundingAccount, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	expectedFunding := new(big.Int).Sub(initialFunding, new(big.Int).Add(amount, feeWei))
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(expectedFunding) != 0 {
		t.Fatalf("expected funding balance %s (initial - amount - fee), got %v", expectedFunding, fundingAccount.BalanceZNHB)
	}

	treasuryAccount, err := sp.GetAccount(treasuryBytes[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAccount == nil || treasuryAccount.BalanceZNHB == nil || treasuryAccount.BalanceZNHB.Cmp(feeWei) != 0 {
		t.Fatalf("expected treasury balance %s, got %v", feeWei, treasuryAccount.BalanceZNHB)
	}

	debitedFromFunding := new(big.Int).Sub(initialFunding, fundingAccount.BalanceZNHB)
	creditedElsewhere := new(big.Int).Add(paymasterAccount.BalanceZNHB, treasuryAccount.BalanceZNHB)
	if debitedFromFunding.Cmp(creditedElsewhere) != 0 {
		t.Fatalf("value not conserved: debited %s from funding, but only %s credited to paymaster+treasury", debitedFromFunding, creditedElsewhere)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "success" {
		t.Fatalf("expected success status, got %#v", evt.Attributes)
	}
	if got := evt.Attributes["feeWei"]; got != feeWei.String() {
		t.Fatalf("expected feeWei attribute %s, got %q", feeWei, got)
	}
}

// TestPaymasterAutoTopUpFeeDefaultsToZero confirms that leaving
// governance.ParamKeyPaymasterTopUpFeeWei unset (the state of the world on
// every existing deployment today, since this key is brand new) reproduces
// the pre-fee behavior exactly: the funding account is debited only the
// TopUpAmountWei, and the treasury balance stays untouched (does not even
// get created as an account). This is the regression guard that this
// change is additive, not a silent behavior change, for every paymaster
// that governance has not opted into the fee for.
func TestPaymasterAutoTopUpFeeDefaultsToZero(t *testing.T) {
	amount := big.NewInt(2_500)
	sp, manager, actors, _, treasuryBytes := newFeeTestSetup(t, amount, big.NewInt(10_000))

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterAddr, tx := buildFeeTestTx(t, sp, manager, 0x22)

	start := time.Unix(1_700_700_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	paymasterAccount, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if paymasterAccount.BalanceZNHB == nil || paymasterAccount.BalanceZNHB.Cmp(amount) != 0 {
		t.Fatalf("expected paymaster balance %s, got %v", amount, paymasterAccount.BalanceZNHB)
	}

	fundingAccount, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	expectedFunding := new(big.Int).Sub(initialFunding, amount)
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(expectedFunding) != 0 {
		t.Fatalf("expected funding balance %s (initial - amount, no fee), got %v", expectedFunding, fundingAccount.BalanceZNHB)
	}

	treasuryAccount, err := sp.GetAccount(treasuryBytes[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAccount != nil && treasuryAccount.BalanceZNHB != nil && treasuryAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected treasury untouched with no governed fee, got balance %v", treasuryAccount.BalanceZNHB)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if _, ok := evt.Attributes["feeWei"]; ok {
		t.Fatalf("expected no feeWei attribute when the fee is zero, got %#v", evt.Attributes)
	}
}

// TestPaymasterAutoTopUpFeeInsufficientForFeePlusAmount confirms the
// insufficient-funds check covers amount+fee, not just amount: a funding
// account that can afford the bare top-up amount but not the fee on top
// must fail the whole top-up atomically, with zero mutation anywhere
// (paymaster, funding, and treasury balances all unchanged) -- there must
// be no partial state where the paymaster is credited but the fee silently
// goes uncollected.
func TestPaymasterAutoTopUpFeeInsufficientForFeePlusAmount(t *testing.T) {
	amount := big.NewInt(2_500)
	feeWei := big.NewInt(100)
	sp, manager, actors, _, treasuryBytes := newFeeTestSetup(t, amount, big.NewInt(10_000))

	if err := manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte(feeWei.String())); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}

	// Exactly enough for the bare amount, one wei short of amount+fee.
	initialFunding := new(big.Int).Set(amount)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterAddr, tx := buildFeeTestTx(t, sp, manager, 0x33)

	start := time.Unix(1_700_800_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	paymasterAccount, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if paymasterAccount.BalanceZNHB == nil || paymasterAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected paymaster balance unchanged (top-up must fail atomically), got %v", paymasterAccount.BalanceZNHB)
	}

	fundingAccount, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(initialFunding) != 0 {
		t.Fatalf("expected funding balance unchanged at %s, got %v", initialFunding, fundingAccount.BalanceZNHB)
	}

	treasuryAccount, err := sp.GetAccount(treasuryBytes[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAccount != nil && treasuryAccount.BalanceZNHB != nil && treasuryAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected treasury untouched on a failed top-up, got balance %v", treasuryAccount.BalanceZNHB)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "funding_insufficient" {
		t.Fatalf("expected funding_insufficient failure, got %#v", evt.Attributes)
	}
}

// TestPaymasterAutoTopUpFeeCountsTowardDailyCap confirms DailyCapWei bounds
// the funding account's total daily outflow (amount+fee), not just the bare
// TopUpAmountWei -- a cap that comfortably fits amount alone but not
// amount+fee must reject the top-up as daily_cap_exceeded.
func TestPaymasterAutoTopUpFeeCountsTowardDailyCap(t *testing.T) {
	amount := big.NewInt(2_500)
	feeWei := big.NewInt(100)
	// Cap fits amount (2500) exactly but not amount+fee (2600).
	sp, manager, actors, _, treasuryBytes := newFeeTestSetup(t, amount, big.NewInt(2_500))

	if err := manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte(feeWei.String())); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterAddr, tx := buildFeeTestTx(t, sp, manager, 0x44)

	start := time.Unix(1_700_900_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	paymasterAccount, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if paymasterAccount.BalanceZNHB == nil || paymasterAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected paymaster balance unchanged, got %v", paymasterAccount.BalanceZNHB)
	}

	treasuryAccount, err := sp.GetAccount(treasuryBytes[:])
	if err != nil {
		t.Fatalf("get treasury: %v", err)
	}
	if treasuryAccount != nil && treasuryAccount.BalanceZNHB != nil && treasuryAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected treasury untouched, got balance %v", treasuryAccount.BalanceZNHB)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "daily_cap_exceeded" {
		t.Fatalf("expected daily_cap_exceeded failure, got %#v", evt.Attributes)
	}
}

// TestPaymasterAutoTopUpFeeMissingTreasuryFailsClosed confirms that a
// nonzero governed fee with no escrow fee treasury configured
// (core/node.go wires this via SetEscrowFeeTreasury from the genesis
// AdminWallet/NHB_MASTER_TREASURY; a node that has not done so has the zero
// address here) fails the top-up closed rather than silently burning the
// fee into the zero address.
func TestPaymasterAutoTopUpFeeMissingTreasuryFailsClosed(t *testing.T) {
	amount := big.NewInt(2_500)
	feeWei := big.NewInt(100)
	sp, manager, actors, _, _ := newFeeTestSetup(t, amount, big.NewInt(10_000))
	// Undo the treasury wiring newFeeTestSetup performs, to exercise the
	// unconfigured-treasury path specifically.
	sp.SetEscrowFeeTreasury([20]byte{})

	if err := manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte(feeWei.String())); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}

	initialFunding := big.NewInt(25_000)
	putZNHBAccount(t, sp, manager, actors.fundingAddr, initialFunding)
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(10_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}

	paymasterAddr, tx := buildFeeTestTx(t, sp, manager, 0x55)

	start := time.Unix(1_701_000_000, 0).UTC()
	sp.BeginBlock(1, start)
	defer sp.EndBlock()

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	paymasterAccount, err := sp.GetAccount(paymasterAddr.Bytes())
	if err != nil {
		t.Fatalf("get paymaster: %v", err)
	}
	if paymasterAccount.BalanceZNHB == nil || paymasterAccount.BalanceZNHB.Sign() != 0 {
		t.Fatalf("expected paymaster balance unchanged, got %v", paymasterAccount.BalanceZNHB)
	}

	fundingAccount, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil {
		t.Fatalf("get funding: %v", err)
	}
	if fundingAccount == nil || fundingAccount.BalanceZNHB.Cmp(initialFunding) != 0 {
		t.Fatalf("expected funding balance unchanged at %s, got %v", initialFunding, fundingAccount.BalanceZNHB)
	}

	eventsList := sp.Events()
	evt := findEventByType(eventsList, events.TypePaymasterAutoTopUp)
	if evt == nil {
		t.Fatalf("expected auto top-up event, got %#v", eventsList)
	}
	if evt.Attributes["status"] != "failure" || evt.Attributes["reason"] != "treasury_missing" {
		t.Fatalf("expected treasury_missing failure, got %#v", evt.Attributes)
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
		MinterRole:     "ROLE_PAYMASTER_AUTOFUND",
	}
	sp.SetPaymasterAutoTopUpPolicy(policy)

	if err := manager.SetRole(policy.MinterRole, actors.minterAddr.Bytes()); err != nil {
		t.Fatalf("assign minter role: %v", err)
	}
	if err := manager.SetRole(policy.ApproverRole, actors.approverAddr.Bytes()); err != nil {
		t.Fatalf("assign approver role: %v", err)
	}

	putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(50_000))

	paymasterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate paymaster key: %v", err)
	}
	paymasterAddr := paymasterKey.PubKey().Address()

	sp.SetPaymasterEnabled(true)
	paymasterAccount := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := manager.PutAccount(paymasterAddr.Bytes(), paymasterAccount); err != nil {
		t.Fatalf("put paymaster account: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit paymaster setup: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	senderAddr := senderKey.PubKey().Address()
	if err := manager.SetBalance(senderAddr.Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit initial state: %v", err)
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

	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit before apply: %v", err)
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
