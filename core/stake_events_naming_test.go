package core

import (
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func fixedStakeKey(t *testing.T, seed byte) *crypto.PrivateKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	key, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		t.Fatalf("private key from bytes: %v", err)
	}
	return key
}

// describeEvents renders an event log one line per event, attributes sorted,
// so two logs can be compared as text.
func describeEvents(log []types.Event) []string {
	lines := make([]string, 0, len(log))
	for _, evt := range log {
		keys := make([]string, 0, len(evt.Attributes))
		for k := range evt.Attributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+evt.Attributes[k])
		}
		lines = append(lines, evt.Type+" "+strings.Join(parts, " "))
	}
	return lines
}

func stakeTx(t *testing.T, key *crypto.PrivateKey, txType types.TxType, nonce uint64, value int64) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: txType, Nonce: nonce, GasLimit: 21000, GasPrice: big.NewInt(1)}
	if value > 0 {
		tx.Value = big.NewInt(value)
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign stake tx: %v", err)
	}
	return tx
}

// A matured unbond that is claimed back is announced as stake.unbondClaimed.
// The name stake.claimed belongs to the legacy alias of the rewards claim and
// must not be reused for an event with different attributes.
func TestUnbondClaimEventHasItsOwnName(t *testing.T) {
	if events.TypeStakeRewardsClaimedLegacy != "stake.claimed" {
		t.Fatalf("the rewards claim alias is %q, want it unchanged as stake.claimed", events.TypeStakeRewardsClaimedLegacy)
	}
	if events.TypeStakeUnbondClaimed == events.TypeStakeRewardsClaimedLegacy || events.TypeStakeUnbondClaimed == events.TypeStakeRewardsClaimed {
		t.Fatalf("the unbond claim event shares its name %q with a rewards claim event", events.TypeStakeUnbondClaimed)
	}

	sp := newStakingStateProcessor(t)
	base := time.Unix(1700000000, 0)
	sp.nowFunc = func() time.Time { return base }
	var delegator, validator [20]byte
	delegator[19] = 0x62
	validator[19] = 0x63
	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(2000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)})
	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(1200)); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	unbond, err := sp.StakeUndelegate(delegator[:], big.NewInt(1200))
	if err != nil {
		t.Fatalf("undelegate: %v", err)
	}
	base = base.Add(unbondingPeriod + time.Hour)
	sp.nowFunc = func() time.Time { return base }
	before := len(sp.Events())
	if _, err := sp.StakeClaim(delegator[:], unbond.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	claimEvents := sp.Events()[before:]
	if len(claimEvents) != 1 {
		t.Fatalf("expected exactly one event from the unbond claim, got %v", describeEvents(claimEvents))
	}
	evt := claimEvents[0]
	if evt.Type != events.TypeStakeUnbondClaimed {
		t.Fatalf("unbond claim event type = %q, want %q", evt.Type, events.TypeStakeUnbondClaimed)
	}
	want := map[string]string{
		"delegator":   crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String(),
		"validator":   crypto.MustNewAddress(crypto.NHBPrefix, validator[:]).String(),
		"amount":      "1200",
		"unbondingId": "1",
	}
	if len(evt.Attributes) != len(want) {
		t.Fatalf("unbond claim attributes = %v, want %v", evt.Attributes, want)
	}
	for k, v := range want {
		if evt.Attributes[k] != v {
			t.Fatalf("unbond claim attribute %s = %q, want %q", k, evt.Attributes[k], v)
		}
	}
	for _, e := range sp.Events() {
		if e.Type == "stake.claimed" {
			t.Fatalf("the unbond claim flow still emits a stake.claimed event: %v", e.Attributes)
		}
	}
}

// A staking request refused because the module is paused reports the pause
// through a stake.paused event. Running it as a transaction used to discard
// that event together with the failed transaction's other events.
func TestStakePausedEventSurvivesARefusedTransaction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		txType    types.TxType
		value     int64
		operation string
	}{
		{"stake", types.TxTypeStake, 100, events.StakeOperationDelegate},
		{"claim rewards", types.TxTypeStakeClaimRewards, 0, events.StakeOperationClaimRewards},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetPauseView(staticPauseView{moduleStaking: true})
			key := fixedStakeKey(t, 0x41)
			var addr [20]byte
			copy(addr[:], key.PubKey().Address().Bytes())
			writeAccount(t, sp, addr, &types.Account{BalanceZNHB: big.NewInt(5_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)})

			err := sp.ApplyTransaction(stakeTx(t, key, tc.txType, 0, tc.value))
			if err == nil {
				t.Fatalf("expected the transaction to be refused while staking is paused")
			}
			var paused *types.Event
			log := sp.Events()
			for i := range log {
				if log[i].Type == events.TypeStakePaused {
					paused = &log[i]
				}
			}
			if paused == nil {
				t.Fatalf("the refusal dropped its stake.paused event, log = %v", describeEvents(log))
			}
			if paused.Attributes["operation"] != tc.operation || paused.Attributes["addr"] != key.PubKey().Address().String() {
				t.Fatalf("unexpected stake.paused attributes %v", paused.Attributes)
			}
		})
	}
}

// A refused transaction that is not a pause still discards the events it
// appended before it failed.
func TestRefusedStakeTransactionOtherwiseDropsItsEvents(t *testing.T) {
	sp := newStakingStateProcessor(t)
	key := fixedStakeKey(t, 0x42)
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	writeAccount(t, sp, addr, &types.Account{BalanceZNHB: big.NewInt(10), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)})
	if err := sp.ApplyTransaction(stakeTx(t, key, types.TxTypeStake, 0, 1_000)); err == nil {
		t.Fatalf("expected staking more than the balance to be refused")
	}
	if log := sp.Events(); len(log) != 0 {
		t.Fatalf("a refused stake left events behind: %v", describeEvents(log))
	}
}

// stakeTransactionEvents are the events a successful TxTypeStake (0x06), the
// only staking transaction type the live chain has included, emits. They were
// recorded from release/hardening-r4 and must stay as they are.
var stakeTransactionEvents = []string{
	"stake.delegated addr=nhb1t96uz5h7trxukl39tp4rexuefgtdhds4mu4340 amount=1200 lastIndex=1000000000000000000 locked=1200 newShares=0 sharesAdded=0 validator=nhb1t96uz5h7trxukl39tp4rexuefgtdhds4mu4340",
}

func TestStakeTransactionEventsAreUnchanged(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.nowFunc = func() time.Time { return time.Unix(1700000000, 0) }
	key := fixedStakeKey(t, 0x43)
	var addr [20]byte
	copy(addr[:], key.PubKey().Address().Bytes())
	writeAccount(t, sp, addr, &types.Account{BalanceZNHB: big.NewInt(5_000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)})
	if err := sp.ApplyTransaction(stakeTx(t, key, types.TxTypeStake, 0, 1_200)); err != nil {
		t.Fatalf("apply stake: %v", err)
	}
	got := describeEvents(sp.Events())
	if strings.Join(got, "\n") != strings.Join(stakeTransactionEvents, "\n") {
		for _, line := range got {
			t.Logf("RECORDED %s", line)
		}
		t.Fatalf("TxTypeStake events changed:\n got %v\nwant %v", got, stakeTransactionEvents)
	}
}
