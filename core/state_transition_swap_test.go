package core

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/swap"
	swapv1 "nhbchain/proto/swap/v1"
	"nhbchain/storage"
)

func TestApplySwapPayoutReceiptRequiresRecoverableSignature(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

	receipt := &swapv1.PayoutReceipt{
		ReceiptId:    "rcpt-1",
		IntentId:     "intent-1",
		StableAsset:  "USDC",
		StableAmount: "1000",
		NhbAmount:    "1000",
		TxHash:       "0xdeadbeef",
		EvidenceUri:  "https://example.com/receipt",
		SettledAt:    1,
	}
	msg := &swapv1.MsgPayoutReceipt{Authority: "treasury", Receipt: receipt}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		GasPrice: big.NewInt(0),
		Data:     raw,
	}
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected signature recovery error")
	}
	if !strings.Contains(err.Error(), "transaction missing signature") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Authorization now comes from the recovered signer holding
// RoleSwapPayoutAttestor, not from the payload's self-declared Authority
// string -- these two tests prove that directly: a signer WITH the role is
// accepted regardless of what it claims in Authority, and a signer WITHOUT
// it is rejected even when it claims to be "treasury" (the exact bug this
// fix closes -- see applySwapPayoutReceipt's doc comment).

func TestApplySwapPayoutReceiptAcceptsAuthorizedAttestor(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

	attestorKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestorAddr := ethcrypto.PubkeyToAddress(attestorKey.PublicKey).Bytes()
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleSwapPayoutAttestor, attestorAddr); err != nil {
		t.Fatalf("grant attestor role: %v", err)
	}

	tx := buildSignedSwapPayoutReceiptTx(t, "treasury", attestorKey)
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected missing intent error")
	}
	if strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("unexpected authority rejection: %v", err)
	}
}

func TestApplySwapPayoutReceiptRejectsUnauthorizedAttestor(t *testing.T) {
	sp, _ := newTestStateProcessor(t)

	fraudsterKey, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate fraudster key: %v", err)
	}

	// Claiming to be "treasury" in the payload must not matter -- this
	// signer holds no role at all.
	tx := buildSignedSwapPayoutReceiptTx(t, "treasury", fraudsterKey)
	err = sp.applySwapPayoutReceipt(tx)
	if err == nil {
		t.Fatalf("expected unauthorized attestor error")
	}
	if !strings.Contains(err.Error(), "unauthorized payout attestor") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func buildSignedSwapPayoutReceiptTx(t *testing.T, authority string, key *ecdsa.PrivateKey) *types.Transaction {
	t.Helper()
	receipt := &swapv1.PayoutReceipt{
		ReceiptId:    "rcpt-1",
		IntentId:     "intent-1",
		StableAsset:  "USDC",
		StableAmount: "1000",
		NhbAmount:    "1000",
		TxHash:       "0xdeadbeef",
		EvidenceUri:  "https://example.com/receipt",
		SettledAt:    1,
	}
	msg := &swapv1.MsgPayoutReceipt{Authority: authority, Receipt: receipt}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		Nonce:    1,
		GasPrice: big.NewInt(0),
		Data:     raw,
	}
	if err := tx.Sign(key); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

// payoutReceiptBlockTime is far from any real wall-clock reading, so a stored
// timestamp that came from the host clock cannot be mistaken for it.
var payoutReceiptBlockTime = time.Unix(1_700_000_000, 0).UTC()

// payoutReceiptTestHeight is the height of the block the tests below execute in.
const payoutReceiptTestHeight = 7

// newPayoutReceiptState returns a state with the attestor role granted and one
// pending cash-out intent, seeded straight through the store with every
// timestamp pinned. activation is the swap payout receipt activation height the
// processor is configured with (0 leaves the block-time rules off).
func newPayoutReceiptState(t *testing.T, attestor []byte, activation uint64) *StateProcessor {
	t.Helper()
	sp, manager := newTestStateProcessor(t)
	swapCfg := sp.swapConfig
	swapCfg.PayoutReceiptActivationHeight = activation
	sp.SetSwapConfig(swapCfg)
	if err := manager.SetRole(RoleSwapPayoutAttestor, attestor); err != nil {
		t.Fatalf("grant attestor role: %v", err)
	}
	seededAt := time.Unix(1_600_000_000, 0)
	store := swap.NewStableStore(manager)
	store.SetClock(func() time.Time { return seededAt })
	if err := store.PutDepositVoucher(&swap.DepositVoucher{
		InvoiceID:    "inv-1",
		Provider:     "test",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(1000),
		NhbAmount:    big.NewInt(1000),
		Account:      "nhb1test",
		CreatedAt:    seededAt.Unix(),
	}); err != nil {
		t.Fatalf("seed deposit voucher: %v", err)
	}
	if _, err := manager.AdjustTokenSupply("NHB", big.NewInt(1000)); err != nil {
		t.Fatalf("seed NHB supply: %v", err)
	}
	if err := store.CreateCashOutIntent(&swap.CashOutIntent{
		IntentID:     "intent-1",
		InvoiceID:    "inv-1",
		Account:      "nhb1test",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(1000),
		NhbAmount:    big.NewInt(1000),
		CreatedAt:    seededAt.Unix(),
	}); err != nil {
		t.Fatalf("seed cash-out intent: %v", err)
	}
	return sp
}

