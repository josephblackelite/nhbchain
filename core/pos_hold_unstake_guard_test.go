package core

import (
	"bytes"
	"errors"
	"math/big"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	posv1 "nhbchain/proto/pos"
)

// A POS hold and a delegation draw on the same LockedZNHB. These tests cover the
// guard that stops StakeUndelegate from unbonding the part of it that pending
// holds still need, which only applies from an activation height and is off by
// default.

const (
	posGuardActivation = uint64(100)
	posGuardMinStake   = 5_000
)

// posGuardFixture is a validator funded only by delegations, a delegator that
// is not otherwise involved, and a holder that has both delegated and placed a
// POS hold. The holder has delegated 100 and holds 6000, so its locked balance is
// 6100 of which only 100 is stake; the validator's stake is 5100 against a
// minimum of 5000. other holds an unrelated hold that expires after the
// holder's.
type posGuardFixture struct {
	t         *testing.T
	sp        *StateProcessor
	lifecycle *pos.Lifecycle
	now       time.Time

	validator, honest, holder, other, merchant [20]byte
	holderHold, otherHold                      [32]byte
}

// newPOSGuardFixture builds the fixture with the guard configured for
// activation and the chain at height. The holds are placed at that height.
func newPOSGuardFixture(t *testing.T, activation, height uint64) *posGuardFixture {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetPOSHoldUnstakeGuardActivationHeight(activation)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(posGuardMinStake)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	f := &posGuardFixture{t: t, sp: sp, now: time.Unix(1_700_000_000, 0).UTC()}
	f.validator[19], f.honest[19], f.holder[19], f.other[19], f.merchant[19] = 0xc1, 0xc2, 0xc3, 0xc4, 0xc5
	writeAccount(t, sp, f.validator, &types.Account{BalanceZNHB: big.NewInt(0)})
	writeAccount(t, sp, f.honest, &types.Account{BalanceZNHB: big.NewInt(50_000)})
	writeAccount(t, sp, f.holder, &types.Account{BalanceZNHB: big.NewInt(20_000)})
	writeAccount(t, sp, f.other, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	writeAccount(t, sp, f.merchant, &types.Account{BalanceZNHB: big.NewInt(0)})

	sp.BeginBlock(height, f.now)
	if err := sp.applyStake(&types.Transaction{Value: big.NewInt(0), Data: mustEncodeStakePayload(t, stakePayload{RegisterValidator: true})}, f.validator[:]); err != nil {
		t.Fatalf("register validator: %v", err)
	}
	if _, err := sp.StakeDelegate(f.honest[:], f.validator[:], big.NewInt(posGuardMinStake)); err != nil {
		t.Fatalf("honest delegation: %v", err)
	}
	if _, err := sp.StakeDelegate(f.holder[:], f.validator[:], big.NewInt(100)); err != nil {
		t.Fatalf("holder delegation: %v", err)
	}
	if !f.eligible() {
		t.Fatalf("the delegation-funded validator is not eligible before the test starts")
	}

	f.lifecycle = pos.NewLifecycle(manager)
	f.lifecycle.SetNowFunc(func() time.Time { return sp.blockTimestamp() })
	f.holderHold = f.authorize(f.holder, 6_000, time.Minute)
	f.otherHold = f.authorize(f.other, 1_000, 2*time.Minute)
	return f
}

func (f *posGuardFixture) authorize(payer [20]byte, amount int64, expiresIn time.Duration) [32]byte {
	f.t.Helper()
	auth, err := f.lifecycle.Authorize(payer, f.merchant, big.NewInt(amount), uint64(f.now.Add(expiresIn).Unix()), nil)
	if err != nil {
		f.t.Fatalf("authorize: %v", err)
	}
	return auth.ID
}

func (f *posGuardFixture) eligible() bool {
	_, ok := f.sp.EligibleValidators[string(f.validator[:])]
	return ok
}

func (f *posGuardFixture) account(addr [20]byte) *types.Account {
	f.t.Helper()
	acc, err := f.sp.getAccount(addr[:])
	if err != nil {
		f.t.Fatalf("load account: %v", err)
	}
	return acc
}

// atHeight moves the block context to height, the given time after the fixture's
// start.
func (f *posGuardFixture) atHeight(height uint64, after time.Duration) {
	f.sp.BeginBlock(height, f.now.Add(after))
}

func (f *posGuardFixture) requireLocked(addr [20]byte, want int64) {
	f.t.Helper()
	if got := f.account(addr).LockedZNHB; got.Cmp(big.NewInt(want)) != 0 {
		f.t.Fatalf("locked balance is %s, want %d", got, want)
	}
}

func (f *posGuardFixture) requireValidatorStake(want int64) {
	f.t.Helper()
	if got := f.account(f.validator).Stake; got.Cmp(big.NewInt(want)) != 0 {
		f.t.Fatalf("validator stake is %s, want %d", got, want)
	}
}

func (f *posGuardFixture) holdStatus(id [32]byte) pos.AuthorizationStatus {
	f.t.Helper()
	auth, err := f.lifecycle.Get(id)
	if err != nil {
		f.t.Fatalf("load hold: %v", err)
	}
	return auth.Status
}

// With the guard off, by default or below its height, the unstake rules are the
// ones they always were: an unstake of the whole locked balance goes through and
// leaves a hold its payer cannot cover.
func TestPOSHoldUnstakeGuardOffKeepsTheOldRules(t *testing.T) {
	for _, tc := range []struct {
		name               string
		activation, height uint64
	}{
		{"default", 0, 500},
		{"below the activation height", posGuardActivation, posGuardActivation - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSGuardFixture(t, tc.activation, tc.height)
			if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(5_100)); err != nil {
				t.Fatalf("unstake of the whole locked balance refused with the guard off: %v", err)
			}
			f.requireValidatorStake(0)
			if f.eligible() {
				t.Fatalf("the validator stayed eligible although its stake is gone")
			}
			f.requireLocked(f.holder, 1_000)
			if _, err := f.sp.StakeUndelegate(f.honest[:], big.NewInt(posGuardMinStake)); err == nil {
				t.Fatalf("the honest delegator could unstake from a validator whose stake was taken")
			}

			// The hold is pending for 6000 against 1000 locked: it can be neither
			// released by its payer nor swept once it expires.
			if _, err := f.lifecycle.Void(f.holderHold, "manual", f.holder); err == nil {
				t.Fatalf("a hold the payer cannot cover was voided")
			}
			f.atHeight(tc.height+1, 10*time.Minute)
			// The sweep skip is a separate setting and is off here, so the sweep
			// ends at the hold it cannot release with the locked-balance error.
			if _, err := f.sp.SweepExpiredPOSAuthorizations(f.now.Add(10 * time.Minute)); err == nil || err.Error() != "pos: locked balance inconsistent" {
				t.Fatalf("sweep: got %v, want the locked-balance error", err)
			}
			if f.holdStatus(f.holderHold) != pos.AuthorizationStatusPending {
				t.Fatalf("the uncovered hold was released by the sweep")
			}
		})
	}
}

