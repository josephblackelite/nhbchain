package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
)

// PL-BC-C: the ZNHB bridge escrow. These run the real apply path (ApplyTransaction) on a state processor with an
// admin wallet and bootstrapped pools, and check the supply invariant and the bridge invariant after every step.

const (
	bridgeTestChain = "eip155:8453"
	bridgeTestDest  = "0x1111111111111111111111111111111111111111"
)

func lockPayload() bridgeLockPayload {
	return bridgeLockPayload{DestChain: bridgeTestChain, Dest: bridgeTestDest}
}

func newBridgeFixture(t *testing.T) *bcFixture {
	f := newBCFixture(t, BridgeSettings{ActivationHeight: 100}, AmmSettings{})
	f.fund("alice", wei(1_000), wei(0))
	return f
}

func TestBridgeTypesAreUnknownBeforeTheirHeight(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		height     uint64
	}{{"setting off", 0, 500}, {"one block below the height", 100, 99}} {
		f := newBCFixture(t, BridgeSettings{ActivationHeight: tc.activation}, AmmSettings{})
		f.fund("alice", wei(1_000), wei(0))
		f.atHeight(tc.height)
		before, _ := f.balances(f.addr["alice"])
		for _, typ := range []types.TxType{types.TxTypeBridgeLock, types.TxTypeBridgeRelease} {
			err := f.send("alice", typ, wei(10), lockPayload())
			if !errors.Is(err, ErrUnknownTransactionType) {
				t.Fatalf("%s: type 0x%02X before its height: %v, want an unknown transaction type", tc.name, byte(typ), err)
			}
		}
		after, _ := f.balances(f.addr["alice"])
		if before.Cmp(after) != 0 {
			t.Fatalf("%s: a refused unknown type changed a balance", tc.name)
		}
	}
}

func TestBridgeLockMovesZNHBIntoTheEscrowAndRecordsIt(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.send("alice", types.TxTypeBridgeLock, wei(300), lockPayload()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	znhb, _ := f.balances(f.addr["alice"])
	escrowZ, _ := f.balances(bridgeEscrowAddress())
	if znhb.Cmp(wei(700)) != 0 || escrowZ.Cmp(wei(300)) != 0 {
		t.Fatalf("alice holds %s and the escrow %s, want 700 and 300 ZNHB", znhb, escrowZ)
	}
	view, err := f.sp.BridgeStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if view.LockedWei.Cmp(wei(300)) != 0 || view.ReleasedWei.Sign() != 0 || view.OutstandingWei.Cmp(wei(300)) != 0 || view.NextLockID != 2 {
		t.Fatalf("status %+v after one lock of 300", view)
	}
	found := false
	for _, evt := range f.sp.Events() {
		if evt.Type == "bridge.locked" && evt.Attributes["amountWei"] == wei(300).String() && evt.Attributes["destChain"] == bridgeTestChain && evt.Attributes["id"] == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no bridge.locked event for lock 1")
	}
	acc, _ := f.sp.getAccount(f.addr["alice"])
	if acc.Nonce != 1 {
		t.Fatalf("alice's nonce is %d after a lock, want 1", acc.Nonce)
	}
	f.checkInvariants("after a lock")
}

func TestBridgeLockRefusals(t *testing.T) {
	f := newBridgeFixture(t)
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, MaxLockWei: wei(500), Params: []BridgeParams{f.defaultParams(100)}})
	bad := func(chain, dest string) bridgeLockPayload { return bridgeLockPayload{DestChain: chain, Dest: dest} }
	for name, tc := range map[string]struct {
		value   *big.Int
		payload interface{}
		want    error
	}{
		"destination chain not an eip155 id": {wei(1), bad("solana", bridgeTestDest), ErrBridgeInvalidPayload},
		"destination chain id of zero":       {wei(1), bad("eip155:0", bridgeTestDest), ErrBridgeInvalidPayload},
		"destination address too short":      {wei(1), bad(bridgeTestChain, "0x1234"), ErrBridgeInvalidPayload},
		"destination address not hex":        {wei(1), bad(bridgeTestChain, "0xZZ11111111111111111111111111111111111111"), ErrBridgeInvalidPayload},
		"no destination":                     {wei(1), bad("", ""), ErrBridgeInvalidPayload},
		"payload that does not decode":       {wei(1), []byte{0x01, 0x02}, ErrBridgeInvalidPayload},
		"zero amount":                        {big.NewInt(0), lockPayload(), ErrBridgeInvalidPayload},
		"above the per-lock cap":             {wei(501), lockPayload(), ErrBridgeAboveMax},
	} {
		before, _ := f.balances(f.addr["alice"])
		err := f.send("alice", types.TxTypeBridgeLock, tc.value, tc.payload)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
		after, _ := f.balances(f.addr["alice"])
		if before.Cmp(after) != 0 {
			t.Fatalf("%s: a refused lock changed the sender's balance", name)
		}
	}
	// More than the sender holds, with the cap out of the way, is a balance error (skipped, not pruned).
	f.sp.SetBridge(BridgeSettings{ActivationHeight: 100, Params: []BridgeParams{f.defaultParams(100)}})
	if err := f.send("alice", types.TxTypeBridgeLock, wei(2_000), lockPayload()); !errors.Is(err, ErrBridgeInsufficientBalance) {
		t.Fatalf("a lock above the balance: %v, want ErrBridgeInsufficientBalance", err)
	}
	f.checkInvariants("after refusals")
}

