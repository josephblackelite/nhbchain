package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-91: a stake that only registers the sender as a validator candidate and an
// unstake that only deregisters it, each with no value, never advanced the sender's
// nonce, so the signed bytes of one of them could be submitted again and again.

type flagNonceFixture struct {
	sp   *StateProcessor
	key  *crypto.PrivateKey
	addr [20]byte
}

func newFlagNonceFixture(t *testing.T, activation uint64) *flagNonceFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	sp.SetValidatorFlagNonceActivationHeight(activation)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	writeAccount(t, sp, addr, &types.Account{BalanceZNHB: big.NewInt(30_000), Stake: big.NewInt(8_000)})
	return &flagNonceFixture{sp: sp, key: key, addr: addr}
}

func (f *flagNonceFixture) signed(t *testing.T, txType types.TxType, nonce uint64, value int64, data []byte) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    big.NewInt(value),
		Data:     data,
		GasLimit: 25_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(f.key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tx
}

func (f *flagNonceFixture) register(t *testing.T, nonce uint64) *types.Transaction {
	return f.signed(t, types.TxTypeStake, nonce, 0, mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}))
}

func (f *flagNonceFixture) deregister(t *testing.T, nonce uint64) *types.Transaction {
	return f.signed(t, types.TxTypeUnstake, nonce, 0, mustEncodeUnstakePayload(t, unstakePayload{DeregisterValidator: true}))
}

// apply runs the transaction as the only transaction of a block at height.
func (f *flagNonceFixture) apply(height uint64, tx *types.Transaction) error {
	f.sp.BeginBlock(height, time.Unix(int64(1_700_000_000+height), 0).UTC())
	defer f.sp.EndBlock()
	return f.sp.ApplyTransaction(tx)
}

func (f *flagNonceFixture) account(t *testing.T) *types.Account {
	t.Helper()
	acct, err := f.sp.getAccount(f.addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	return acct
}

func requireNonceTooLow(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want a nonce rejection", what)
	}
	if !errors.Is(err, ErrNonceTooLow) {
		t.Fatalf("%s: want ErrNonceTooLow, got %v", what, err)
	}
}

// With the setting off the behaviour is the one every committed block was built
// with: the same signed bytes are accepted again and the nonce never moves.
func TestValidatorFlagNonce_OffLeavesTheNonceAlone(t *testing.T) {
	f := newFlagNonceFixture(t, 0)
	reg := f.register(t, 0)
	for i := 0; i < 3; i++ {
		if err := f.apply(10+uint64(i), reg); err != nil {
			t.Fatalf("register #%d: %v", i, err)
		}
	}
	if got := f.account(t).Nonce; got != 0 {
		t.Fatalf("nonce after three registrations with the setting off: %d, want 0", got)
	}
	dereg := f.deregister(t, 0)
	for i := 0; i < 3; i++ {
		if err := f.apply(20+uint64(i), dereg); err != nil {
			t.Fatalf("deregister #%d: %v", i, err)
		}
	}
	if got := f.account(t).Nonce; got != 0 {
		t.Fatalf("nonce after three deregistrations with the setting off: %d, want 0", got)
	}
}

func TestValidatorFlagNonce_BelowTheHeightLeavesTheNonceAlone(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	reg := f.register(t, 0)
	if err := f.apply(99, reg); err != nil {
		t.Fatalf("register below the height: %v", err)
	}
	if err := f.apply(99, reg); err != nil {
		t.Fatalf("replayed register below the height: %v", err)
	}
	if got := f.account(t).Nonce; got != 0 {
		t.Fatalf("nonce below the height: %d, want 0", got)
	}
}

func TestValidatorFlagNonce_AtTheHeightARegisterConsumesTheNonce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	reg := f.register(t, 0)
	if err := f.apply(100, reg); err != nil {
		t.Fatalf("register at the height: %v", err)
	}
	acct := f.account(t)
	if acct.Nonce != 1 {
		t.Fatalf("nonce after a register at the height: %d, want 1", acct.Nonce)
	}
	if !acct.ValidatorRegistered {
		t.Fatalf("the register must still flip the flag")
	}
	requireNonceTooLow(t, f.apply(101, reg), "the same signed register submitted again")
	if got := f.account(t).Nonce; got != 1 {
		t.Fatalf("a rejected replay moved the nonce to %d", got)
	}
}

