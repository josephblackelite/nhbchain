package core

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

// milestoneTestSigner generates a real key and returns its address plus
// the raw ecdsa key, for producing genuine wallet signatures over
// milestone action/create envelopes (NHB-AUDIT-C3: these RPCs no longer
// accept a bare, unauthenticated caller address).
func milestoneTestSigner(t *testing.T) ([20]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	return addr, key.PrivateKey
}

func signMilestoneCreate(t *testing.T, project *escrow.MilestoneProject, priv *ecdsa.PrivateKey) []byte {
	t.Helper()
	sig, err := escrow.SignMilestoneCreateEnvelope(project, priv)
	if err != nil {
		t.Fatalf("sign milestone create: %v", err)
	}
	return sig
}

func signMilestoneAction(t *testing.T, id [32]byte, legID uint64, action string, active *bool, priv *ecdsa.PrivateKey) []byte {
	t.Helper()
	sig, err := escrow.SignMilestoneActionEnvelope(id, legID, action, active, priv)
	if err != nil {
		t.Fatalf("sign milestone action: %v", err)
	}
	return sig
}

func findCoreEventByType(events []types.Event, eventType string) *types.Event {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func TestEscrowMilestoneFundAndReleaseMoveBalances(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_000_000, 0).UTC()
	node := &Node{
		state:      sp,
		timeSource: func() time.Time { return current },
	}

	payer, payerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x22

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(500), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(25), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer:   payer,
		Payee:   payee,
		RealmID: "freelance",
		Legs: []*escrow.MilestoneLeg{{
			ID:       1,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "phase 1",
			Token:    "NHB",
			Amount:   big.NewInt(100),
			Deadline: current.Add(2 * time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	fundSig := signMilestoneAction(t, project.ID, 1, escrow.MilestoneActionFund, nil, payerKey)
	if err := node.EscrowMilestoneFund(project.ID, 1, fundSig); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}

	vault := milestoneVaultAddress(project.ID, 1, "NHB")
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())

	payerAcc, err := sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected payer balance after fund: %s", payerAcc.BalanceNHB)
	}
	vaultAcc, err := sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected vault balance after fund: %s", vaultAcc.BalanceNHB)
	}
	stored, err := node.EscrowMilestoneGet(project.ID)
	if err != nil {
		t.Fatalf("get milestone after fund: %v", err)
	}
	if stored.Status != escrow.MilestoneStatusActive {
		t.Fatalf("unexpected project status after fund: %d", stored.Status)
	}
	if leg := stored.FindLeg(1); leg == nil || leg.Status != escrow.MilestoneLegFunded {
		t.Fatalf("expected funded leg, got %#v", leg)
	}

	releaseSig := signMilestoneAction(t, project.ID, 1, escrow.MilestoneActionRelease, nil, payerKey)
	if err := node.EscrowMilestoneRelease(project.ID, 1, releaseSig); err != nil {
		t.Fatalf("release milestone: %v", err)
	}

	payeeAcc, err := sp.GetAccount(payee[:])
	if err != nil {
		t.Fatalf("get payee: %v", err)
	}
	if payeeAcc.BalanceNHB.Cmp(big.NewInt(125)) != 0 {
		t.Fatalf("unexpected payee balance after release: %s", payeeAcc.BalanceNHB)
	}
	vaultAcc, err = sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault after release: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected empty vault after release, got %s", vaultAcc.BalanceNHB)
	}
	stored, err = node.EscrowMilestoneGet(project.ID)
	if err != nil {
		t.Fatalf("get milestone after release: %v", err)
	}
	if stored.Status != escrow.MilestoneStatusCompleted {
		t.Fatalf("unexpected project status after release: %d", stored.Status)
	}
	if leg := stored.FindLeg(1); leg == nil || leg.Status != escrow.MilestoneLegReleased {
		t.Fatalf("expected released leg, got %#v", leg)
	}

	events := sp.Events()
	if findCoreEventByType(events, escrow.EventTypeMilestoneCreated) == nil {
		t.Fatalf("missing milestone created event")
	}
	if findCoreEventByType(events, escrow.EventTypeMilestoneFunded) == nil {
		t.Fatalf("missing milestone funded event")
	}
	if findCoreEventByType(events, escrow.EventTypeMilestoneReleased) == nil {
		t.Fatalf("missing milestone released event")
	}
}

