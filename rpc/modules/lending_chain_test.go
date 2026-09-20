package modules

// A chain with lending transactions at known heights, added straight to the
// block store behind a store that counts reads, for the tests of the lending
// reads.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	"nhbchain/storage"
)

// countingStore counts reads that reach the block store.
type countingStore struct {
	storage.Database
	gets atomic.Int64
}

func (c *countingStore) Get(key []byte) ([]byte, error) {
	c.gets.Add(1)
	return c.Database.Get(key)
}

type lendingChain struct {
	node    *core.Node
	store   *countingStore
	key     *crypto.PrivateKey
	users   []*crypto.PrivateKey
	rng     *rand.Rand
	prev    []byte
	height  uint64
	stamp   int64
	lending map[uint64]bool // heights that hold a lending transaction
}

func newLendingChain(t *testing.T, seed int64) *lendingChain {
	t.Helper()
	t.Setenv("NHB_ENV", "dev")
	store := &countingStore{Database: storage.NewMemDB()}
	t.Cleanup(func() { store.Close() })
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := core.NewNode(store, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	lc := &lendingChain{node: node, store: store, key: key, rng: rand.New(rand.NewSource(seed)), lending: map[uint64]bool{}}
	for i := 0; i < 5; i++ {
		k, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate user key: %v", err)
		}
		lc.users = append(lc.users, k)
	}
	lc.prev = node.Chain().Tip()
	lc.stamp = time.Now().Unix() - 1_000_000
	return lc
}

func (lc *lendingChain) tx(t *testing.T, typ types.TxType, nonce uint64, value int64, pool string) *types.Transaction {
	t.Helper()
	user := lc.users[lc.rng.Intn(len(lc.users))]
	var data []byte
	switch pool {
	case "":
	default:
		data, _ = json.Marshal(map[string]string{"poolId": pool})
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     typ,
		Nonce:    nonce,
		Value:    big.NewInt(value),
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(user.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tx
}

// extend adds n blocks; about one in `every` carries lending transactions (of
// every replayed kind, in two pools and with no pool named) and some carry other
// transactions or nothing.
func (lc *lendingChain) extend(t *testing.T, n, every int) {
	t.Helper()
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	prevOut := os.Stdout
	os.Stdout = null
	defer func() { os.Stdout = prevOut; null.Close() }()

	chain := lc.node.Chain()
	head := chain.CurrentHeader()
	kinds := []types.TxType{
		types.TxTypeLendingSupplyNHB, types.TxTypeLendingWithdrawNHB, types.TxTypeLendingDepositZNHB,
		types.TxTypeLendingWithdrawZNHB, types.TxTypeLendingBorrowNHB, types.TxTypeLendingRepayNHB,
	}
	pools := []string{"", "default", "poolB", "poolB", "poolC"}
	nonce := uint64(lc.height * 10)
	for i := 0; i < n; i++ {
		lc.height = chain.GetHeight() + 1
		var txs []*types.Transaction
		hasLending := false
		if every > 0 && lc.rng.Intn(every) == 0 {
			for c := 1 + lc.rng.Intn(3); c > 0; c-- {
				nonce++
				txs = append(txs, lc.tx(t, kinds[lc.rng.Intn(len(kinds))], nonce, int64(1+lc.rng.Intn(1000)), pools[lc.rng.Intn(len(pools))]))
			}
			hasLending = true
		}
		if lc.rng.Intn(4) == 0 {
			nonce++
			txs = append(txs, lc.tx(t, types.TxTypeTransfer, nonce, 5, ""))
		}
		root, err := core.ComputeTxRoot(txs)
		if err != nil {
			t.Fatalf("tx root: %v", err)
		}
		lc.stamp += 2
		header := &types.BlockHeader{
			Height:             lc.height,
			Timestamp:          lc.stamp,
			PrevHash:           chain.Tip(),
			StateRoot:          head.StateRoot,
			TxRoot:             root,
			ExecutionGraphRoot: bytes.Repeat([]byte{0xE1}, 32),
			Validator:          lc.key.PubKey().Address().Bytes(),
		}
		if err := chain.AddBlock(&types.Block{Header: header, Transactions: txs}); err != nil {
			t.Fatalf("add block %d: %v", lc.height, err)
		}
		if hasLending {
			lc.lending[lc.height] = true
		}
	}
}

func (lc *lendingChain) lendingHeights() []uint64 {
	var out []uint64
	for h := range lc.lending {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func describeReplay(market *lending.Market, users map[[20]byte]*lending.UserAccount) string {
	if market == nil {
		return "nil"
	}
	raw, err := json.Marshal(market)
	if err != nil {
		return "marshal error: " + err.Error()
	}
	keys := make([][20]byte, 0, len(users))
	for k := range users {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	out := string(raw)
	for _, k := range keys {
		u := users[k]
		out += fmt.Sprintf("|%x:%v,%v,%v,%v", k, u.CollateralZNHB, u.SupplyShares, u.DebtNHB, u.ScaledDebt)
	}
	return out
}