// From the activation height the same unstake is refused, nothing it would have
// touched changes, and the amount that is really delegated can still be taken.
func TestPOSHoldUnstakeGuardRefusesTakingFundsThatBackHolds(t *testing.T) {
	f := newPOSGuardFixture(t, posGuardActivation, posGuardActivation)

	for _, amount := range []int64{5_100, 6_100, 101} {
		if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(amount)); !errors.Is(err, ErrStakeBackedByPOSHold) {
			t.Fatalf("unstake of %d: got %v, want ErrStakeBackedByPOSHold", amount, err)
		}
	}
	f.requireValidatorStake(5_100)
	f.requireLocked(f.holder, 6_100)
	if !f.eligible() {
		t.Fatalf("a refused unstake removed the validator")
	}
	if len(f.account(f.holder).PendingUnbonds) != 0 {
		t.Fatalf("a refused unstake started an unbonding")
	}

	// What holder delegated is its own to take, in pieces too.
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(40)); err != nil {
		t.Fatalf("unstake of delegated funds: %v", err)
	}
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(61)); !errors.Is(err, ErrStakeBackedByPOSHold) {
		t.Fatalf("unstake past the delegated remainder: got %v, want ErrStakeBackedByPOSHold", err)
	}
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(60)); err != nil {
		t.Fatalf("unstake of the delegated remainder: %v", err)
	}
	f.requireValidatorStake(5_000)
	f.requireLocked(f.holder, 6_000)
	if !f.eligible() {
		t.Fatalf("the validator lost eligibility although only delegated funds were unstaked")
	}

	// The hold stays covered and the expiry sweep releases it with the other
	// payer's, in one block.
	f.atHeight(posGuardActivation+1, 3*time.Minute)
	if n, err := f.sp.SweepExpiredPOSAuthorizations(f.now.Add(3 * time.Minute)); err != nil || n != 2 {
		t.Fatalf("sweep released %d holds, err %v, want 2", n, err)
	}
	f.requireLocked(f.holder, 0)
	// 20000 funded, 100 delegated, 6000 held and released again.
	if got := f.account(f.holder).BalanceZNHB; got.Cmp(big.NewInt(19_900)) != 0 {
		t.Fatalf("payer balance after the sweep is %s, want 19900", got)
	}
	f.requireValidatorStake(5_000)
}