func buildSignedPayoutReceiptTxFor(t *testing.T, key *ecdsa.PrivateKey, intentID string, nonce uint64, settledAt int64) *types.Transaction {
	t.Helper()
	msg := &swapv1.MsgPayoutReceipt{
		Authority: "treasury",
		Receipt: &swapv1.PayoutReceipt{
			ReceiptId:    "rcpt-1",
			IntentId:     intentID,
			StableAsset:  "USDC",
			StableAmount: "1000",
			NhbAmount:    "1000",
			TxHash:       "0xdeadbeef",
			EvidenceUri:  "https://example.com/receipt",
			SettledAt:    settledAt,
		},
	}
	packed, err := anypb.New(msg)
	if err != nil {
		t.Fatalf("pack payout receipt: %v", err)
	}
	raw, err := proto.Marshal(packed)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapPayoutReceipt,
		Nonce:    nonce,
		GasPrice: big.NewInt(0),
		Data:     raw,
	}
	if err := tx.Sign(key); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

// From the activation height every timestamp a payout receipt records must come
// from the block being executed, never from the host clock, or two validators
// applying the same block would write different state. The block time here is
// nowhere near the real time, so a value read from the host clock cannot pass for
// it. The processor is configured with an activation height equal to the block's
// own height, so this also covers the first active block.
func TestApplySwapPayoutReceiptRecordsBlockTime(t *testing.T) {
	cases := []struct {
		name         string
		settledAt    int64
		wantSettleAt int64
	}{
		{name: "settled_at unset", settledAt: 0, wantSettleAt: payoutReceiptBlockTime.Unix()},
		{name: "settled_at supplied", settledAt: 1_650_000_000, wantSettleAt: 1_650_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := ethcrypto.GenerateKey()
			if err != nil {
				t.Fatalf("generate attestor key: %v", err)
			}
			attestor := ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()
			sp := newPayoutReceiptState(t, attestor, payoutReceiptTestHeight)
			sp.BeginBlock(payoutReceiptTestHeight, payoutReceiptBlockTime)
			defer sp.EndBlock()

			tx := buildSignedPayoutReceiptTxFor(t, key, "intent-1", 0, tc.settledAt)
			if err := sp.ApplyTransaction(tx); err != nil {
				t.Fatalf("apply payout receipt: %v", err)
			}

			store := swap.NewStableStore(nhbstate.NewManager(sp.Trie))
			intent, ok, err := store.GetCashOutIntent("intent-1")
			if err != nil || !ok {
				t.Fatalf("load intent: %v ok=%v", err, ok)
			}
			if intent.Status != swap.CashOutStatusSettled || intent.SettledAt != tc.wantSettleAt {
				t.Fatalf("intent status=%s settledAt=%d, want settled at %d", intent.Status, intent.SettledAt, tc.wantSettleAt)
			}
			receipt, ok, err := store.GetPayoutReceipt("intent-1")
			if err != nil || !ok {
				t.Fatalf("load receipt: %v ok=%v", err, ok)
			}
			if receipt.SettledAt != tc.wantSettleAt {
				t.Fatalf("receipt settledAt=%d, want %d", receipt.SettledAt, tc.wantSettleAt)
			}
			lock, ok, err := store.GetEscrowLock("intent-1")
			if err != nil || !ok {
				t.Fatalf("load escrow lock: %v ok=%v", err, ok)
			}
			if !lock.Burned || lock.BurnedAt != tc.wantSettleAt {
				t.Fatalf("lock burned=%v burnedAt=%d, want burned at %d", lock.Burned, lock.BurnedAt, tc.wantSettleAt)
			}
			inventory, err := store.GetSoftInventory(swap.StableAssetUSDC)
			if err != nil {
				t.Fatalf("load inventory: %v", err)
			}
			if inventory.UpdatedAt != payoutReceiptBlockTime.Unix() {
				t.Fatalf("inventory updatedAt=%d, want block time %d", inventory.UpdatedAt, payoutReceiptBlockTime.Unix())
			}
		})
	}
}

