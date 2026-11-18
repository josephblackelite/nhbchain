package state

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// PL-R5T-51: the stored form of a lending market and the key of the sweep mark are
// part of the running line's state and were changed with no activation height. They
// are pinned here byte for byte, so that the next change to either cannot slip in
// without a gate. If a test here fails, do not edit the expected bytes: the change is a
// consensus change and needs an activation height (docs/consensus/invariants.md,
// "Lending rules already live without an activation height").

func goldenMarket(stamp uint64) *lending.Market {
	owner := crypto.MustNewAddress(crypto.NHBPrefix, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20})
	return &lending.Market{
		PoolID:              "default",
		DeveloperOwner:      owner,
		DeveloperFeeBps:     0,
		TotalNHBSupplied:    big.NewInt(1_000),
		TotalSupplyShares:   big.NewInt(900),
		TotalNHBBorrowed:    big.NewInt(300),
		SupplyIndex:         big.NewInt(1_000_000),
		BorrowIndex:         big.NewInt(1_100_000),
		LastUpdateBlock:     77,
		ReserveFactor:       1_000,
		LastUpdateTimestamp: stamp,
	}
}

func storedHex(t *testing.T, market *lending.Market) string {
	t.Helper()
	encoded, err := rlp.EncodeToBytes(newStoredLendingMarket(market))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return hex.EncodeToString(encoded)
}

func TestLiveMarketRecordAddsTheBlockTimeStampAsATrailingOptionalField(t *testing.T) {
	unstamped := storedHex(t, goldenMarket(0))
	stamped := storedHex(t, goldenMarket(1_700_000_000))
	if unstamped != goldenMarketUnstampedHex {
		t.Errorf("an unstamped market is stored as\n%s\nthe running line stores\n%s", unstamped, goldenMarketUnstampedHex)
	}
	if stamped != goldenMarketStampedHex {
		t.Errorf("a stamped market is stored as\n%s\nthe running line stores\n%s", stamped, goldenMarketStampedHex)
	}
}

func TestLiveSweepMarkIsOneKeyPerPoolHoldingTrue(t *testing.T) {
	if got := string(lendingLegacyReconciledKey("default")); got != "lending/legacy-reconciled/default" {
		t.Errorf("the sweep mark key for the default pool is %q, the running line uses %q", got, "lending/legacy-reconciled/default")
	}
	if got := string(lendingLegacyReconciledKey("  acme  ")); got != "lending/legacy-reconciled/acme" {
		t.Errorf("the key trims the pool id: got %q", got)
	}
	encoded, err := rlp.EncodeToBytes(true)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(encoded) != "01" {
		t.Errorf("the mark's value is stored as %x, the running line stores 01", encoded)
	}
}

const (
	goldenMarketUnstampedHex = "f8488764656661756c74940102030405060708090a0b0c0d0e0f1011121314940000000000000000000000000000000000000000808203e882038482012c830f42408310c8e04d8203e8"
	goldenMarketStampedHex   = "f8568764656661756c74940102030405060708090a0b0c0d0e0f1011121314940000000000000000000000000000000000000000808203e882038482012c830f42408310c8e04d8203e8808080808080808080846553f100"
)
