package core

import (
	"errors"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// A mint voucher carries an invoice identifier and the chain executes each identifier once. A second
// submission of the same identifier is refused with ErrMintInvoiceUsed in two different situations: the
// first mint has been executed in a committed block, or the first mint is only waiting in this node's
// mempool. A submitter that gets that refusal cannot tell which of the two it was. MintInvoiceSettled
// answers the question the refusal leaves open: it reads the committed state only.

// submitInvoiceMint signs and submits one mint voucher for invoiceID and returns the minter key used.
func submitInvoiceMint(t *testing.T, node *Node, minterKey *crypto.PrivateKey, invoiceID string, expiry time.Time) MintVoucher {
	t.Helper()
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	voucher := MintVoucher{
		InvoiceID: invoiceID,
		Recipient: recipientKey.PubKey().Address().String(),
		Token:     "NHB",
		Amount:    "125",
		ChainID:   MintChainID,
		Expiry:    expiry.Unix(),
	}
	if _, err := node.MintWithSignature(&voucher, signVoucher(t, minterKey, voucher)); err != nil {
		t.Fatalf("mint submission for %q: %v", invoiceID, err)
	}
	return voucher
}

func newInvoiceTestNode(t *testing.T) (*Node, *crypto.PrivateKey) {
	t.Helper()
	node := newTestNode(t)
	minterKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("minter key: %v", err)
	}
	assignRole(t, node, "MINTER_NHB", toAddress(minterKey))
	return node, minterKey
}

func commitWaitingTransactions(t *testing.T, node *Node) {
	t.Helper()
	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
}

func mustReadInvoice(t *testing.T, node *Node, invoiceID string) MintInvoiceStatus {
	t.Helper()
	status, err := node.MintInvoiceSettled(invoiceID)
	if err != nil {
		t.Fatalf("read invoice %q: %v", invoiceID, err)
	}
	return status
}

func TestMintInvoiceSettledIsFalseForAnInvoiceTheChainNeverSaw(t *testing.T) {
	node, _ := newInvoiceTestNode(t)

	status := mustReadInvoice(t, node, "inv-never-seen")
	if status.Settled {
		t.Fatalf("an invoice no mint ever named reads settled")
	}
	if status.InvoiceID != "inv-never-seen" {
		t.Fatalf("the answer names invoice %q, want the one asked about", status.InvoiceID)
	}
	if want := node.chain.GetHeight() + 1; status.Height != want {
		t.Fatalf("height %d, want the next block's height %d", status.Height, want)
	}
}

// The case the read exists for: the mint was accepted and is waiting, a second submission of the same
// identifier is refused as "already settled", and the read still says it is not settled.
func TestMintInvoiceSettledIsFalseWhileTheMintIsOnlyWaiting(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	voucher := submitInvoiceMint(t, node, minterKey, "inv-waiting", time.Now().Add(time.Hour))
	if got := len(node.mempool); got != 1 {
		t.Fatalf("expected the mint to be waiting in the mempool, got %d transactions", got)
	}

	if _, err := node.MintWithSignature(&voucher, signVoucher(t, minterKey, voucher)); !errors.Is(err, ErrMintInvoiceUsed) {
		t.Fatalf("a second submission while the first waits: got %v, want ErrMintInvoiceUsed", err)
	}
	if status := mustReadInvoice(t, node, "inv-waiting"); status.Settled {
		t.Fatalf("a mint that is only waiting in the mempool reads settled")
	}
}

// A block that was built with the mint in it, and not committed, has settled nothing.
func TestMintInvoiceSettledIsFalseForABlockThatWasBuiltAndNotCommitted(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "inv-proposed", time.Now().Add(time.Hour))

	if _, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...)); err != nil {
		t.Fatalf("create block: %v", err)
	}
	if status := mustReadInvoice(t, node, "inv-proposed"); status.Settled {
		t.Fatalf("a mint in a block that was never committed reads settled")
	}
}

func TestMintInvoiceSettledIsTrueOnceTheMintIsInACommittedBlock(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	voucher := submitInvoiceMint(t, node, minterKey, "inv-landed", time.Now().Add(time.Hour))
	before := mustReadInvoice(t, node, "inv-landed")
	if before.Settled {
		t.Fatalf("settled before any block")
	}

	commitWaitingTransactions(t, node)

	after := mustReadInvoice(t, node, "inv-landed")
	if !after.Settled {
		t.Fatalf("the mint is in a committed block and the invoice does not read settled")
	}
	if after.Height != before.Height+1 {
		t.Fatalf("height went from %d to %d over one block, want one more", before.Height, after.Height)
	}
	if want := node.chain.GetHeight() + 1; after.Height != want {
		t.Fatalf("height %d, want the next block's height %d", after.Height, want)
	}
	// The refusal a submitter gets now is the same error as while the mint was waiting: only the read tells them apart.
	if _, err := node.MintWithSignature(&voucher, signVoucher(t, minterKey, voucher)); !errors.Is(err, ErrMintInvoiceUsed) {
		t.Fatalf("a second submission after the first landed: got %v, want ErrMintInvoiceUsed", err)
	}
}