// Once the rule is active, two nodes executing the same block a wall-clock second
// apart must end on the same state root.
func TestApplySwapPayoutReceiptRootIgnoresWallClock(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestor := ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()
	tx := buildSignedPayoutReceiptTxFor(t, key, "intent-1", 0, 0)

	apply := func() [32]byte {
		sp := newPayoutReceiptState(t, attestor, payoutReceiptTestHeight)
		sp.BeginBlock(payoutReceiptTestHeight, payoutReceiptBlockTime)
		defer sp.EndBlock()
		if err := sp.ApplyTransaction(tx); err != nil {
			t.Fatalf("apply payout receipt: %v", err)
		}
		return sp.Trie.Hash()
	}
	first := apply()
	time.Sleep(1100 * time.Millisecond)
	second := apply()
	if first != second {
		t.Fatalf("state root depends on the wall clock: %x != %x", first, second)
	}
}

// Once the rule is active, the attestor's nonce is consumed by a successful
// receipt, like every other signed transaction, so the same signed bytes cannot
// be applied twice; a rejected receipt consumes nothing.
func TestApplySwapPayoutReceiptConsumesNonce(t *testing.T) {
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestor := ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()
	sp := newPayoutReceiptState(t, attestor, payoutReceiptTestHeight)
	sp.BeginBlock(payoutReceiptTestHeight, payoutReceiptBlockTime)
	defer sp.EndBlock()

	nonceOf := func() uint64 {
		account, err := sp.getAccount(attestor)
		if err != nil {
			t.Fatalf("load attestor account: %v", err)
		}
		return account.Nonce
	}

	unknown := buildSignedPayoutReceiptTxFor(t, key, "no-such-intent", 0, 0)
	if err := sp.ApplyTransaction(unknown); err == nil {
		t.Fatalf("expected a receipt for an unknown intent to be rejected")
	}
	if got := nonceOf(); got != 0 {
		t.Fatalf("rejected receipt changed the attestor nonce to %d", got)
	}

	tx := buildSignedPayoutReceiptTxFor(t, key, "intent-1", 0, 0)
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply payout receipt: %v", err)
	}
	if got := nonceOf(); got != 1 {
		t.Fatalf("attestor nonce after a successful receipt = %d, want 1", got)
	}

	err = sp.ApplyTransaction(tx)
	if !errors.Is(err, ErrNonceTooLow) {
		t.Fatalf("replaying the same signed receipt: got %v, want ErrNonceTooLow", err)
	}
}

// payoutReceiptStamps are the four times a payout receipt writes into state.
type payoutReceiptStamps struct {
	intentSettledAt    int64
	receiptSettledAt   int64
	lockBurnedAt       int64
	inventoryUpdatedAt int64
}

func readPayoutReceiptStamps(t *testing.T, sp *StateProcessor) payoutReceiptStamps {
	t.Helper()
	store := swap.NewStableStore(nhbstate.NewManager(sp.Trie))
	intent, ok, err := store.GetCashOutIntent("intent-1")
	if err != nil || !ok {
		t.Fatalf("load intent: %v ok=%v", err, ok)
	}
	receipt, ok, err := store.GetPayoutReceipt("intent-1")
	if err != nil || !ok {
		t.Fatalf("load receipt: %v ok=%v", err, ok)
	}
	lock, ok, err := store.GetEscrowLock("intent-1")
	if err != nil || !ok {
		t.Fatalf("load escrow lock: %v ok=%v", err, ok)
	}
	inventory, err := store.GetSoftInventory(swap.StableAssetUSDC)
	if err != nil {
		t.Fatalf("load inventory: %v", err)
	}
	return payoutReceiptStamps{
		intentSettledAt:    intent.SettledAt,
		receiptSettledAt:   receipt.SettledAt,
		lockBurnedAt:       lock.BurnedAt,
		inventoryUpdatedAt: int64(inventory.UpdatedAt),
	}
}

