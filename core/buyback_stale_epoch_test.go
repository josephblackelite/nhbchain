package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// This file covers the 2026-09-18 block-production halt: a
// TxTypeBuybackRefPrice whose epoch no longer matched the epoch the next
// block is evaluated in used to abort the ENTIRE block build on every
// proposal, on every validator, forever -- applyBuybackRefPrice returned a
// bare error that classifyProposalError could not recognize, so it fell
// through to proposalDispositionAbort, and the transaction was never pruned
// from the mempool either. Like the duplicate-ref-price incident documented
// on ErrBuybackRefPriceAlreadyRecorded, the failure needs a MIXED block (the
// poison transaction alongside anything else) or at least a real
// CreateBlock -- an AddTransaction-only test can never see it, because
// admission-time simulation runs against the height the tx was valid at.

// commitBlockOnBoth drives the same propose/validate/commit sequence the
// real two-validator topology does: the proposer commits its own block, the
// independent validator must re-derive the same state root from the block's
// transactions alone.
func commitBlockOnBoth(t *testing.T, proposer, validator *Node, block *types.Block) {
	t.Helper()
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatalf("proposer commit block %d: %v", block.Header.Height, err)
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected block %d: %v", block.Header.Height, err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatalf("validator commit block %d: %v", block.Header.Height, err)
	}
}