func TestEscrowMilestoneCancelRefundsFundedLeg(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_100_000, 0).UTC()
	node := &Node{
		state:      sp,
		timeSource: func() time.Time { return current },
	}

	payer, payerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x44

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(300), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer: payer,
		Payee: payee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       7,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "cancel me",
			Token:    "NHB",
			Amount:   big.NewInt(80),
			Deadline: current.Add(time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	fundSig := signMilestoneAction(t, project.ID, 7, escrow.MilestoneActionFund, nil, payerKey)
	if err := node.EscrowMilestoneFund(project.ID, 7, fundSig); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}
	cancelSig := signMilestoneAction(t, project.ID, 7, escrow.MilestoneActionCancel, nil, payerKey)
	if err := node.EscrowMilestoneCancel(project.ID, 7, cancelSig); err != nil {
		t.Fatalf("cancel milestone: %v", err)
	}

	payerAcc, err := sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("expected payer refunded to 300, got %s", payerAcc.BalanceNHB)
	}
	vault := milestoneVaultAddress(project.ID, 7, "NHB")
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())
	vaultAcc, err := sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected empty vault after cancel, got %s", vaultAcc.BalanceNHB)
	}
	stored, err := node.EscrowMilestoneGet(project.ID)
	if err != nil {
		t.Fatalf("get milestone: %v", err)
	}
	if stored.Status != escrow.MilestoneStatusCancelled {
		t.Fatalf("unexpected project status after cancel: %d", stored.Status)
	}
	if leg := stored.FindLeg(7); leg == nil || leg.Status != escrow.MilestoneLegCancelled {
		t.Fatalf("expected cancelled leg, got %#v", leg)
	}
	if findCoreEventByType(sp.Events(), escrow.EventTypeMilestoneCancelled) == nil {
		t.Fatalf("missing milestone cancelled event")
	}
}

// TestEscrowMilestoneGetShowsExpiredWithoutMutatingLiveState is the direct
// regression test for NHB-AUDIT-C4: EscrowMilestoneGet (a read endpoint)
// must show an accurate, computed "as of now" status for a leg whose
// deadline has passed -- but must NEVER actually move real balances,
// persist the expiry, or emit an event as a side effect of a mere read,
// since none of that corresponds to a transaction any other validator
// could independently replay (see WithStateView's doc comment on the
// state-root fork risk). The real refund + persisted expiry + event only
// happen once an actual mutating call (here, Cancel) runs against the
// project afterward.
func TestEscrowMilestoneGetShowsExpiredWithoutMutatingLiveState(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_200_000, 0).UTC()
	node := &Node{
		state:      sp,
		timeSource: func() time.Time { return current },
	}

	payer, payerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x66

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(250), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer: payer,
		Payee: payee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       3,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "due soon",
			Token:    "NHB",
			Amount:   big.NewInt(70),
			Deadline: current.Add(10 * time.Minute).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	fundSig := signMilestoneAction(t, project.ID, 3, escrow.MilestoneActionFund, nil, payerKey)
	if err := node.EscrowMilestoneFund(project.ID, 3, fundSig); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}

	current = current.Add(2 * time.Hour)

	// Get must show the correct computed status ("expired") -- but must
	// NOT have moved any real balance, persisted anything, or emitted an
	// event.
	stored, err := node.EscrowMilestoneGet(project.ID)
	if err != nil {
		t.Fatalf("get milestone after deadline: %v", err)
	}
	if stored.Status != escrow.MilestoneStatusCancelled {
		t.Fatalf("unexpected project status after due read: %d", stored.Status)
	}
	if leg := stored.FindLeg(3); leg == nil || leg.Status != escrow.MilestoneLegExpired {
		t.Fatalf("expected expired leg in the computed read view, got %#v", leg)
	}

	vault := milestoneVaultAddress(project.ID, 3, "NHB")
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())

	payerAcc, err := sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(180)) != 0 {
		t.Fatalf("SECURITY REGRESSION: a mere read moved real balance (payer expected still-debited 180, got %s)", payerAcc.BalanceNHB)
	}
	vaultAcc, err := sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(70)) != 0 {
		t.Fatalf("SECURITY REGRESSION: a mere read drained the vault (expected still-funded 70, got %s)", vaultAcc.BalanceNHB)
	}
	if findCoreEventByType(sp.Events(), escrow.EventTypeMilestoneDue) != nil {
		t.Fatalf("SECURITY REGRESSION: a mere read emitted a persisted milestone-due event")
	}

	// The project's REAL persisted status must still show funded/active --
	// the expiry above was purely a computed read-time projection.
	rawManager := nhbstate.NewManager(sp.Trie)
	rawProject, ok, err := getMilestoneProject(rawManager, project.ID)
	if err != nil || !ok {
		t.Fatalf("read raw persisted milestone: ok=%v err=%v", ok, err)
	}
	if leg := rawProject.FindLeg(3); leg == nil || leg.Status != escrow.MilestoneLegFunded {
		t.Fatalf("expected the REAL persisted leg to remain funded until an actual mutation runs, got %#v", leg)
	}

	// Now an actual mutating call runs (Cancel) -- THIS is what performs
	// the real sweep: real vault refund, real persisted expiry, real event.
	cancelSig := signMilestoneAction(t, project.ID, 3, escrow.MilestoneActionCancel, nil, payerKey)
	if err := node.EscrowMilestoneCancel(project.ID, 3, cancelSig); err != nil {
		t.Fatalf("cancel milestone: %v", err)
	}

	payerAcc, err = sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer after real sweep: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected payer refunded after the real sweep, got %s", payerAcc.BalanceNHB)
	}
	vaultAcc, err = sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault after real sweep: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected empty vault after the real sweep, got %s", vaultAcc.BalanceNHB)
	}
	if findCoreEventByType(sp.Events(), escrow.EventTypeMilestoneDue) == nil {
		t.Fatalf("missing milestone due event after the real sweep")
	}
}

