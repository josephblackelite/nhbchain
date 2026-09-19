package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
	posv1 "nhbchain/proto/pos"
)

// posAuthFixture authorizes a hold through the real transaction path and
// returns the authorization id exactly as the chain reports it: as the
// lowercase hex "authorizationId" attribute of the payments.authorized event.
type posAuthFixture struct {
	node        *Node
	payerKey    *crypto.PrivateKey
	merchantKey *crypto.PrivateKey
	payer       [20]byte
	merchant    [20]byte
	id          string
}

func applyPOSMsg(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64, txType types.TxType, msg proto.Message) error {
	t.Helper()
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     payload,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return node.state.ApplyTransaction(tx)
}

// newPOSAuthFixture funds the payer with payerFunds ZNHB and authorizes hold
// against merchantKey (the payer itself when samePayerMerchant is set).
func newPOSAuthFixture(t *testing.T, payerFunds, hold int64, samePayerMerchant bool) *posAuthFixture {
	t.Helper()
	f := &posAuthFixture{node: newTestNode(t)}
	var err error
	if f.payerKey, err = crypto.GeneratePrivateKey(); err != nil {
		t.Fatalf("payer key: %v", err)
	}
	f.merchantKey = f.payerKey
	if !samePayerMerchant {
		if f.merchantKey, err = crypto.GeneratePrivateKey(); err != nil {
			t.Fatalf("merchant key: %v", err)
		}
	}
	f.payer = toAddress(f.payerKey)
	f.merchant = toAddress(f.merchantKey)
	fundAccount(t, f.node, f.payer, big.NewInt(payerFunds))

	msg := &posv1.MsgAuthorizePayment{
		Payer:    crypto.MustNewAddress(crypto.ZNHBPrefix, f.payer[:]).String(),
		Merchant: crypto.MustNewAddress(crypto.ZNHBPrefix, f.merchant[:]).String(),
		Amount:   big.NewInt(hold).String(),
		Expiry:   4102444800,
	}
	if err := applyPOSMsg(t, f.node, f.payerKey, 0, types.TxTypePOSAuthorize, msg); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	for _, evt := range f.node.Events() {
		if evt.Type == events.TypePaymentAuthorized {
			f.id = evt.Attributes["authorizationId"]
		}
	}
	if len(f.id) != 64 {
		t.Fatalf("expected a 64-character hex authorization id in the authorize event, got %q", f.id)
	}
	return f
}

func (f *posAuthFixture) totals(t *testing.T) (payerBal, payerLocked, merchantBal, merchantLocked *big.Int) {
	t.Helper()
	p, err := f.node.state.GetAccount(f.payer[:])
	if err != nil {
		t.Fatalf("payer account: %v", err)
	}
	m, err := f.node.state.GetAccount(f.merchant[:])
	if err != nil {
		t.Fatalf("merchant account: %v", err)
	}
	return p.BalanceZNHB, p.LockedZNHB, m.BalanceZNHB, m.LockedZNHB
}

// idForms lists every spelling of the authorization id the chain reports or
// the portal sends: bare hex (event attribute, portal encoder) and 0x-prefixed
// hex (pos_getAuthorization result), in both letter cases.
func (f *posAuthFixture) idForms() map[string]string {
	return map[string]string{
		"bare hex":          f.id,
		"0x hex":            "0x" + f.id,
		"0X hex":            "0X" + f.id,
		"upper-case hex":    strings.ToUpper(f.id),
		"0x upper-case hex": "0x" + strings.ToUpper(f.id),
	}
}

func TestPOSCaptureAcceptsEveryAuthorizeIDEncoding(t *testing.T) {
	probe := newPOSAuthFixture(t, 1_000, 400, false)
	for name := range probe.idForms() {
		name := name
		t.Run(name, func(t *testing.T) {
			f := newPOSAuthFixture(t, 1_000, 400, false)
			msg := &posv1.MsgCapturePayment{AuthorizationId: f.idForms()[name], Amount: "300"}
			if err := applyPOSMsg(t, f.node, f.merchantKey, 0, types.TxTypePOSCapture, msg); err != nil {
				t.Fatalf("capture with %s id: %v", name, err)
			}
			pBal, pLocked, mBal, mLocked := f.totals(t)
			// 400 held, 300 captured, 100 refunded: 600 + 100 back for the
			// payer, 300 for the merchant, nothing left locked, 1000 in total.
			if pBal.Cmp(big.NewInt(700)) != 0 || pLocked.Sign() != 0 || mBal.Cmp(big.NewInt(300)) != 0 || mLocked.Sign() != 0 {
				t.Fatalf("balances after capture: payer %s/%s merchant %s/%s", pBal, pLocked, mBal, mLocked)
			}
		})
	}
}

