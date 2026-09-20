package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
)

// TestEvidenceSlashEventReportsWhatWasForfeited processes an equivocation
// report against an offender whose recorded stake (1000) is larger than the ZNHB
// it holds locked itself (400), the rest being stake other accounts delegated
// to it. The penalty asks for the whole stake, the slasher can only forfeit the
// 400 the offender has bonded, and the penalty event must say 400: it used to
// carry the 1000 that was asked for while the treasury received 400.
func TestEvidenceSlashEventReportsWhatWasForfeited(t *testing.T) {
	genesisPath, treasuryAddr := writeEvidenceTestGenesis(t)
	node := buildSwapAdminTestNode(t, genesisPath)

	offenderKey := newEvidenceTestKey(t)
	reporterKey := newEvidenceTestKey(t)
	offender := offenderKey.address()

	const evidenceHeight = 50
	const applyHeight = 200

	seedEvidenceOffenderStake(t, node, offender, big.NewInt(1_000))
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	account, err := manager.GetAccount(offender[:])
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load offender account: %v", err)
	}
	account.LockedZNHB = big.NewInt(400)
	if err := manager.PutAccount(offender[:], account); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("lower the offender's locked ZNHB: %v", err)
	}
	node.stateMu.Unlock()
	seedEvidenceReporterBond(t, node, reporterKey.address())

	ev, _ := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, evidenceHeight)
	tx := signedSubmitEvidenceTx(t, ev, reporterKey, 0)

	blockTime := time.Unix(1_700_000_000, 0).UTC()
	node.stateMu.Lock()
	node.state.BeginBlock(applyHeight, blockTime)
	if err := node.state.ApplyTransaction(tx); err != nil {
		node.state.EndBlock()
		node.stateMu.Unlock()
		t.Fatalf("apply evidence transaction: %v", err)
	}
	if err := node.processPendingEvidenceForState(node.state, applyHeight); err != nil {
		node.state.EndBlock()
		node.stateMu.Unlock()
		t.Fatalf("process pending evidence: %v", err)
	}
	node.state.EndBlock()
	logged := node.state.Events()
	offenderAfter, offenderErr := manager.GetAccount(offender[:])
	treasuryAfter, treasuryErr := manager.GetAccount(treasuryAddr[:])
	node.stateMu.Unlock()
	if offenderErr != nil || treasuryErr != nil {
		t.Fatalf("load accounts: %v %v", offenderErr, treasuryErr)
	}

	if treasuryAfter.BalanceZNHB == nil || treasuryAfter.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("treasury holds %v, want the 400 the offender had bonded", treasuryAfter.BalanceZNHB)
	}
	if offenderAfter.LockedZNHB.Sign() != 0 || offenderAfter.Stake.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("offender ended with locked=%s stake=%s, want 0 and 600", offenderAfter.LockedZNHB, offenderAfter.Stake)
	}

	var reported string
	for _, evt := range logged {
		if evt.Type == events.TypePotsoPenaltyApplied {
			reported = evt.Attributes["slashAmt"]
		}
	}
	if reported != "400" {
		t.Fatalf("penalty event slashAmt = %q, want 400 (the amount forfeited)", reported)
	}
}
