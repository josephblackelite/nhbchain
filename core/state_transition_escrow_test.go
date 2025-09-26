package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

func TestEscrowNativeLifecycle(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xAA
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	var payeeAccountAddr [20]byte
	copy(payeeAccountAddr[:], payeeAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(1_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	meta := [32]byte{}
	escrowID := ethcrypto.Keccak256Hash(payerAddr.Bytes(), payeeAddr.Bytes(), meta[:])

	createPayload := struct {
		Payee    []byte   `json:"payee"`
		Token    string   `json:"token"`
		Amount   *big.Int `json:"amount"`
		FeeBps   uint32   `json:"feeBps"`
		Deadline int64    `json:"deadline"`
	}{
		Payee:    payeeAddr.Bytes(),
		Token:    "NHB",
		Amount:   big.NewInt(100),
		FeeBps:   100,
		Deadline: time.Now().Add(2 * time.Hour).Unix(),
	}
	createData, err := jsonMarshal(createPayload)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	createTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeCreateEscrow,
		Nonce:    0,
		Data:     createData,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := createTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow not stored")
	}
	if esc.Status != escrow.EscrowInit {
		t.Fatalf("unexpected escrow status after create: %v", esc.Status)
	}

	fundTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLockEscrow,
		Nonce:    1,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := fundTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign fund: %v", err)
	}
	if err := sp.ApplyTransaction(fundTx); err != nil {
		t.Fatalf("apply fund: %v", err)
	}
	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after fund")
	}
	if esc.Status != escrow.EscrowFunded {
		t.Fatalf("unexpected status after fund: %v", esc.Status)
	}

	releaseTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeReleaseEscrow,
		Nonce:    0,
		Data:     escrowID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := releaseTx.Sign(payeeKey.PrivateKey); err != nil {
		t.Fatalf("sign release: %v", err)
	}
	if err := sp.ApplyTransaction(releaseTx); err != nil {
		t.Fatalf("apply release: %v", err)
	}

	esc, ok = manager.EscrowGet(escrowID)
	if !ok {
		t.Fatalf("escrow missing after release")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("unexpected status after release: %v", esc.Status)
	}

	payerAccount, err := sp.getAccount(payerAddr.Bytes())
	if err != nil {
		t.Fatalf("load payer account: %v", err)
	}
	if payerAccount.Nonce != 2 {
		t.Fatalf("unexpected payer nonce: %d", payerAccount.Nonce)
	}
	expectedPayer := big.NewInt(1_000)
	expectedPayer.Sub(expectedPayer, big.NewInt(100))
	if payerAccount.BalanceNHB.Cmp(expectedPayer) != 0 {
		t.Fatalf("unexpected payer balance: %s", payerAccount.BalanceNHB)
	}

	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee account: %v", err)
	}
	if payeeAccount.Nonce != 1 {
		t.Fatalf("unexpected payee nonce: %d", payeeAccount.Nonce)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(99)) != 0 {
		t.Fatalf("unexpected payee balance: %s", payeeAccount.BalanceNHB)
	}

	treasuryAccount, err := sp.getAccount(treasury[:])
	if err != nil {
		t.Fatalf("load treasury account: %v", err)
	}
	if treasuryAccount.BalanceNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("unexpected treasury balance: %s", treasuryAccount.BalanceNHB)
	}
}

func TestEscrowLegacyMigration(t *testing.T) {
	sp := newStakingStateProcessor(t)

	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	payeeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payee key: %v", err)
	}

	payerAddr := payerKey.PubKey().Address()
	payeeAddr := payeeKey.PubKey().Address()

	var treasury [20]byte
	treasury[0] = 0xBB
	sp.SetEscrowFeeTreasury(treasury)

	var payerAccountAddr [20]byte
	copy(payerAccountAddr[:], payerAddr.Bytes())
	var payeeAccountAddr [20]byte
	copy(payeeAccountAddr[:], payeeAddr.Bytes())

	writeAccount(t, sp, payerAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payeeAccountAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, treasury, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	legacyID := ethcrypto.Keccak256Hash([]byte("legacy"), payerAddr.Bytes(), payeeAddr.Bytes())
	legacy := &escrow.LegacyEscrow{
		ID:     append([]byte(nil), legacyID[:]...),
		Buyer:  append([]byte(nil), payeeAddr.Bytes()...),
		Seller: append([]byte(nil), payerAddr.Bytes()...),
		Amount: big.NewInt(50),
		Status: escrow.LegacyStatusInProgress,
	}
	encodedLegacy, err := rlp.EncodeToBytes(legacy)
	if err != nil {
		t.Fatalf("encode legacy: %v", err)
	}
	legacyKey := ethcrypto.Keccak256(append([]byte("escrow-"), legacyID[:]...))
	if err := sp.Trie.Update(legacyKey, encodedLegacy); err != nil {
		t.Fatalf("write legacy escrow: %v", err)
	}

	releaseTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeReleaseEscrow,
		Nonce:    0,
		Data:     legacyID[:],
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := releaseTx.Sign(payeeKey.PrivateKey); err != nil {
		t.Fatalf("sign legacy release: %v", err)
	}
	if err := sp.ApplyTransaction(releaseTx); err != nil {
		t.Fatalf("apply legacy release: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	esc, ok := manager.EscrowGet(legacyID)
	if !ok {
		t.Fatalf("escrow not migrated")
	}
	if esc.Status != escrow.EscrowReleased {
		t.Fatalf("expected released status, got %v", esc.Status)
	}

	payeeAccount, err := sp.getAccount(payeeAddr.Bytes())
	if err != nil {
		t.Fatalf("load payee: %v", err)
	}
	if payeeAccount.BalanceNHB.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("unexpected payee balance after migration: %s", payeeAccount.BalanceNHB)
	}
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}
