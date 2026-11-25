package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// PL-EXT-87 (a bug-bounty report): a transfer that carries a LEGACY paymaster signature (a
// signature over tx.Hash(), the rule while PaymasterSponsorBindingActivationHeight is 0) is said to
// be admitted by the proposer and to pass its simulation, while the peer rejects every proposal that
// contains it with "state root mismatch", so nothing lands while it sits in the mempool.
//
// This drives the real CreateBlock (proposer) and ValidateBlock (receiving validator) on two
// independent nodes from one genesis, the same way the buyback incident was reproduced
// (buyback_consensus_test.go), with the shipped sponsorship settings (binding off).

type sponsoredTwoNode struct {
	proposer, validator *Node
	alice, bob          *crypto.PrivateKey // sponsor and sender
	carol               []byte
	lastBlock           *types.Block
}

func newSponsoredTwoNode(t *testing.T) *sponsoredTwoNode {
	t.Helper()
	mk := func() *crypto.PrivateKey {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		return key
	}
	alice, bob, carol, admin := mk(), mk(), mk(), mk()
	gvA, gvB := mk(), mk()
	thousand := "1000000000000000000000"
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: gvA.PubKey().Address().String(), Power: 11440},
			{Address: gvB.PubKey().Address().String(), Power: 11336},
		},
		Alloc: map[string]map[string]string{
			admin.PubKey().Address().String(): {"NHB": "0", "ZNHB": znhbExpectedTotalSupplyWei.String()},
			alice.PubKey().Address().String(): {"NHB": thousand, "ZNHB": "0"},
			bob.PubKey().Address().String():   {"NHB": thousand, "ZNHB": "0"},
		},
		AdminWallet: admin.PubKey().Address().String(),
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}
	path := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}
	fixed := time.Unix(1_800_000_000, 0).UTC()
	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		node, err := NewNode(db, mk(), path, false, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		node.SetTimeSource(func() time.Time { return fixed })
		return node
	}
	h := &sponsoredTwoNode{proposer: build(), validator: build(), alice: alice, bob: bob, carol: carol.PubKey().Address().Bytes()}
	// Height 1, an empty block, on both nodes.
	block1, err := h.proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	if err := h.proposer.CommitBlock(block1); err != nil {
		t.Fatalf("proposer commit block 1: %v", err)
	}
	if err := h.validator.ValidateBlock(block1); err != nil {
		t.Fatalf("validator rejects block 1: %v", err)
	}
	if err := h.validator.CommitBlock(block1); err != nil {
		t.Fatalf("validator commit block 1: %v", err)
	}
	return h
}

// legacySponsored builds a transfer of 100 NHB from bob to `to`, sponsored by alice, with the legacy
// signatures: bob's envelope signature and alice's sponsor signature are both over tx.Hash().
func (h *sponsoredTwoNode) legacySponsored(t *testing.T, to []byte) *types.Transaction {
	t.Helper()
	value, _ := new(big.Int).SetString("100000000000000000000", 10)
	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        append([]byte(nil), to...),
		Value:     value,
		GasLimit:  21_000,
		GasPrice:  big.NewInt(1),
		Paymaster: append([]byte(nil), h.alice.PubKey().Address().Bytes()...),
	}
	signTransaction(t, tx, h.bob)
	signPaymaster(t, tx, h.alice)
	return tx
}

// proposeAndValidate admits txs to the proposer's mempool, builds a block from the mempool and has the
// other node validate it. It returns what happened without failing, so the callers can state it.
func (h *sponsoredTwoNode) proposeAndValidate(t *testing.T, txs ...*types.Transaction) (admitted int, blockTxs int, validateErr error) {
	t.Helper()
	for i, tx := range txs {
		if err := h.proposer.AddTransaction(tx); err != nil {
			t.Logf("tx %d refused at the proposer's mempool: %v", i, err)
			continue
		}
		admitted++
	}
	block, err := h.proposer.CreateBlock(h.proposer.GetMempool())
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	blockTxs = len(block.Transactions)
	h.lastBlock = block
	return admitted, blockTxs, h.validator.ValidateBlock(block)
}