// The rule starts at its height and not before, and stays on after it.
func TestPOSHoldUnstakeGuardStartsAtItsHeight(t *testing.T) {
	for _, tc := range []struct {
		height  uint64
		refused bool
	}{
		{posGuardActivation - 1, false},
		{posGuardActivation, true},
		{posGuardActivation + 1, true},
		{posGuardActivation + 10_000, true},
	} {
		f := newPOSGuardFixture(t, posGuardActivation, tc.height)
		_, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(5_100))
		if refused := errors.Is(err, ErrStakeBackedByPOSHold); refused != tc.refused || (err != nil) != tc.refused {
			t.Fatalf("height %d: err %v, refused=%v want %v", tc.height, err, refused, tc.refused)
		}
	}
}

// Holds and delegations that exist before the activation height are protected
// from the first block at it: the guard reads the pending holds themselves, so
// there is nothing to backfill and no block in which a pre-existing hold is
// unprotected.
func TestPOSHoldUnstakeGuardCoversHoldsPlacedBeforeActivation(t *testing.T) {
	f := newPOSGuardFixture(t, posGuardActivation, 50)
	f.atHeight(posGuardActivation, time.Second)
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(5_100)); !errors.Is(err, ErrStakeBackedByPOSHold) {
		t.Fatalf("a hold placed before the activation height was not protected: %v", err)
	}
	f.requireValidatorStake(5_100)
}

