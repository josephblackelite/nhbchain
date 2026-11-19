package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
)

// PL-DC-19: the engine accrues a protocol share of the interest in a counter and has
// a method that pays it out, but no transaction reached the method, so the share
// accrued for ever. TxTypeLendingWithdrawProtocolFees is that transaction.

const feeWithdrawTestActivation = 100

type feeWithdrawFixture struct {
	sp        *StateProcessor
	admin     *crypto.PrivateKey
	outsider  *crypto.PrivateKey
	recipient *crypto.PrivateKey
}

// newFeeWithdrawFixture builds a pool holding 3,000 NHB of liquidity and a protocol
// fee counter of 100 NHB, an account holding the role and one that does not.
func newFeeWithdrawFixture(t *testing.T, gate uint64) *feeWithdrawFixture {
	t.Helper()
	sp, _, _ := lendingTwoBorrowers(t, 0)
	sp.SetLendingFeeWithdrawActivationHeight(gate)
	f := &feeWithdrawFixture{sp: sp, admin: newKey(t), outsider: newKey(t), recipient: newKey(t)}
	for _, k := range []*crypto.PrivateKey{f.admin, f.outsider} {
		if err := sp.setAccount(k.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.setAccount(f.recipient.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}); err != nil {
		t.Fatal(err)
	}
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetRole(RoleLendingProtocolAdmin, f.admin.PubKey().Address().Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := manager.LendingPutFeeAccrual("default", &lending.FeeAccrual{
		ProtocolFeesWei:  mustBigInt(t, "100000000000000000000"),
		DeveloperFeesWei: big.NewInt(0),
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *feeWithdrawFixture) tx(t *testing.T, signer *crypto.PrivateKey, nonce uint64, poolID, recipient, amount string) *types.Transaction {
	t.Helper()
	data := []byte(`{"poolId":"` + poolID + `","recipient":"` + recipient + `","amountWei":"` + amount + `"}`)
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLendingWithdrawProtocolFees,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    big.NewInt(0),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(signer.PrivateKey); err != nil {
		t.Fatal(err)
	}
	return tx
}

func (f *feeWithdrawFixture) apply(t *testing.T, tx *types.Transaction, height uint64) error {
	t.Helper()
	f.sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
	defer f.sp.EndBlock()
	return f.sp.ApplyTransaction(tx)
}

func (f *feeWithdrawFixture) recipientString() string { return f.recipient.PubKey().Address().String() }

func (f *feeWithdrawFixture) state(t *testing.T) (moduleNHB, recipientNHB, supplied, counter *big.Int) {
	t.Helper()
	manager := nhbstate.NewManager(f.sp.Trie)
	module, err := manager.GetAccount(f.sp.lendingModuleAddr.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	rec, err := manager.GetAccount(f.recipient.PubKey().Address().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	market, _, err := manager.LendingGetMarket("default")
	if err != nil {
		t.Fatal(err)
	}
	fees, _, err := manager.LendingGetFeeAccrual("default")
	if err != nil {
		t.Fatal(err)
	}
	return module.BalanceNHB, rec.BalanceNHB, market.TotalNHBSupplied, fees.ProtocolFeesWei
}

const ten = "10000000000000000000"

func TestTheProtocolFeeWithdrawalIsAnUnknownTransactionBeforeItsHeight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gate   uint64
		height uint64
	}{
		{"default off", 0, 10_000_000},
		{"below the height", feeWithdrawTestActivation, feeWithdrawTestActivation - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFeeWithdrawFixture(t, tc.gate)
			moduleBefore, recipientBefore, suppliedBefore, counterBefore := f.state(t)
			err := f.apply(t, f.tx(t, f.admin, 0, "default", f.recipientString(), ten), tc.height)
			if !errors.Is(err, ErrUnknownTransactionType) {
				t.Fatalf("got %v, want ErrUnknownTransactionType", err)
			}
			// The very error a byte nobody defined gets.
			unknown := f.tx(t, f.admin, 0, "default", f.recipientString(), ten)
			unknown.Type = 0x4E
			if err := unknown.Sign(f.admin.PrivateKey); err != nil {
				t.Fatal(err)
			}
			errUnknown := f.apply(t, unknown, tc.height)
			if errUnknown == nil || !strings.Contains(errUnknown.Error(), ErrUnknownTransactionType.Error()) {
				t.Fatalf("an undefined byte must be rejected the same way, got %v", errUnknown)
			}
			module, recipient, supplied, counter := f.state(t)
			if module.Cmp(moduleBefore) != 0 || recipient.Cmp(recipientBefore) != 0 || supplied.Cmp(suppliedBefore) != 0 || counter.Cmp(counterBefore) != 0 {
				t.Fatalf("a rejected withdrawal changed state")
			}
		})
	}
}

func TestTheRoleHolderWithdrawsProtocolFeesFromTheHeight(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	moduleBefore, _, suppliedBefore, counterBefore := f.state(t)
	if err := f.apply(t, f.tx(t, f.admin, 0, "default", f.recipientString(), ten), feeWithdrawTestActivation); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	module, recipient, supplied, counter := f.state(t)
	amount := mustBigInt(t, ten)
	if recipient.Cmp(amount) != 0 {
		t.Fatalf("the recipient holds %s, want %s", recipient, amount)
	}
	if want := new(big.Int).Sub(moduleBefore, amount); module.Cmp(want) != 0 {
		t.Fatalf("the module holds %s, want %s", module, want)
	}
	if want := new(big.Int).Sub(suppliedBefore, amount); supplied.Cmp(want) != 0 {
		t.Fatalf("the suppliers' total is %s, want %s", supplied, want)
	}
	if want := new(big.Int).Sub(counterBefore, amount); counter.Cmp(want) != 0 {
		t.Fatalf("the fee counter is %s, want %s", counter, want)
	}
	var found *types.Event
	for _, evt := range f.sp.Events() {
		evt := evt
		if evt.Type == "lending.protocolfees.withdrawn" {
			found = &evt
		}
	}
	if found == nil {
		t.Fatalf("no lending.protocolfees.withdrawn event was emitted")
	}
	if found.Attributes["poolId"] != "default" || found.Attributes["amountWei"] != ten ||
		found.Attributes["recipient"] != f.recipientString() || found.Attributes["admin"] != f.admin.PubKey().Address().String() {
		t.Fatalf("the event carries the wrong values: %v", found.Attributes)
	}
	acc, err := f.sp.getAccount(f.admin.PubKey().Address().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if acc.Nonce != 1 {
		t.Fatalf("the sender's nonce is %d, want 1", acc.Nonce)
	}
}

// Replay is the sender's nonce: the same signed transaction cannot pay twice.
func TestAProtocolFeeWithdrawalCannotBeReplayed(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	tx := f.tx(t, f.admin, 0, "default", f.recipientString(), ten)
	if err := f.apply(t, tx, feeWithdrawTestActivation); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, recipientAfterFirst, _, _ := f.state(t)
	if err := f.apply(t, tx, feeWithdrawTestActivation+1); err == nil {
		t.Fatalf("the same signed withdrawal applied twice")
	}
	if _, recipient, _, _ := f.state(t); recipient.Cmp(recipientAfterFirst) != 0 {
		t.Fatalf("a replay paid out: %s", recipient)
	}
}

func TestOnlyTheRoleHolderMayWithdrawProtocolFees(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	moduleBefore, recipientBefore, suppliedBefore, counterBefore := f.state(t)
	err := f.apply(t, f.tx(t, f.outsider, 0, "default", f.recipientString(), ten), feeWithdrawTestActivation)
	if !errors.Is(err, ErrLendingFeeWithdrawUnauthorized) {
		t.Fatalf("got %v, want ErrLendingFeeWithdrawUnauthorized", err)
	}
	module, recipient, supplied, counter := f.state(t)
	if module.Cmp(moduleBefore) != 0 || recipient.Cmp(recipientBefore) != 0 || supplied.Cmp(suppliedBefore) != 0 || counter.Cmp(counterBefore) != 0 {
		t.Fatalf("a refused withdrawal changed state")
	}
}

func TestAProtocolFeeWithdrawalIsBoundedByTheCounterAndTheFreeLiquidity(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	over := "100000000000000000001" // one wei past the 100 NHB counter
	if err := f.apply(t, f.tx(t, f.admin, 0, "default", f.recipientString(), over), feeWithdrawTestActivation); !errors.Is(err, ErrLendingFeeWithdrawExceedsAvailable) {
		t.Fatalf("over the counter: got %v, want ErrLendingFeeWithdrawExceedsAvailable", err)
	}
	// Exactly the counter is paid.
	if err := f.apply(t, f.tx(t, f.admin, 0, "default", f.recipientString(), "100000000000000000000"), feeWithdrawTestActivation); err != nil {
		t.Fatalf("exactly the counter: %v", err)
	}
	if _, _, _, counter := f.state(t); counter.Sign() != 0 {
		t.Fatalf("the counter is %s after paying all of it", counter)
	}
}

func TestAProtocolFeeWithdrawalIsRefusedForABadPayloadOrAMissingPool(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	for _, tc := range []struct {
		name string
		tx   *types.Transaction
		want error
	}{
		{"zero amount", f.tx(t, f.admin, 0, "default", f.recipientString(), "0"), ErrLendingFeeWithdrawInvalidPayload},
		{"negative amount", f.tx(t, f.admin, 0, "default", f.recipientString(), "-5"), ErrLendingFeeWithdrawInvalidPayload},
		{"amount that is not a number", f.tx(t, f.admin, 0, "default", f.recipientString(), "ten"), ErrLendingFeeWithdrawInvalidPayload},
		{"recipient that is not an address", f.tx(t, f.admin, 0, "default", "nope", ten), ErrLendingFeeWithdrawInvalidPayload},
		{"recipient with the wrong prefix", f.tx(t, f.admin, 0, "default", crypto.MustNewAddress(crypto.ZNHBPrefix, f.recipient.PubKey().Address().Bytes()).String(), ten), ErrLendingFeeWithdrawInvalidPayload},
		{"empty payload", f.tx(t, f.admin, 0, "default", "", ""), ErrLendingFeeWithdrawInvalidPayload},
		{"pool that does not exist", f.tx(t, f.admin, 0, "ghost", f.recipientString(), ten), ErrLendingPoolNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.apply(t, tc.tx, feeWithdrawTestActivation); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	// A payload that is not JSON at all.
	tx := f.tx(t, f.admin, 0, "default", f.recipientString(), ten)
	tx.Data = []byte("not json")
	if err := tx.Sign(f.admin.PrivateKey); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(t, tx, feeWithdrawTestActivation); !errors.Is(err, ErrLendingFeeWithdrawInvalidPayload) {
		t.Fatalf("not JSON: got %v", err)
	}
	if poolExists(t, f.sp, "ghost") {
		t.Fatalf("a withdrawal from a pool that does not exist created it")
	}
}

func TestAProtocolFeeWithdrawalRespectsTheLendingPause(t *testing.T) {
	f := newFeeWithdrawFixture(t, feeWithdrawTestActivation)
	f.sp.SetPauseView(stubPauses{"lending": true})
	if err := f.apply(t, f.tx(t, f.admin, 0, "default", f.recipientString(), ten), feeWithdrawTestActivation); !errors.Is(err, nativecommon.ErrModulePaused) {
		t.Fatalf("got %v, want ErrModulePaused", err)
	}
}

func TestFeeWithdrawalRefusalsAreClassifiedSoTheyNeverHaltABlock(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want proposalTxDisposition
	}{
		{ErrLendingFeeWithdrawInvalidPayload, proposalDispositionPrune},
		{ErrLendingFeeWithdrawUnauthorized, proposalDispositionSkip},
		{ErrLendingFeeWithdrawExceedsAvailable, proposalDispositionSkip},
	} {
		if got := classifyProposalError(tc.err); got != tc.want {
			t.Errorf("%v is classified %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestLendingFeeWithdrawGateReachesBlockState(t *testing.T) {
	node := newTestNode(t)
	node.SetLendingFeeWithdrawActivationHeight(feeWithdrawTestActivation)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if node.state.lendingFeeWithdrawFromHeight != feeWithdrawTestActivation {
		t.Fatalf("node state holds %d", node.state.lendingFeeWithdrawFromHeight)
	}
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatal(err)
	}
	if copied.lendingFeeWithdrawFromHeight != feeWithdrawTestActivation {
		t.Fatalf("a copy holds %d", copied.lendingFeeWithdrawFromHeight)
	}
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatal(err)
	}
	if node.state.lendingFeeWithdrawFromHeight != feeWithdrawTestActivation {
		t.Fatalf("a rebuilt state processor holds %d", node.state.lendingFeeWithdrawFromHeight)
	}
}

// Refused withdrawals and a valid pool creation in one proposal: the block is built
// with the valid one, the refused ones leave the mempool, and a second node reaches the
// same state root.
func TestCreateBlockWithRefusedFeeWithdrawalsStillProducesABlock(t *testing.T) {
	build := func() *Node {
		node := newTestNode(t)
		node.SetLendingFeeWithdrawActivationHeight(1)
		return node
	}
	proposer, validator := build(), build()
	outsider, other, creator := newKey(t), newKey(t), newKey(t)
	f := &feeWithdrawFixture{}
	unauthorised := f.tx(t, outsider, 0, "default", outsider.PubKey().Address().String(), ten)
	garbage := f.tx(t, other, 0, "default", "nobody", "x")
	create := lendingCreatePoolTx(t, 0, "good-pool")
	if err := create.Sign(creator.PrivateKey); err != nil {
		t.Fatal(err)
	}
	block, err := proposer.CreateBlock([]*types.Transaction{unauthorised, garbage, create})
	if err != nil {
		t.Fatalf("refused withdrawals must not stop the block: %v", err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Type != types.TxTypeLendingCreatePool {
		t.Fatalf("expected only the pool creation in the block, got %d txs", len(block.Transactions))
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected the block: %v", err)
	}
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatal(err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatal(err)
	}
	if proposer.state.CurrentRoot() != validator.state.CurrentRoot() {
		t.Fatalf("proposer and validator diverged")
	}
}