// confusionCopy is the transaction of PL-R5T-22 part 1: the same fields as the sponsored transfer, with the
// sponsor's signature (a signature over tx.Hash()) placed in the envelope fields, so that it names the
// sponsor as the sender.
func confusionCopy(orig *types.Transaction) *types.Transaction {
	return &types.Transaction{
		ChainID: orig.ChainID, Type: orig.Type, Nonce: orig.Nonce, To: append([]byte(nil), orig.To...),
		Value: new(big.Int).Set(orig.Value), GasLimit: orig.GasLimit, GasPrice: new(big.Int).Set(orig.GasPrice),
		Paymaster: append([]byte(nil), orig.Paymaster...),
		R:         orig.PaymasterR, S: orig.PaymasterS, V: orig.PaymasterV,
		PaymasterR: orig.PaymasterR, PaymasterS: orig.PaymasterS, PaymasterV: orig.PaymasterV,
	}
}

func (h *sponsoredTwoNode) nhb(t *testing.T, node *Node, addr []byte) *big.Int {
	t.Helper()
	acc, err := node.GetAccount(addr)
	if err != nil || acc == nil || acc.BalanceNHB == nil {
		t.Fatalf("read account: %v", err)
	}
	return new(big.Int).Set(acc.BalanceNHB)
}

func TestLegacySponsoredTransferProposerAndValidatorAgree(t *testing.T) {
	t.Run("sender sends to a third party", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		admitted, blockTxs, err := h.proposeAndValidate(t, h.legacySponsored(t, h.carol))
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if err != nil {
			t.Fatalf("the validator rejected the proposer's block: %v", err)
		}
	})

	t.Run("sender sends to himself (the reporter's case)", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		bobAddr := h.bob.PubKey().Address().Bytes()
		admitted, blockTxs, err := h.proposeAndValidate(t, h.legacySponsored(t, bobAddr))
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if err != nil {
			t.Fatalf("the validator rejected the proposer's block: %v", err)
		}
	})

	t.Run("copy whose envelope signature is the sponsor's signature (the sponsor is the sender)", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		orig := h.legacySponsored(t, h.bob.PubKey().Address().Bytes())
		copyTx := &types.Transaction{
			ChainID: orig.ChainID, Type: orig.Type, Nonce: orig.Nonce, To: append([]byte(nil), orig.To...),
			Value: new(big.Int).Set(orig.Value), GasLimit: orig.GasLimit, GasPrice: new(big.Int).Set(orig.GasPrice),
			Paymaster: append([]byte(nil), orig.Paymaster...),
			R:         orig.PaymasterR, S: orig.PaymasterS, V: orig.PaymasterV,
			PaymasterR: orig.PaymasterR, PaymasterS: orig.PaymasterS, PaymasterV: orig.PaymasterV,
		}
		admitted, blockTxs, err := h.proposeAndValidate(t, copyTx)
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if err != nil {
			t.Fatalf("the validator rejected the proposer's block: %v", err)
		}
	})

	// A characterisation, not a claim about the live validators: when the two nodes' sponsorship caps
	// differ (they are per-node settings, set in each node's own configuration), a sponsored transfer
	// that the proposer admits is one the other node refuses. The reporter's lab may have been such a
	// pair; compare the two live validators' [Paymaster] sections before ruling that in or out.
	t.Run("two nodes with different sponsorship caps", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		h.validator.SetPaymasterLimits(PaymasterLimits{GlobalDailyCapWei: big.NewInt(1)})
		admitted, blockTxs, err := h.proposeAndValidate(t, h.legacySponsored(t, h.carol))
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if err == nil {
			t.Fatalf("expected the node with the lower cap to refuse the proposal; the characterisation no longer holds")
		}
	})

	t.Run("both together in one block", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		orig := h.legacySponsored(t, h.bob.PubKey().Address().Bytes())
		copyTx := &types.Transaction{
			ChainID: orig.ChainID, Type: orig.Type, Nonce: orig.Nonce, To: append([]byte(nil), orig.To...),
			Value: new(big.Int).Set(orig.Value), GasLimit: orig.GasLimit, GasPrice: new(big.Int).Set(orig.GasPrice),
			Paymaster: append([]byte(nil), orig.Paymaster...),
			R:         orig.PaymasterR, S: orig.PaymasterS, V: orig.PaymasterV,
			PaymasterR: orig.PaymasterR, PaymasterS: orig.PaymasterS, PaymasterV: orig.PaymasterV,
		}
		admitted, blockTxs, err := h.proposeAndValidate(t, orig, copyTx)
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if err != nil {
			t.Fatalf("the validator rejected the proposer's block: %v", err)
		}
	})
}

