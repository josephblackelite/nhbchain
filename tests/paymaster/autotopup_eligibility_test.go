package paymaster

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// PL-R5T-64 / PL-EXT-03: the automatic top-up refills any address that signs a
// sponsored transfer, and its caps were keyed by sponsor address, so a fresh key
// got a fresh allowance. SponsorRole and AggregateDailyCapWei close both.

const sponsorRole = "ROLE_PAYMASTER_SPONSOR"

// topUpOnce runs one sponsored transfer by a fresh sender for the given sponsor key
// and returns the sponsor's ZNHB balance afterwards and the last top-up event.
func topUpOnce(t *testing.T, sp *core.StateProcessor, manager *nhbstate.Manager, sponsorKey *crypto.PrivateKey, height uint64, at time.Time, to byte) (*big.Int, map[string]string) {
	t.Helper()
	tx := buildFeeTestTxFor(t, sp, manager, sponsorKey, to)
	sp.BeginBlock(height, at)
	defer sp.EndBlock()
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}
	account, err := sp.GetAccount(sponsorKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get sponsor: %v", err)
	}
	var attrs map[string]string
	for _, e := range sp.Events() {
		if e.Type == events.TypePaymasterAutoTopUp {
			attrs = e.Attributes
		}
	}
	return account.BalanceZNHB, attrs
}

func eligibilitySetup(t *testing.T, mutate func(*core.PaymasterAutoTopUpPolicy)) (*core.StateProcessor, *nhbstate.Manager, autoTopUpActors, core.PaymasterAutoTopUpPolicy) {
	t.Helper()
	sp, manager, actors, policy, _ := newFeeTestSetup(t, big.NewInt(2_500), big.NewInt(10_000))
	if mutate != nil {
		mutate(&policy)
		sp.SetPaymasterAutoTopUpPolicy(policy)
	}
	putZNHBAccount(t, sp, manager, actors.fundingAddr, big.NewInt(100_000))
	if err := manager.SetBalance(actors.fundingAddr.Bytes(), "ZNHB", big.NewInt(100_000)); err != nil {
		t.Fatalf("seed funding balance: %v", err)
	}
	return sp, manager, actors, policy
}

func freshKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func fundingBalance(t *testing.T, sp *core.StateProcessor, actors autoTopUpActors) *big.Int {
	t.Helper()
	acc, err := sp.GetAccount(actors.fundingAddr.Bytes())
	if err != nil || acc == nil {
		t.Fatalf("funding account: %v", err)
	}
	return acc.BalanceZNHB
}

// With neither setting the policy behaves as it did: any fresh key is topped up. A
// zero cap, which is what a blank config value parses to, is no cap.
func TestAutoTopUpWithoutEligibilitySettingsTopsUpAFreshKey(t *testing.T) {
	for _, zero := range []*big.Int{nil, big.NewInt(0)} {
		sp, manager, _, _ := eligibilitySetup(t, func(p *core.PaymasterAutoTopUpPolicy) { p.AggregateDailyCapWei = zero })
		balance, attrs := topUpOnce(t, sp, manager, freshKey(t), 1, time.Unix(1_700_000_000, 0).UTC(), 0x10)
		if balance.Cmp(big.NewInt(2_500)) != 0 {
			t.Fatalf("aggregate cap %v: a fresh key was not topped up: %s (%v)", zero, balance, attrs)
		}
		dayKey := time.Unix(1_700_000_000, 0).UTC().Format(nhbstate.PaymasterDayFormat)
		if rec, _, _ := manager.PaymasterGetTopUpDay("aggregate", dayKey); rec != nil {
			t.Fatalf("aggregate cap %v: an aggregate record was written", zero)
		}
	}
	sp, manager, _, _ := eligibilitySetup(t, nil)
	key := freshKey(t)
	balance, attrs := topUpOnce(t, sp, manager, key, 1, time.Unix(1_700_000_000, 0).UTC(), 0x11)
	if balance.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("a fresh key without the settings was not topped up: %s (%v)", balance, attrs)
	}
}

