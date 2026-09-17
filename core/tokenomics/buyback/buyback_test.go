package buyback

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func wei(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

func testConfig(t *testing.T, threshold uint32, numSigners int) (Config, []*crypto.PrivateKey) {
	t.Helper()
	keys := make([]*crypto.PrivateKey, numSigners)
	signers := make([][20]byte, numSigners)
	for i := 0; i < numSigners; i++ {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate signer key %d: %v", i, err)
		}
		keys[i] = key
		copy(signers[i][:], key.PubKey().Address().Bytes())
	}
	return Config{
		FeeShareBps:     2000,
		DiscountBps:     500,
		SafetyMarginBps: 500,
		SignerThreshold: threshold,
		Signers:         signers,
	}, keys
}

func signRefPrice(t *testing.T, rp *ReferencePrice, key *crypto.PrivateKey) []byte {
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

func TestConfigValidate(t *testing.T) {
	cfg, _ := testConfig(t, 2, 3)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	bad := cfg.Clone()
	bad.SignerThreshold = 4
	if err := bad.Validate(); err == nil {
		t.Fatalf("expected error for threshold exceeding signer count")
	}

	bad2 := cfg.Clone()
	bad2.Signers = nil
	if err := bad2.Validate(); err == nil {
		t.Fatalf("expected error for empty signer set")
	}

	bad3 := cfg.Clone()
	bad3.DiscountBps = 10_001
	if err := bad3.Validate(); err == nil {
		t.Fatalf("expected error for discount exceeding 100%%")
	}
}

func TestVerifyReferencePrice_MeetsThreshold(t *testing.T) {
	cfg, keys := testConfig(t, 2, 3)
	rp := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 42, Timestamp: time.Unix(1_800_000_000, 0)}
	sigs := [][]byte{
		signRefPrice(t, rp, keys[0]),
		signRefPrice(t, rp, keys[1]),
	}
	signers, err := VerifyReferencePrice(cfg, rp, sigs)
	if err != nil {
		t.Fatalf("expected verification to succeed: %v", err)
	}
	if len(signers) != 2 {
		t.Fatalf("expected 2 unique signers, got %d", len(signers))
	}
}

func TestVerifyReferencePrice_InsufficientQuorum(t *testing.T) {
	cfg, keys := testConfig(t, 2, 3)
	rp := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 42, Timestamp: time.Unix(1_800_000_000, 0)}
	sigs := [][]byte{signRefPrice(t, rp, keys[0])}
	if _, err := VerifyReferencePrice(cfg, rp, sigs); err == nil {
		t.Fatalf("expected error for insufficient quorum")
	}
}

func TestVerifyReferencePrice_RejectsUnauthorizedSigner(t *testing.T) {
	cfg, keys := testConfig(t, 1, 2)
	rogue, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}
	rp := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 42, Timestamp: time.Unix(1_800_000_000, 0)}
	sigs := [][]byte{signRefPrice(t, rp, rogue)}
	if _, err := VerifyReferencePrice(cfg, rp, sigs); err == nil {
		t.Fatalf("expected error for an unauthorized signer")
	}
	_ = keys
}

func TestVerifyReferencePrice_DuplicateSignerDoesNotCountTwice(t *testing.T) {
	cfg, keys := testConfig(t, 2, 3)
	rp := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 42, Timestamp: time.Unix(1_800_000_000, 0)}
	sig := signRefPrice(t, rp, keys[0])
	sigs := [][]byte{sig, sig}
	if _, err := VerifyReferencePrice(cfg, rp, sigs); err == nil {
		t.Fatalf("expected error: two copies of the same signature should not satisfy a threshold of 2")
	}
}

func TestVerifyReferencePrice_DifferentEpochChangesDigest(t *testing.T) {
	cfg, keys := testConfig(t, 1, 1)
	rp1 := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 42, Timestamp: time.Unix(1_800_000_000, 0)}
	sig := signRefPrice(t, rp1, keys[0])
	rp2 := &ReferencePrice{Rate: big.NewRat(5, 100), Epoch: 43, Timestamp: time.Unix(1_800_000_000, 0)}
	if _, err := VerifyReferencePrice(cfg, rp2, [][]byte{sig}); err == nil {
		t.Fatalf("expected a signature over epoch 42 to be rejected for epoch 43 (replay across epochs)")
	}
}

func TestMaxBuybackPrice_TakesTheLesser(t *testing.T) {
	curvePrice := big.NewRat(1, 10)                             // $0.10
	refPrice := big.NewRat(1, 20)                               // $0.05 -- the more conservative (lower) figure
	got, err := MaxBuybackPrice(curvePrice, refPrice, 500, 500) // 5% off each
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// refPrice * 0.95 = 0.0475, curvePrice * 0.95 = 0.095 -- the ref-derived
	// figure is lower and must win.
	want := new(big.Rat).Mul(refPrice, big.NewRat(95, 100))
	if got.Cmp(want) != 0 {
		t.Fatalf("MaxBuybackPrice = %v, want %v (the lesser of the two discounted prices)", got, want)
	}
}