// newSignedBuybackRefPriceTx builds a TxTypeBuybackRefPrice carrying a valid
// 2-of-3 signature bundle for the given epoch, exactly the way
// Node.SubmitBuybackRefPrice does, but WITHOUT going through AddTransaction's
// admission-time simulation -- so a test can place a transaction in the
// mempool whose epoch is not (or is no longer) the epoch the chain is about
// to evaluate.
func newSignedBuybackRefPriceTx(t *testing.T, keys []*crypto.PrivateKey, epoch uint64) *types.Transaction {
	t.Helper()
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(1_800_000_000)
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epoch,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	payload, err := rlp.EncodeToBytes(buybackRefPricePayload{
		RateNum:    rateNum,
		RateDenom:  rateDenom,
		Epoch:      epoch,
		Timestamp:  ts,
		Signatures: [][]byte{signRefPrice(t, keys[0], rp), signRefPrice(t, keys[1], rp)},
	})
	if err != nil {
		t.Fatalf("encode ref price payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeBuybackRefPrice,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
}

func newSignedTransferTx(t *testing.T, senderKey *crypto.PrivateKey, nonce uint64) *types.Transaction {
	t.Helper()
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(1_000),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	return tx
}

// mempoolContainsType reports whether n's real mempool (not the in-flight
// view GetMempool hands out) still holds a transaction of the given type.
func mempoolContainsType(n *Node, txType types.TxType) bool {
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	for _, tx := range n.mempool {
		if tx != nil && tx.Type == txType {
			return true
		}
	}
	return false
}

func blockTxTypes(block *types.Block) []types.TxType {
	out := make([]types.TxType, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		out = append(out, tx.Type)
	}
	return out
}

// TestCreateBlockStaleEpochBuybackRefPriceIsPrunedNotAborting reproduces the
// incident end to end through the real CreateBlock/ValidateBlock paths.
//
// Timeline (epoch length 2, so heights 1-2 are epoch 1 and height 3 is epoch
// 2): a reference price for epoch 1 is submitted while height 2 -- the LAST
// block of epoch 1 -- is next, and is perfectly valid at that moment
// (admission simulation passes). Block 2 is then produced without it (the
// proposal was already built, or the proposer never received the
// transaction). The chain is now at height 2, so the next block, 3, lives
// in epoch 2 and the still-pending epoch-1 transaction can never apply
// again. Before the fix, every CreateBlock from here on failed with
// "submitted epoch 1 does not match the current open epoch 2" and the
// transaction was never dropped -- block production stopped for as long as
// it sat in the mempool.
func TestCreateBlockStaleEpochBuybackRefPriceIsPrunedNotAborting(t *testing.T) {
	proposer, validator, keys, senderKey := newBuybackConsensusHarness(t)

	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	epochNum, ok := proposer.CurrentBuybackEpoch()
	if !ok || epochNum != 1 {
		t.Fatalf("expected the next block (height 2) to be in epoch 1, got epoch %d (ok=%v)", epochNum, ok)
	}
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	ts := uint64(1_800_000_000)
	rp := &buyback.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Epoch:     epochNum,
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	sigs := [][]byte{signRefPrice(t, keys[0], rp), signRefPrice(t, keys[1], rp)}
	if _, err := proposer.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs); err != nil {
		t.Fatalf("submit ref price for the open epoch: %v", err)
	}

	// Block 2, the last block of epoch 1, is produced WITHOUT the ref price.
	block2, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block2)

	if got, _ := proposer.CurrentBuybackEpoch(); got != 2 {
		t.Fatalf("expected the next block (height 3) to be in epoch 2, got %d", got)
	}
	if !mempoolContainsType(proposer, types.TxTypeBuybackRefPrice) {
		t.Fatalf("test setup: the epoch-1 ref price must still be pending after block 2 skipped it")
	}

	// Admission now refuses a fresh submission for the closed epoch, with
	// the operator-facing message unchanged and the sentinel reachable
	// through the admission wrapping.
	_, staleErr := proposer.SubmitBuybackRefPrice(rateNum, rateDenom, epochNum, ts, sigs)
	if staleErr == nil {
		t.Fatalf("expected admission to refuse a ref price for the already-closed epoch")
	}
	if !errors.Is(staleErr, ErrBuybackRefPriceStaleEpoch) || errors.Is(staleErr, ErrBuybackRefPriceFutureEpoch) {
		t.Fatalf("expected a stale-epoch error, got %v", staleErr)
	}
	if want := "buybackRefPrice: submitted epoch 1 does not match the current open epoch 2"; !strings.Contains(staleErr.Error(), want) {
		t.Fatalf("operator-facing message changed: %q does not contain %q", staleErr.Error(), want)
	}
	if got := classifyProposalError(staleErr); got != proposalDispositionPrune {
		t.Fatalf("stale-epoch error classified %v, want prune", got)
	}

	transfer := newSignedTransferTx(t, senderKey, 0)
	if err := proposer.AddTransaction(transfer); err != nil {
		t.Fatalf("add transfer: %v", err)
	}
	pending := proposer.GetMempool()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending txs (stale ref price + transfer), got %d", len(pending))
	}

	block3, err := proposer.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal because of a stale-epoch ref price: %v", err)
	}
	types3 := blockTxTypes(block3)
	if len(types3) != 1 || types3[0] != types.TxTypeTransfer {
		t.Fatalf("expected block 3 to contain exactly the transfer, got tx types %v", types3)
	}
	if mempoolContainsType(proposer, types.TxTypeBuybackRefPrice) {
		t.Fatalf("the stale-epoch ref price can never apply again and must be pruned from the mempool")
	}
	commitBlockOnBoth(t, proposer, validator, block3)

	// The pruned transaction must not be offered again on the next attempt.
	next := proposer.GetMempool()
	if len(next) != 0 {
		t.Fatalf("expected an empty mempool after the transfer committed and the stale tx was pruned, got %d txs", len(next))
	}
	block4, err := proposer.CreateBlock(next)
	if err != nil {
		t.Fatalf("create block 4: %v", err)
	}
	if len(block4.Transactions) != 0 {
		t.Fatalf("expected an empty block 4, got %d txs", len(block4.Transactions))
	}
	commitBlockOnBoth(t, proposer, validator, block4)

	if rootP, rootV := proposer.state.CurrentRoot(), validator.state.CurrentRoot(); rootP != rootV {
		t.Fatalf("proposer and validator diverged (proposer=%s validator=%s)", rootP.Hex(), rootV.Hex())
	}
}

