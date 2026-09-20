package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// lendingRefPriceHistoryRoots are the state roots of a processor that holds two
// lending markets while lending reference prices (TxTypeLendingRefPrice, 0x26,
// the only lending transaction the live chain has included) are applied and
// blocks go by, block after block. They were recorded from
// release/hardening-r4, before the market record could carry a block time and
// before interest accrued by block time, so this test fails if anything the
// chain has ever executed writes a different record: a node that replays
// history must reach these same roots.
var lendingRefPriceHistoryRoots = []string{
	"0x5fec541da36ca5c5dffa820a8bfe23c6fd421c1cab1d74685dffcf61675db718",
	"0x4239fa5e0833ace87c166c6c5f31c914d6be7091a497c166a8c277dc2dc4b07b",
	"0x9fd71459436f6346b23e774c5f3634d776e5b5dd2b25bd3e32f69f37e181482e",
	"0xbd6425165d577570d80a915788aef44ae8f34c175d882940e542aba16ab2fef2",
	"0xbd6425165d577570d80a915788aef44ae8f34c175d882940e542aba16ab2fef2",
	"0x314d7ad3f670b7e96951966e488f3c7155bcb92324d961778547a84a497aa42f",
	"0xb4a26d26efa46f5b478958c0afa5c1f7db5edde4f8c4224cc20c61714464a694",
	"0x6540770718ebaa07a3bc318ea63e7881f1a43b3dfaa7ce4f67ca2faefa9c2781",
	"0x6540770718ebaa07a3bc318ea63e7881f1a43b3dfaa7ce4f67ca2faefa9c2781",
}

func lendingHistoryKey(t *testing.T, seed byte) *crypto.PrivateKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	key, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		t.Fatalf("private key from bytes: %v", err)
	}
	return key
}

func TestLendingRefPriceExecutionRootsAreUnchanged(t *testing.T) {
	sp := newRewardTestState(t)
	key := lendingHistoryKey(t, 0x77)
	var signer [20]byte
	copy(signer[:], key.PubKey().Address().Bytes())
	if err := sp.SetBuybackConfig(buyback.Config{FeeShareBps: 2000, SignerThreshold: 1, Signers: [][20]byte{signer}}); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	seed := func(poolID string, supplied, borrowed, median int64) {
		market := &lending.Market{
			PoolID:              poolID,
			DeveloperFeeBps:     50,
			TotalNHBSupplied:    big.NewInt(supplied),
			TotalSupplyShares:   big.NewInt(supplied),
			TotalNHBBorrowed:    big.NewInt(borrowed),
			SupplyIndex:         new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1_000_000_000_000_000_000)),
			BorrowIndex:         new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1_000_000_000_000_000_000)),
			LastUpdateBlock:     7,
			ReserveFactor:       1000,
			BorrowedThisBlock:   big.NewInt(0),
			OracleMedianWei:     big.NewInt(median),
			OraclePrevMedianWei: big.NewInt(0),
			OracleUpdatedBlock:  3,
		}
		if err := manager.LendingPutMarket(poolID, market); err != nil {
			t.Fatalf("seed market %q: %v", poolID, err)
		}
	}
	seed("default", 5_000_000, 1_000_000, 0)
	seed("second", 0, 0, 500_000_000_000_000_000)

	start := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	roots := []string{sp.PendingRoot().Hex()}
	refPrice := func(rateNum, rateDenom int64, at time.Time) {
		rp := &lendingoracle.ReferencePrice{Rate: new(big.Rat).SetFrac(big.NewInt(rateNum), big.NewInt(rateDenom)), Timestamp: at}
		digest, err := rp.Hash()
		if err != nil {
			t.Fatalf("hash reference price: %v", err)
		}
		sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
		if err != nil {
			t.Fatalf("sign reference price: %v", err)
		}
		data, err := rlp.EncodeToBytes(lendingRefPricePayload{RateNum: big.NewInt(rateNum), RateDenom: big.NewInt(rateDenom), Timestamp: uint64(at.Unix()), Signatures: [][]byte{sig}})
		if err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		if err := sp.applyLendingRefPriceTransaction(&types.Transaction{Type: types.TxTypeLendingRefPrice, Data: data}); err != nil {
			t.Fatalf("apply lending reference price: %v", err)
		}
	}
	for step := 0; step < 8; step++ {
		now := start.Add(time.Duration(step) * 2 * time.Second)
		height := uint64(step + 10)
		sp.BeginBlock(height, now)
		if step == 1 {
			refPrice(5, 100, now)
		}
		if step == 5 {
			refPrice(6, 100, now)
		}
		if err := sp.ProcessBlockLifecycle(height, now.Unix()); err != nil {
			sp.EndBlock()
			t.Fatalf("lifecycle at step %d: %v", step, err)
		}
		roots = append(roots, sp.PendingRoot().Hex())
		sp.EndBlock()
	}
	if len(lendingRefPriceHistoryRoots) != len(roots) {
		for i, root := range roots {
			t.Logf("RECORDED step %d %s", i, root)
		}
		t.Fatalf("no recorded roots to compare with (%d recorded, %d produced)", len(lendingRefPriceHistoryRoots), len(roots))
	}
	for i, root := range roots {
		if root != lendingRefPriceHistoryRoots[i] {
			t.Fatalf("root at step %d = %s, recorded %s", i, root, lendingRefPriceHistoryRoots[i])
		}
	}
}