// TestEscrowMilestoneCreateRejectsAttackerNamingVictimAsPayer is the direct
// regression test for NHB-AUDIT-C3's exploit: before this fix, Create
// trusted a bare Payer address field with no proof of control, letting an
// attacker create a project naming a victim as payer (and themselves as
// payee) with zero consent from the victim. Now the attacker can only
// produce a signature recovering to their OWN address, which never
// matches the claimed victim payer, so creation must fail outright --
// before any money can ever move.
func TestEscrowMilestoneCreateRejectsAttackerNamingVictimAsPayer(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_300_000, 0).UTC()
	node := &Node{state: sp, timeSource: func() time.Time { return current }}

	victim, _ := milestoneTestSigner(t)
	_, attackerKey := milestoneTestSigner(t)
	var attackerAsPayee [20]byte
	attackerAsPayee[0] = 0x99

	maliciousProject := &escrow.MilestoneProject{
		Payer: victim, // attacker names the victim as payer
		Payee: attackerAsPayee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       1,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "steal",
			Token:    "NHB",
			Amount:   big.NewInt(100),
			Deadline: current.Add(time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	// The attacker can only sign with their OWN key -- they have no
	// access to the victim's.
	forgedSig := signMilestoneCreate(t, maliciousProject, attackerKey)

	_, err := node.EscrowMilestoneCreate(maliciousProject, forgedSig)
	if err == nil {
		t.Fatalf("SECURITY REGRESSION: attacker created a milestone project naming a victim as payer with no proof of consent")
	}
}

// TestEscrowMilestoneFundRejectsCallerWithoutRealSignature is the direct
// regression test for the fund/release half of NHB-AUDIT-C3's exploit:
// even for a project the payer genuinely and validly created, an attacker
// who is not the payer cannot fund or release it merely by claiming to be
// them -- they must produce a genuine signature, which they cannot.
func TestEscrowMilestoneFundRejectsCallerWithoutRealSignature(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_400_000, 0).UTC()
	node := &Node{state: sp, timeSource: func() time.Time { return current }}

	payer, payerKey := milestoneTestSigner(t)
	_, attackerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x77

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(500), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer: payer,
		Payee: payee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       1,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "phase 1",
			Token:    "NHB",
			Amount:   big.NewInt(100),
			Deadline: current.Add(time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	// The attacker signs with their OWN key, not the payer's.
	attackerSig := signMilestoneAction(t, project.ID, 1, escrow.MilestoneActionFund, nil, attackerKey)
	if err := node.EscrowMilestoneFund(project.ID, 1, attackerSig); err == nil {
		t.Fatalf("SECURITY REGRESSION: non-payer funded a milestone leg with no real signature from the payer")
	}

	payerAcc, err := sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("SECURITY REGRESSION: payer balance moved despite the forged fund attempt being rejected, got %s", payerAcc.BalanceNHB)
	}
}

