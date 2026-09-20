package state

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/native/lending"
)

// legacyStoredLendingMarket is the layout of a persisted lending market before
// it could carry a block time: the same fields, in the same order, without
// LastUpdateTimestamp. Markets the chain has written, and the ones the lending
// reference price transaction rewrites, are encoded this way.
type legacyStoredLendingMarket struct {
	PoolID                                  string
	DeveloperOwner                          [20]byte
	DeveloperCollector                      [20]byte
	DeveloperFeeBps                         uint64
	TotalNHBSupplied                        *big.Int
	TotalSupplyShares                       *big.Int
	TotalNHBBorrowed                        *big.Int
	SupplyIndex                             *big.Int
	BorrowIndex                             *big.Int
	LastUpdateBlock                         uint64
	ReserveFactor                           uint64
	BorrowedThisBlock                       *big.Int `rlp:"optional"`
	LastBorrowBlock                         uint64   `rlp:"optional"`
	OracleMedianWei                         *big.Int `rlp:"optional"`
	OraclePrevMedianWei                     *big.Int `rlp:"optional"`
	OracleUpdatedBlock                      uint64   `rlp:"optional"`
	TotalFixedTermDepositPrincipalWei       *big.Int `rlp:"optional"`
	TotalFixedTermDepositInterestOwedWei    *big.Int `rlp:"optional"`
	FixedTermDepositReserveWei              *big.Int `rlp:"optional"`
	TotalFixedTermLoanInterestReceivableWei *big.Int `rlp:"optional"`
}

func legacyLayout(s *storedLendingMarket) legacyStoredLendingMarket {
	return legacyStoredLendingMarket{
		PoolID:                                  s.PoolID,
		DeveloperOwner:                          s.DeveloperOwner,
		DeveloperCollector:                      s.DeveloperCollector,
		DeveloperFeeBps:                         s.DeveloperFeeBps,
		TotalNHBSupplied:                        s.TotalNHBSupplied,
		TotalSupplyShares:                       s.TotalSupplyShares,
		TotalNHBBorrowed:                        s.TotalNHBBorrowed,
		SupplyIndex:                             s.SupplyIndex,
		BorrowIndex:                             s.BorrowIndex,
		LastUpdateBlock:                         s.LastUpdateBlock,
		ReserveFactor:                           s.ReserveFactor,
		BorrowedThisBlock:                       s.BorrowedThisBlock,
		LastBorrowBlock:                         s.LastBorrowBlock,
		OracleMedianWei:                         s.OracleMedianWei,
		OraclePrevMedianWei:                     s.OraclePrevMedianWei,
		OracleUpdatedBlock:                      s.OracleUpdatedBlock,
		TotalFixedTermDepositPrincipalWei:       s.TotalFixedTermDepositPrincipalWei,
		TotalFixedTermDepositInterestOwedWei:    s.TotalFixedTermDepositInterestOwedWei,
		FixedTermDepositReserveWei:              s.FixedTermDepositReserveWei,
		TotalFixedTermLoanInterestReceivableWei: s.TotalFixedTermLoanInterestReceivableWei,
	}
}

// A market without a block time encodes byte for byte as it did before the
// field existed, whatever its other optional fields hold, so the state roots of
// blocks that write such markets do not change.
func TestLendingMarketWithoutABlockTimeEncodesAsBefore(t *testing.T) {
	zero := big.NewInt(0)
	for name, market := range map[string]*lending.Market{
		"as the reference price leaves it": {
			PoolID:              "default",
			DeveloperFeeBps:     50,
			TotalNHBSupplied:    big.NewInt(5_000_000),
			TotalSupplyShares:   big.NewInt(5_000_000),
			TotalNHBBorrowed:    big.NewInt(1_000_000),
			SupplyIndex:         big.NewInt(1_000_000),
			BorrowIndex:         big.NewInt(1_000_000),
			LastUpdateBlock:     7,
			ReserveFactor:       1000,
			BorrowedThisBlock:   zero,
			OracleMedianWei:     big.NewInt(50_000_000_000_000_000),
			OraclePrevMedianWei: zero,
			OracleUpdatedBlock:  12,
		},
		"with every trailing field set": {
			PoolID:                                  "second",
			TotalNHBSupplied:                        big.NewInt(1),
			TotalSupplyShares:                       big.NewInt(1),
			TotalNHBBorrowed:                        big.NewInt(1),
			SupplyIndex:                             big.NewInt(1),
			BorrowIndex:                             big.NewInt(1),
			BorrowedThisBlock:                       big.NewInt(2),
			LastBorrowBlock:                         3,
			OracleMedianWei:                         big.NewInt(4),
			OraclePrevMedianWei:                     big.NewInt(5),
			OracleUpdatedBlock:                      6,
			TotalFixedTermDepositPrincipalWei:       big.NewInt(7),
			TotalFixedTermDepositInterestOwedWei:    big.NewInt(8),
			FixedTermDepositReserveWei:              big.NewInt(9),
			TotalFixedTermLoanInterestReceivableWei: zero,
		},
		"bare": {PoolID: "bare"},
	} {
		stored := newStoredLendingMarket(market)
		got, err := rlp.EncodeToBytes(stored)
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		want, err := rlp.EncodeToBytes(legacyLayout(stored))
		if err != nil {
			t.Fatalf("%s: encode legacy layout: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: market encodes as %x, before the block time field it encoded as %x", name, got, want)
		}
	}
}