func TestValidatorFlagNonce_AtTheHeightADeregisterConsumesTheNonce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	if err := f.apply(100, f.register(t, 0)); err != nil {
		t.Fatalf("register: %v", err)
	}
	dereg := f.deregister(t, 1)
	if err := f.apply(101, dereg); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	acct := f.account(t)
	if acct.Nonce != 2 {
		t.Fatalf("nonce after register and deregister: %d, want 2", acct.Nonce)
	}
	if acct.ValidatorRegistered {
		t.Fatalf("the deregister must still clear the flag")
	}
	requireNonceTooLow(t, f.apply(102, dereg), "the same signed deregister submitted again")
}

// The attack: the owner registers, later deregisters; anyone holding the first
// signed bytes registers the key again.
func TestValidatorFlagNonce_AStaleRegisterCannotUndoALaterDeregister(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	reg := f.register(t, 0)
	if err := f.apply(100, reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := f.apply(101, f.deregister(t, 1)); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	requireNonceTooLow(t, f.apply(102, reg), "a recorded register replayed after the deregister")
	if f.account(t).ValidatorRegistered {
		t.Fatalf("the replay re-registered the validator")
	}
}

// A register for a key that is already registered, and a deregister for one that is
// not, change nothing but must still consume the nonce.
func TestValidatorFlagNonce_ANoOpRegisterStillConsumesTheNonce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	if err := f.apply(100, f.register(t, 0)); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := f.apply(101, f.register(t, 1)); err != nil {
		t.Fatalf("second register: %v", err)
	}
	if got := f.account(t).Nonce; got != 2 {
		t.Fatalf("nonce after a register of an already registered key: %d, want 2", got)
	}
}

func TestValidatorFlagNonce_ANoOpDeregisterStillConsumesTheNonce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	if err := f.apply(100, f.deregister(t, 0)); err != nil {
		t.Fatalf("deregister of a key that is not registered: %v", err)
	}
	if got := f.account(t).Nonce; got != 1 {
		t.Fatalf("nonce after a deregister of an unregistered key: %d, want 1", got)
	}
}

// The value-bearing paths already advance the nonce exactly once; the new rule must
// not advance it a second time.
func TestValidatorFlagNonce_ValueBearingPathsAdvanceTheNonceExactlyOnce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	stake := f.signed(t, types.TxTypeStake, 0, 1_000, mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}))
	if err := f.apply(100, stake); err != nil {
		t.Fatalf("stake and register: %v", err)
	}
	if got := f.account(t).Nonce; got != 1 {
		t.Fatalf("nonce after a stake that also registers: %d, want 1", got)
	}
	unstake := f.signed(t, types.TxTypeUnstake, 1, 500, mustEncodeUnstakePayload(t, unstakePayload{DeregisterValidator: true}))
	if err := f.apply(101, unstake); err != nil {
		t.Fatalf("unstake and deregister: %v", err)
	}
	if got := f.account(t).Nonce; got != 2 {
		t.Fatalf("nonce after an unstake that also deregisters: %d, want 2", got)
	}
}

// A plain stake that carries a payload without the register flag is not a flag
// transaction and is not touched.
func TestValidatorFlagNonce_AStakeWithoutTheRegisterFlagIsNotAFlagTransaction(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	stake := f.signed(t, types.TxTypeStake, 0, 2_000, nil)
	if err := f.apply(100, stake); err != nil {
		t.Fatalf("plain stake: %v", err)
	}
	if got := f.account(t).Nonce; got != 1 {
		t.Fatalf("nonce after a plain stake: %d, want 1", got)
	}
}

