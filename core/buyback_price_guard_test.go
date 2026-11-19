package core

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// The buyback price guard (PL-EXT-28), driven through the real transaction path
// of the state processor: propose, pass, queue and execute are signed transactions
// applied at a block height.

const priceGuardHeight = uint64(100)

func applyGovTxErr(t *testing.T, sp *StateProcessor, key *crypto.PrivateKey, txType types.TxType, payload interface{}) error {
	t.Helper()
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode governance payload: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: txType, Data: data, GasPrice: big.NewInt(0)}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign governance tx: %v", err)
	}
	return sp.ApplyTransaction(tx)
}

func buybackPayloadJSON(feeShare, discount, margin int) string {
	return fmt.Sprintf(`{"feeShareBps":%d,"discountBps":%d,"safetyMarginBps":%d}`, feeShare, discount, margin)
}

// runBuybackParamsProposal submits at submitHeight and, if that was accepted,
// passes, queues and executes at executeHeight.
func runBuybackParamsProposal(t *testing.T, sp *StateProcessor, payload string, submitHeight, executeHeight uint64) (submitErr, executeErr error) {
	t.Helper()
	sp.SetGovernancePolicy(governance.ProposalPolicy{MinDepositWei: big.NewInt(0), VotingPeriodSeconds: 60, TimelockSeconds: 10})
	ts := time.Now().UTC().Add(time.Hour)

	sp.BeginBlock(submitHeight, ts)
	submitErr = applyGovTxErr(t, sp, freshGovAccountKey(t, sp, big.NewInt(0)), types.TxTypeGovPropose, govProposePayload{
		Kind: governance.ProposalKindBuybackParams, Payload: payload, Deposit: big.NewInt(0),
	})
	sp.EndBlock()
	if submitErr != nil {
		return submitErr, nil
	}
	id := latestGovProposalID(t, sp)
	markGovProposalPassed(t, sp, id)
	sp.BeginBlock(submitHeight, ts)
	if err := applyGovTxErr(t, sp, freshGovAccountKey(t, sp, big.NewInt(0)), types.TxTypeGovQueue, govProposalIDPayload{ProposalID: id}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	sp.EndBlock()
	clearGovProposalTimelock(t, sp, id)
	sp.BeginBlock(executeHeight, ts)
	executeErr = applyGovTxErr(t, sp, freshGovAccountKey(t, sp, big.NewInt(0)), types.TxTypeGovExecute, govProposalIDPayload{ProposalID: id})
	sp.EndBlock()
	return nil, executeErr
}

func storedBuybackParam(t *testing.T, sp *StateProcessor, key string) (string, bool) {
	t.Helper()
	raw, ok, err := nhbstate.NewManager(sp.Trie).ParamStoreGet(key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return string(raw), ok
}

func TestBuybackPriceGuardThroughTheStateProcessor(t *testing.T) {
	cases := []struct {
		name          string
		guardFrom     uint64
		payload       string
		submitHeight  uint64
		executeHeight uint64
		wantSubmitErr bool
		wantExecErr   bool
	}{
		{"guard off: 10000 discount is accepted and executes", 0, buybackPayloadJSON(2000, 10000, 0), priceGuardHeight + 50, priceGuardHeight + 60, false, false},
		{"guard off: 10000 margin is accepted and executes", 0, buybackPayloadJSON(2000, 0, 10000), priceGuardHeight + 50, priceGuardHeight + 60, false, false},
		{"before the height: 10000 discount is accepted and executes", priceGuardHeight, buybackPayloadJSON(2000, 10000, 0), priceGuardHeight - 5, priceGuardHeight - 4, false, false},
		{"at the height: 10000 discount is refused on submission", priceGuardHeight, buybackPayloadJSON(2000, 10000, 0), priceGuardHeight, priceGuardHeight + 1, true, false},
		{"after the height: 10000 margin is refused on submission", priceGuardHeight, buybackPayloadJSON(2000, 0, 10000), priceGuardHeight + 9, priceGuardHeight + 10, true, false},
		{"after the height: both at 10000 is refused on submission", priceGuardHeight, buybackPayloadJSON(2000, 10000, 10000), priceGuardHeight + 9, priceGuardHeight + 10, true, false},
		{"stored before the height, executed at it: refused", priceGuardHeight, buybackPayloadJSON(2000, 10000, 0), priceGuardHeight - 5, priceGuardHeight, false, true},
		{"stored before the height, executed after it: refused", priceGuardHeight, buybackPayloadJSON(2000, 0, 10000), priceGuardHeight - 5, priceGuardHeight + 30, false, true},
		{"after the height: 9999 and 9999 is accepted and executes", priceGuardHeight, buybackPayloadJSON(10000, 9999, 9999), priceGuardHeight + 9, priceGuardHeight + 10, false, false},
		{"after the height: fee share alone at 10000 is accepted", priceGuardHeight, buybackPayloadJSON(10000, 0, 0), priceGuardHeight + 9, priceGuardHeight + 10, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetBuybackPriceGuard(tc.guardFrom)
			submitErr, execErr := runBuybackParamsProposal(t, sp, tc.payload, tc.submitHeight, tc.executeHeight)
			if tc.wantSubmitErr {
				if submitErr == nil {
					t.Fatalf("submission accepted, want it refused")
				}
				if !strings.Contains(submitErr.Error(), "highest buyback price") {
					t.Fatalf("refusal does not say why: %v", submitErr)
				}
				return
			}
			if submitErr != nil {
				t.Fatalf("submission refused: %v", submitErr)
			}
			if tc.wantExecErr {
				if execErr == nil {
					t.Fatalf("execution accepted, want it refused")
				}
				if !strings.Contains(execErr.Error(), "highest buyback price") {
					t.Fatalf("refusal does not say why: %v", execErr)
				}
				for _, key := range []string{governance.ParamKeyBuybackFeeShareBps, governance.ParamKeyBuybackDiscountBps, governance.ParamKeyBuybackSafetyMarginBps} {
					if v, ok := storedBuybackParam(t, sp, key); ok {
						t.Fatalf("a refused execution stored %s = %s", key, v)
					}
				}
				return
			}
			if execErr != nil {
				t.Fatalf("execution refused: %v", execErr)
			}
			if _, ok := storedBuybackParam(t, sp, governance.ParamKeyBuybackDiscountBps); !ok {
				t.Fatalf("the executed proposal stored nothing")
			}
		})
	}
}

// The setting is carried by every copy of the state processor, by the node's
// rebuild, and reaches the governance engine the state processor builds.
func TestSettlementFailsafeSettingsAreCarried(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	sp.SetBuybackPriceGuard(11)
	sp.SetEpochSettlementFailsafe(13)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.buybackPriceGuardFromHeight != 11 || copied.epochSettlementFailsafeFromHeight != 13 {
		t.Fatalf("Copy lost a setting: %d, %d", copied.buybackPriceGuardFromHeight, copied.epochSettlementFailsafeFromHeight)
	}

	node := newTestNode(t)
	node.SetBuybackPriceGuard(17)
	node.SetEpochSettlementFailsafe(19)
	node.stateMu.Lock()
	setGuard, setFailsafe := node.state.buybackPriceGuardFromHeight, node.state.epochSettlementFailsafeFromHeight
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltGuard, rebuiltFailsafe := node.state.buybackPriceGuardFromHeight, node.state.epochSettlementFailsafeFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if setGuard != 17 || setFailsafe != 19 {
		t.Fatalf("the setters left the processor at %d, %d", setGuard, setFailsafe)
	}
	if rebuiltGuard != 17 || rebuiltFailsafe != 19 {
		t.Fatalf("a rebuilt processor lost a setting: %d, %d", rebuiltGuard, rebuiltFailsafe)
	}
	node.SetBuybackPriceGuard(0)
	node.SetEpochSettlementFailsafe(0)
	if node.state.buybackPriceGuardFromHeight != 0 || node.state.epochSettlementFailsafeFromHeight != 0 {
		t.Fatalf("the setters do not switch the rules off again")
	}
}

// A passed proposal at a height before the setting leaves every parameter exactly
// as the old code stored it, byte for byte.
func TestBuybackPriceGuardOffStoresWhatItAlwaysDid(t *testing.T) {
	spOn := newStakingStateProcessor(t)
	spOn.SetBuybackPriceGuard(priceGuardHeight)
	spOff := newStakingStateProcessor(t)
	payload := buybackPayloadJSON(3000, 750, 250)
	for _, sp := range []*StateProcessor{spOn, spOff} {
		if _, err := runBuybackParamsProposal(t, sp, payload, 10, 11); err != nil {
			t.Fatalf("execute: %v", err)
		}
	}
	for _, key := range []string{governance.ParamKeyBuybackFeeShareBps, governance.ParamKeyBuybackDiscountBps, governance.ParamKeyBuybackSafetyMarginBps} {
		a, okA := storedBuybackParam(t, spOn, key)
		b, okB := storedBuybackParam(t, spOff, key)
		if !okA || !okB || a != b {
			t.Fatalf("%s: %q/%v with the guard configured, %q/%v without", key, a, okA, b, okB)
		}
	}
}