// payoutReceiptOutcome is where the times a receipt recorded came from, and
// whether the attestor's nonce moved. The block time is far from the real time,
// and the host clock is read on either side of the call, so the source of every
// stamp is unambiguous.
type payoutReceiptOutcome struct {
	stamps         payoutReceiptStamps
	stampedByBlock bool
	stampedByHost  bool
	nonce          uint64
	sp             *StateProcessor
	tx             *types.Transaction
}

// applyPayoutReceiptAt applies one receipt (settled_at unset) to a fresh state
// configured with the given activation height, inside a block at the given
// height (or outside any block).
func applyPayoutReceiptAt(t *testing.T, activation, height uint64, withBlockContext bool) payoutReceiptOutcome {
	t.Helper()
	key, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate attestor key: %v", err)
	}
	attestor := ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()
	sp := newPayoutReceiptState(t, attestor, activation)
	if withBlockContext {
		sp.BeginBlock(height, payoutReceiptBlockTime)
		t.Cleanup(sp.EndBlock)
	}
	tx := buildSignedPayoutReceiptTxFor(t, key, "intent-1", 0, 0)
	hostBefore := time.Now().Unix()
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply payout receipt: %v", err)
	}
	hostAfter := time.Now().Unix()

	out := payoutReceiptOutcome{stamps: readPayoutReceiptStamps(t, sp), sp: sp, tx: tx}
	all := []int64{out.stamps.intentSettledAt, out.stamps.receiptSettledAt, out.stamps.lockBurnedAt, out.stamps.inventoryUpdatedAt}
	out.stampedByBlock, out.stampedByHost = true, true
	for _, v := range all {
		if v != payoutReceiptBlockTime.Unix() {
			out.stampedByBlock = false
		}
		if v < hostBefore || v > hostAfter {
			out.stampedByHost = false
		}
	}
	account, err := sp.getAccount(attestor)
	if err != nil {
		t.Fatalf("load attestor account: %v", err)
	}
	out.nonce = account.Nonce
	return out
}

// With the activation height left at zero, or not yet reached, a receipt is
// recorded exactly as the earlier builds recorded it: every time comes from the
// host clock (not from the block) and the attestor's nonce is left alone, so the
// same signed bytes are stopped only by the receipt already being stored. A
// value of zero must never switch the block-time rules on.
func TestApplySwapPayoutReceiptKeepsEarlierRulesBeforeActivation(t *testing.T) {
	cases := []struct {
		name       string
		activation uint64
		height     uint64
	}{
		{name: "setting left at zero", activation: 0, height: payoutReceiptTestHeight},
		{name: "setting left at zero at the first height", activation: 0, height: 1},
		{name: "activation one block ahead", activation: payoutReceiptTestHeight + 1, height: payoutReceiptTestHeight},
		{name: "activation far ahead", activation: 1 << 62, height: payoutReceiptTestHeight},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := applyPayoutReceiptAt(t, tc.activation, tc.height, true)
			if !out.stampedByHost || out.stampedByBlock {
				t.Fatalf("expected every time to come from the host clock, got %+v", out.stamps)
			}
			if out.nonce != 0 {
				t.Fatalf("attestor nonce = %d, want it left at 0", out.nonce)
			}
			err := out.sp.ApplyTransaction(out.tx)
			if err == nil || errors.Is(err, ErrNonceMismatch) || !strings.Contains(err.Error(), "receipt already stored") {
				t.Fatalf("replaying the same signed receipt: got %v, want it stopped by the stored receipt", err)
			}
		})
	}
}

// The rule switches on at the configured height and stays on: the block before
// it keeps the earlier behaviour, the block at it and every later block use the
// block time and consume the nonce.
func TestApplySwapPayoutReceiptActivationBoundary(t *testing.T) {
	const activation = 10
	cases := []struct {
		name   string
		height uint64
		active bool
	}{
		{name: "block before activation", height: activation - 1, active: false},
		{name: "activation block", height: activation, active: true},
		{name: "block after activation", height: activation + 1, active: true},
		{name: "much later block", height: activation + 1_000_000, active: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := applyPayoutReceiptAt(t, activation, tc.height, true)
			if tc.active {
				if !out.stampedByBlock || out.nonce != 1 {
					t.Fatalf("height %d: want block-time stamps and nonce 1, got %+v nonce=%d", tc.height, out.stamps, out.nonce)
				}
				return
			}
			if !out.stampedByHost || out.nonce != 0 {
				t.Fatalf("height %d: want host-clock stamps and nonce 0, got %+v nonce=%d", tc.height, out.stamps, out.nonce)
			}
		})
	}

	// The lowest height an operator can name is 1; zero means off.
	if out := applyPayoutReceiptAt(t, 1, 1, true); !out.stampedByBlock || out.nonce != 1 {
		t.Fatalf("activation 1 at height 1: want block-time stamps and nonce 1, got %+v nonce=%d", out.stamps, out.nonce)
	}
}