// Funds come free again as the holds that backed them are captured, voided or
// expire; the guard follows the pending set and nothing else.
func TestPOSHoldUnstakeGuardFreesFundsWhenHoldsClear(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release func(f *posGuardFixture)
	}{
		{"captured", func(f *posGuardFixture) {
			if _, err := f.lifecycle.Capture(f.holderHold, big.NewInt(2_500), f.merchant); err != nil {
				t.Fatalf("capture: %v", err)
			}
		}},
		{"voided", func(f *posGuardFixture) {
			if _, err := f.lifecycle.Void(f.holderHold, "manual", f.holder); err != nil {
				t.Fatalf("void: %v", err)
			}
		}},
		{"expired", func(f *posGuardFixture) {
			f.atHeight(posGuardActivation+1, 3*time.Minute)
			if _, err := f.sp.SweepExpiredPOSAuthorizations(f.now.Add(3 * time.Minute)); err != nil {
				t.Fatalf("sweep: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSGuardFixture(t, posGuardActivation, posGuardActivation)
			held, err := f.lifecycle.PendingHeld(f.holder)
			if err != nil || held.Cmp(big.NewInt(6_000)) != 0 {
				t.Fatalf("held before: %v, %v, want 6000", held, err)
			}
			tc.release(f)
			if held, err = f.lifecycle.PendingHeld(f.holder); err != nil || held.Sign() != 0 {
				t.Fatalf("held after: %v, %v, want 0", held, err)
			}
			f.requireLocked(f.holder, 100)
			// 100 is all that is left of the locked balance, and all of it is
			// delegated: the old rule applies again.
			if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(101)); err == nil || errors.Is(err, ErrStakeBackedByPOSHold) {
				t.Fatalf("unstake past the locked balance: got %v, want the insufficient-stake error", err)
			}
			if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(100)); err != nil {
				t.Fatalf("unstake of the delegated funds after the hold cleared: %v", err)
			}
			f.requireValidatorStake(5_000)
		})
	}
}

// A hold added later narrows what can be unstaked at once; a payer with
// several holds is held to their sum.
func TestPOSHoldUnstakeGuardSumsAllPendingHolds(t *testing.T) {
	f := newPOSGuardFixture(t, posGuardActivation, posGuardActivation)
	f.authorize(f.holder, 3_000, time.Hour)
	f.requireLocked(f.holder, 9_100)
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(101)); !errors.Is(err, ErrStakeBackedByPOSHold) {
		t.Fatalf("unstake past the delegated 100: got %v", err)
	}
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(100)); err != nil {
		t.Fatalf("unstake of the delegated 100: %v", err)
	}
	f.requireLocked(f.holder, 9_000)
}

// A payer whose holds already exceed its locked balance when the guard turns on
// (left that way by an unstake before the height) cannot unstake what is left,
// since all of it counts as backing the holds. The payer repairs it by
// delegating more, after which the hold can be released again.
func TestPOSHoldUnstakeGuardWithHoldsAlreadyAboveTheLockedBalance(t *testing.T) {
	f := newPOSGuardFixture(t, posGuardActivation, posGuardActivation-10)
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(5_100)); err != nil {
		t.Fatalf("unstake of the whole locked balance before the activation height: %v", err)
	}
	f.requireLocked(f.holder, 1_000)

	f.atHeight(posGuardActivation, time.Second)
	if _, err := f.sp.StakeUndelegate(f.holder[:], big.NewInt(1)); !errors.Is(err, ErrStakeBackedByPOSHold) {
		t.Fatalf("unstake while the hold is uncovered: got %v, want ErrStakeBackedByPOSHold", err)
	}
	if _, err := f.lifecycle.Void(f.holderHold, "manual", f.holder); err == nil {
		t.Fatalf("an uncovered hold was voided")
	}

	if _, err := f.sp.StakeDelegate(f.holder[:], f.validator[:], big.NewInt(5_000)); err != nil {
		t.Fatalf("repairing delegation: %v", err)
	}
	if _, err := f.lifecycle.Void(f.holderHold, "manual", f.holder); err != nil {
		t.Fatalf("void after the repair: %v", err)
	}
	if f.holdStatus(f.holderHold) != pos.AuthorizationStatusVoided {
		t.Fatalf("the hold is not voided")
	}
}

