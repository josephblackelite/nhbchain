package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// reverseEnv is a node with a bootstrapped Sale Pool, one voucher minted
// through a real block and a RoleSwapAdmin signer.
type reverseEnv struct {
	node         *Node
	adminWallet  [20]byte
	recipient    [20]byte
	swapAdminKey *crypto.PrivateKey
	providerTxID string
	amount       *big.Int
}

// newReverseEnv mints a voucher for a fresh recipient through the real mint
// path and grants RoleSwapAdmin to a fresh signer.
func newReverseEnv(t *testing.T) *reverseEnv {
	t.Helper()
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	env := &reverseEnv{node: node, adminWallet: node.state.adminWallet, providerTxID: "PROVIDER-REVERSE-1"}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	env.recipient = toAddress(recipientKey)

	voucher := swapVoucherTestVoucher(node.chain.ChainID(), env.recipient, "0.05", "ORDER-REVERSE")
	env.amount = new(big.Int).Set(voucher.Amount)
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signSwapVoucherCore(t, minterKey, voucher),
		Provider:     "nowpayments",
		ProviderTxID: env.providerTxID,
		PriceProof:   signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now()),
	}
	if _, _, err := node.SwapSubmitVoucher(submission); err != nil {
		t.Fatalf("submit voucher: %v", err)
	}
	block, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	env.swapAdminKey, err = crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("swap admin key: %v", err)
	}
	assignRole(t, node, RoleSwapAdmin, toAddress(env.swapAdminKey))
	return env
}

func (e *reverseEnv) reverse(t *testing.T) error {
	t.Helper()
	signature, err := ethcrypto.Sign(SwapVoucherReverseSigningHash(e.providerTxID), e.swapAdminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reversal: %v", err)
	}
	payload, err := encodeSwapVoucherReverseTransaction(e.providerTxID, signature)
	if err != nil {
		t.Fatalf("encode reversal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapVoucherReverse,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}
	e.node.stateMu.Lock()
	defer e.node.stateMu.Unlock()
	return e.node.state.ApplyTransaction(tx)
}

type reverseSnapshot struct {
	admin, recipient, sink *big.Int
	salePool, cumulative   *big.Int
}

func snapshotReverseState(t *testing.T, node *Node, adminWallet, recipient, sink [20]byte) reverseSnapshot {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	bal := func(addr [20]byte) *big.Int {
		b, err := znhbAccountBalance(manager, addr)
		if err != nil {
			t.Fatalf("read balance: %v", err)
		}
		return b
	}
	snap := reverseSnapshot{admin: bal(adminWallet), recipient: bal(recipient), sink: bal(sink)}
	var err error
	if snap.salePool, err = manager.ZNHBSalePoolBalance(); err != nil {
		t.Fatalf("sale pool: %v", err)
	}
	if snap.cumulative, err = manager.ZNHBCumulativeSaleDistributed(); err != nil {
		t.Fatalf("cumulative: %v", err)
	}
	return snap
}

func (e *reverseEnv) snapshot(t *testing.T, sink [20]byte) reverseSnapshot {
	return snapshotReverseState(t, e.node, e.adminWallet, e.recipient, sink)
}

// TestSwapVoucherMintThenReverseToAdminWalletRestoresSalePool mints a real
// voucher and reverses it into the admin wallet: the account balances, the
// Sale Pool counter and the distribution-curve counter all return to their
// pre-mint values and the supply invariant holds at every step.
func TestSwapVoucherMintThenReverseToAdminWalletRestoresSalePool(t *testing.T) {
	// The pre-mint values, read from an identical node that minted nothing.
	base, _, _ := setupSwapVoucherTestNode(t)
	pre := snapshotReverseState(t, base, base.state.adminWallet, [20]byte{0xEE}, base.state.adminWallet)

	env := newReverseEnv(t)
	env.node.SetSwapRefundSink(env.adminWallet)

	minted := env.snapshot(t, env.adminWallet)
	if minted.recipient.Cmp(env.amount) != 0 {
		t.Fatalf("minted recipient balance: want %s, got %s", env.amount, minted.recipient)
	}
	if err := env.node.state.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant after mint: %v", err)
	}

	if err := env.reverse(t); err != nil {
		t.Fatalf("reverse voucher: %v", err)
	}

	after := env.snapshot(t, env.adminWallet)
	if after.recipient.Sign() != 0 {
		t.Fatalf("recipient balance after reversal: want 0, got %s", after.recipient)
	}
	if after.admin.Cmp(pre.admin) != 0 {
		t.Fatalf("admin wallet must return to its pre-mint balance %s, got %s", pre.admin, after.admin)
	}
	if after.salePool.Cmp(pre.salePool) != 0 {
		t.Fatalf("sale pool must return to %s, got %s", pre.salePool, after.salePool)
	}
	if after.cumulative.Cmp(pre.cumulative) != 0 {
		t.Fatalf("cumulative sale distributed must return to %s, got %s", pre.cumulative, after.cumulative)
	}
	total := new(big.Int).Add(after.admin, after.recipient)
	preTotal := new(big.Int).Add(pre.admin, pre.recipient)
	if total.Cmp(preTotal) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", preTotal, total)
	}
	if err := env.node.state.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant after reversal: %v", err)
	}
}