// Other invoices are not settled by this one: the identifier is matched whole and with its case.
func TestMintInvoiceSettledMatchesTheWholeIdentifier(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "inv-exact", time.Now().Add(time.Hour))
	commitWaitingTransactions(t, node)

	for _, other := range []string{"inv-exac", "inv-exact2", "INV-EXACT", "inv", "xinv-exact", "inv-exact/", "inv exact"} {
		if status := mustReadInvoice(t, node, other); status.Settled {
			t.Fatalf("invoice %q reads settled because %q is", other, "inv-exact")
		}
	}
	if status := mustReadInvoice(t, node, "inv-exact"); !status.Settled {
		t.Fatalf("the invoice itself no longer reads settled")
	}
}

// The mint trims the identifier before it records it; the read trims the same way and says which identifier it looked up.
func TestMintInvoiceSettledTrimsTheIdentifierAsTheMintDoes(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "  inv-trim \t", time.Now().Add(time.Hour))
	commitWaitingTransactions(t, node)

	for _, asked := range []string{"inv-trim", " inv-trim", "inv-trim\n", "\t inv-trim  "} {
		status := mustReadInvoice(t, node, asked)
		if !status.Settled {
			t.Fatalf("asked as %q: not settled", asked)
		}
		if status.InvoiceID != "inv-trim" {
			t.Fatalf("asked as %q: the answer names %q, want the trimmed identifier", asked, status.InvoiceID)
		}
	}
}

func TestMintInvoiceSettledRefusesAnEmptyIdentifier(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "inv-some", time.Now().Add(time.Hour))
	commitWaitingTransactions(t, node)

	for _, empty := range []string{"", " ", "\t\n  "} {
		status, err := node.MintInvoiceSettled(empty)
		if !errors.Is(err, ErrMintInvoiceIDRequired) {
			t.Fatalf("identifier %q: got error %v, want ErrMintInvoiceIDRequired", empty, err)
		}
		if status.Settled {
			t.Fatalf("identifier %q: a refused read still says settled", empty)
		}
	}
}

// A mint that the chain dropped (its voucher expired while it waited) has settled nothing, so a submitter may try again.
func TestMintInvoiceSettledIsFalseForAMintThatWasDropped(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	current := time.Now().UTC().Truncate(time.Second)
	node.SetTimeSource(func() time.Time { return current })
	defer node.SetTimeSource(nil)

	submitInvoiceMint(t, node, minterKey, "inv-dropped", current.Add(2*time.Second))
	current = current.Add(5 * time.Second)
	if proposed := node.GetMempool(); len(proposed) != 0 {
		t.Fatalf("expected the expired mint to be dropped, %d still proposed", len(proposed))
	}
	commitWaitingTransactions(t, node)

	if status := mustReadInvoice(t, node, "inv-dropped"); status.Settled {
		t.Fatalf("a mint that was dropped reads settled")
	}
}

// The read changes nothing: the state root and the mempool are what they were.
func TestMintInvoiceSettledChangesNothing(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "inv-read-a", time.Now().Add(time.Hour))
	commitWaitingTransactions(t, node)
	submitInvoiceMint(t, node, minterKey, "inv-read-b", time.Now().Add(time.Hour))

	rootBefore := node.state.Trie.Hash()
	heightBefore := node.chain.GetHeight()
	waitingBefore := len(node.mempool)

	for _, id := range []string{"inv-read-a", "inv-read-b", "inv-read-c", ""} {
		_, _ = node.MintInvoiceSettled(id)
	}

	if got := node.state.Trie.Hash(); got != rootBefore {
		t.Fatalf("the state root changed from %x to %x", rootBefore, got)
	}
	if got := node.chain.GetHeight(); got != heightBefore {
		t.Fatalf("the chain height changed from %d to %d", heightBefore, got)
	}
	if got := len(node.mempool); got != waitingBefore {
		t.Fatalf("the mempool changed from %d to %d transactions", waitingBefore, got)
	}
}

// When the state cannot be read the answer is an error, never "not settled": a caller that took a failed
// read for "not settled" would treat a landed mint as one it may submit again.
func TestMintInvoiceSettledSaysSoWhenTheStateCannotBeRead(t *testing.T) {
	node, minterKey := newInvoiceTestNode(t)
	submitInvoiceMint(t, node, minterKey, "inv-unreadable", time.Now().Add(time.Hour))
	commitWaitingTransactions(t, node)

	node.stateMu.Lock()
	saved := node.state
	node.state = nil
	node.stateMu.Unlock()
	defer func() {
		node.stateMu.Lock()
		node.state = saved
		node.stateMu.Unlock()
	}()

	status, err := node.MintInvoiceSettled("inv-unreadable")
	if err == nil {
		t.Fatalf("a read of a state that is not there answered %+v", status)
	}
	if status != (MintInvoiceStatus{}) {
		t.Fatalf("a failed read still carries an answer: %+v", status)
	}
}

func TestMintInvoiceSettledOnAMissingNode(t *testing.T) {
	var node *Node
	if _, err := node.MintInvoiceSettled("inv-x"); err == nil {
		t.Fatalf("a nil node answered")
	}
}