func TestBridgeAdminWalletCannotLockOrReceive(t *testing.T) {
	f := newBridgeFixture(t)
	// Lock from the admin wallet: it has no key here, so check the guard directly on the apply function.
	tx := &types.Transaction{Type: types.TxTypeBridgeLock, Value: wei(1)}
	data, err := rlpEncode(lockPayload())
	if err != nil {
		t.Fatal(err)
	}
	tx.Data = data
	if err := f.sp.applyBridgeLockTransaction(tx, f.admin[:]); !errors.Is(err, ErrBridgeAdminWallet) {
		t.Fatalf("a lock from the admin wallet: %v, want ErrBridgeAdminWallet", err)
	}
	// Release to the admin wallet.
	if err := f.send("alice", types.TxTypeBridgeLock, wei(100), lockPayload()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	err = f.releaseTo("burn-admin", f.admin[:], wei(10))
	if !errors.Is(err, ErrBridgeAdminWallet) {
		t.Fatalf("a release to the admin wallet: %v, want ErrBridgeAdminWallet", err)
	}
	f.checkInvariants("after admin wallet attempts")
}

func TestBridgeReleaseRules(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.send("alice", types.TxTypeBridgeLock, wei(400), lockPayload()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	// One signature of the two needed is not enough, and nobody pays for a refused release.
	if err := f.release("burn-1", "bob", wei(10), "signer1"); !errors.Is(err, ErrBridgeBadSignatures) {
		t.Fatalf("a release with one signer of two: %v, want ErrBridgeBadSignatures", err)
	}
	// A good release pays the recipient and shrinks the escrow; anyone (here a relayer with no role) may send it.
	if err := f.release("burn-1", "bob", wei(150)); err != nil {
		t.Fatalf("release: %v", err)
	}
	bobZ, _ := f.balances(f.addr["bob"])
	escrowZ, _ := f.balances(bridgeEscrowAddress())
	if bobZ.Cmp(wei(150)) != 0 || escrowZ.Cmp(wei(250)) != 0 {
		t.Fatalf("bob holds %s and the escrow %s, want 150 and 250", bobZ, escrowZ)
	}
	// The same burn id cannot be released again, for any amount or recipient.
	if err := f.release("burn-1", "bob", wei(1)); !errors.Is(err, ErrBridgeAlreadyReleased) {
		t.Fatalf("a repeated burn id: %v, want ErrBridgeAlreadyReleased", err)
	}
	if err := f.release("burn-1", "alice", wei(150)); !errors.Is(err, ErrBridgeAlreadyReleased) {
		t.Fatalf("a repeated burn id to another recipient: %v, want ErrBridgeAlreadyReleased", err)
	}
	// More than what is outstanding is refused, and so is a bad burn id or recipient or amount.
	if err := f.release("burn-2", "bob", wei(251)); !errors.Is(err, ErrBridgeEscrowShort) {
		t.Fatalf("a release above the outstanding amount: %v, want ErrBridgeEscrowShort", err)
	}
	for name, payload := range map[string]bridgeReleasePayload{
		"an empty burn id":     {Op: BridgeOpRelease, Recipient: f.addr["bob"], Amount: wei(1)},
		"a short recipient":    {Op: BridgeOpRelease, BurnID: []byte("burn-3"), Recipient: []byte{1, 2}, Amount: wei(1)},
		"a zero release":       {Op: BridgeOpRelease, BurnID: []byte("burn-4"), Recipient: f.addr["bob"], Amount: big.NewInt(0)},
		"no amount":            {Op: BridgeOpRelease, BurnID: []byte("burn-4"), Recipient: f.addr["bob"]},
		"an unknown operation": {Op: 9, BurnID: []byte("burn-4"), Recipient: f.addr["bob"], Amount: wei(1)},
	} {
		if err := f.send("relayer", types.TxTypeBridgeRelease, big.NewInt(0), payload); !errors.Is(err, ErrBridgeInvalidPayload) {
			t.Fatalf("%s: %v, want ErrBridgeInvalidPayload", name, err)
		}
	}
	// A release carries its amount in the payload: a value on the transaction is refused.
	if err := f.send("relayer", types.TxTypeBridgeRelease, wei(1), f.releasePayload("burn-6", f.addr["bob"], wei(1), "signer1", "signer2")); !errors.Is(err, ErrBridgeInvalidPayload) {
		t.Fatalf("a release with a transaction value: %v, want ErrBridgeInvalidPayload", err)
	}
	// Releasing the rest is allowed, and the books close at zero.
	if err := f.release("burn-5", "bob", wei(250)); err != nil {
		t.Fatalf("release of the rest: %v", err)
	}
	view, _ := f.sp.BridgeStatus()
	if view.OutstandingWei.Sign() != 0 || view.EscrowBalanceWei.Sign() != 0 || view.ReleasedWei.Cmp(wei(400)) != 0 {
		t.Fatalf("after releasing everything: %+v", view)
	}
	f.checkInvariants("after releases")
}

// A plain transfer to the escrow address is a gift to the bridge: the escrow holds more than was locked, and a release
// still cannot exceed what was locked and not yet released.
func TestBridgeGiftToTheEscrowIsNotReleasable(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.send("alice", types.TxTypeBridgeLock, wei(100), lockPayload()); err != nil {
		t.Fatalf("lock: %v", err)
	}
	f.fund("bob", wei(0), wei(0))
	escrow, err := f.sp.getAccount(bridgeEscrowAddress())
	if err != nil {
		t.Fatal(err)
	}
	escrow.BalanceZNHB = new(big.Int).Add(escrow.BalanceZNHB, wei(50)) // the gift
	if err := f.sp.setAccount(bridgeEscrowAddress(), escrow); err != nil {
		t.Fatal(err)
	}
	err = f.release("burn-gift", "bob", wei(120))
	if !errors.Is(err, ErrBridgeEscrowShort) {
		t.Fatalf("a release above the outstanding amount while the escrow holds a gift: %v, want ErrBridgeEscrowShort", err)
	}
	if err := f.sp.CheckBridgeInvariant(); err != nil {
		t.Fatalf("a gift must not break the bridge invariant: %v", err)
	}
}

// The escrow never touches the Sale Pool or Reward Pool counters, whoever locks.
func TestBridgeNeverMovesThePoolCounters(t *testing.T) {
	f := newBridgeFixture(t)
	before := rewardPoolBalance(t, f.sp)
	for i := 0; i < 3; i++ {
		if err := f.send("alice", types.TxTypeBridgeLock, wei(10), lockPayload()); err != nil {
			t.Fatalf("lock %d: %v", i, err)
		}
	}
	if err := f.release("pool-check", "bob", wei(25)); err != nil {
		t.Fatalf("release: %v", err)
	}
	if after := rewardPoolBalance(t, f.sp); after.Cmp(before) != 0 {
		t.Fatalf("the reward pool counter moved from %s to %s", before, after)
	}
	f.checkInvariants("after several operations")
}

// Every refusal is classified: none may fall to the default quarantine (ledger C12).
func TestBridgeAndPoolErrorsAreAllClassified(t *testing.T) {
	prune := []error{ErrBridgeInvalidPayload, ErrBridgeAdminWallet, ErrBridgeAboveMax, ErrBridgeAboveMaxRelease, ErrBridgeNotGuardian, ErrBridgeBadSignatures, ErrBridgeAlreadyReleased, ErrAmmInvalidPayload, ErrAmmUnauthorized, ErrAmmAdminWallet}
	skip := []error{ErrBridgeInsufficientBalance, ErrBridgeEscrowShort, ErrBridgePaused, ErrBridgeWindowCap, ErrBridgeNotPending, ErrBridgeNotYet, ErrAmmSwapsClosed, ErrAmmInsufficientBalance, ErrAmmSlippage, ErrAmmTooLarge, ErrAmmReserveFloor, ErrAmmEmptyPool, ErrAmmZeroOutput}
	for _, e := range prune {
		wrapped := errors.Join(errors.New("apply"), e)
		if got := classifyProposalError(wrapped); got != proposalDispositionPrune {
			t.Fatalf("%v is classified %v, want prune", e, got)
		}
	}
	for _, e := range skip {
		wrapped := errors.Join(errors.New("apply"), e)
		if got := classifyProposalError(wrapped); got != proposalDispositionSkip {
			t.Fatalf("%v is classified %v, want skip", e, got)
		}
	}
	// An error nobody classified still quarantines: the helper must not swallow unknown errors.
	if got := classifyProposalError(errors.New("something else")); got == proposalDispositionPrune || got == proposalDispositionSkip {
		t.Fatalf("an unclassified error became %v", got)
	}
}

func TestBridgeSettingsAreCopiedAndSurviveARebuild(t *testing.T) {
	f := newBridgeFixture(t)
	cp, err := f.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.bridge.ActivationHeight != 100 || len(cp.bridge.Params) != 1 || cp.bridge.Params[0].Threshold != 2 {
		t.Fatalf("the copy dropped the bridge settings: %+v", cp.bridge)
	}
	node := newTestNode(t)
	node.SetBridgeAndAmm(BridgeSettings{ActivationHeight: 9, Params: []BridgeParams{testBridgeParams(9)}}, AmmSettings{ActivationHeight: 11})
	node.stateMu.Lock()
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	b, a := node.state.bridge.ActivationHeight, node.state.amm.ActivationHeight
	rules := len(node.state.bridge.Params)
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if b != 9 || a != 11 || rules != 1 {
		t.Fatalf("a rebuilt processor holds bridge %d (%d rule sets) and pool %d, want 9 (1) and 11", b, rules, a)
	}
}

func TestBridgeAmmSettingsFromConfig(t *testing.T) {
	// Off blocks are not looked at, however unfinished.
	cfgOff := newTestConfigForBridge()
	cfgOff.Bridge.Guardian = "not an address"
	cfgOff.Bridge.Signers = []string{"nope"}
	cfgOff.Amm.LiquidityProvider = "also not"
	if b, a, err := BridgeAmmSettingsFromConfig(cfgOff); err != nil || b.ActivationHeight != 0 || a.ActivationHeight != 0 {
		t.Fatalf("an unactivated block must be ignored: %+v %+v %v", b, a, err)
	}
	// An activated block with a bad value is an error (the mains refuse to start).
	cfgBad := newTestConfigForBridge()
	cfgBad.Bridge.BridgeActivationHeight = 5
	cfgBad.Bridge.Signers = []string{"nope", "nope too"}
	if _, _, err := BridgeAmmSettingsFromConfig(cfgBad); err == nil || !strings.Contains(err.Error(), "Signers") {
		t.Fatalf("an activated bridge with bad signers: %v", err)
	}
	cfgBadAmm := newTestConfigForBridge()
	cfgBadAmm.Amm.AmmActivationHeight = 5
	cfgBadAmm.Amm.LiquidityProvider = ""
	if _, _, err := BridgeAmmSettingsFromConfig(cfgBadAmm); err == nil {
		t.Fatalf("an activated pool with no provider was accepted")
	}
}
