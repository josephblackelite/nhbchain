package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// These tests pin the epoch reward rule that RewardsByStakeFromEpoch switches on (core/rewards_logic.go, distributeStakeWeighted):
//   - switched off (0) or not yet reached, the split is exactly the earlier one: 20 percent equally per validator, 30 percent by engagement score, both to
//     the validator's own account, 50 percent by stake and to the wallets that hold it;
//   - from the named epoch on, all three pools follow stake and reach the wallets that hold it, so a validator that is only delegated to keeps nothing.
//
// The reward config of newRewardTestState emits 100 units an epoch: 20 validators, 50 stakers, 30 engagement.

func fixedAddr(b byte) [20]byte {
	var a [20]byte
	a[0] = 0x42
	a[19] = b
	return a
}

// seedFixedValidator is seedEligibleValidator with a chosen address, so two states built the same way hold the same accounts.
func seedFixedValidator(t *testing.T, sp *StateProcessor, addr [20]byte, stake int64, engagement uint64) {
	t.Helper()
	account := &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(stake),
		EngagementScore:         engagement,
		EngagementLastHeartbeat: uint64(rewardBlockTimestamp1),
		ValidatorRegistered:     true,
	}
	if err := sp.setAccount(addr[:], account); err != nil {
		t.Fatalf("set account: %v", err)
	}
	if sp.EligibleValidators == nil {
		sp.EligibleValidators = make(map[string]*big.Int)
	}
	sp.EligibleValidators[string(addr[:])] = big.NewInt(stake)
}

func newFixedRewardState(t *testing.T, fromEpoch uint64) *StateProcessor {
	t.Helper()
	sp := newRewardTestState(t)
	// small raw stakes, not the real 18-decimal ZNHB scale: explicit minimum, as seedEligibleValidator does
	if err := nhbstate.NewManager(sp.Trie).SetMinimumValidatorStake(big.NewInt(1000)); err != nil {
		t.Fatalf("set test minimum stake: %v", err)
	}
	sp.SetRewardsByStakeFromEpoch(fromEpoch)
	return sp
}