func TestAutoTopUpRefusesASponsorWithoutTheRole(t *testing.T) {
	sp, manager, actors, policy := eligibilitySetup(t, func(p *core.PaymasterAutoTopUpPolicy) { p.SponsorRole = sponsorRole })
	key := freshKey(t)
	fundingBefore := fundingBalance(t, sp, actors)
	balance, attrs := topUpOnce(t, sp, manager, key, 1, time.Unix(1_700_000_000, 0).UTC(), 0x12)
	if balance.Sign() != 0 {
		t.Fatalf("a sponsor without the role was topped up: %s", balance)
	}
	if attrs["status"] != "failure" || attrs["reason"] != "sponsor_not_registered" {
		t.Fatalf("want failure sponsor_not_registered, got %#v", attrs)
	}
	if fundingBalance(t, sp, actors).Cmp(fundingBefore) != 0 {
		t.Fatalf("the funding account paid for a refused sponsor")
	}
	dayKey := time.Unix(1_700_000_000, 0).UTC().Format(nhbstate.PaymasterDayFormat)
	if rec, _, _ := manager.PaymasterGetTopUpDay(paymasterStorageKey(key.PubKey().Address()), dayKey); rec != nil {
		t.Fatalf("a day record was written for a refused sponsor: %#v", rec)
	}

	// Granting the role is all it takes.
	if err := manager.SetRole(policy.SponsorRole, key.PubKey().Address().Bytes()); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	balance, attrs = topUpOnce(t, sp, manager, key, 2, time.Unix(1_700_000_100, 0).UTC(), 0x13)
	if balance.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("a sponsor with the role was not topped up: %s (%v)", balance, attrs)
	}
}

func TestAutoTopUpAggregateCapBoundsTheFundingAccountAcrossSponsors(t *testing.T) {
	sp, manager, actors, _ := eligibilitySetup(t, func(p *core.PaymasterAutoTopUpPolicy) {
		p.AggregateDailyCapWei = big.NewInt(5_000) // two top-ups of 2,500
	})
	day1 := time.Unix(1_700_000_000, 0).UTC()
	fundingBefore := fundingBalance(t, sp, actors)

	// Six fresh keys, each with a full per-sponsor allowance of 10,000: the
	// aggregate cap admits the first two only.
	paid := 0
	var refused map[string]string
	for i := 0; i < 6; i++ {
		balance, attrs := topUpOnce(t, sp, manager, freshKey(t), uint64(i+1), day1.Add(time.Duration(i)*time.Second), byte(0x20+i))
		if balance.Sign() > 0 {
			paid++
		} else {
			refused = attrs
		}
	}
	if paid != 2 {
		t.Fatalf("%d sponsors were topped up under an aggregate cap of two top-ups", paid)
	}
	if refused["reason"] != "global_daily_cap_exceeded" {
		t.Fatalf("refusal reason = %#v, want global_daily_cap_exceeded", refused)
	}
	outflow := new(big.Int).Sub(fundingBefore, fundingBalance(t, sp, actors))
	if outflow.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("funding outflow %s, want exactly the aggregate cap 5000", outflow)
	}

	// The next UTC day the cap starts again.
	day2 := day1.Add(24 * time.Hour)
	balance, attrs := topUpOnce(t, sp, manager, freshKey(t), 100, day2, 0x30)
	if balance.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("next day: not topped up: %s (%v)", balance, attrs)
	}
}