// PL-R5T-22 part 1 executed through the real block path (ledger PL-R5T-22, a bug-bounty
// report): with the shipped setting (the sponsor binding off) the sponsor's signature over tx.Hash() is
// also a valid ENVELOPE signature for the sponsor, so a copy of a sponsored transfer makes the sponsor
// the sender and moves the sponsor's own NHB. With the binding on at the next height the copy is refused.
func TestSponsorSignatureConfusionThroughRealBlocks(t *testing.T) {
	hundred, _ := new(big.Int).SetString("100000000000000000000", 10)

	t.Run("binding off (shipped): the sponsor's balance is debited", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		aliceAddr := h.alice.PubKey().Address().Bytes()
		bobAddr := h.bob.PubKey().Address().Bytes()
		aliceBefore, bobBefore := h.nhb(t, h.proposer, aliceAddr), h.nhb(t, h.proposer, bobAddr)
		orig := h.legacySponsored(t, bobAddr)
		admitted, blockTxs, err := h.proposeAndValidate(t, confusionCopy(orig))
		if err != nil || admitted != 1 || blockTxs != 1 {
			t.Fatalf("admitted=%d block transactions=%d validator error=%v: expected the copy to be admitted, proposed and validated", admitted, blockTxs, err)
		}
		if err := h.proposer.CommitBlock(h.lastBlock); err != nil {
			t.Fatalf("proposer commit: %v", err)
		}
		if err := h.validator.CommitBlock(h.lastBlock); err != nil {
			t.Fatalf("validator commit: %v", err)
		}
		for name, node := range map[string]*Node{"proposer": h.proposer, "validator": h.validator} {
			aliceAfter, bobAfter := h.nhb(t, node, aliceAddr), h.nhb(t, node, bobAddr)
			lost := new(big.Int).Sub(aliceBefore, aliceAfter)
			gained := new(big.Int).Sub(bobAfter, bobBefore)
			t.Logf("%s: sponsor lost %s wei, the sender-victim account Bob gained %s wei", name, lost, gained)
			if lost.Cmp(hundred) < 0 {
				t.Fatalf("%s: the sponsor lost %s, expected at least the transferred 100 NHB", name, lost)
			}
			if gained.Cmp(hundred) != 0 {
				t.Fatalf("%s: Bob gained %s, expected exactly 100 NHB", name, gained)
			}
		}
	})

	t.Run("binding on from the next height: the copy is refused and nothing moves", func(t *testing.T) {
		h := newSponsoredTwoNode(t)
		h.proposer.SetPaymasterSponsorBinding(2)
		h.validator.SetPaymasterSponsorBinding(2)
		aliceAddr := h.alice.PubKey().Address().Bytes()
		bobAddr := h.bob.PubKey().Address().Bytes()
		aliceBefore := h.nhb(t, h.proposer, aliceAddr)
		orig := h.legacySponsored(t, bobAddr)
		admitted, blockTxs, err := h.proposeAndValidate(t, confusionCopy(orig))
		t.Logf("admitted=%d block transactions=%d validator error=%v", admitted, blockTxs, err)
		if admitted != 0 || blockTxs != 0 {
			t.Fatalf("with the binding on the copy must not be admitted (admitted=%d, in block=%d)", admitted, blockTxs)
		}
		if after := h.nhb(t, h.proposer, aliceAddr); after.Cmp(aliceBefore) != 0 {
			t.Fatalf("the sponsor's balance moved from %s to %s", aliceBefore, after)
		}
	})
}