// TestCreateBlockFutureEpochBuybackRefPriceIsSkippedNotAborting covers the
// mirror case: a reference price for an epoch the chain has not reached yet
// (for example one relayed from a peer that is a block ahead) is NOT
// permanently dead -- it becomes valid the moment the chain enters that
// epoch -- so it must be excluded from the current block (which must still
// be produced, with the other transactions) but stay in the mempool and be
// applied once its epoch opens.
func TestCreateBlockFutureEpochBuybackRefPriceIsSkippedNotAborting(t *testing.T) {
	proposer, validator, keys, senderKey := newBuybackConsensusHarness(t)

	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	if got, _ := proposer.CurrentBuybackEpoch(); got != 1 {
		t.Fatalf("expected the next block (height 2) to be in epoch 1, got %d", got)
	}

	// Height 2 is still epoch 1, so a ref price for epoch 2 is a FUTURE one.
	// Injected straight into the mempool: AddTransaction's admission
	// simulation would (correctly) refuse it locally.
	futureTx := newSignedBuybackRefPriceTx(t, keys, 2)
	admissionErr := proposer.AddTransaction(futureTx)
	if admissionErr == nil {
		t.Fatalf("expected admission to refuse a ref price for a not-yet-open epoch")
	}
	if !errors.Is(admissionErr, ErrBuybackRefPriceFutureEpoch) || errors.Is(admissionErr, ErrBuybackRefPriceStaleEpoch) {
		t.Fatalf("expected a future-epoch error, got %v", admissionErr)
	}
	if want := "buybackRefPrice: submitted epoch 2 does not match the current open epoch 1"; !strings.Contains(admissionErr.Error(), want) {
		t.Fatalf("operator-facing message changed: %q does not contain %q", admissionErr.Error(), want)
	}
	if got := classifyProposalError(admissionErr); got != proposalDispositionSkip {
		t.Fatalf("future-epoch error classified %v, want skip", got)
	}
	proposer.mempoolMu.Lock()
	proposer.mempool = append(proposer.mempool, futureTx)
	proposer.mempoolMu.Unlock()

	transfer := newSignedTransferTx(t, senderKey, 0)
	if err := proposer.AddTransaction(transfer); err != nil {
		t.Fatalf("add transfer: %v", err)
	}
	pending := proposer.GetMempool()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending txs (future ref price + transfer), got %d", len(pending))
	}

	block2, err := proposer.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort the whole proposal because of a future-epoch ref price: %v", err)
	}
	types2 := blockTxTypes(block2)
	if len(types2) != 1 || types2[0] != types.TxTypeTransfer {
		t.Fatalf("expected block 2 to contain exactly the transfer, got tx types %v", types2)
	}
	if !mempoolContainsType(proposer, types.TxTypeBuybackRefPrice) {
		t.Fatalf("the future-epoch ref price is not dead and must remain in the mempool")
	}
	commitBlockOnBoth(t, proposer, validator, block2)

	if status, err := proposer.BuybackRefPriceStatusForEpoch(2); err != nil || status.HasRefPrice {
		t.Fatalf("epoch 2 must not have a reference price yet (status=%+v err=%v)", status, err)
	}

	// The chain is now at height 2, so the next block (3) opens epoch 2 and
	// the previously-skipped transaction must be offered again and applied.
	if got, _ := proposer.CurrentBuybackEpoch(); got != 2 {
		t.Fatalf("expected the next block (height 3) to be in epoch 2, got %d", got)
	}
	pending2 := proposer.GetMempool()
	if len(pending2) != 1 {
		t.Fatalf("expected the skipped ref price to be offered again, got %d pending txs", len(pending2))
	}
	block3, err := proposer.CreateBlock(pending2)
	if err != nil {
		t.Fatalf("create block 3: %v", err)
	}
	types3 := blockTxTypes(block3)
	if len(types3) != 1 || types3[0] != types.TxTypeBuybackRefPrice {
		t.Fatalf("expected block 3 to contain the now-valid ref price, got tx types %v", types3)
	}
	commitBlockOnBoth(t, proposer, validator, block3)

	status, err := proposer.BuybackRefPriceStatusForEpoch(2)
	if err != nil || !status.HasRefPrice {
		t.Fatalf("expected the epoch-2 reference price to be recorded (status=%+v err=%v)", status, err)
	}
	if mempoolContainsType(proposer, types.TxTypeBuybackRefPrice) {
		t.Fatalf("the applied ref price must have left the mempool")
	}
	if rootP, rootV := proposer.state.CurrentRoot(), validator.state.CurrentRoot(); rootP != rootV {
		t.Fatalf("proposer and validator diverged (proposer=%s validator=%s)", rootP.Hex(), rootV.Hex())
	}
}
