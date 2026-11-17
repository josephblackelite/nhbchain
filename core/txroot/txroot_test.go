package txroot

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/core/types"
)

func goldenTx(n uint64) *types.Transaction {
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    n,
		To:       []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, byte(n)},
		Value:    big.NewInt(int64(1000 + n)),
		Data:     []byte("golden"),
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
		R:        big.NewInt(7),
		S:        big.NewInt(9),
		V:        big.NewInt(27),
	}
}

// The roots below were produced by core.ComputeTxRoot before it moved into this
// package. A block header commits to the root of its transactions, so a change to
// what Compute returns for any list would make every node disagree with the blocks
// already on the chain: these must never change.
func TestComputeReturnsTheRootsTheChainWasBuiltWith(t *testing.T) {
	many := make([]*types.Transaction, 0, 200)
	for i := 0; i < 200; i++ {
		many = append(many, goldenTx(uint64(i)))
	}
	cases := []struct {
		name string
		txs  []*types.Transaction
		want string
	}{
		{"no transactions", nil, "56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"},
		{"one", []*types.Transaction{goldenTx(1)}, "c6f204d1e62cb5037216817a681ef7e88b6632bde5ebefee3a85e23f86aa1ea2"},
		{"three", []*types.Transaction{goldenTx(1), goldenTx(2), goldenTx(3)}, "570898897a48441974aff1ec390b1d505443c17ee65d34bb18eb48e345ee0184"},
		{"two blank ones", []*types.Transaction{{}, {}}, "e9c967a8584c6b9dd00b2a437dc5b19e09a7afda04064fa15d6165d61d6ab71a"},
		{"a nonce only", []*types.Transaction{{Nonce: 666}}, "febd8bec8801497c636ca4b1a7292cdb24de4fe1a6fafa3ff08fe53b51230413"},
		// More than 127 transactions: the trie keys pass from one byte to two.
		{"two hundred", many, "484b693437c9106376c319a6d86986a005205fbaff78b45dd17c341c11fd1109"},
	}
	for _, tc := range cases {
		root, err := Compute(tc.txs)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := hex.EncodeToString(root); got != tc.want {
			t.Fatalf("%s: root %s, want %s", tc.name, got, tc.want)
		}
	}
}

// A block with no transactions commits to the empty trie, which is what a header
// built for one carries.
func TestComputeOfNoTransactionsIsTheEmptyRoot(t *testing.T) {
	for _, txs := range [][]*types.Transaction{nil, {}} {
		root, err := Compute(txs)
		if err != nil {
			t.Fatalf("compute: %v", err)
		}
		if got := common.BytesToHash(root); got != gethtypes.EmptyRootHash {
			t.Fatalf("the root of no transactions is %x, want %x", got, gethtypes.EmptyRootHash)
		}
	}
}

// The root tells transactions apart: a different transaction, another order, one
// more or one less all change it, so a body that is not the one a header was signed
// for cannot carry that header's root.
func TestComputeTellsDifferentBodiesApart(t *testing.T) {
	a, b, c := goldenTx(1), goldenTx(2), goldenTx(3)
	base, err := Compute([]*types.Transaction{a, b})
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	others := map[string][]*types.Transaction{
		"a different transaction": {a, c},
		"another order":           {b, a},
		"one more":                {a, b, c},
		"one less":                {a},
		"none":                    nil,
	}
	for name, txs := range others {
		root, err := Compute(txs)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(root) == string(base) {
			t.Fatalf("%s has the same root as the body it should differ from", name)
		}
	}
	again, err := Compute([]*types.Transaction{a, b})
	if err != nil || string(again) != string(base) {
		t.Fatalf("the same transactions must give the same root (err %v)", err)
	}
}
