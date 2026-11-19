package core

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/native/fees"
	posv1 "nhbchain/proto/pos"
)

// Which asset point-of-sale authorize, capture and void settle in, and what the "pos" fee domain does and does not
// touch (ledger PL-CLM-06 item 4). The fee domain carries policies for both assets, which leaves open which asset a
// point-of-sale payment settles in. These tests pin what the code does, so that a change of either answer has to be
// made on purpose.
//
// The trace, with file and line references (release/final-r12, 808c1d1):
//   - core/state_transition.go:4427-4432 dispatches TxTypePOSAuthorize / Capture / Void to applyPOSAuthorize /
//     applyPOSCapture / applyPOSVoid (core/state_pos.go:28, 112, 147). The messages carry an amount and no asset.
//   - native/pos/auth.go:175-218 (Authorize) moves the amount from the payer's BalanceZNHB to LockedZNHB; line 199
//     refuses when BalanceZNHB is short. NHB is never read.
//   - native/pos/auth.go:300-360 (Capture) takes the whole hold out of the payer's LockedZNHB, returns the part that
//     is not captured to BalanceZNHB (329-331) and credits the merchant's BalanceZNHB with the captured amount
//     (333). Nothing is deducted: no fee is computed anywhere on this path.
//   - native/pos/auth.go:432-436 (Void) returns the hold from LockedZNHB to BalanceZNHB.
//   - The "pos" fee domain (native/fees/apply.go:12, registered from [global.Fees] at core/node.go:1583) is applied
//     only by StateProcessor.applyTransactionFee (core/state_transition.go:529), which acts on TxTypeTransfer (NHB)
//     and TxTypeTransferZNHB (ZNHB) whose MerchantAddress field names the domain, and on nothing else.
//   - With the shipped [global.Fees] (a merchant discount rate of 150 basis points for NHB and 200 for ZNHB) a
//     merchant who is paid by authorize and capture therefore receives ZNHB and pays no fee; a merchant paid by an
//     ordinary transfer that names the "pos" domain pays 1.5% in NHB or 2% in ZNHB.

func posFeePolicy(owner [20]byte) fees.Policy {
	return fees.Policy{
		Domains: map[string]fees.DomainPolicy{
			fees.DomainPOS: {
				MDRBasisPoints:        150,
				OwnerWallet:           owner,
				FreeTierTxPerMonthSet: true,
				Assets: map[string]fees.AssetPolicy{
					fees.AssetNHB:  {MDRBasisPoints: 150, OwnerWallet: owner},
					fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: owner},
				},
			},
		},
	}
}

func accountOf(t *testing.T, f *posAuthFixture, addr [20]byte) *types.Account {
	t.Helper()
	acc, err := f.node.state.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	return acc
}

func isZero(n *big.Int) bool { return n == nil || n.Sign() == 0 }

// A payer who holds ZNHB and no NHB at all can authorize and capture, and only ZNHB moves.
func TestPOSAuthorizeCaptureAndVoidSettleInZNHBAndNeverInNHB(t *testing.T) {
	f := newPOSAuthFixture(t, 1_000, 400, false)

	payer := accountOf(t, f, f.payer)
	if payer.BalanceZNHB.Cmp(big.NewInt(600)) != 0 || payer.LockedZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("authorize must lock the hold in ZNHB: balance %s locked %s, want 600 / 400", payer.BalanceZNHB, payer.LockedZNHB)
	}
	if !isZero(payer.BalanceNHB) {
		t.Fatalf("the payer never held NHB, so authorize cannot have used it: NHB balance %s", payer.BalanceNHB)
	}

	// give both sides some NHB, so that a capture that touched NHB could not hide behind zero balances
	for _, addr := range [][20]byte{f.payer, f.merchant} {
		acc := accountOf(t, f, addr)
		acc.BalanceNHB = big.NewInt(5_000)
		if err := f.node.state.PutAccount(addr[:], acc); err != nil {
			t.Fatalf("put account: %v", err)
		}
	}

	if err := applyPOSMsg(t, f.node, f.merchantKey, 0, types.TxTypePOSCapture, &posv1.MsgCapturePayment{AuthorizationId: f.id, Amount: "300"}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	payer, merchant := accountOf(t, f, f.payer), accountOf(t, f, f.merchant)
	if merchant.BalanceZNHB.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("the merchant must receive exactly the captured 300 ZNHB (no fee taken), got %s", merchant.BalanceZNHB)
	}
	if payer.BalanceZNHB.Cmp(big.NewInt(700)) != 0 || payer.LockedZNHB.Sign() != 0 {
		t.Fatalf("the part of the hold that was not captured must go back to the payer's ZNHB: balance %s locked %s, want 700 / 0", payer.BalanceZNHB, payer.LockedZNHB)
	}
	if payer.BalanceNHB.Cmp(big.NewInt(5_000)) != 0 || merchant.BalanceNHB.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("a capture must not move NHB: payer %s merchant %s, want 5000 / 5000", payer.BalanceNHB, merchant.BalanceNHB)
	}

	// void: a second hold, returned whole, in ZNHB
	g := newPOSAuthFixture(t, 1_000, 250, false)
	if err := applyPOSMsg(t, g.node, g.merchantKey, 0, types.TxTypePOSVoid, &posv1.MsgVoidPayment{AuthorizationId: g.id}); err != nil {
		t.Fatalf("void: %v", err)
	}
	voided := accountOf(t, g, g.payer)
	if voided.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 || voided.LockedZNHB.Sign() != 0 || !isZero(voided.BalanceNHB) {
		t.Fatalf("a void must return the hold in ZNHB and leave NHB alone: balance %s locked %s NHB %s", voided.BalanceZNHB, voided.LockedZNHB, voided.BalanceNHB)
	}
}

