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

// TestFormatTxTypeAndAssetLabelFixedTermLendingTypes is the regression test
// for the same gap TestFormatTxTypeMarketTypes guards against, this time for
// TxTypeLendingSupplyFixedTerm (Milestone 3): it was originally missing from
// both formatTxType and assetLabel, so a fixed-term deposit transaction would
// render as raw hex in the explorer and -- since applyLendingSupplyFixedTerm
// emits no custom event -- get NO fallback transfer log at all in its RPC
// receipt (buildFallbackTransferLog drops the log entirely when assetLabel
// returns ""), unlike its Borrow/Repay fixed-term siblings. Covers all three
// fixed-term lending tx types together so a future addition to this family
// is caught the same way.
func TestFormatTxTypeAndAssetLabelFixedTermLendingTypes(t *testing.T) {
	cases := []struct {
		txType    types.TxType
		wantLabel string
	}{
		{types.TxTypeLendingBorrowFixedTerm, "LendingBorrowFixedTerm"},
		{types.TxTypeLendingRepayFixedTerm, "LendingRepayFixedTerm"},
		{types.TxTypeLendingSupplyFixedTerm, "LendingSupplyFixedTerm"},
	}
	for _, tc := range cases {
		if got := formatTxType(tc.txType); got != tc.wantLabel {
			t.Errorf("formatTxType(0x%02x) = %q, want %q", byte(tc.txType), got, tc.wantLabel)
		}
		if got := assetLabel(tc.txType); got != "NHB" {
			t.Errorf("assetLabel(0x%02x) = %q, want %q", byte(tc.txType), got, "NHB")
		}
	}
}
