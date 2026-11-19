package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
)

func TestSaleReference_TheTokenomicsReadShowsTheRuleOnlyWhereItIsSet(t *testing.T) {
	node := newZNHBRPCTestNode(t)
	state, err := node.GetZNHBTokenomicsState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.SaleReference != nil {
		t.Fatalf("the rule is not set and the read still carries it: %+v", state.SaleReference)
	}

	node.SetValidatorRules(ValidatorRules{SaleReferenceActivationHeight: 1, SaleReferenceMaxAgeSeconds: 3600})
	state, err = node.GetZNHBTokenomicsState()
	if err != nil || state.SaleReference == nil {
		t.Fatalf("state with the rule set: %+v err=%v", state, err)
	}
	if !state.SaleReference.Active || !state.SaleReference.Paused || state.SaleReference.RateNHBPerZNHB != "" {
		t.Fatalf("no reference yet, so active and paused with no rate: %+v", state.SaleReference)
	}

	now := node.state.blockTimestamp()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 3, RateNum: big.NewInt(5), RateDenom: big.NewInt(2), TimestampAt: uint64(now.Unix()) - 30}); err != nil {
		t.Fatalf("set: %v", err)
	}
	state, err = node.GetZNHBTokenomicsState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	ref := state.SaleReference
	if ref.Paused || ref.RateNHBPerZNHB != "2.500000000000000000" || ref.AgeSeconds < 30 || ref.AgeSeconds > 40 || ref.MaxAgeSeconds != 3600 || ref.ActivationHeight != 1 {
		t.Fatalf("fresh reference: %+v", ref)
	}

	if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 3, RateNum: big.NewInt(5), RateDenom: big.NewInt(2), TimestampAt: uint64(now.Unix()) - 4000}); err != nil {
		t.Fatalf("set: %v", err)
	}
	state, _ = node.GetZNHBTokenomicsState()
	if !state.SaleReference.Paused || state.SaleReference.RateNHBPerZNHB == "" {
		t.Fatalf("a stale reference: paused with the old rate still shown: %+v", state.SaleReference)
	}

	node.SetValidatorRules(ValidatorRules{SaleReferenceActivationHeight: 1_000_000, SaleReferenceMaxAgeSeconds: 3600})
	state, _ = node.GetZNHBTokenomicsState()
	if state.SaleReference == nil || state.SaleReference.Active || state.SaleReference.Paused {
		t.Fatalf("set but not yet reached: neither active nor paused: %+v", state.SaleReference)
	}
}