// With the "pos" fee domain configured for both assets, capturing a payment still takes no fee: the merchant gets the
// whole amount and the fee wallet gets nothing.
func TestPOSCaptureTakesNoFeeEvenWhenThePOSFeeDomainIsConfigured(t *testing.T) {
	f := newPOSAuthFixture(t, 10_000, 10_000, false)
	var owner [20]byte
	owner[19] = 0x7E
	f.node.state.SetFeePolicy(posFeePolicy(owner))

	if err := applyPOSMsg(t, f.node, f.merchantKey, 0, types.TxTypePOSCapture, &posv1.MsgCapturePayment{AuthorizationId: f.id, Amount: "10000"}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	merchant := accountOf(t, f, f.merchant)
	if merchant.BalanceZNHB.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("the merchant must receive the whole captured amount, got %s of 10000 (a 2%% fee would leave 9800)", merchant.BalanceZNHB)
	}
	route := accountOf(t, f, owner)
	if !isZero(route.BalanceZNHB) || !isZero(route.BalanceNHB) {
		t.Fatalf("nothing may be routed to the fee wallet: ZNHB %s NHB %s", route.BalanceZNHB, route.BalanceNHB)
	}
}

// The same policy does charge an ordinary transfer that names the domain, in the asset of that transfer; and the
// fee function does nothing for the point-of-sale transaction types even when they carry the domain name.
func TestThePOSFeeDomainChargesNamedTransfersAndNeverThePOSTransactionTypes(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var owner [20]byte
	owner[19] = 0x7E
	sp.SetFeePolicy(posFeePolicy(owner))
	sender := make([]byte, 20)
	sender[19] = 0xAA

	for _, tc := range []struct {
		name    string
		txType  types.TxType
		znhb    bool
		domain  string
		wantFee int64 // of a 10,000 amount
	}{
		{"NHB transfer naming the domain: 150 basis points", types.TxTypeTransfer, false, fees.DomainPOS, 150},
		{"ZNHB transfer naming the domain: 200 basis points", types.TxTypeTransferZNHB, true, fees.DomainPOS, 200},
		{"NHB transfer naming no domain: nothing", types.TxTypeTransfer, false, "", 0},
		{"NHB transfer naming a domain with no policy: nothing", types.TxTypeTransfer, false, "unlisted", 0},
		{"POS authorize naming the domain: nothing", types.TxTypePOSAuthorize, true, fees.DomainPOS, 0},
		{"POS capture naming the domain: nothing", types.TxTypePOSCapture, true, fees.DomainPOS, 0},
		{"POS void naming the domain: nothing", types.TxTypePOSVoid, true, fees.DomainPOS, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from := &types.Account{BalanceNHB: big.NewInt(100_000), BalanceZNHB: big.NewInt(100_000)}
			to := &types.Account{BalanceNHB: big.NewInt(50_000), BalanceZNHB: big.NewInt(50_000)}
			tx := &types.Transaction{Type: tc.txType, MerchantAddress: tc.domain, Value: big.NewInt(10_000)}
			if err := sp.applyTransactionFee(tx, sender, from, to); err != nil {
				t.Fatalf("apply fee: %v", err)
			}
			wantNHB, wantZNHB := int64(100_000), int64(100_000)
			if tc.znhb {
				wantZNHB -= tc.wantFee
			} else {
				wantNHB -= tc.wantFee
			}
			if from.BalanceNHB.Int64() != wantNHB || from.BalanceZNHB.Int64() != wantZNHB {
				t.Fatalf("sender after the fee: NHB %s ZNHB %s, want %d / %d", from.BalanceNHB, from.BalanceZNHB, wantNHB, wantZNHB)
			}
		})
	}
}
