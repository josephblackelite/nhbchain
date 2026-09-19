package core

import (
	"bytes"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// Shared helpers for the block-production liveness tests (poison matrix,
// conflict pairs, panic injection, strike book, performance, two-node).

// livenessPayload is one hostile Data payload the matrix feeds every
// transaction type.
type livenessPayload struct {
	name string
	data []byte
}

// livenessPayloads returns the fixed set of malformed payloads used by the
// poison matrix: empty, garbage, oversized, truncated RLP, empty lists, and
// several JSON shapes that decode into surprising values.
func livenessPayloads() []livenessPayload {
	garbage := make([]byte, 64)
	for i := range garbage {
		garbage[i] = byte(i*37 + 11)
	}
	return []livenessPayload{
		{"empty", nil},
		{"garbage64", garbage},
		{"huge64k", bytes.Repeat([]byte{0xAB}, 64*1024)},
		{"rlp_trunc_c1", []byte{0xC1}},
		{"rlp_trunc_f8", []byte{0xF8, 0xFF}},
		{"rlp_trunc_83", []byte{0x83, 0x01}},
		{"rlp_emptylist", []byte{0xC0}},
		{"rlp_empty_str", []byte{0x80}},
		{"json_null", []byte("null")},
		{"json_obj", []byte("{}")},
		{"json_arr", []byte("[]")},
		{"json_nullfields", []byte(`{"amount":null,"payee":null}`)},
		{"json_deep", append(bytes.Repeat([]byte("["), 2000), bytes.Repeat([]byte("]"), 2000)...)},
		{"json_bigexp", []byte(`{"amount":1e999999999}`)},
	}
}

// newLivenessNode is newTestNode without the per-test environment change, so
// tests that run subtests in parallel can create nodes concurrently. The caller
// (the parent test) must have set NHB_ENV=dev with t.Setenv first.
func newLivenessNode(t testing.TB) *Node {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	return node
}

func livenessKey(t testing.TB) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// livenessFund gives addr the supplied NHB and ZNHB balances through the node's
// direct-state path (the same route the probes and most existing tests use).
func livenessFund(t testing.TB, node *Node, addr []byte, nhb, znhb int64) {
	t.Helper()
	err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(addr, &types.Account{
			BalanceNHB:  big.NewInt(nhb),
			BalanceZNHB: big.NewInt(znhb),
			Stake:       big.NewInt(0),
		})
	})
	if err != nil {
		t.Fatalf("fund account: %v", err)
	}
}

// livenessFundBig is livenessFund for balances that do not fit an int64.
func livenessFundBig(t testing.TB, node *Node, addr []byte, nhb, znhb *big.Int) {
	t.Helper()
	err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(addr, &types.Account{
			BalanceNHB:  new(big.Int).Set(nhb),
			BalanceZNHB: new(big.Int).Set(znhb),
			Stake:       big.NewInt(0),
		})
	})
	if err != nil {
		t.Fatalf("fund account: %v", err)
	}
}

// livenessSign builds and signs a transaction. Senderless types are signed too
// (Sign works for every type); their signature is simply never recovered.
func livenessSign(t testing.TB, key *crypto.PrivateKey, typ types.TxType, nonce uint64, data, to []byte, value int64) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     typ,
		Nonce:    nonce,
		Data:     data,
		To:       to,
		Value:    big.NewInt(value),
		GasLimit: 100_000,
		GasPrice: big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

// livenessTransfer builds a valid NHB transfer of 1 wei to a fresh recipient.
func livenessTransfer(t testing.TB, key *crypto.PrivateKey, nonce uint64) *types.Transaction {
	t.Helper()
	to := make([]byte, 20)
	copy(to, livenessKey(t).PubKey().Address().Bytes())
	return livenessSign(t, key, types.TxTypeTransfer, nonce, nil, to, 1)
}

// livenessBlockContains reports whether block holds exactly tx (same hash and
// sender).
func livenessBlockContains(t testing.TB, block *types.Block, tx *types.Transaction) bool {
	t.Helper()
	want, err := transactionKey(tx)
	if err != nil {
		t.Fatalf("key of wanted transaction: %v", err)
	}
	for _, candidate := range block.Transactions {
		if key, keyErr := transactionKey(candidate); keyErr == nil && key == want {
			return true
		}
	}
	return false
}

// livenessResident reports whether tx is still in the node's real mempool.
func livenessResident(t testing.TB, node *Node, tx *types.Transaction) bool {
	t.Helper()
	want, err := transactionKey(tx)
	if err != nil {
		t.Fatalf("key of transaction: %v", err)
	}
	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()
	for _, candidate := range node.mempool {
		if key, keyErr := transactionKey(candidate); keyErr == nil && key == want {
			return true
		}
	}
	return false
}

// livenessInFlight is the number of transactions currently leased to a proposal.
func livenessInFlight(node *Node) int {
	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()
	return len(node.proposedTxs)
}

// livenessInject places txs straight into the mempool, bypassing admission (and
// its simulation), the way the existing regression tests do.
func livenessInject(node *Node, txs ...*types.Transaction) {
	node.mempoolMu.Lock()
	node.mempool = append(node.mempool, txs...)
	node.mempoolMu.Unlock()
}

// livenessMine drives one GetMempool -> CreateBlock -> CommitBlock cycle on a
// single node and returns the committed block.
func livenessMine(t testing.TB, node *Node) *types.Block {
	t.Helper()
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("CommitBlock: %v", err)
	}
	return block
}
