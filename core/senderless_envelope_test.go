package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

// senderlessEnvelopeMessage is the stable part of the admission error for a
// sender-less transaction whose envelope is not the canonical one.
const senderlessEnvelopeMessage = "sender-less transaction envelope"

// senderlessTestRefPriceTx builds a canonical (the shape Node.SubmitLendingRefPrice
// produces) TxTypeLendingRefPrice transaction carrying a valid signature
// bundle for the given timestamp, without submitting it anywhere.
func senderlessTestRefPriceTx(t *testing.T, key *crypto.PrivateKey, ts uint64) *types.Transaction {
	t.Helper()
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(rateNum, rateDenom),
		Timestamp: time.Unix(int64(ts), 0).UTC(),
	}
	payload, err := rlp.EncodeToBytes(lendingRefPricePayload{
		RateNum:    rateNum,
		RateDenom:  rateDenom,
		Timestamp:  ts,
		Signatures: [][]byte{signLendingRefPrice(t, rp, key)},
	})
	if err != nil {
		t.Fatalf("encode ref price payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingRefPrice,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
}

func senderlessMempoolLen(n *Node) int {
	n.mempoolMu.Lock()
	defer n.mempoolMu.Unlock()
	return len(n.mempool)
}

// senderlessReviewedTypes lists every transaction type that carries no
// recoverable sender. A new entry in types.RequiresSignature's exemption list
// must be added here on purpose: doing so is the moment to decide whether the
// type may use the intent fields of its envelope (senderlessEnvelopeCarriesIntent)
// and whether it needs a per-type pending bound (senderlessTypePendingLimit).
var senderlessReviewedTypes = map[types.TxType]struct{}{
	types.TxTypeMint:               {},
	types.TxTypeSwapVoucherMint:    {},
	types.TxTypeBuybackRefPrice:    {},
	types.TxTypeLendingRefPrice:    {},
	types.TxTypeSwapVoucherReverse: {},
	types.TxTypeSwapMarkReconciled: {},
}

func TestSenderlessTypesAreAllReviewed(t *testing.T) {
	seen := make(map[types.TxType]struct{})
	for _, typ := range types.AllTxTypes() {
		if types.RequiresSignature(typ) {
			continue
		}
		seen[typ] = struct{}{}
		if _, ok := senderlessReviewedTypes[typ]; !ok {
			t.Errorf("transaction type 0x%02X (%s) carries no sender but is not in senderlessReviewedTypes: decide its envelope rule and pending bound, then add it", byte(typ), types.TxTypeName(typ))
		}
	}
	for typ := range senderlessReviewedTypes {
		if _, ok := seen[typ]; !ok {
			t.Errorf("senderlessReviewedTypes lists 0x%02X (%s), which is no longer sender-less", byte(typ), types.TxTypeName(typ))
		}
	}
}

func TestSenderlessAdmissionAcceptsCanonicalEnvelope(t *testing.T) {
	proposer, _, key := newLendingRefPriceConsensusHarness(t)

	first := senderlessTestRefPriceTx(t, key, 1_800_000_000)
	if err := proposer.AddTransaction(first); err != nil {
		t.Fatalf("canonical envelope must be admitted: %v", err)
	}
	// The node's own submitter builds the same envelope and must keep working.
	rp := &lendingoracle.ReferencePrice{
		Rate:      new(big.Rat).SetFrac(big.NewInt(7), big.NewInt(100)),
		Timestamp: time.Unix(1_800_000_001, 0).UTC(),
	}
	sig := signLendingRefPrice(t, rp, key)
	if _, err := proposer.SubmitLendingRefPrice(big.NewInt(7), big.NewInt(100), 1_800_000_001, [][]byte{sig}); err != nil {
		t.Fatalf("SubmitLendingRefPrice must keep working: %v", err)
	}
	if got := senderlessMempoolLen(proposer); got != 2 {
		t.Fatalf("expected both distinct submissions pending, got %d", got)
	}
}

func TestSenderlessAdmissionRejectsNonCanonicalEnvelope(t *testing.T) {
	proposer, _, key := newLendingRefPriceConsensusHarness(t)
	one := big.NewInt(1)
	addr := bytes.Repeat([]byte{0x11}, 20)

	cases := []struct {
		name   string
		mutate func(tx *types.Transaction)
	}{
		{"nonce", func(tx *types.Transaction) { tx.Nonce = 7 }},
		{"gas limit", func(tx *types.Transaction) { tx.GasLimit = 21_000 }},
		{"gas price", func(tx *types.Transaction) { tx.GasPrice = big.NewInt(3) }},
		{"value", func(tx *types.Transaction) { tx.Value = big.NewInt(9) }},
		{"max block height", func(tx *types.Transaction) { tx.MaxBlockHeight = 1 << 40 }},
		{"to", func(tx *types.Transaction) { tx.To = addr }},
		{"paymaster", func(tx *types.Transaction) { tx.Paymaster = addr }},
		{"merchant address", func(tx *types.Transaction) { tx.MerchantAddress = "merchant" }},
		{"device id", func(tx *types.Transaction) { tx.DeviceID = "device" }},
		{"refund of", func(tx *types.Transaction) { tx.RefundOf = "refund" }},
		{"signature r", func(tx *types.Transaction) { tx.R = one }},
		{"signature s", func(tx *types.Transaction) { tx.S = one }},
		{"signature v", func(tx *types.Transaction) { tx.V = big.NewInt(27) }},
		{"paymaster signature r", func(tx *types.Transaction) { tx.PaymasterR = one }},
		{"paymaster signature s", func(tx *types.Transaction) { tx.PaymasterS = one }},
		{"paymaster signature v", func(tx *types.Transaction) { tx.PaymasterV = big.NewInt(27) }},
		{"intent ref", func(tx *types.Transaction) { tx.IntentRef = []byte("some-reference") }},
		{"intent expiry", func(tx *types.Transaction) { tx.IntentExpiry = 1_800_003_600 }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tx := senderlessTestRefPriceTx(t, key, 1_800_000_000)
			tc.mutate(tx)
			err := proposer.AddTransaction(tx)
			if err == nil {
				t.Fatalf("a sender-less transaction with a non-canonical %s must be rejected", tc.name)
			}
			if !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("expected ErrInvalidTransaction (so the peer path scores the sender), got %v", err)
			}
			if !strings.Contains(err.Error(), senderlessEnvelopeMessage) {
				t.Fatalf("expected the envelope rule to be the reason, got %v", err)
			}
			if got := senderlessMempoolLen(proposer); got != 0 {
				t.Fatalf("a rejected transaction must not be queued, mempool has %d", got)
			}
		})
	}

	// Control: the same payload in the canonical envelope is admitted, so the
	// rejections above are about the envelope and nothing else.
	if err := proposer.AddTransaction(senderlessTestRefPriceTx(t, key, 1_800_000_000)); err != nil {
		t.Fatalf("control: canonical envelope rejected: %v", err)
	}
}