// TestSwapVoucherMintThenReverseToOtherSinkKeepsPoolsUntouched covers a refund
// sink that is not the admin wallet: the funds move recipient -> sink and the
// pools (which mirror the admin wallet) stay exactly as the mint left them.
func TestSwapVoucherMintThenReverseToOtherSinkKeepsPoolsUntouched(t *testing.T) {
	env := newReverseEnv(t)
	sinkKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("sink key: %v", err)
	}
	sink := toAddress(sinkKey)
	env.node.SetSwapRefundSink(sink)

	minted := env.snapshot(t, sink)
	if err := env.reverse(t); err != nil {
		t.Fatalf("reverse voucher: %v", err)
	}
	after := env.snapshot(t, sink)

	if after.recipient.Sign() != 0 {
		t.Fatalf("recipient balance after reversal: want 0, got %s", after.recipient)
	}
	if want := new(big.Int).Add(minted.sink, env.amount); after.sink.Cmp(want) != 0 {
		t.Fatalf("sink balance: want %s, got %s", want, after.sink)
	}
	if after.admin.Cmp(minted.admin) != 0 || after.salePool.Cmp(minted.salePool) != 0 || after.cumulative.Cmp(minted.cumulative) != 0 {
		t.Fatalf("admin wallet and pools must be untouched: admin %s/%s pool %s/%s cumulative %s/%s",
			minted.admin, after.admin, minted.salePool, after.salePool, minted.cumulative, after.cumulative)
	}
	movedBefore := new(big.Int).Add(minted.recipient, minted.sink)
	movedAfter := new(big.Int).Add(after.recipient, after.sink)
	if movedBefore.Cmp(movedAfter) != 0 {
		t.Fatalf("ZNHB not conserved: before %s after %s", movedBefore, movedAfter)
	}
	if err := env.node.state.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("invariant after reversal: %v", err)
	}
}

// TestSwapVoucherReverseFailsWhenRecipientSpentTheFunds keeps the reversal
// fail-closed: it cannot claw back more than the recipient still holds.
func TestSwapVoucherReverseFailsWhenRecipientSpentTheFunds(t *testing.T) {
	env := newReverseEnv(t)
	env.node.SetSwapRefundSink(env.adminWallet)
	env.node.stateMu.Lock()
	acc, err := env.node.state.getAccount(env.recipient[:])
	if err == nil {
		acc.BalanceZNHB = new(big.Int).Sub(acc.BalanceZNHB, big.NewInt(1))
		err = env.node.state.setAccount(env.recipient[:], acc)
	}
	env.node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("spend funds: %v", err)
	}
	if err := env.reverse(t); !errors.Is(err, ErrSwapReversalInsufficientBalance) {
		t.Fatalf("expected ErrSwapReversalInsufficientBalance, got %v", err)
	}
}

// TestSwapVoucherReverseRejectsRecipientThatIsTheSink covers the collision of
// the recipient and the refund sink: nothing would move, so it is rejected.
func TestSwapVoucherReverseRejectsRecipientThatIsTheSink(t *testing.T) {
	env := newReverseEnv(t)
	env.node.SetSwapRefundSink(env.recipient)
	before := env.snapshot(t, env.recipient)
	err := env.reverse(t)
	if !errors.Is(err, ErrSwapAdminInvalidPayload) {
		t.Fatalf("expected ErrSwapAdminInvalidPayload, got %v", err)
	}
	after := env.snapshot(t, env.recipient)
	if after.recipient.Cmp(before.recipient) != 0 || after.salePool.Cmp(before.salePool) != 0 {
		t.Fatalf("a rejected reversal must not change state")
	}
}

// TestSwapVoucherMintRejectsAdminWalletRecipient covers the recipient and the
// Sale Pool source being one account, which used to burn the voucher amount
// while advancing the pool counters.
func TestSwapVoucherMintRejectsAdminWalletRecipient(t *testing.T) {
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	admin := node.state.adminWallet
	voucher := swapVoucherTestVoucher(node.chain.ChainID(), admin, "0.05", "ORDER-ADMIN-RECIPIENT")
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signSwapVoucherCore(t, minterKey, voucher),
		Provider:     "nowpayments",
		ProviderTxID: "PROVIDER-ADMIN-RECIPIENT",
		PriceProof:   signedPriceProofCore(t, oracleKey, "nowpayments", "0.05", time.Now()),
	}
	_, _, err := node.SwapSubmitVoucher(submission)
	if !errors.Is(err, ErrSwapVoucherInvalidPayload) {
		t.Fatalf("expected ErrSwapVoucherInvalidPayload, got %v", err)
	}
}