// TestEscrowMilestoneActionSignatureCannotBeReplayedAcrossActions proves
// a signature authorizing one action (e.g. fund) cannot be reused to
// authorize a different action (e.g. release, cancel) on the same
// project/leg -- the action string is part of the signed envelope, so
// the recovered signer only matches when it was produced for exactly
// this action.
func TestEscrowMilestoneActionSignatureCannotBeReplayedAcrossActions(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_500_000, 0).UTC()
	node := &Node{state: sp, timeSource: func() time.Time { return current }}

	payer, payerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x88

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(200), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer: payer,
		Payee: payee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       1,
			Type:     escrow.MilestoneLegTypeDeliverable,
			Title:    "phase 1",
			Token:    "NHB",
			Amount:   big.NewInt(50),
			Deadline: current.Add(time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	// A genuine signature for "fund" must not authorize "release" or
	// "cancel" on the same leg.
	fundSig := signMilestoneAction(t, project.ID, 1, escrow.MilestoneActionFund, nil, payerKey)
	if err := node.EscrowMilestoneRelease(project.ID, 1, fundSig); err == nil {
		t.Fatalf("SECURITY REGRESSION: a fund signature was accepted to authorize release")
	}
	if err := node.EscrowMilestoneCancel(project.ID, 1, fundSig); err == nil {
		t.Fatalf("SECURITY REGRESSION: a fund signature was accepted to authorize cancel")
	}

	// The genuine fund signature must still work for its real action.
	if err := node.EscrowMilestoneFund(project.ID, 1, fundSig); err != nil {
		t.Fatalf("expected the genuine fund signature to authorize fund: %v", err)
	}
}

// TestEscrowMilestoneSubscriptionSignatureBindsActiveValue proves a
// signature authorizing subscription active=true cannot be replayed to
// also mean active=false (or vice versa) -- the toggle's target value
// travels inside the signed envelope.
func TestEscrowMilestoneSubscriptionSignatureBindsActiveValue(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_600_000, 0).UTC()
	node := &Node{state: sp, timeSource: func() time.Time { return current }}

	payer, payerKey := milestoneTestSigner(t)
	var payee [20]byte
	payee[0] = 0x66

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	newProject := &escrow.MilestoneProject{
		Payer: payer,
		Payee: payee,
		Legs: []*escrow.MilestoneLeg{{
			ID:       1,
			Type:     escrow.MilestoneLegTypeTimebox,
			Title:    "retainer",
			Token:    "NHB",
			Amount:   big.NewInt(10),
			Deadline: current.Add(time.Hour).Unix(),
			Status:   escrow.MilestoneLegPending,
		}},
		Subscription: &escrow.MilestoneSubscription{
			IntervalSeconds: 3600,
			NextReleaseAt:   current.Add(time.Hour).Unix(),
			Active:          true,
		},
	}
	project, err := node.EscrowMilestoneCreate(newProject, signMilestoneCreate(t, newProject, payerKey))
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	falseVal := false
	sigForFalse := signMilestoneAction(t, project.ID, 0, escrow.MilestoneActionSubscriptionUpdate, &falseVal, payerKey)

	// Using the "turn off" signature to request "turn on" must fail.
	if _, err := node.EscrowMilestoneSubscriptionUpdate(project.ID, true, sigForFalse); err == nil {
		t.Fatalf("SECURITY REGRESSION: a signature for active=false authorized active=true")
	}

	// The genuine "turn off" signature must still work for its real value.
	updated, err := node.EscrowMilestoneSubscriptionUpdate(project.ID, false, sigForFalse)
	if err != nil {
		t.Fatalf("expected the genuine active=false signature to authorize the toggle: %v", err)
	}
	if updated.Subscription.Active {
		t.Fatalf("expected subscription to be inactive after toggle")
	}
}