func TestMaxBuybackPrice_RejectsOutOfRangeBps(t *testing.T) {
	if _, err := MaxBuybackPrice(big.NewRat(1, 10), big.NewRat(1, 10), 10_001, 0); err == nil {
		t.Fatalf("expected error for discount bps exceeding 10000")
	}
}

func TestFillAsksProRata_FullyFundedFillsEveryoneInFull(t *testing.T) {
	var sellerA, sellerB [20]byte
	sellerA[0] = 0xA
	sellerB[0] = 0xB
	asks := []Ask{
		{Seller: sellerA, AmountWei: wei(1000)},
		{Seller: sellerB, AmountWei: wei(2000)},
	}
	// Budget covers 10,000 ZNHB at $0.05 -- well above the 3,000 ZNHB asked.
	budget := wei(500) // 500 NHB
	price := big.NewRat(5, 100)
	fills, err := FillAsksProRata(asks, budget, price)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fills) != 2 {
		t.Fatalf("expected 2 fills, got %d", len(fills))
	}
	for _, f := range fills {
		if f.ZNHBRefunded.Sign() != 0 {
			t.Fatalf("expected zero refund when fully funded, got %s for seller %x", f.ZNHBRefunded, f.Seller)
		}
	}
	if fills[0].ZNHBFilled.Cmp(wei(1000)) != 0 || fills[1].ZNHBFilled.Cmp(wei(2000)) != 0 {
		t.Fatalf("expected full fills, got %s and %s", fills[0].ZNHBFilled, fills[1].ZNHBFilled)
	}
}

func TestFillAsksProRata_OversubscribedScalesDownProportionally(t *testing.T) {
	var sellerA, sellerB [20]byte
	sellerA[0] = 0xA
	sellerB[0] = 0xB
	// A asks 3x what B asks.
	asks := []Ask{
		{Seller: sellerA, AmountWei: wei(3000)},
		{Seller: sellerB, AmountWei: wei(1000)},
	}
	price := big.NewRat(5, 100) // $0.05/ZNHB
	// Budget only covers 2,000 ZNHB (half of the 4,000 ZNHB asked).
	budget := wei(100) // 100 NHB / 0.05 = 2000 ZNHB buyable
	fills, err := FillAsksProRata(asks, budget, price)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Each seller should be filled at exactly half their ask (2000/4000).
	wantA := wei(1500)
	wantB := wei(500)
	if fills[0].ZNHBFilled.Cmp(wantA) != 0 {
		t.Fatalf("seller A filled = %s, want %s", fills[0].ZNHBFilled, wantA)
	}
	if fills[1].ZNHBFilled.Cmp(wantB) != 0 {
		t.Fatalf("seller B filled = %s, want %s", fills[1].ZNHBFilled, wantB)
	}
	if fills[0].ZNHBRefunded.Sign() <= 0 || fills[1].ZNHBRefunded.Sign() <= 0 {
		t.Fatalf("expected both sellers to have a positive refund when oversubscribed")
	}
	total := new(big.Int).Add(fills[0].NHBPaid, fills[1].NHBPaid)
	if total.Cmp(budget) > 0 {
		t.Fatalf("total NHB paid %s exceeds budget %s", total, budget)
	}
}

func TestFillAsksProRata_ZeroBudgetRefundsEveryone(t *testing.T) {
	var seller [20]byte
	seller[0] = 0xA
	asks := []Ask{{Seller: seller, AmountWei: wei(500)}}
	fills, err := FillAsksProRata(asks, big.NewInt(0), big.NewRat(5, 100))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fills[0].ZNHBFilled.Sign() != 0 {
		t.Fatalf("expected zero fill with zero budget, got %s", fills[0].ZNHBFilled)
	}
	if fills[0].ZNHBRefunded.Cmp(wei(500)) != 0 {
		t.Fatalf("expected the full ask refunded, got %s", fills[0].ZNHBRefunded)
	}
}

func TestFillAsksProRata_NeverExceedsBudgetAcrossManySmallAsks(t *testing.T) {
	// A stress case for the rounding-safety check: many small, unevenly
	// sized asks that don't divide budget cleanly.
	var asks []Ask
	for i := 0; i < 37; i++ {
		var seller [20]byte
		seller[0] = byte(i + 1)
		asks = append(asks, Ask{Seller: seller, AmountWei: big.NewInt(int64(997 + i))})
	}
	budget := big.NewInt(123_456_789_012_345)
	price := new(big.Rat).SetFrac(big.NewInt(7), big.NewInt(3)) // an awkward, non-terminating rate
	fills, err := FillAsksProRata(asks, budget, price)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	total := big.NewInt(0)
	for _, f := range fills {
		total.Add(total, f.NHBPaid)
	}
	if total.Cmp(budget) > 0 {
		t.Fatalf("total NHB paid %s exceeds budget %s across %d asks", total, budget, len(asks))
	}
}
