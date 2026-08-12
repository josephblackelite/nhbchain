package core

import (
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/crypto"
)

// newBuybackSubmissionTestNode builds a full Node (not just a
// StateProcessor, since SubmitBuybackRefPrice/CurrentBuybackEpoch exercise
// Node-level plumbing -- AddTransaction's simulation path, n.chain.GetHeight())
// with a 2-of-3 buyback signer quorum and a short epoch length so tests
// don't need to drive real block production to get a valid open epoch.
func newBuybackSubmissionTestNode(t *testing.T) (*Node, []*crypto.PrivateKey, [][20]byte) {
	t.Helper()
	node := newTestNode(t)

	node.stateMu.Lock()
	cfg := node.state.EpochConfig()
	cfg.Length = 2
	if err := node.state.SetEpochConfig(cfg); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("set epoch config: %v", err)
	}
	node.stateMu.Unlock()

	keys := make([]*crypto.PrivateKey, 3)
	addrs := make([][20]byte, 3)
	for i := range keys {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate signer key %d: %v", i, err)
		}
		keys[i] = key
		copy(addrs[i][:], key.PubKey().Address().Bytes())
	}
	buybackCfg := buyback.Config{
		FeeShareBps:     2000,
		DiscountBps:     0,
		SafetyMarginBps: 0,
		SignerThreshold: 2,
		Signers:         addrs,
	}
	if err := node.ConfigureBuybackForTests(buybackCfg); err != nil {
		t.Fatalf("configure buyback: %v", err)
	}
	return node, keys, addrs
}

func signRefPrice(t *testing.T, key *crypto.PrivateKey, rp *buyback.ReferencePrice) []byte {
	t.Helper()
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash reference price: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign reference price: %v", err)
	}
	return sig
}

func TestCurrentBuybackEpoch_ComputesFromChainHeightPlusOne(t *testing.T) {
	node, _, _ := newBuybackSubmissionTestNode(t)
	epochNum, ok := node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	height := node.chain.GetHeight() + 1
	want := (height + 2 - 1) / 2
	if epochNum != want {
		t.Fatalf("epoch = %d, want %d (height=%d, length=2)", epochNum, want, height)
	}
}

func TestSubmitBuybackRefPrice_ValidBundleAccepted(t *testing.T) {
	node, keys, _ := newBuybackSubmissionTestNode(t)
	epochNum, ok := node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signRefPrice(t, keys[0], rp),
		signRefPrice(t, keys[1], rp),
	}
	txHash, err := node.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs)
	if err != nil {
		t.Fatalf("submit ref price: %v", err)
	}
	if txHash == "" {
		t.Fatalf("expected a non-empty tx hash")
	}
}

func TestSubmitBuybackRefPrice_InsufficientSignaturesRejected(t *testing.T) {
	node, keys, _ := newBuybackSubmissionTestNode(t)
	epochNum, ok := node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	// Threshold is 2; only one of three signers signs.
	sigs := [][]byte{signRefPrice(t, keys[0], rp)}
	if _, err := node.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs); err == nil {
		t.Fatalf("expected an error for a below-threshold signature bundle")
	}
}

func TestSubmitBuybackRefPrice_WrongEpochRejected(t *testing.T) {
	node, keys, _ := newBuybackSubmissionTestNode(t)
	epochNum, ok := node.CurrentBuybackEpoch()
	if !ok {
		t.Fatalf("expected epoch scheduling to be enabled")
	}
	wrongEpoch := epochNum + 1
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(time.Now().UTC().Unix())
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     wrongEpoch,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{
		signRefPrice(t, keys[0], rp),
		signRefPrice(t, keys[1], rp),
	}
	if _, err := node.SubmitBuybackRefPrice(rateNum, rateDenom, wrongEpoch, ts, sigs); err == nil {
		t.Fatalf("expected an error for a reference price epoch that doesn't match the current open epoch")
	}
}

func TestBuybackRefPriceStatusForEpoch_NoRecordYet(t *testing.T) {
	node, _, _ := newBuybackSubmissionTestNode(t)
	status, err := node.BuybackRefPriceStatusForEpoch(1)
	if err != nil {
		t.Fatalf("load status: %v", err)
	}
	if status.HasRefPrice {
		t.Fatalf("expected no reference price on file yet")
	}
	if status.Epoch != 1 {
		t.Fatalf("status.Epoch = %d, want 1", status.Epoch)
	}
}