func TestSenderlessAdmissionEnvelopeRuleCoversEveryType(t *testing.T) {
	node := newTestNode(t)
	for _, typ := range types.AllTxTypes() {
		if types.RequiresSignature(typ) {
			continue
		}
		typ := typ
		t.Run(types.TxTypeName(typ), func(t *testing.T) {
			tx := &types.Transaction{
				ChainID:  types.NHBChainID(),
				Type:     typ,
				Nonce:    1,
				Data:     []byte("payload"),
				GasPrice: big.NewInt(0),
			}
			err := node.AddTransaction(tx)
			if !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), senderlessEnvelopeMessage) {
				t.Fatalf("expected an envelope rejection, got %v", err)
			}

			// No sender-less type on this line may carry the intent fields.
			withIntent := &types.Transaction{
				ChainID:      types.NHBChainID(),
				Type:         typ,
				Data:         []byte("payload"),
				GasPrice:     big.NewInt(0),
				IntentRef:    []byte("intent"),
				IntentExpiry: 1_800_003_600,
			}
			if senderlessEnvelopeError(withIntent) == nil {
				t.Fatalf("this type must not carry intent fields")
			}
		})
	}
	if got := senderlessMempoolLen(node); got != 0 {
		t.Fatalf("nothing should have been queued, mempool has %d", got)
	}
}

