package core

import (
	"errors"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"
)

func markReconciledTx(t *testing.T, adminKey *crypto.PrivateKey, ids ...string) *types.Transaction {
	t.Helper()
	signature, err := ethcrypto.Sign(SwapMarkReconciledSigningHash(ids), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reconciliation: %v", err)
	}
	payload, err := encodeSwapMarkReconciledTransaction(ids, signature)
	if err != nil {
		t.Fatalf("encode reconciliation: %v", err)
	}
	return &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSwapMarkReconciled, Data: payload, GasLimit: 0, GasPrice: big.NewInt(0)}
}

func voucherStatusOnNode(t *testing.T, node *Node, id string) string {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	record, ok, err := swap.NewLedger(nhbstate.NewManager(node.state.Trie)).Get(id)
	if err != nil || !ok {
		t.Fatalf("voucher %s: ok=%v err=%v", id, ok, err)
	}
	return record.Status
}

// TestSwapRecordBurnNamingARefusedVoucherRecordsNothing covers the burn receipt
// call, which stores a receipt and reconciles the vouchers it names. A receipt
// naming a reversed or unknown voucher is refused before anything is written, so
// the same receipt id can be sent again once the list is corrected; before, the
// receipt was stored first and the refusal came after it, leaving the id taken.
func TestSwapRecordBurnNamingARefusedVoucherRecordsNothing(t *testing.T) {
	node := newTestNode(t)
	seed := func(id string) {
		t.Helper()
		if err := node.WithState(func(m *nhbstate.Manager) error {
			return swap.NewLedger(m).Put(&swap.VoucherRecord{
				Provider:        "test",
				ProviderTxID:    id,
				FiatCurrency:    "USD",
				FiatAmount:      "1",
				Rate:            "1",
				Token:           "ZNHB",
				MintAmountWei:   big.NewInt(1),
				QuoteTimestamp:  1,
				OracleSource:    "manual",
				PriceProofID:    "proof-" + id,
				OracleFeeders:   []string{"manual"},
				MinterSignature: "sig",
			})
		}); err != nil {
			t.Fatalf("seed voucher %s: %v", id, err)
		}
	}
	seed("np-live")
	seed("np-reversed")
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return swap.NewLedger(m).MarkReversed("np-reversed")
	}); err != nil {
		t.Fatalf("mark reversed: %v", err)
	}
	burnRecorded := func() bool {
		t.Helper()
		found := false
		if err := node.WithState(func(m *nhbstate.Manager) error {
			_, ok, err := swap.NewBurnLedger(m).Get("burn-1")
			found = ok
			return err
		}); err != nil {
			t.Fatalf("read burn ledger: %v", err)
		}
		return found
	}
	receipt := func(voucherIDs ...string) *swap.BurnReceipt {
		return &swap.BurnReceipt{ReceiptID: "burn-1", ProviderTxID: "np-live", Token: "ZNHB", AmountWei: big.NewInt(1), VoucherIDs: voucherIDs, ObservedAt: 2_100_000_000}
	}

	for _, tc := range []struct {
		name string
		ids  []string
		want error
	}{
		{"reversed voucher", []string{"np-live", "np-reversed"}, swap.ErrVoucherNotReconcilable},
		{"unknown voucher", []string{"np-live", "np-unknown"}, swap.ErrVoucherNotFound},
	} {
		if err := node.SwapRecordBurn(receipt(tc.ids...)); !errors.Is(err, tc.want) {
			t.Fatalf("receipt naming a %s: got %v, want %v", tc.name, err, tc.want)
		}
		if burnRecorded() {
			t.Fatalf("a receipt naming a %s was stored before it was refused", tc.name)
		}
		if got := voucherStatusOnNode(t, node, "np-live"); got != swap.VoucherStatusMinted {
			t.Fatalf("voucher listed beside a %s is %q, want minted", tc.name, got)
		}
		for _, evt := range node.Events() {
			if evt.Type == events.TypeSwapBurnRecorded || evt.Type == events.TypeSwapTreasuryReconciled {
				t.Fatalf("a refused receipt left a %s event", evt.Type)
			}
		}
	}

	if err := node.SwapRecordBurn(receipt("np-live")); err != nil {
		t.Fatalf("the same receipt id with the list corrected: %v", err)
	}
	if !burnRecorded() {
		t.Fatalf("the corrected receipt was not stored")
	}
	if got := voucherStatusOnNode(t, node, "np-live"); got != swap.VoucherStatusReconciled {
		t.Fatalf("voucher is %q after its receipt was recorded, want reconciled", got)
	}
	if got := voucherStatusOnNode(t, node, "np-reversed"); got != swap.VoucherStatusReversed {
		t.Fatalf("reversed voucher is %q, want it untouched", got)
	}
}

// TestSwapMarkReconciledRefusesAReversedOrUnknownVoucher applies a signed
// reconciliation batch through the state processor: a reversed voucher stays
// reversed, an id the ledger does not know is an error rather than a silent
// skip, and a refused batch leaves the vouchers that were listed with it
// untouched.
func TestSwapMarkReconciledRefusesAReversedOrUnknownVoucher(t *testing.T) {
	node := buildSwapAdminTestNode(t, writeSwapAdminGenesis(t))

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	seedSwapAdminVoucher(t, node, adminAddr, recipient, "voucher-live", big.NewInt(500))
	seedSwapAdminVoucher(t, node, adminAddr, recipient, "voucher-reversed", big.NewInt(500))
	node.stateMu.Lock()
	if err := swap.NewLedger(nhbstate.NewManager(node.state.Trie)).MarkReversed("voucher-reversed"); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("mark reversed: %v", err)
	}
	node.stateMu.Unlock()

	apply := func(tx *types.Transaction) error {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		return node.state.ApplyTransaction(tx)
	}

	if err := apply(markReconciledTx(t, adminKey, "voucher-live", "voucher-reversed")); !errors.Is(err, swap.ErrVoucherNotReconcilable) {
		t.Fatalf("batch naming a reversed voucher: got %v, want swap.ErrVoucherNotReconcilable", err)
	}
	if got := voucherStatusOnNode(t, node, "voucher-reversed"); got != swap.VoucherStatusReversed {
		t.Fatalf("reversed voucher ended up %q after a reconciliation batch", got)
	}
	if got := voucherStatusOnNode(t, node, "voucher-live"); got != swap.VoucherStatusMinted {
		t.Fatalf("voucher listed beside a reversed one is %q, want minted (nothing written on a refused batch)", got)
	}

	if err := apply(markReconciledTx(t, adminKey, "voucher-live", "voucher-never-minted")); !errors.Is(err, swap.ErrVoucherNotFound) {
		t.Fatalf("batch naming an unknown voucher: got %v, want swap.ErrVoucherNotFound", err)
	}
	if got := voucherStatusOnNode(t, node, "voucher-live"); got != swap.VoucherStatusMinted {
		t.Fatalf("voucher listed beside an unknown one is %q, want minted", got)
	}

	if err := apply(markReconciledTx(t, adminKey, "voucher-live")); err != nil {
		t.Fatalf("reconciling a minted voucher: %v", err)
	}
	if got := voucherStatusOnNode(t, node, "voucher-live"); got != swap.VoucherStatusReconciled {
		t.Fatalf("minted voucher is %q after reconciliation, want reconciled", got)
	}
	if err := apply(markReconciledTx(t, adminKey, "voucher-live")); err != nil {
		t.Fatalf("resubmitting a reconciliation for an already reconciled voucher: %v", err)
	}
}