// A transaction applied outside any block (no height to compare against) never
// switches the rule on, so a processor used without a block context behaves as
// it always did.
func TestApplySwapPayoutReceiptWithoutBlockContextKeepsEarlierRules(t *testing.T) {
	out := applyPayoutReceiptAt(t, 1, 0, false)
	if !out.stampedByHost || out.nonce != 0 {
		t.Fatalf("want host-clock stamps and nonce 0 outside a block, got %+v nonce=%d", out.stamps, out.nonce)
	}
}

// The activation height is part of the swap configuration, so it must survive
// every route by which a node's configuration reaches a processor that executes
// blocks: the setter, the copy a block is built or validated on, and the
// processor rebuilt after a state reset. Dropping it on any of them would make
// one validator apply the earlier rules to a block the other applies the new ones
// to.
func TestNodeCarriesPayoutReceiptActivationToEveryProcessor(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	node, err := NewNode(db, key, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	heightOf := func(sp *StateProcessor) uint64 { return sp.swapConfig.PayoutReceiptActivationHeight }
	copyOf := func() *StateProcessor {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		cp, err := node.state.Copy()
		if err != nil {
			t.Fatalf("copy state processor: %v", err)
		}
		return cp
	}

	if got := node.swapConfig().PayoutReceiptActivationHeight; got != 0 {
		t.Fatalf("a new node must leave the rule off, got %d", got)
	}
	if got := heightOf(node.state); got != 0 {
		t.Fatalf("a new node's processor must leave the rule off, got %d", got)
	}
	if got := heightOf(copyOf()); got != 0 {
		t.Fatalf("a copy must leave the rule off, got %d", got)
	}

	cfg := node.swapConfig()
	cfg.PayoutReceiptActivationHeight = 123
	node.SetSwapConfig(cfg)
	if got := node.swapConfig().PayoutReceiptActivationHeight; got != 123 {
		t.Fatalf("node dropped the activation height: %d", got)
	}
	if got := heightOf(node.state); got != 123 {
		t.Fatalf("state processor dropped the activation height: %d", got)
	}
	if got := heightOf(copyOf()); got != 123 {
		t.Fatalf("copy dropped the activation height: %d", got)
	}

	node.stateMu.Lock()
	err = node.rebuildStateProcessorLocked(node.state.committedRoot)
	rebuilt := node.state
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	if got := heightOf(rebuilt); got != 123 {
		t.Fatalf("rebuilt state processor dropped the activation height: %d", got)
	}
}

// seedPayoutReceiptIntent gives the node the attestor role and one pending
// cash-out intent, as a committed (empty) block, the way the repository's other
// two-node tests seed state no transaction can create.
func seedPayoutReceiptIntent(t *testing.T, node *Node, attestor []byte) {
	t.Helper()
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	seededAt := time.Unix(1_600_000_000, 0)
	store := swap.NewStableStore(manager)
	store.SetClock(func() time.Time { return seededAt })
	seed := func() error {
		if err := manager.SetRole(RoleSwapPayoutAttestor, attestor); err != nil {
			return err
		}
		if err := store.PutDepositVoucher(&swap.DepositVoucher{
			InvoiceID:    "inv-1",
			Provider:     "test",
			StableAsset:  swap.StableAssetUSDC,
			StableAmount: big.NewInt(1000),
			NhbAmount:    big.NewInt(1000),
			Account:      "nhb1test",
			CreatedAt:    seededAt.Unix(),
		}); err != nil {
			return err
		}
		if _, err := manager.AdjustTokenSupply("NHB", big.NewInt(1000)); err != nil {
			return err
		}
		return store.CreateCashOutIntent(&swap.CashOutIntent{
			IntentID:     "intent-1",
			InvoiceID:    "inv-1",
			Account:      "nhb1test",
			StableAsset:  swap.StableAssetUSDC,
			StableAmount: big.NewInt(1000),
			NhbAmount:    big.NewInt(1000),
			CreatedAt:    seededAt.Unix(),
		})
	}
	err := seed()
	var root common.Hash
	if err == nil {
		root, err = node.state.Commit(node.chain.GetHeight())
	}
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("seed payout receipt state: %v", err)
	}
	commitStateAsEmptyBlock(t, node, root)
}

