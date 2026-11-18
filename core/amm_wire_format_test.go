package core

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

// PL-BC-B2: the bytes a wallet must put in the data field of a TxTypeAmmSwap transaction, RLP([dir, amountIn, minOut]).
// The wallet's own encoder is tested against the same three strings (src/lib/components/market2/poolMath.test.ts), so
// a change on either side that the other does not follow fails a test on both.
func TestAmmSwapPayloadWireFormatIsPinned(t *testing.T) {
	unit, _ := new(big.Int).SetString("1000000000000000000", 10)
	cases := []struct {
		payload ammSwapPayload
		want    string
	}{
		{ammSwapPayload{Dir: AmmDirNhbToZnhb, AmountIn: unit, MinOut: big.NewInt(0)}, "cb01880de0b6b3a764000080"},
		{ammSwapPayload{Dir: AmmDirZnhbToNhb, AmountIn: big.NewInt(12345), MinOut: big.NewInt(678)}, "c7808230398202a6"},
		{ammSwapPayload{Dir: AmmDirZnhbToNhb, AmountIn: new(big.Int).Mul(unit, big.NewInt(1000)), MinOut: new(big.Int).Mul(unit, big.NewInt(49))},
			"d580893635c9adc5dea000008902a802f8630a240000"},
	}
	for _, c := range cases {
		got, err := rlp.EncodeToBytes(c.payload)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(got) != c.want {
			t.Fatalf("dir %d in %s min %s encodes to %x, want %s", c.payload.Dir, c.payload.AmountIn, c.payload.MinOut, got, c.want)
		}
		var back ammSwapPayload
		if err := rlp.DecodeBytes(got, &back); err != nil || back.Dir != c.payload.Dir || back.AmountIn.Cmp(c.payload.AmountIn) != 0 || back.MinOut.Cmp(c.payload.MinOut) != 0 {
			t.Fatalf("the payload does not decode back to itself: %+v (%v)", back, err)
		}
	}
	if AmmDirZnhbToNhb != 0 || AmmDirNhbToZnhb != 1 {
		t.Fatalf("the wallet sends 0 for ZNHB to NHB and 1 for NHB to ZNHB")
	}
}
