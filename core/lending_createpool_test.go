package core

import (
	"encoding/json"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func lendingCreatePoolTx(t *testing.T, nonce uint64, poolID string) *types.Transaction {
	t.Helper()
	data, err := json.Marshal(lendingNativePayload{PoolID: poolID})
	if err != nil {
		t.Fatalf("encode create-pool payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingCreatePool,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
}

func TestApplyLendingCreatePoolTransaction_CreatesOwnedByTheSigner(t *testing.T) {
	sp := newRewardTestState(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sender := key.PubKey().Address().Bytes()

	tx := lendingCreatePoolTx(t, 0, "acme-pool")
	if err := sp.applyLendingCreatePoolTransaction(tx, sender); err != nil {
		t.Fatalf("apply create pool: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	market, ok, err := manager.LendingGetMarket("acme-pool")
	if err != nil {
		t.Fatalf("load market: %v", err)
	}
	if !ok || market == nil {
		t.Fatalf("expected market to exist")
	}
	if market.DeveloperOwner.String() != key.PubKey().Address().String() {
		t.Fatalf("DeveloperOwner = %s, want %s (the signer, not a client-supplied field)", market.DeveloperOwner.String(), key.PubKey().Address().String())
	}

	account, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("load sender account: %v", err)
	}
	if account.Nonce != 1 {
		t.Fatalf("sender nonce = %d, want 1", account.Nonce)
	}
}

func TestApplyLendingCreatePoolTransaction_RejectsDuplicateAndDefaultPoolID(t *testing.T) {
	sp := newRewardTestState(t)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sender := key.PubKey().Address().Bytes()

	if err := sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, 0, "acme-pool"), sender); err != nil {
		t.Fatalf("first create pool: %v", err)
	}
	if err := sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, 1, "acme-pool"), sender); err == nil {
		t.Fatalf("expected an error creating a pool that already exists")
	}
	if err := sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, 1, "default"), sender); err == nil {
		t.Fatalf("expected an error explicitly naming the reserved 'default' pool")
	}
	if err := sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, 1, ""), sender); err == nil {
		t.Fatalf("expected an error for an empty poolId (defaults to 'default')")
	}
}

// TestLendingCreatePoolBlock_ProposerAndValidatorAgree drives the real
// CreateBlock/ValidateBlock/CommitBlock production code paths for a block
// containing a real, validly-signed TxTypeLendingCreatePool transaction --
// proving two independently constructed nodes derive the same state root,
// the way the buyback/lending-refprice/governance regression tests do for
// their own senderless/signed transaction types. This is the direct
// regression test for the bug this fix closes: LendingModule.CreatePool
// used to mutate each validator's local trie directly, outside of
// consensus.
func TestLendingCreatePoolBlock_ProposerAndValidatorAgree(t *testing.T) {
	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		validatorKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate node validator key: %v", err)
		}
		node, err := NewNode(db, validatorKey, "", true, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		return node
	}
	proposer := build()
	validator := build()

	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	tx := lendingCreatePoolTx(t, 0, "acme-pool")
	if err := tx.Sign(ownerKey.PrivateKey); err != nil {
		t.Fatalf("sign create pool tx: %v", err)
	}
	if err := proposer.AddTransaction(tx); err != nil {
		t.Fatalf("add create pool tx: %v", err)
	}

	block, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the create-pool tx to survive into the proposed block, got %d txs", len(block.Transactions))
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected proposer's block: %v", err)
	}
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatalf("proposer commit block: %v", err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatalf("validator commit block: %v", err)
	}

	for _, node := range []*Node{proposer, validator} {
		var ownerAddr string
		if err := node.WithState(func(m *nhbstate.Manager) error {
			market, ok, err := m.LendingGetMarket("acme-pool")
			if err != nil {
				return err
			}
			if !ok || market == nil {
				t.Fatalf("expected market to exist")
			}
			ownerAddr = market.DeveloperOwner.String()
			return nil
		}); err != nil {
			t.Fatalf("read market: %v", err)
		}
		if ownerAddr != ownerKey.PubKey().Address().String() {
			t.Fatalf("DeveloperOwner = %s, want %s", ownerAddr, ownerKey.PubKey().Address().String())
		}
	}
}
