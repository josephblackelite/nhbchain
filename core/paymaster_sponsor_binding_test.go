package core

import (
	"bytes"
	"crypto/sha256"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-22: a sponsor's signature is bound to the sender from a configured height.

const bindingHeight = 100

type bindingEnv struct {
	sp         *StateProcessor
	sponsorKey *crypto.PrivateKey
	sponsor    []byte
}

func newBindingEnv(t *testing.T, activation uint64) *bindingEnv {
	t.Helper()
	sp := newSponsorshipState(t)
	sp.SetPaymasterEnabled(true)
	sp.SetPaymasterSponsorBinding(activation)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("sponsor key: %v", err)
	}
	env := &bindingEnv{sp: sp, sponsorKey: key, sponsor: key.PubKey().Address().Bytes()}
	env.fund(t, env.sponsor, 1_000_000_000)
	return env
}

func (e *bindingEnv) fund(t *testing.T, addr []byte, nhb int64) {
	t.Helper()
	if err := e.sp.setAccount(addr, &types.Account{BalanceNHB: big.NewInt(nhb), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func (e *bindingEnv) newSender(t *testing.T) (*crypto.PrivateKey, []byte) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("sender key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	e.fund(t, addr, 10_000_000)
	return key, addr
}

// fields returns the sponsored transfer every test uses; only the signatures differ.
func (e *bindingEnv) fields() *types.Transaction {
	return &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        append([]byte(nil), common.Address{0xAB}.Bytes()...),
		Value:     big.NewInt(5),
		GasLimit:  21_000,
		GasPrice:  big.NewInt(1),
		Paymaster: append([]byte(nil), e.sponsor...),
	}
}

func (e *bindingEnv) at(height uint64) { e.sp.BeginBlock(height, time.Unix(1_700_000_000, 0).UTC()) }

func bindingStatus(t *testing.T, sp *StateProcessor, tx *types.Transaction) SponsorshipStatus {
	t.Helper()
	assessment, err := sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return assessment.Status
}

func boundTx(t *testing.T, e *bindingEnv, senderKey *crypto.PrivateKey, senderAddr []byte) *types.Transaction {
	t.Helper()
	tx := e.fields()
	signTransaction(t, tx, senderKey)
	if err := tx.SignPaymasterBound(e.sponsorKey.PrivateKey, senderAddr); err != nil {
		t.Fatalf("sign bound: %v", err)
	}
	return tx
}

func legacyTx(t *testing.T, e *bindingEnv, senderKey *crypto.PrivateKey) *types.Transaction {
	t.Helper()
	tx := e.fields()
	signTransaction(t, tx, senderKey)
	signPaymaster(t, tx, e.sponsorKey)
	return tx
}

// With the setting off nothing changes: the signature over tx.Hash() is accepted,
// and so are its two weaknesses, which the tests below show on the bound rule.
func TestPaymasterSponsorBindingOffKeepsTheLegacyRule(t *testing.T) {
	for _, activation := range []uint64{0} {
		e := newBindingEnv(t, activation)
		e.at(1_000_000)
		keyA, _ := e.newSender(t)
		keyB, _ := e.newSender(t)

		txA := legacyTx(t, e, keyA)
		if got := bindingStatus(t, e.sp, txA); got != SponsorshipStatusReady {
			t.Fatalf("legacy signature with the setting off: want ready, got %s", got)
		}
		// One sponsorship, two senders: the same PaymasterR/S/V validates for B.
		txB := e.fields()
		signTransaction(t, txB, keyB)
		txB.PaymasterR, txB.PaymasterS, txB.PaymasterV = txA.PaymasterR, txA.PaymasterS, txA.PaymasterV
		if got := bindingStatus(t, e.sp, txB); got != SponsorshipStatusReady {
			t.Fatalf("legacy reuse by a second sender with the setting off: want ready (the flaw this setting closes), got %s", got)
		}
		// The sponsor's signature is a valid envelope signature: a copy of the fields
		// signed with it names the sponsor as the sender.
		copyTx := e.fields()
		copyTx.R, copyTx.S, copyTx.V = txA.PaymasterR, txA.PaymasterS, txA.PaymasterV
		from, err := copyTx.From()
		if err != nil {
			t.Fatalf("from: %v", err)
		}
		if !bytes.Equal(from, e.sponsor) {
			t.Fatalf("legacy role confusion: want the sponsor as sender, got %x", from)
		}
	}
}

func TestPaymasterSponsorBindingOnAcceptsTheBoundSignature(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	for _, height := range []uint64{bindingHeight, bindingHeight + 1, bindingHeight + 5000} {
		e.at(height)
		if got := bindingStatus(t, e.sp, boundTx(t, e, keyA, addrA)); got != SponsorshipStatusReady {
			t.Fatalf("height %d: a signature bound to the sender must be accepted, got %s", height, got)
		}
	}
}

func TestPaymasterSponsorBindingOnRefusesTheLegacySignature(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, _ := e.newSender(t)
	e.at(bindingHeight)
	if got := bindingStatus(t, e.sp, legacyTx(t, e, keyA)); got != SponsorshipStatusSignatureInvalid {
		t.Fatalf("a signature over tx.Hash() alone must be refused from the height, got %s", got)
	}
}

func TestPaymasterSponsorBindingOnRefusesReuseForAnotherSender(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	keyB, _ := e.newSender(t)
	e.at(bindingHeight)

	txA := boundTx(t, e, keyA, addrA)
	if got := bindingStatus(t, e.sp, txA); got != SponsorshipStatusReady {
		t.Fatalf("sender A: want ready, got %s", got)
	}
	// Sender B signs the same fields and attaches A's sponsor signature.
	txB := e.fields()
	signTransaction(t, txB, keyB)
	txB.PaymasterR, txB.PaymasterS, txB.PaymasterV = txA.PaymasterR, txA.PaymasterS, txA.PaymasterV
	if got := bindingStatus(t, e.sp, txB); got != SponsorshipStatusSignatureInvalid {
		t.Fatalf("one sponsorship reused by a second sender must be refused, got %s", got)
	}
}

func TestPaymasterSponsorBindingBeforeTheHeightIsTheOldRule(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	e.at(bindingHeight - 1)
	if got := bindingStatus(t, e.sp, legacyTx(t, e, keyA)); got != SponsorshipStatusReady {
		t.Fatalf("below the height the old signature must still be accepted, got %s", got)
	}
	if got := bindingStatus(t, e.sp, boundTx(t, e, keyA, addrA)); got != SponsorshipStatusSignatureInvalid {
		t.Fatalf("below the height the bound signature is not the rule yet, got %s", got)
	}
}

// A signature over the bound digest is not a valid envelope signature, so it cannot
// be copied into R/S/V to make the sponsor the sender of a copy of the fields.
func TestPaymasterSponsorBindingSignatureIsNotAnEnvelopeSignature(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	e.at(bindingHeight)
	txA := boundTx(t, e, keyA, addrA)

	copyTx := e.fields()
	copyTx.R, copyTx.S, copyTx.V = txA.PaymasterR, txA.PaymasterS, txA.PaymasterV
	from, err := copyTx.From()
	if err == nil && bytes.Equal(from, e.sponsor) {
		t.Fatalf("the bound sponsor signature recovered the sponsor as the envelope sender")
	}
}

func TestPaymasterSponsorBindingDigestDependsOnTheSenderAndTheFields(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	_, addrA := e.newSender(t)
	_, addrB := e.newSender(t)
	tx := e.fields()
	dA, err := tx.PaymasterSponsorDigest(addrA)
	if err != nil {
		t.Fatalf("digest A: %v", err)
	}
	dB, _ := tx.PaymasterSponsorDigest(addrB)
	if bytes.Equal(dA, dB) {
		t.Fatalf("digest does not depend on the sender")
	}
	plain, _ := tx.Hash()
	if bytes.Equal(dA, plain) {
		t.Fatalf("digest equals tx.Hash(): a sponsor signature could be an envelope signature")
	}
	other := e.fields()
	other.Value = big.NewInt(6)
	dOther, _ := other.PaymasterSponsorDigest(addrA)
	if bytes.Equal(dA, dOther) {
		t.Fatalf("digest does not depend on the fields")
	}
	if _, err := tx.PaymasterSponsorDigest(addrA[:19]); err == nil {
		t.Fatalf("a sender that is not 20 bytes must be refused")
	}
	// tx.Hash() itself did not change by adding the bound digest.
	again, _ := tx.Hash()
	if !bytes.Equal(plain, again) {
		t.Fatalf("tx.Hash() changed")
	}
}

// Through the real transfer path: the sponsor pays once for the sender it signed
// for, and the replay by a second sender is refused with the state untouched.
func TestPaymasterSponsorBindingEndToEnd(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	keyB, addrB := e.newSender(t)
	e.at(bindingHeight)
	if _, err := e.sp.Commit(bindingHeight); err != nil {
		t.Fatalf("commit: %v", err)
	}
	e.at(bindingHeight + 1)

	txA := boundTx(t, e, keyA, addrA)
	if err := e.sp.ApplyTransaction(txA); err != nil {
		t.Fatalf("sponsored transfer by the sender it was signed for: %v", err)
	}
	if got := captureAccountSnapshot(t, e.sp, addrA); got.nonce != 1 {
		t.Fatalf("sender A's transfer was not applied (nonce %d)", got.nonce)
	}

	txB := e.fields()
	signTransaction(t, txB, keyB)
	txB.PaymasterR, txB.PaymasterS, txB.PaymasterV = txA.PaymasterR, txA.PaymasterS, txA.PaymasterV
	rootBefore := e.sp.Trie.Hash()
	sponsorMid := captureAccountSnapshot(t, e.sp, e.sponsor)
	err := e.sp.ApplyTransaction(txB)
	if err == nil {
		t.Fatalf("the replay of one sponsorship by a second sender was applied")
	}
	if rootAfter := e.sp.Trie.Hash(); rootAfter != rootBefore {
		t.Fatalf("a refused replay changed the state")
	}
	if got := captureAccountSnapshot(t, e.sp, e.sponsor); !got.equal(sponsorMid) {
		t.Fatalf("the sponsor paid for the refused replay")
	}
	if got := captureAccountSnapshot(t, e.sp, addrB); got.nonce != 0 {
		t.Fatalf("sender B's nonce moved on a refused replay")
	}
}

func TestPaymasterSponsorBindingIsCarriedByCopy(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	dup, err := e.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if dup.paymasterSponsorBindingFromHeight != bindingHeight {
		t.Fatalf("Copy dropped the sponsor binding height: %d", dup.paymasterSponsorBindingFromHeight)
	}
}

func TestNodePaymasterSponsorBinding_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetPaymasterSponsorBinding(7)
	node.stateMu.Lock()
	gotH := node.state.paymasterSponsorBindingFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.paymasterSponsorBindingFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 7 {
		t.Fatalf("setter left the processor at %d, want 7", gotH)
	}
	if rebuiltH != 7 {
		t.Fatalf("a rebuilt processor lost the setting: %d, want 7", rebuiltH)
	}
}

// The malleable twin of a valid bound signature (s replaced by n-s with the
// recovery bit flipped) recovers the same sponsor, so only the low-s rule refuses it.
func TestPaymasterSponsorBindingRefusesTheHighSTwin(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	keyA, addrA := e.newSender(t)
	e.at(bindingHeight)
	tx := boundTx(t, e, keyA, addrA)
	if got := bindingStatus(t, e.sp, tx); got != SponsorshipStatusReady {
		t.Fatalf("the original must be ready, got %s", got)
	}
	curveN := ethcrypto.S256().Params().N
	tx.PaymasterS = new(big.Int).Sub(curveN, tx.PaymasterS)
	if tx.PaymasterV.Uint64() == 27 {
		tx.PaymasterV = big.NewInt(28)
	} else {
		tx.PaymasterV = big.NewInt(27)
	}
	if got := bindingStatus(t, e.sp, tx); got != SponsorshipStatusSignatureInvalid {
		t.Fatalf("a high-s twin must be refused, got %s", got)
	}
}

// The wire format an SDK signs, pinned independently of the implementation:
// sha256(tag || transaction digest || 20-byte sender).
func TestPaymasterSponsorBindingDigestKnownAnswer(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	_, addr := e.newSender(t)
	tx := e.fields()
	plain, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h := sha256.New()
	h.Write([]byte("NHB/paymaster-sponsor/v2"))
	h.Write(plain)
	h.Write(addr)
	want := h.Sum(nil)
	got, err := tx.PaymasterSponsorDigest(addr)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("digest = %x, want %x", got, want)
	}
}

// A transaction whose envelope signature cannot be recovered has no sender to
// bind to: the sponsorship is refused and the reason says why.
func TestPaymasterSponsorBindingNeedsARecoverableSender(t *testing.T) {
	e := newBindingEnv(t, bindingHeight)
	e.at(bindingHeight)
	tx := e.fields()
	tx.PaymasterR, tx.PaymasterS, tx.PaymasterV = big.NewInt(1), big.NewInt(1), big.NewInt(27)
	assessment, err := e.sp.EvaluateSponsorship(tx)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if assessment.Status != SponsorshipStatusSignatureInvalid || !strings.Contains(assessment.Reason, "sender") {
		t.Fatalf("want signature_invalid naming the sender, got %s / %q", assessment.Status, assessment.Reason)
	}
}