func TestSenderlessAdmissionPeerPathScoresNonCanonicalEnvelope(t *testing.T) {
	proposer, _, key := newLendingRefPriceConsensusHarness(t)

	bad := senderlessTestRefPriceTx(t, key, 1_800_000_000)
	bad.Nonce = 3
	msg, err := p2p.NewTxMessage(bad)
	if err != nil {
		t.Fatalf("encode gossip message: %v", err)
	}
	if err := proposer.ProcessNetworkMessage(msg); !errors.Is(err, p2p.ErrInvalidPayload) {
		t.Fatalf("a peer relaying a non-canonical envelope must be scored as sending an invalid payload, got %v", err)
	}

	good, err := p2p.NewTxMessage(senderlessTestRefPriceTx(t, key, 1_800_000_000))
	if err != nil {
		t.Fatalf("encode gossip message: %v", err)
	}
	if err := proposer.ProcessNetworkMessage(good); err != nil {
		t.Fatalf("canonical gossip must be accepted: %v", err)
	}
	// Hearing the same payload again (another peer, or an echo) is not an
	// offence and does not queue a second copy.
	if err := proposer.ProcessNetworkMessage(good); err != nil {
		t.Fatalf("a repeated canonical gossip message must not be an error: %v", err)
	}
	if got := senderlessMempoolLen(proposer); got != 1 {
		t.Fatalf("expected exactly one pending copy, got %d", got)
	}
}

func TestSenderlessAdmissionDoesNotQueueAPayloadTwice(t *testing.T) {
	proposer, _, key := newLendingRefPriceConsensusHarness(t)

	tx := senderlessTestRefPriceTx(t, key, 1_800_000_000)
	if err := proposer.AddTransaction(tx); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	// A retry of the same payload still reports success, so a submitter that
	// resends after a timeout keeps working, but it is not queued again.
	if err := proposer.AddTransaction(senderlessTestRefPriceTx(t, key, 1_800_000_000)); err != nil {
		t.Fatalf("a repeated submission must still report success: %v", err)
	}
	if got := senderlessMempoolLen(proposer); got != 1 {
		t.Fatalf("expected one pending copy after a repeated submission, got %d", got)
	}
	resubmitted, err := proposer.SubmitLendingRefPrice(big.NewInt(5), big.NewInt(100), 1_800_000_000, [][]byte{mustDecodeRefPriceSignature(t, tx)})
	if err != nil {
		t.Fatalf("SubmitLendingRefPrice retry: %v", err)
	}
	wantHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.EqualFold(resubmitted, "0x"+hex.EncodeToString(wantHash)) {
		t.Fatalf("a retry through the submitter must report the pending transaction's hash, got %s", resubmitted)
	}
	if got := senderlessMempoolLen(proposer); got != 1 {
		t.Fatalf("expected one pending copy after the submitter retry, got %d", got)
	}

	// A different payload is a different submission and is queued.
	if err := proposer.AddTransaction(senderlessTestRefPriceTx(t, key, 1_800_000_001)); err != nil {
		t.Fatalf("distinct payload: %v", err)
	}
	if got := senderlessMempoolLen(proposer); got != 2 {
		t.Fatalf("expected two pending transactions, got %d", got)
	}
}

func mustDecodeRefPriceSignature(t *testing.T, tx *types.Transaction) []byte {
	t.Helper()
	var payload lendingRefPricePayload
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		t.Fatalf("decode ref price payload: %v", err)
	}
	if len(payload.Signatures) != 1 {
		t.Fatalf("expected one signature, got %d", len(payload.Signatures))
	}
	return payload.Signatures[0]
}