func balanceOf(t *testing.T, sp *StateProcessor, addr [20]byte) *big.Int {
	t.Helper()
	acc, err := sp.getAccount(addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if acc.BalanceZNHB == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(acc.BalanceZNHB)
}

func requireBalance(t *testing.T, sp *StateProcessor, name string, addr [20]byte, want int64) {
	t.Helper()
	if got := balanceOf(t, sp, addr); got.Cmp(big.NewInt(want)) != 0 {
		t.Fatalf("%s: balance %s, want %d", name, got, want)
	}
}

func processEpochBlocks(t *testing.T, sp *StateProcessor, fromBlock uint64) {
	t.Helper()
	// epoch length is 2 in newRewardTestState: two blocks close one epoch
	for h := fromBlock; h < fromBlock+2; h++ {
		if err := sp.ProcessBlockLifecycle(h, rewardBlockTimestamp1+int64(h)); err != nil {
			t.Fatalf("process block %d: %v", h, err)
		}
	}
}

func TestRewardsByStake_OffIsTheEarlierSplit(t *testing.T) {
	a, b := fixedAddr(1), fixedAddr(2)
	for name, fromEpoch := range map[string]uint64{"never (0)": 0, "an epoch not reached yet": 99} {
		sp := newFixedRewardState(t, fromEpoch)
		seedFixedValidator(t, sp, a, 9000, 10)
		seedFixedValidator(t, sp, b, 1000, 5)
		processEpochBlocks(t, sp, 1)
		// validators pool 20: equal, 10 each; stakers pool 50: by stake, 45 and 5; engagement pool 30: by score 10:5, 20 and 10
		requireBalance(t, sp, name+" A", a, 10+45+20)
		requireBalance(t, sp, name+" B", b, 10+5+10)
	}
}

func TestRewardsByStake_PaysByStake(t *testing.T) {
	sp := newFixedRewardState(t, 1)
	a, b := fixedAddr(1), fixedAddr(2)
	seedFixedValidator(t, sp, a, 9000, 10)
	seedFixedValidator(t, sp, b, 1000, 5)
	processEpochBlocks(t, sp, 1)
	// validators pool 20 by stake 9:1 = 18 and 2; stakers pool 50 = 45 and 5; engagement pool 30 by stake 9:1 = 27 and 3
	requireBalance(t, sp, "A", a, 18+45+27)
	requireBalance(t, sp, "B", b, 2+5+3)

	settlement, ok := sp.LatestRewardEpochSettlement()
	if !ok {
		t.Fatalf("expected a settlement")
	}
	if settlement.PaidTotal.Cmp(settlement.PlannedTotal) != 0 {
		t.Fatalf("paid %s of %s planned", settlement.PaidTotal, settlement.PlannedTotal)
	}
	if settlement.ValidatorsPaid.String() != "20" || settlement.StakersPaid.String() != "50" || settlement.EngagementPaid.String() != "30" {
		t.Fatalf("pools paid %s/%s/%s, want 20/50/30", settlement.ValidatorsPaid, settlement.StakersPaid, settlement.EngagementPaid)
	}
}

func TestRewardsByStake_SplittingStakeEarnsNothingExtra(t *testing.T) {
	// One validator holding 3000 against three validators holding 1000 each: the same total stake must earn the same total.
	one := newFixedRewardState(t, 1)
	seedFixedValidator(t, one, fixedAddr(1), 3000, 10)
	seedFixedValidator(t, one, fixedAddr(9), 3000, 10) // an unrelated validator of equal size to dilute against
	processEpochBlocks(t, one, 1)
	single := balanceOf(t, one, fixedAddr(1))

	split := newFixedRewardState(t, 1)
	for i := byte(1); i <= 3; i++ {
		seedFixedValidator(t, split, fixedAddr(i), 1000, 10)
	}
	seedFixedValidator(t, split, fixedAddr(9), 3000, 10)
	processEpochBlocks(t, split, 1)
	total := big.NewInt(0)
	for i := byte(1); i <= 3; i++ {
		total.Add(total, balanceOf(t, split, fixedAddr(i)))
	}
	if single.Cmp(total) != 0 {
		t.Fatalf("one validator of 3000 earns %s, three of 1000 earn %s together: splitting must not pay more", single, total)
	}

	// and under the earlier split it did pay more (this is the weakness the new rule closes)
	old := newFixedRewardState(t, 0)
	for i := byte(1); i <= 3; i++ {
		seedFixedValidator(t, old, fixedAddr(i), 1000, 10)
	}
	seedFixedValidator(t, old, fixedAddr(9), 3000, 10)
	processEpochBlocks(t, old, 1)
	oldTotal := big.NewInt(0)
	for i := byte(1); i <= 3; i++ {
		oldTotal.Add(oldTotal, balanceOf(t, old, fixedAddr(i)))
	}
	if oldTotal.Cmp(total) <= 0 {
		t.Fatalf("expected the earlier split to pay three small validators more than the stake rule (%s vs %s)", oldTotal, total)
	}
}

func TestRewardsByStake_DelegatedValidatorKeepsNothingAndDelegatorReceivesAll(t *testing.T) {
	setup := func(fromEpoch uint64) (*StateProcessor, [20]byte, [20]byte, [20]byte) {
		sp := newFixedRewardState(t, fromEpoch)
		v, w, d := fixedAddr(1), fixedAddr(2), fixedAddr(3)
		seedFixedValidator(t, sp, v, 0, 10) // all of its stake will be delegated in
		seedFixedValidator(t, sp, w, 3000, 10)
		writeAccount(t, sp, d, &types.Account{BalanceZNHB: big.NewInt(3000)})
		if _, err := sp.StakeDelegate(d[:], v[:], big.NewInt(3000)); err != nil {
			t.Fatalf("delegate: %v", err)
		}
		processEpochBlocks(t, sp, 1)
		return sp, v, w, d
	}

	// earlier split: V's own account keeps its equal validator share (10) and its engagement share (15); the delegator gets only the stakers' 25
	sp, v, w, d := setup(0)
	requireBalance(t, sp, "old: validator V", v, 10+15)
	requireBalance(t, sp, "old: delegator D", d, 25)
	requireBalance(t, sp, "old: validator W", w, 10+25+15)

	// by stake: V's account keeps nothing, the delegator receives everything attributable to V's stake (10 + 25 + 15)
	sp, v, w, d = setup(1)
	requireBalance(t, sp, "new: validator V", v, 0)
	requireBalance(t, sp, "new: delegator D", d, 10+25+15)
	requireBalance(t, sp, "new: validator W", w, 10+25+15)

	settlement, _ := sp.LatestRewardEpochSettlement()
	if settlement.PaidTotal.String() != "100" {
		t.Fatalf("paid %s, want 100", settlement.PaidTotal)
	}
	// the delegator's payout carries all three categories
	for _, payout := range settlement.Payouts {
		if string(payout.Account) == string(d[:]) {
			if payout.Validators.String() != "10" || payout.Stakers.String() != "25" || payout.Engagement.String() != "15" {
				t.Fatalf("delegator payout split %s/%s/%s, want 10/25/15", payout.Validators, payout.Stakers, payout.Engagement)
			}
		}
	}
}

func TestRewardsByStake_ValidatorsOwnStakeStillEarnsOnItsOwnAccount(t *testing.T) {
	// A validator that staked its own ZNHB (and has one small delegator) keeps the share of its own part.
	sp := newFixedRewardState(t, 1)
	v, d := fixedAddr(1), fixedAddr(3)
	seedFixedValidator(t, sp, v, 4000, 10)
	writeAccount(t, sp, d, &types.Account{BalanceZNHB: big.NewInt(1000)})
	if _, err := sp.StakeDelegate(d[:], v[:], big.NewInt(1000)); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	processEpochBlocks(t, sp, 1)
	// V holds 4000 of its own and 1000 delegated in: everything V earns (100: it is the only validator) splits 4000:1000 between V and D
	requireBalance(t, sp, "validator V", v, 80)
	requireBalance(t, sp, "delegator D", d, 20)
}

func TestRewardsByStake_EngagementPoolSkipsAValidatorThatIsNotTakingPart(t *testing.T) {
	sp := newFixedRewardState(t, 1)
	a, b := fixedAddr(1), fixedAddr(2)
	seedFixedValidator(t, sp, a, 5000, 10)
	seedFixedValidator(t, sp, b, 5000, 0)
	processEpochBlocks(t, sp, 1)
	// validators 10/10, stakers 25/25, engagement 30 only to A
	requireBalance(t, sp, "A", a, 10+25+30)
	requireBalance(t, sp, "B", b, 10+25)
}

func TestRewardsByStake_TakesEffectAtTheNamedEpoch(t *testing.T) {
	sp := newFixedRewardState(t, 2)
	a, b := fixedAddr(1), fixedAddr(2)
	seedFixedValidator(t, sp, a, 9000, 10)
	seedFixedValidator(t, sp, b, 1000, 5)

	processEpochBlocks(t, sp, 1) // epoch 1: the earlier split
	requireBalance(t, sp, "epoch 1, A", a, 10+45+20)
	requireBalance(t, sp, "epoch 1, B", b, 10+5+10)

	processEpochBlocks(t, sp, 3) // epoch 2: by stake
	requireBalance(t, sp, "after epoch 2, A", a, (10+45+20)+(18+45+27))
	requireBalance(t, sp, "after epoch 2, B", b, (10+5+10)+(2+5+3))
}

func TestRewardsByStake_PaidNeverExceedsPlannedAndBalancesSumToPaid(t *testing.T) {
	sp := newFixedRewardState(t, 1)
	rewardCfg := sp.RewardConfig()
	rewardCfg.Schedule[0].Amount = big.NewInt(101) // an amount that does not divide evenly
	if err := sp.SetRewardConfig(rewardCfg); err != nil {
		t.Fatalf("reward config: %v", err)
	}
	addrs := []([20]byte){fixedAddr(1), fixedAddr(2), fixedAddr(3)}
	for i, stake := range []int64{3333, 3333, 3334} {
		seedFixedValidator(t, sp, addrs[i], stake, uint64(i+1))
	}
	d := fixedAddr(7)
	writeAccount(t, sp, d, &types.Account{BalanceZNHB: big.NewInt(1777)})
	if _, err := sp.StakeDelegate(d[:], addrs[0][:], big.NewInt(1777)); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	processEpochBlocks(t, sp, 1)

	settlement, _ := sp.LatestRewardEpochSettlement()
	if settlement.PaidTotal.Cmp(settlement.PlannedTotal) > 0 {
		t.Fatalf("paid %s exceeds planned %s", settlement.PaidTotal, settlement.PlannedTotal)
	}
	sum := big.NewInt(0)
	for _, addr := range append(addrs, d) {
		sum.Add(sum, balanceOf(t, sp, addr))
	}
	if sum.Cmp(settlement.PaidTotal) != 0 {
		t.Fatalf("balances add up to %s, the settlement says %s was paid", sum, settlement.PaidTotal)
	}
	payoutSum := big.NewInt(0)
	for _, payout := range settlement.Payouts {
		payoutSum.Add(payoutSum, payout.Total)
	}
	if payoutSum.Cmp(settlement.PaidTotal) != 0 {
		t.Fatalf("payouts add up to %s, paid total is %s", payoutSum, settlement.PaidTotal)
	}
}

func TestRewardsByStake_SameInputsGiveTheSameStateRoot(t *testing.T) {
	build := func(fromEpoch uint64) []byte {
		sp := newFixedRewardState(t, fromEpoch)
		seedFixedValidator(t, sp, fixedAddr(1), 9000, 10)
		seedFixedValidator(t, sp, fixedAddr(2), 1000, 5)
		d, target := fixedAddr(3), fixedAddr(2)
		writeAccount(t, sp, d, &types.Account{BalanceZNHB: big.NewInt(2000)})
		if _, err := sp.StakeDelegate(d[:], target[:], big.NewInt(2000)); err != nil {
			t.Fatalf("delegate: %v", err)
		}
		processEpochBlocks(t, sp, 1)
		processEpochBlocks(t, sp, 3)
		return append([]byte(nil), sp.Trie.Hash().Bytes()...)
	}
	if first, second := build(1), build(1); string(first) != string(second) {
		t.Fatalf("two identical runs ended on different state roots: %x vs %x", first, second)
	}
	if first, second := build(0), build(0); string(first) != string(second) {
		t.Fatalf("two identical earlier-split runs ended on different state roots: %x vs %x", first, second)
	}
	if string(build(0)) == string(build(1)) {
		t.Fatalf("the rule changes who is paid, so the state root must differ from the earlier split")
	}
	// a setting that has not been reached yet leaves the root exactly as it is with the setting off
	if string(build(0)) != string(build(99)) {
		t.Fatalf("an activation epoch that is never reached must not change the state root")
	}
}

func TestRewardsByStake_SettingSurvivesProcessorCopy(t *testing.T) {
	sp := newFixedRewardState(t, 7)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := cp.RewardsByStakeFromEpoch(); got != 7 {
		t.Fatalf("copied processor carries epoch %d, want 7: a block built on the copy would be paid by the earlier rule", got)
	}
}
