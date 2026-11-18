package core

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
)

// With the bridge and the pool ACTIVE, a transaction of any of the five new types that is refused (garbage bytes, a
// wrong shape, a sender who may not, an amount nobody holds) must never stop a block from being built: the valid
// transfer beside it is still included and the block validates. This is the mixed-block rule every new type ships with
// (the poison matrix covers the same types while they are inactive).
func TestActiveBridgeAndPoolNeverStallBlockBuilding(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	node := newLivenessNode(t)
	var provider [20]byte
	provider[19] = 0x42
	node.SetBridgeAndAmm(
		BridgeSettings{ActivationHeight: 1, Params: []BridgeParams{testBridgeParams(1)}},
		AmmSettings{ActivationHeight: 1, SwapStartHeight: 1, FeeBps: 30, MaxSwapBps: 500, Provider: provider, HasProvider: true, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)},
	)
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)

	garbage := map[string][]byte{
		"no payload":         nil,
		"one byte":           {0x01},
		"not rlp":            {0xff, 0xff, 0xff, 0xff},
		"a list of one":      {0xc1, 0x01},
		"huge declared size": {0xbf, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}
	typesUnderTest := []types.TxType{types.TxTypeBridgeLock, types.TxTypeBridgeRelease, types.TxTypeAmmAddLiquidity, types.TxTypeAmmRemoveLiquidity, types.TxTypeAmmSwap}
	for _, typ := range typesUnderTest {
		for name, data := range garbage {
			livenessResetStrikes(node)
			poisonKey := livenessKey(t)
			livenessFund(t, node, poisonKey.PubKey().Address().Bytes(), 1_000_000_000_000, 1_000_000_000_000)
			to := make([]byte, 20)
			to[19] = 0x02
			poison := livenessSign(t, poisonKey, typ, 0, data, to, 1_000)
			transfer := livenessTransfer(t, honest, 0)
			block, err := node.CreateBlock([]*types.Transaction{poison, transfer})
			if err != nil {
				t.Fatalf("type 0x%02X with %s: CreateBlock aborted: %v", byte(typ), name, err)
			}
			if block == nil || !livenessBlockContains(t, block, transfer) {
				t.Fatalf("type 0x%02X with %s: the valid transfer is missing from the block", byte(typ), name)
			}
			if err := node.ValidateBlock(block); err != nil {
				t.Fatalf("type 0x%02X with %s: the block does not validate: %v", byte(typ), name, err)
			}
		}
	}
}