// A zero-value stake without the register flag is refused as before and must not
// consume the nonce.
func TestValidatorFlagNonce_ARejectedZeroValueStakeDoesNotConsumeTheNonce(t *testing.T) {
	f := newFlagNonceFixture(t, 100)
	bad := f.signed(t, types.TxTypeStake, 0, 0, nil)
	if err := f.apply(100, bad); err == nil {
		t.Fatalf("a zero-value stake without the register flag was accepted")
	}
	if got := f.account(t).Nonce; got != 0 {
		t.Fatalf("a rejected stake moved the nonce to %d", got)
	}
}

func TestValidatorFlagNonce_TheSettingSurvivesCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetValidatorFlagNonceActivationHeight(11)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.validatorFlagNonceFromHeight != 11 {
		t.Fatalf("Copy lost the setting: %d", copied.validatorFlagNonceFromHeight)
	}
}

func TestNodeValidatorFlagNonce_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetValidatorFlagNonceActivationHeight(5)
	node.stateMu.Lock()
	gotH := node.state.validatorFlagNonceFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.validatorFlagNonceFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 5 {
		t.Fatalf("setter left the processor at %d, want 5", gotH)
	}
	if rebuiltH != 5 {
		t.Fatalf("a rebuilt processor lost the setting: %d, want 5", rebuiltH)
	}
}

// Through real blocks: a node builds, validates and commits a block holding a
// register; from the height the same signed bytes are left out of the next block
// the node builds, and before the height (or with the setting off) they go in.
func nodeFlagNonceFixture(t *testing.T, node *Node) *flagNonceFixture {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	writeAccount(t, node.state, addr, &types.Account{BalanceZNHB: big.NewInt(30_000), Stake: big.NewInt(8_000)})
	return &flagNonceFixture{sp: node.state, key: key, addr: addr}
}

func commitBlockWith(t *testing.T, node *Node, txs ...*types.Transaction) *types.Block {
	t.Helper()
	block, err := node.CreateBlock(txs)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("validate block %d: %v", block.Header.Height, err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block %d: %v", block.Header.Height, err)
	}
	return block
}

func TestNodeValidatorFlagNonce_AReplayedRegisterIsLeftOutOfBlocksFromTheHeight(t *testing.T) {
	node := newTestNode(t)
	f := nodeFlagNonceFixture(t, node)
	node.SetValidatorFlagNonceActivationHeight(3)
	reg := f.register(t, 0)

	commitEmptyBlock(t, node) // 1
	b2 := commitBlockWith(t, node, reg)
	if len(b2.Transactions) != 1 {
		t.Fatalf("block 2 is below the height; the register should be in it: %d transactions", len(b2.Transactions))
	}
	// Below the height the nonce did not move, so the same bytes are valid again.
	b3 := commitBlockWith(t, node, reg)
	if b3.Header.Height != 3 {
		t.Fatalf("expected height 3, got %d", b3.Header.Height)
	}
	if len(b3.Transactions) != 1 {
		t.Fatalf("block 3 (the height itself) should take the register: %d transactions", len(b3.Transactions))
	}
	b4 := commitBlockWith(t, node, reg)
	if len(b4.Transactions) != 0 {
		t.Fatalf("block 4 is past the height and the nonce is spent; the replay must be left out: %d transactions", len(b4.Transactions))
	}
	node.stateMu.Lock()
	acct, err := node.state.getAccount(f.addr[:])
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if acct.Nonce != 1 {
		t.Fatalf("nonce after the run: %d, want 1", acct.Nonce)
	}
}

func TestNodeValidatorFlagNonce_DefaultTakesTheReplayEveryTime(t *testing.T) {
	node := newTestNode(t)
	f := nodeFlagNonceFixture(t, node)
	reg := f.register(t, 0)
	commitEmptyBlock(t, node)
	for i := 0; i < 4; i++ {
		b := commitBlockWith(t, node, reg)
		if len(b.Transactions) != 1 {
			t.Fatalf("with the setting off block %d should still take the register: %d transactions", b.Header.Height, len(b.Transactions))
		}
	}
	node.stateMu.Lock()
	acct, _ := node.state.getAccount(f.addr[:])
	node.stateMu.Unlock()
	if acct.Nonce != 0 {
		t.Fatalf("nonce with the setting off: %d, want 0", acct.Nonce)
	}
}