// The fee the funding account pays on top of the amount counts toward the cap, as it
// does toward the per-sponsor cap.
func TestAutoTopUpAggregateCapCountsTheFee(t *testing.T) {
	sp, manager, actors, _ := eligibilitySetup(t, func(p *core.PaymasterAutoTopUpPolicy) {
		p.AggregateDailyCapWei = big.NewInt(2_500) // fits the amount but not amount+fee
	})
	if err := manager.ParamStoreSet(governance.ParamKeyPaymasterTopUpFeeWei, []byte("100")); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	fundingBefore := fundingBalance(t, sp, actors)
	balance, attrs := topUpOnce(t, sp, manager, freshKey(t), 1, time.Unix(1_700_000_000, 0).UTC(), 0x31)
	if balance.Sign() != 0 || attrs["reason"] != "global_daily_cap_exceeded" {
		t.Fatalf("amount+fee above the aggregate cap was paid: balance %s, %v", balance, attrs)
	}
	if fundingBalance(t, sp, actors).Cmp(fundingBefore) != 0 {
		t.Fatalf("the funding account paid")
	}
}

// A top-up that is refused for another reason leaves the aggregate record alone.
func TestAutoTopUpAggregateCapIsNotChargedByARefusedTopUp(t *testing.T) {
	sp, manager, _, policy := eligibilitySetup(t, func(p *core.PaymasterAutoTopUpPolicy) {
		p.AggregateDailyCapWei = big.NewInt(5_000)
		p.SponsorRole = sponsorRole
	})
	day := time.Unix(1_700_000_000, 0).UTC()
	_, _ = topUpOnce(t, sp, manager, freshKey(t), 1, day, 0x32) // no role: refused
	rec, _, err := manager.PaymasterGetTopUpDay("aggregate", day.Format(nhbstate.PaymasterDayFormat))
	if err != nil {
		t.Fatalf("aggregate record: %v", err)
	}
	if rec != nil && rec.DebitedWei.Sign() != 0 {
		t.Fatalf("a refused top-up charged the aggregate: %#v", rec)
	}
	// And a paid one does charge it.
	key := freshKey(t)
	if err := manager.SetRole(policy.SponsorRole, key.PubKey().Address().Bytes()); err != nil {
		t.Fatalf("grant role: %v", err)
	}
	if _, err := sp.Commit(0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_, _ = topUpOnce(t, sp, manager, key, 2, day.Add(time.Second), 0x33)
	rec, _, err = manager.PaymasterGetTopUpDay("aggregate", day.Format(nhbstate.PaymasterDayFormat))
	if err != nil || rec == nil || rec.DebitedWei.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("aggregate record after one top-up: %#v (%v), want 2500", rec, err)
	}
}

// With no aggregate cap nothing extra is written: the state of the policy as it was.
func TestAutoTopUpWithoutAnAggregateCapWritesNoAggregateRecord(t *testing.T) {
	sp, manager, _, _ := eligibilitySetup(t, nil)
	day := time.Unix(1_700_000_000, 0).UTC()
	_, _ = topUpOnce(t, sp, manager, freshKey(t), 1, day, 0x34)
	rec, _, err := manager.PaymasterGetTopUpDay("aggregate", day.Format(nhbstate.PaymasterDayFormat))
	if err != nil || rec != nil {
		t.Fatalf("an aggregate record was written without a cap: %#v (%v)", rec, err)
	}
}

// buildFeeTestTxFor is buildFeeTestTx with the sponsor's key given by the caller.
func buildFeeTestTxFor(t *testing.T, sp *core.StateProcessor, manager *nhbstate.Manager, sponsorKey *crypto.PrivateKey, to byte) *types.Transaction {
	t.Helper()
	sp.SetPaymasterEnabled(true)
	senderKey := freshKey(t)
	putAccountBalances(t, sp, senderKey.PubKey().Address(), big.NewInt(1), big.NewInt(0))
	if err := manager.SetBalance(senderKey.PubKey().Address().Bytes(), "ZNHB", big.NewInt(0)); err != nil {
		t.Fatalf("seed sender metadata: %v", err)
	}
	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        common.Address{to}.Bytes(),
		Value:     big.NewInt(1),
		GasLimit:  21000,
		GasPrice:  big.NewInt(0),
		Paymaster: sponsorKey.PubKey().Address().Bytes(),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign sender: %v", err)
	}
	signPaymaster(t, tx, sponsorKey)
	return tx
}