// A validator that stakes on itself is held to the same rule: its own hold may
// not be unbonded out of the stake that others delegated to it.
func TestPOSHoldUnstakeGuardForASelfStakingValidator(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		height     uint64
		refused    bool
	}{
		{"guard off", 0, 500, false},
		{"guard on", posGuardActivation, posGuardActivation, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPOSGuardFixture(t, tc.activation, tc.height)
			var v, delegator [20]byte
			v[19], delegator[19] = 0xc9, 0xca
			writeAccount(t, f.sp, v, &types.Account{BalanceZNHB: big.NewInt(10_000)})
			writeAccount(t, f.sp, delegator, &types.Account{BalanceZNHB: big.NewInt(10_000)})
			if _, err := f.sp.StakeDelegate(v[:], nil, big.NewInt(1_000)); err != nil {
				t.Fatalf("self-stake: %v", err)
			}
			if _, err := f.sp.StakeDelegate(delegator[:], v[:], big.NewInt(5_000)); err != nil {
				t.Fatalf("delegation to the self-staker: %v", err)
			}
			f.authorize(v, 6_000, time.Hour)
			if got := f.account(v).Stake; got.Cmp(big.NewInt(6_000)) != 0 {
				t.Fatalf("self-staker stake is %s, want 6000 (1000 own, 5000 delegated)", got)
			}

			_, err := f.sp.StakeUndelegate(v[:], big.NewInt(6_000))
			if refused := errors.Is(err, ErrStakeBackedByPOSHold); refused != tc.refused || (err != nil) != tc.refused {
				t.Fatalf("unstake of 6000: err %v, refused=%v want %v", err, refused, tc.refused)
			}
			if tc.refused {
				if _, err := f.sp.StakeUndelegate(v[:], big.NewInt(1_000)); err != nil {
					t.Fatalf("unstake of the validator's own 1000: %v", err)
				}
				if got := f.account(v).Stake; got.Cmp(big.NewInt(5_000)) != 0 {
					t.Fatalf("delegated stake after the own unstake is %s, want 5000", got)
				}
			} else if got := f.account(v).Stake; got.Sign() != 0 {
				t.Fatalf("with the guard off the delegated stake was taken: %s left", got)
			}
		})
	}
}

// posGuardRun applies the same blocks on a fresh fixture and records, for each
// unstake attempt, its error text, then returns the pending state root.
func posGuardRun(t *testing.T, activation uint64) (string, []string) {
	t.Helper()
	f := newPOSGuardFixture(t, activation, posGuardActivation)
	var results []string
	attempt := func(addr [20]byte, amount int64) {
		_, err := f.sp.StakeUndelegate(addr[:], big.NewInt(amount))
		if err != nil {
			results = append(results, err.Error())
		} else {
			results = append(results, "ok")
		}
	}
	attempt(f.holder, 5_100)
	attempt(f.holder, 100)
	attempt(f.honest, 5_000)
	f.atHeight(posGuardActivation+1, 3*time.Minute)
	if _, err := f.sp.SweepExpiredPOSAuthorizations(f.now.Add(3 * time.Minute)); err != nil {
		results = append(results, "sweep: "+err.Error())
	}
	return f.sp.PendingRoot().Hex(), results
}

// Two state processors configured alike reach the same root and the same
// verdicts, with the guard on or off; configured differently they diverge on a
// block that carries an unstake the guard refuses, which is why every validator
// must run the same height.
func TestPOSHoldUnstakeGuardAgreesBetweenStateProcessors(t *testing.T) {
	onA, onResultsA := posGuardRun(t, posGuardActivation)
	onB, onResultsB := posGuardRun(t, posGuardActivation)
	offA, offResultsA := posGuardRun(t, 0)
	offB, offResultsB := posGuardRun(t, 0)
	if onA != onB || len(onResultsA) != len(onResultsB) {
		t.Fatalf("two processors with the guard on disagree: %s vs %s", onA, onB)
	}
	if offA != offB {
		t.Fatalf("two processors with the guard off disagree: %s vs %s", offA, offB)
	}
	for i := range onResultsA {
		if onResultsA[i] != onResultsB[i] {
			t.Fatalf("verdict %d differs with the guard on: %q vs %q", i, onResultsA[i], onResultsB[i])
		}
	}
	for i := range offResultsA {
		if offResultsA[i] != offResultsB[i] {
			t.Fatalf("verdict %d differs with the guard off: %q vs %q", i, offResultsA[i], offResultsB[i])
		}
	}
	if onResultsA[0] != ErrStakeBackedByPOSHold.Error() || offResultsA[0] != "ok" {
		t.Fatalf("first unstake: guard on %q, guard off %q", onResultsA[0], offResultsA[0])
	}
	if onA == offA {
		t.Fatalf("guard on and off reached the same root although an unstake was refused")
	}
}

