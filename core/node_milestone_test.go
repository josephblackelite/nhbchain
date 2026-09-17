package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/native/escrow"
)

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

	var payer [20]byte
	payer[0] = 0x11
	var payee [20]byte
	payee[0] = 0x22

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(500), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(25), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	project, err := node.EscrowMilestoneCreate(&escrow.MilestoneProject{
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
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}

	if err := node.EscrowMilestoneFund(project.ID, 1, payer); err != nil {
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

	if err := node.EscrowMilestoneRelease(project.ID, 1, payer); err != nil {
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

	var payer [20]byte
	payer[0] = 0x33
	var payee [20]byte
	payee[0] = 0x44

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(300), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	project, err := node.EscrowMilestoneCreate(&escrow.MilestoneProject{
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
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	if err := node.EscrowMilestoneFund(project.ID, 7, payer); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}
	if err := node.EscrowMilestoneCancel(project.ID, 7, payer); err != nil {
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

func TestEscrowMilestoneGetSweepsDueLegAndRefundsPayer(t *testing.T) {
	sp := newStakingStateProcessor(t)
	current := time.Unix(1_700_200_000, 0).UTC()
	node := &Node{
		state:      sp,
		timeSource: func() time.Time { return current },
	}

	var payer [20]byte
	payer[0] = 0x55
	var payee [20]byte
	payee[0] = 0x66

	writeAccount(t, sp, payer, &types.Account{BalanceNHB: big.NewInt(250), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	writeAccount(t, sp, payee, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})

	project, err := node.EscrowMilestoneCreate(&escrow.MilestoneProject{
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
	})
	if err != nil {
		t.Fatalf("create milestone: %v", err)
	}
	if err := node.EscrowMilestoneFund(project.ID, 3, payer); err != nil {
		t.Fatalf("fund milestone: %v", err)
	}

	current = current.Add(2 * time.Hour)
	stored, err := node.EscrowMilestoneGet(project.ID)
	if err != nil {
		t.Fatalf("get milestone after deadline: %v", err)
	}
	if stored.Status != escrow.MilestoneStatusCancelled {
		t.Fatalf("unexpected project status after due sweep: %d", stored.Status)
	}
	if leg := stored.FindLeg(3); leg == nil || leg.Status != escrow.MilestoneLegExpired {
		t.Fatalf("expected expired leg after sweep, got %#v", leg)
	}

	payerAcc, err := sp.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if payerAcc.BalanceNHB.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected payer refunded after due sweep, got %s", payerAcc.BalanceNHB)
	}
	vault := milestoneVaultAddress(project.ID, 3, "NHB")
	var vaultAddr [20]byte
	copy(vaultAddr[:], vault.Bytes())
	vaultAcc, err := sp.GetAccount(vaultAddr[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("expected empty vault after due sweep, got %s", vaultAcc.BalanceNHB)
	}
	if findCoreEventByType(sp.Events(), escrow.EventTypeMilestoneDue) == nil {
		t.Fatalf("missing milestone due event")
	}
}