// Two independent validators, started from one genesis and carrying the same
// activation height, build, commit and re-derive a block holding a payout receipt
// with a real wall-clock second between every phase, and end on the same state
// root with the nonce consumed and every time taken from the block. Without the
// block-time rule the times would be read from each host's clock at each phase
// and the roots could not be expected to agree.
func TestPayoutReceiptBlockAgreesOnTwoNodesAcrossWallClockTicks(t *testing.T) {
	attestorKey := livenessKey(t)
	attestor := attestorKey.PubKey().Address().Bytes()
	pair := newLivenessPair(t, livenessPairOptions{sharedValidatorKey: true})
	// The seeded empty block is height 1, so the receipt block is height 2 and
	// is the first block the rule applies to.
	const activation = 2
	for _, node := range []*Node{pair.proposer, pair.validator} {
		cfg := node.swapConfig()
		cfg.PayoutReceiptActivationHeight = activation
		node.SetSwapConfig(cfg)
		seedPayoutReceiptIntent(t, node, attestor)
	}
	pair.requireAgreement()

	tx := buildSignedPayoutReceiptTxFor(t, attestorKey.PrivateKey, "intent-1", 0, 0)
	block, err := pair.proposer.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if block.Header.Height != activation || len(block.Transactions) != 1 {
		t.Fatalf("expected the receipt in the block at height %d, got height %d with %d transactions", activation, block.Header.Height, len(block.Transactions))
	}
	time.Sleep(1100 * time.Millisecond)
	if err := pair.proposer.CommitBlock(block); err != nil {
		t.Fatalf("proposer could not commit its own block: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := pair.validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected the proposer's block: %v", err)
	}
	if err := pair.validator.CommitBlock(block); err != nil {
		t.Fatalf("validator commit: %v", err)
	}
	pair.requireAgreement()

	for name, node := range map[string]*Node{"proposer": pair.proposer, "validator": pair.validator} {
		node.stateMu.Lock()
		stamps := readPayoutReceiptStamps(t, node.state)
		account, aerr := node.state.getAccount(attestor)
		node.stateMu.Unlock()
		if aerr != nil {
			t.Fatalf("%s: load attestor account: %v", name, aerr)
		}
		want := block.Header.Timestamp
		if stamps.intentSettledAt != want || stamps.receiptSettledAt != want || stamps.lockBurnedAt != want || stamps.inventoryUpdatedAt != want {
			t.Fatalf("%s: times %+v, want every one at the block time %d", name, stamps, want)
		}
		if account.Nonce != 1 {
			t.Fatalf("%s: attestor nonce = %d, want 1", name, account.Nonce)
		}
	}
}

// The activation height is consensus-relevant: a validator configured with a
// different value from the proposer's reaches a different state root for a block
// holding a payout receipt and refuses it, which is why the value must be the
// same on every validator before the chain reaches it. The same block is accepted
// once the validator carries the proposer's value, so the refusal is down to the
// setting alone.
func TestPayoutReceiptBlockIsRefusedByAValidatorWithADifferentActivation(t *testing.T) {
	attestorKey := livenessKey(t)
	attestor := attestorKey.PubKey().Address().Bytes()
	pair := newLivenessPair(t, livenessPairOptions{sharedValidatorKey: true})
	const activation = 2 // the receipt block is height 2, after the seeded block
	for _, node := range []*Node{pair.proposer, pair.validator} {
		seedPayoutReceiptIntent(t, node, attestor)
	}
	pair.requireAgreement()
	setActivation := func(node *Node, height uint64) {
		cfg := node.swapConfig()
		cfg.PayoutReceiptActivationHeight = height
		node.SetSwapConfig(cfg)
	}
	setActivation(pair.proposer, activation)

	tx := buildSignedPayoutReceiptTxFor(t, attestorKey.PrivateKey, "intent-1", 0, 0)
	block, err := pair.proposer.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the receipt in the block, got %d transactions", len(block.Transactions))
	}
	err = pair.validator.ValidateBlock(block)
	if err == nil {
		t.Fatalf("a validator with the rule left off accepted a block built with it on")
	}
	if !strings.Contains(err.Error(), "state root mismatch") {
		t.Fatalf("expected the validator to refuse the block on its state root, got: %v", err)
	}
	setActivation(pair.validator, activation)
	if err := pair.validator.ValidateBlock(block); err != nil {
		t.Fatalf("a validator carrying the same activation height refused the block: %v", err)
	}
}