// A speculative copy of a state processor, which is what block validation and
// proposal run on, applies the same rule.
func TestPOSHoldUnstakeGuardSurvivesStateProcessorCopy(t *testing.T) {
	f := newPOSGuardFixture(t, posGuardActivation, posGuardActivation)
	cp, err := f.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	cp.BeginBlock(posGuardActivation, f.now)
	if _, err := cp.StakeUndelegate(f.holder[:], big.NewInt(5_100)); !errors.Is(err, ErrStakeBackedByPOSHold) {
		t.Fatalf("a copy lost the guard: %v", err)
	}
	off := newPOSGuardFixture(t, 0, 500)
	cpOff, err := off.sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	cpOff.BeginBlock(500, off.now)
	if _, err := cpOff.StakeUndelegate(off.holder[:], big.NewInt(5_100)); err != nil {
		t.Fatalf("a copy of a processor with the guard off refused: %v", err)
	}
}

// The node-level setting reaches the processor, survives a rebuild of it, and
// stays off until set.
func TestPOSHoldUnstakeGuardNodeSetting(t *testing.T) {
	node := newTestNode(t)
	if node.state.posHoldUnstakeGuardFromHeight != 0 || node.posHoldUnstakeGuardHeight != 0 {
		t.Fatalf("the guard is not off by default")
	}
	node.SetPOSHoldUnstakeGuardActivationHeight(posGuardActivation)
	if node.state.posHoldUnstakeGuardFromHeight != posGuardActivation {
		t.Fatalf("the node did not pass the height to its state processor")
	}
	node.stateMu.Lock()
	err := node.rebuildStateProcessorLocked(node.state.CurrentRoot())
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if node.state.posHoldUnstakeGuardFromHeight != posGuardActivation {
		t.Fatalf("a rebuilt state processor lost the height")
	}
	node.SetPOSHoldUnstakeGuardActivationHeight(0)
	if node.state.posHoldUnstakeGuardFromHeight != 0 {
		t.Fatalf("the height could not be cleared")
	}
}

func signedPOSGuardTx(t *testing.T, key *crypto.PrivateKey, typ types.TxType, nonce uint64, value *big.Int, data []byte) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     typ,
		Nonce:    nonce,
		Data:     data,
		Value:    value,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tx
}