func TestSenderlessAdmissionBoundsPendingPerTypeAndFreesAfterABlock(t *testing.T) {
	proposer, validator, key := newLendingRefPriceConsensusHarness(t)

	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	const base = uint64(1_800_000_000)
	for i := 0; i < maxPendingSenderlessPerType; i++ {
		if err := proposer.AddTransaction(senderlessTestRefPriceTx(t, key, base+uint64(i))); err != nil {
			t.Fatalf("submission %d within the bound: %v", i, err)
		}
	}
	overflow := senderlessTestRefPriceTx(t, key, base+uint64(maxPendingSenderlessPerType))
	err = proposer.AddTransaction(overflow)
	if !errors.Is(err, ErrMempoolSenderlessTypeLimit) {
		t.Fatalf("expected the per-type bound to reject submission %d, got %v", maxPendingSenderlessPerType+1, err)
	}
	if !errors.Is(err, ErrMempoolFull) {
		t.Fatalf("the per-type bound must read as back-pressure (wrap ErrMempoolFull), got %v", err)
	}
	if got := senderlessMempoolLen(proposer); got != maxPendingSenderlessPerType {
		t.Fatalf("expected %d pending, got %d", maxPendingSenderlessPerType, got)
	}

	// The newest price lands; every older one can never apply again and is
	// pruned by the next block build, which frees the type's budget.
	proposer.mempoolMu.Lock()
	newest := proposer.mempool[len(proposer.mempool)-1]
	proposer.mempoolMu.Unlock()
	block2, err := proposer.CreateBlock([]*types.Transaction{newest})
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block2)
	block3, err := proposer.CreateBlock(proposer.GetMempool())
	if err != nil {
		t.Fatalf("create block 3: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block3)
	if got := senderlessMempoolLen(proposer); got != 0 {
		t.Fatalf("expected the superseded copies to be pruned, %d remain", got)
	}
	if err := proposer.AddTransaction(senderlessTestRefPriceTx(t, key, base+uint64(maxPendingSenderlessPerType)+1)); err != nil {
		t.Fatalf("a new submission must be admitted once the budget is freed: %v", err)
	}
}

func TestSenderlessPendingBoundIsPerTypeAndSkipsTheMintTypes(t *testing.T) {
	node := newTestNode(t)

	filler := make([]*types.Transaction, 0, 3*maxPendingSenderlessPerType)
	for i := 0; i < maxPendingSenderlessPerType; i++ {
		filler = append(filler, &types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: []byte{0x01, byte(i)}})
	}
	for i := 0; i < 2*maxPendingSenderlessPerType; i++ {
		filler = append(filler, &types.Transaction{Type: types.TxTypeMint, Data: []byte{0x02, byte(i)}})
	}
	livenessInject(node, filler...)

	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()

	if _, err := node.senderlessPendingCheckLocked(&types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: []byte{0x03}}); !errors.Is(err, ErrMempoolSenderlessTypeLimit) {
		t.Fatalf("a full type must reject a new payload, got %v", err)
	}
	if dup, err := node.senderlessPendingCheckLocked(&types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: []byte{0x01, 0x05}}); err != nil || !dup {
		t.Fatalf("a payload already pending is a duplicate, not a limit error: dup=%v err=%v", dup, err)
	}
	// Another type is unaffected by a full neighbour.
	if dup, err := node.senderlessPendingCheckLocked(&types.Transaction{Type: types.TxTypeLendingRefPrice, Data: []byte{0x04}}); err != nil || dup {
		t.Fatalf("a different type must not be bounded by a full neighbour: dup=%v err=%v", dup, err)
	}
	// The mint types already admit one pending transaction per voucher
	// identity, so they are not bounded per type.
	if dup, err := node.senderlessPendingCheckLocked(&types.Transaction{Type: types.TxTypeMint, Data: []byte{0x05}}); err != nil || dup {
		t.Fatalf("the mint types are not bounded per type: dup=%v err=%v", dup, err)
	}
	for _, typ := range []types.TxType{types.TxTypeMint, types.TxTypeSwapVoucherMint} {
		if limit := senderlessTypePendingLimit(typ); limit != 0 {
			t.Fatalf("%s: expected no per-type bound, got %d", types.TxTypeName(typ), limit)
		}
	}
}

// TestSenderlessBlockExecutionIgnoresTheAdmissionRule is the compatibility
// guard: admission is the only place the canonical envelope is enforced. A
// block that already contains a sender-less transaction with any envelope is
// proposed, validated and applied exactly as before, so nodes running this
// change and nodes that do not agree on every block.
func TestSenderlessBlockExecutionIgnoresTheAdmissionRule(t *testing.T) {
	proposer, validator, key := newLendingRefPriceConsensusHarness(t)

	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	tx := senderlessTestRefPriceTx(t, key, 1_800_000_000)
	tx.Nonce = 9
	tx.GasLimit = 50_000
	tx.GasPrice = big.NewInt(2)
	tx.MaxBlockHeight = 1_000_000
	tx.IntentRef = []byte("envelope-compat-ref")
	tx.IntentExpiry = 1_800_000_000 + 600

	block2, err := proposer.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("create block 2: %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("the transaction must still be proposed, block holds %d", len(block2.Transactions))
	}
	commitBlockOnBoth(t, proposer, validator, block2)

	// commitBlockOnBoth validated the block on the second node, which checks
	// the state root in the header against its own execution of the block.
	for name, node := range map[string]*Node{"proposer": proposer, "validator": validator} {
		status, err := node.LendingRefPriceStatus()
		if err != nil {
			t.Fatalf("%s status: %v", name, err)
		}
		if !status.HasRefPrice || status.Timestamp != 1_800_000_000 {
			t.Fatalf("%s: expected the reference price to be applied, got %+v", name, status)
		}
	}
}
