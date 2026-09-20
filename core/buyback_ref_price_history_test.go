package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// buybackRefPriceHistoryRoots are the state roots of a processor with the
// buyback engine configured while buyback reference prices (TxTypeBuybackRefPrice,
// 0x25, the only buyback transaction the live chain has included) are applied
// and epochs are finalized with no ask pending, block after block. They were
// recorded from release/hardening-r4, before an epoch's asks were bounded, so
// this test fails if anything the chain has executed writes a different state:
// a node that replays history must reach these same roots.
var buybackRefPriceHistoryRoots = []string{
	"0x5b7f08458558602859fc510bc368a00637af02ccc55e3773911f0dc069666b64",
	"0x161b9f0ee6b35ae22d13bc03a9ce9e5b86cd04fbf4540b8b432192fc62ba7c92",
	"0xd93a0a71c230c264b3897ba3e601cd2caf636789899db639cd71ba8442f328ea",
	"0x8215b14a26fba7020eb088dabcdd472fe850c15ce73e00d62501989f6fba4d7f",
	"0xf112d993bd046b2549f2714d7235f80bf90ff38994be8338169568c88d54ed32",
	"0xd57e8cd954be87cf2c5dbbd3bfe075548fd46ff6f4adb9e99342ed9c8a99ff08",
	"0x7b5aed3bd9b52e8b63e17f8205e48d71e7137c2d4cb0cf4a797082f40a7c772c",
	"0x70818615bd1cec1d2c794a58d6dd3ade8fe8be540ff88b10b62dc2ae55bc96be",
	"0x9081c645094be2ed788328cb51cd3f31c4b38fb445c80e8d31e6737ea06a179a",
	"0xdb35e097f47b7d6d50214e93599365f2f92577abad60630f5714c680c2f54929",
}

func TestBuybackRefPriceExecutionRootsAreUnchanged(t *testing.T) {
	sp := newRewardTestState(t)
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 0x55
	}
	key, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		t.Fatalf("private key from bytes: %v", err)
	}
	var signer [20]byte
	copy(signer[:], key.PubKey().Address().Bytes())
	if err := sp.SetBuybackConfig(buyback.Config{FeeShareBps: 2000, SignerThreshold: 1, Signers: [][20]byte{signer}}); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	sp.SetBuybackAccrualAddress(deriveModuleAddress("module/tokenomics/buybackAccrual", crypto.NHBPrefix))

	refPrice := func(epoch uint64, rateNum, rateDenom int64, at time.Time) {
		rp := &buyback.ReferencePrice{Rate: new(big.Rat).SetFrac(big.NewInt(rateNum), big.NewInt(rateDenom)), Epoch: epoch, Timestamp: at}
		digest, err := rp.Hash()
		if err != nil {
			t.Fatalf("hash reference price: %v", err)
		}
		sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
		if err != nil {
			t.Fatalf("sign reference price: %v", err)
		}
		data, err := rlp.EncodeToBytes(buybackRefPricePayload{RateNum: big.NewInt(rateNum), RateDenom: big.NewInt(rateDenom), Epoch: epoch, Timestamp: uint64(at.Unix()), Signatures: [][]byte{sig}})
		if err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		if err := sp.applyBuybackRefPrice(&types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: data}); err != nil {
			t.Fatalf("apply buyback reference price: %v", err)
		}
	}

	start := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	var roots []string
	for step := 0; step < 10; step++ {
		now := start.Add(time.Duration(step) * 2 * time.Second)
		height := uint64(step + 1)
		sp.BeginBlock(height, now)
		if epoch, ok := sp.currentBuybackEpoch(); ok && step%2 == 0 {
			refPrice(epoch, int64(5+step), 100, now)
		}
		if err := sp.ProcessBlockLifecycle(height, now.Unix()); err != nil {
			sp.EndBlock()
			t.Fatalf("lifecycle at step %d: %v", step, err)
		}
		roots = append(roots, sp.PendingRoot().Hex())
		sp.EndBlock()
	}
	if len(buybackRefPriceHistoryRoots) != len(roots) {
		for i, root := range roots {
			t.Logf("RECORDED step %d %s", i, root)
		}
		t.Fatalf("no recorded roots to compare with (%d recorded, %d produced)", len(buybackRefPriceHistoryRoots), len(roots))
	}
	for i, root := range roots {
		if root != buybackRefPriceHistoryRoots[i] {
			t.Fatalf("root at step %d = %s, recorded %s", i, root, buybackRefPriceHistoryRoots[i])
		}
	}
}