// The same case through signed transactions, as the node receives them. With the
// guard off, an unstake of more than was delegated succeeds and leaves a hold its
// payer cannot cover; from the activation height it is refused and the hold stays
// coverable.
func TestPOSHoldUnstakeGuardWithSignedTransactions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
	}{
		{"guard off", 0},
		{"guard on", posGuardActivation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			node.SetPOSHoldUnstakeGuardActivationHeight(tc.activation)
			sp := node.state
			if err := nhbstate.NewManager(sp.Trie).SetMinimumValidatorStake(big.NewInt(posGuardMinStake)); err != nil {
				t.Fatalf("set minimum stake: %v", err)
			}
			newKey := func() (*crypto.PrivateKey, [20]byte) {
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Fatalf("generate key: %v", err)
				}
				return key, toAddress(key)
			}
			validatorKey, validator := newKey()
			honestKey, honest := newKey()
			holderKey, holder := newKey()
			_, merchant := newKey()
			fundAccount(t, node, honest, big.NewInt(50_000))
			fundAccount(t, node, holder, big.NewInt(10_000))
			ts := time.Unix(1_700_000_000, 0).UTC()
			sp.BeginBlock(posGuardActivation, ts)
			defer sp.EndBlock()

			nonce := func(addr [20]byte) uint64 {
				acc, err := sp.getAccount(addr[:])
				if err != nil {
					t.Fatalf("load account: %v", err)
				}
				return acc.Nonce
			}
			apply := func(tx *types.Transaction) error { return sp.ApplyTransaction(tx) }
			must := func(tx *types.Transaction, what string) {
				t.Helper()
				if err := apply(tx); err != nil {
					t.Fatalf("%s: %v", what, err)
				}
			}
			stakeTx := func(key *crypto.PrivateKey, addr [20]byte, amount int64, payload stakePayload) *types.Transaction {
				return signedPOSGuardTx(t, key, types.TxTypeStake, nonce(addr), big.NewInt(amount), mustEncodeStakePayload(t, payload))
			}
			must(stakeTx(validatorKey, validator, 0, stakePayload{RegisterValidator: true}), "register validator")
			must(stakeTx(honestKey, honest, posGuardMinStake, stakePayload{Validator: validator[:]}), "honest delegation")
			must(stakeTx(holderKey, holder, 100, stakePayload{Validator: validator[:]}), "holder delegation")
			authorize, err := proto.Marshal(&posv1.MsgAuthorizePayment{
				Payer:    crypto.MustNewAddress(crypto.ZNHBPrefix, holder[:]).String(),
				Merchant: crypto.MustNewAddress(crypto.ZNHBPrefix, merchant[:]).String(),
				Amount:   "6000",
				Expiry:   uint64(ts.Add(time.Hour).Unix()),
			})
			if err != nil {
				t.Fatalf("marshal authorize: %v", err)
			}
			must(signedPOSGuardTx(t, holderKey, types.TxTypePOSAuthorize, nonce(holder), big.NewInt(0), authorize), "authorize")

			unstake := signedPOSGuardTx(t, holderKey, types.TxTypeUnstake, nonce(holder), big.NewInt(5_100), nil)
			err = apply(unstake)
			validatorAcc, loadErr := sp.getAccount(validator[:])
			if loadErr != nil {
				t.Fatalf("load validator: %v", loadErr)
			}
			holderAcc, loadErr := sp.getAccount(holder[:])
			if loadErr != nil {
				t.Fatalf("load holder: %v", loadErr)
			}
			held, heldErr := pos.NewLifecycle(nhbstate.NewManager(sp.Trie)).PendingHeld(holder)
			if heldErr != nil {
				t.Fatalf("held: %v", heldErr)
			}
			if tc.activation == 0 {
				if err != nil {
					t.Fatalf("the unstake of the whole locked balance was refused with the guard off: %v", err)
				}
				if validatorAcc.Stake.Sign() != 0 || holderAcc.LockedZNHB.Cmp(held) >= 0 {
					t.Fatalf("expected an uncoverable hold: validator stake %s, locked %s, held %s", validatorAcc.Stake, holderAcc.LockedZNHB, held)
				}
				return
			}
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(ErrStakeBackedByPOSHold.Error())) {
				t.Fatalf("the unstake of the whole locked balance was not refused for backing a hold: %v", err)
			}
			if validatorAcc.Stake.Cmp(big.NewInt(5_100)) != 0 || holderAcc.LockedZNHB.Cmp(big.NewInt(6_100)) != 0 || held.Cmp(big.NewInt(6_000)) != 0 {
				t.Fatalf("a refused unstake changed state: validator stake %s, locked %s, held %s", validatorAcc.Stake, holderAcc.LockedZNHB, held)
			}
			if holderAcc.Nonce != nonce(holder) {
				t.Fatalf("nonce moved")
			}
			must(signedPOSGuardTx(t, holderKey, types.TxTypeUnstake, nonce(holder), big.NewInt(100), nil), "unstake of the delegated 100")
		})
	}
}