func TestPOSVoidAcceptsEveryAuthorizeIDEncoding(t *testing.T) {
	probe := newPOSAuthFixture(t, 1_000, 400, false)
	for name := range probe.idForms() {
		name := name
		t.Run(name, func(t *testing.T) {
			f := newPOSAuthFixture(t, 1_000, 400, false)
			msg := &posv1.MsgVoidPayment{AuthorizationId: f.idForms()[name], Reason: "cancelled"}
			if err := applyPOSMsg(t, f.node, f.payerKey, 1, types.TxTypePOSVoid, msg); err != nil {
				t.Fatalf("void with %s id: %v", name, err)
			}
			pBal, pLocked, mBal, _ := f.totals(t)
			if pBal.Cmp(big.NewInt(1_000)) != 0 || pLocked.Sign() != 0 || mBal.Sign() != 0 {
				t.Fatalf("balances after void: payer %s/%s merchant %s", pBal, pLocked, mBal)
			}
		})
	}
}

func TestPOSCaptureAndVoidRejectMalformedAuthorizationIDs(t *testing.T) {
	f := newPOSAuthFixture(t, 1_000, 400, false)
	bad := map[string]string{
		"empty":               "",
		"prefix only":         "0x",
		"too short":           f.id[:62],
		"too long":            f.id + "00",
		"non-hex 64 chars":    strings.Repeat("zz", 32),
		"32 ascii characters": "abcdefghijklmnopqrstuvwxyz012345",
		"first 32 characters": f.id[:32],
		"double prefix":       "0x0x" + f.id[:60],
		"padded with spaces":  " " + f.id + " ",
	}
	pBal0, pLocked0, mBal0, mLocked0 := f.totals(t)
	for name, id := range bad {
		capErr := applyPOSMsg(t, f.node, f.merchantKey, 0, types.TxTypePOSCapture, &posv1.MsgCapturePayment{AuthorizationId: id, Amount: "100"})
		if !errors.Is(capErr, ErrPOSInvalidAuthorizationID) {
			t.Fatalf("capture with %s id: got %v, want ErrPOSInvalidAuthorizationID", name, capErr)
		}
		voidErr := applyPOSMsg(t, f.node, f.payerKey, 1, types.TxTypePOSVoid, &posv1.MsgVoidPayment{AuthorizationId: id})
		if !errors.Is(voidErr, ErrPOSInvalidAuthorizationID) {
			t.Fatalf("void with %s id: got %v, want ErrPOSInvalidAuthorizationID", name, voidErr)
		}
		// An ordinary per-transaction rejection: pruned, never a block abort.
		if got := classifyProposalError(capErr); got != proposalDispositionPrune {
			t.Fatalf("capture with %s id classified %v, want prune", name, got)
		}
		if got := classifyProposalError(voidErr); got != proposalDispositionPrune {
			t.Fatalf("void with %s id classified %v, want prune", name, got)
		}
	}
	pBal, pLocked, mBal, mLocked := f.totals(t)
	if pBal.Cmp(pBal0) != 0 || pLocked.Cmp(pLocked0) != 0 || mBal.Cmp(mBal0) != 0 || mLocked.Cmp(mLocked0) != 0 {
		t.Fatalf("rejected transactions changed balances")
	}
	// A well-formed id that names no authorization is still a plain not-found.
	err := applyPOSMsg(t, f.node, f.merchantKey, 0, types.TxTypePOSCapture, &posv1.MsgCapturePayment{AuthorizationId: strings.Repeat("ab", 32), Amount: "100"})
	if err == nil || errors.Is(err, ErrPOSInvalidAuthorizationID) {
		t.Fatalf("unknown well-formed id: got %v, want a not-found error", err)
	}
}

// With one account as both payer and merchant, capture must credit the
// captured amount and release the hold on a single account object: total
// ZNHB (balance plus locked) across the touched accounts is unchanged and
// nothing stays locked.
func TestPOSCaptureSamePayerAndMerchantConservesSupply(t *testing.T) {
	for _, capture := range []string{"400", "250", "1"} {
		capture := capture
		t.Run("capture "+capture, func(t *testing.T) {
			f := newPOSAuthFixture(t, 1_000, 400, true)
			if err := applyPOSMsg(t, f.node, f.merchantKey, 1, types.TxTypePOSCapture, &posv1.MsgCapturePayment{AuthorizationId: f.id, Amount: capture}); err != nil {
				t.Fatalf("capture: %v", err)
			}
			bal, locked, _, _ := f.totals(t)
			if bal.Cmp(big.NewInt(1_000)) != 0 || locked.Sign() != 0 {
				t.Fatalf("payer==merchant capture of %s: balance %s locked %s, want 1000 / 0", capture, bal, locked)
			}
		})
	}
}

func TestPOSVoidSamePayerAndMerchantConservesSupply(t *testing.T) {
	f := newPOSAuthFixture(t, 1_000, 400, true)
	if err := applyPOSMsg(t, f.node, f.merchantKey, 1, types.TxTypePOSVoid, &posv1.MsgVoidPayment{AuthorizationId: "0x" + f.id}); err != nil {
		t.Fatalf("void: %v", err)
	}
	bal, locked, _, _ := f.totals(t)
	if bal.Cmp(big.NewInt(1_000)) != 0 || locked.Sign() != 0 {
		t.Fatalf("payer==merchant void: balance %s locked %s, want 1000 / 0", bal, locked)
	}
}
