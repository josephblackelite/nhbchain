package rpc

import (
	"testing"

	"nhbchain/core/types"
)

// TestFormatTxTypeMarketTypes is the regression test for a real gap: the
// three P2P market transaction types (native/market, wired into the chain
// via core/market_native.go) were never added to formatTxType's switch, so
// the explorer would have displayed every market-related transaction as raw
// hex ("0x35"/"0x36"/"0x37") instead of a readable label once the feature
// went live. Labels mirror the TxType constant names themselves (see
// core/types/transaction.go) for consistency with every other case in this
// switch (e.g. TxTypeStakeClaimRewards -> "StakeClaimRewards").
func TestFormatTxTypeMarketTypes(t *testing.T) {
	cases := []struct {
		txType types.TxType
		want   string
	}{
		{types.TxTypeMarketCreateListing, "MarketCreateListing"},
		{types.TxTypeMarketFillListing, "MarketFillListing"},
		{types.TxTypeMarketCancelListing, "MarketCancelListing"},
	}
	for _, tc := range cases {
		if got := formatTxType(tc.txType); got != tc.want {
			t.Errorf("formatTxType(0x%02x) = %q, want %q", byte(tc.txType), got, tc.want)
		}
	}
}
