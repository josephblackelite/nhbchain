package core

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/subscriptions"
	"nhbchain/storage"
)

// Every charge of a subscription is made by the chain, in the lifecycle of a
// block, with no transaction paying for it. How often a subscription may be
// charged is therefore what bounds the work one subscribe transaction can
// cause. Since settlement stopped clearing a whole due bucket after each pass,
// a subscription whose next cycle falls on the same day is charged again, so
// what stops it being charged on every block is the interval between cycles:
// a plan cannot be created with one under a day, and settlement never
// schedules a cycle sooner than a day after the last whatever the record says.
//
// These tests store the subscription directly, as the fixture does, so they do
// not depend on the plan's bounds.

// A record with a one-second interval and a price of one wei is charged once a
// day, not once a block.
func TestSubscriptionWithAOneSecondIntervalIsChargedOncePerDay(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	start := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	fx.setBalance(fx.payer, 1_000_000_000)
	id := fx.subscribe(start, 1, 1)

	const blocks = 600
	for i := 0; i < blocks; i++ {
		fx.settle(start.Add(time.Duration(i*2) * time.Second))
	}
	if got := len(fx.charges(id)); got != 1 {
		t.Fatalf("%d blocks of two seconds charged the subscription %d times, want 1", blocks, got)
	}
	sub, ok := fx.registry.GetSubscription(id)
	if !ok {
		t.Fatalf("subscription is gone")
	}
	if want := uint64(start.Unix()) + subscriptions.MinPlanIntervalSeconds; sub.NextChargeAt != want {
		t.Fatalf("next charge at %d, want a day after the first, %d", sub.NextChargeAt, want)
	}

	// A second before the day is up nothing happens; at the day it is charged
	// again, once.
	fx.settle(start.Add(24*time.Hour - time.Second))
	if got := len(fx.charges(id)); got != 1 {
		t.Fatalf("charged again a second early: %d charges", got)
	}
	fx.settle(start.Add(24 * time.Hour))
	fx.settle(start.Add(24*time.Hour + 2*time.Second))
	charges := fx.charges(id)
	if len(charges) != 2 || charges[1].AttemptNumber != 2 || charges[1].ChargedAt != uint64(start.Add(24*time.Hour).Unix()) {
		t.Fatalf("expected the second charge at exactly a day, got %+v", charges)
	}
}

// An interval near the end of the uint64 range must not wrap "now plus the
// interval" around to a time in the past: an entry scheduled in the past is due
// on the next block, and again on the one after.
func TestSubscriptionWithTheLongestIntervalIsNotChargedAgainAtOnce(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	start := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	fx.setBalance(fx.payer, 1_000_000_000)
	id := fx.subscribe(start, 1, math.MaxUint64)

	for i := 0; i < 600; i++ {
		fx.settle(start.Add(time.Duration(i*2) * time.Second))
	}
	if got := len(fx.charges(id)); got != 1 {
		t.Fatalf("a subscription with the longest interval was charged %d times in 600 blocks, want 1", got)
	}
	sub, _ := fx.registry.GetSubscription(id)
	if sub.NextChargeAt != math.MaxUint64 {
		t.Fatalf("next charge at %d, want the end of time (%d), not a time in the past", sub.NextChargeAt, uint64(math.MaxUint64))
	}
}

// At the shortest interval a plan may have, a subscription is charged exactly
// once per interval however many blocks come in between, and its history and
// attempt numbers follow.
func TestSubscriptionIsBilledOncePerInterval(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	start := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	fx.setBalance(fx.payer, 1_000_000)
	id := fx.subscribe(start, 100, subscriptions.MinPlanIntervalSeconds)

	const stepSeconds = 600
	for elapsed := 0; elapsed <= 5*86400; elapsed += stepSeconds {
		fx.settle(start.Add(time.Duration(elapsed) * time.Second))
	}
	charges := fx.charges(id)
	if len(charges) != 6 {
		t.Fatalf("expected one charge at the start and one every day for five days (6), got %d", len(charges))
	}
	for i, charge := range charges {
		if charge.Status != subscriptions.ChargeStatusPaid {
			t.Fatalf("charge %d status = %v, want paid", i, charge.Status)
		}
		if want := uint64(start.Unix()) + uint64(i)*86400; charge.ChargedAt != want {
			t.Fatalf("charge %d made at %d, want %d", i, charge.ChargedAt, want)
		}
		if charge.AttemptNumber != uint32(i+1) {
			t.Fatalf("charge %d is attempt %d, want %d", i, charge.AttemptNumber, i+1)
		}
	}
	if count, err := fx.registry.ChargeCount(id); err != nil || count != 6 {
		t.Fatalf("ChargeCount = %d, %v; want 6", count, err)
	}
	if got := fx.balance(fx.merchant); got.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("merchant balance = %s, want 600", got)
	}
}

// A trial that ends later on the day the subscription was made puts its first
// charge in today's bucket, which settlement scans on every block: the charge
// is made when the trial ends, once, and not before.
func TestSubscriptionTrialEndingLaterTodayIsChargedWhenItEnds(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400})
	start := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	fx.setBalance(fx.payer, 1_000_000)
	id := fx.subscribe(start, 100, subscriptions.MinPlanIntervalSeconds)
	sub, _ := fx.registry.GetSubscription(id)
	sub.NextChargeAt += 6 * 3600
	if err := fx.registry.PutSubscription(sub); err != nil {
		t.Fatalf("store subscription: %v", err)
	}

	end := start.Add(6 * time.Hour)
	for now := start; now.Before(end); now = now.Add(5 * time.Minute) {
		fx.settle(now)
	}
	if got := len(fx.charges(id)); got != 0 {
		t.Fatalf("charged %d times before the trial ended", got)
	}
	fx.settle(end)
	fx.settle(end.Add(5 * time.Minute))
	charges := fx.charges(id)
	if len(charges) != 1 || charges[0].ChargedAt != uint64(end.Unix()) {
		t.Fatalf("expected one charge at the end of the trial (%d), got %+v", end.Unix(), charges)
	}
}

// A payer who cannot pay is tried MaxRetries times, a retry interval apart, and
// then the subscription is suspended and leaves the due list: it does not cost
// the chain an attempt on every block.
func TestUnfundedSubscriptionIsAttemptedAtMostMaxRetriesTimes(t *testing.T) {
	fx := newSameDayFixture(t, subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 60})
	start := time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC)
	id := fx.subscribe(start, 100, subscriptions.MinPlanIntervalSeconds)

	for i := 0; i < 1800; i++ { // an hour of two-second blocks
		fx.settle(start.Add(time.Duration(i*2) * time.Second))
	}
	fx.settle(start.Add(48 * time.Hour))

	charges := fx.charges(id)
	if len(charges) != 3 {
		t.Fatalf("an unfunded subscription was attempted %d times, want MaxRetries (3)", len(charges))
	}
	for i, charge := range charges {
		if charge.Status != subscriptions.ChargeStatusFailed || charge.AttemptNumber != uint32(i+1) {
			t.Fatalf("attempt %d = %+v, want a failed attempt numbered %d", i, charge, i+1)
		}
		if want := uint64(start.Unix()) + uint64(i)*60; charge.ChargedAt != want {
			t.Fatalf("attempt %d made at %d, want a retry interval (60 s) after the last: %d", i, charge.ChargedAt, want)
		}
	}
	sub, _ := fx.registry.GetSubscription(id)
	if sub.Status != subscriptions.SubscriptionStatusSuspended {
		t.Fatalf("status = %v, want suspended", sub.Status)
	}
	if due := fx.dueOn(start); len(due) != 0 {
		t.Fatalf("a suspended subscription must leave the due list, got %v", due)
	}
}

// --- plan terms through the real transaction path ----------------------------

func planTermsTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, price *big.Int, interval, trial uint64) *types.Transaction {
	t.Helper()
	data, err := rlp.EncodeToBytes(struct {
		Name               string
		PriceWei           *big.Int
		Asset              string
		IntervalSeconds    uint64
		TrialPeriodSeconds uint64
	}{Name: "Plan", PriceWei: price, Asset: "NHB", IntervalSeconds: interval, TrialPeriodSeconds: trial})
	if err != nil {
		t.Fatalf("encode create-plan payload: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSubscriptionCreatePlan, Nonce: nonce, Data: data, GasLimit: 100_000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign create-plan: %v", err)
	}
	return tx
}

type planTerms struct {
	name     string
	price    *big.Int
	interval uint64
	trial    uint64
}

func refusedPlanTerms() []planTerms {
	token := subscriptions.MinPlanPriceWei()
	return []planTerms{
		{"a plan of one wei every second", big.NewInt(1), 1, 0},
		{"an interval of a second", token, 1, 0},
		{"an interval of an hour", token, 3600, 0},
		{"an interval a second under a day", token, subscriptions.MinPlanIntervalSeconds - 1, 0},
		{"an interval over ten years", token, subscriptions.MaxPlanIntervalSeconds + 1, 0},
		{"the longest possible interval", token, math.MaxUint64, 0},
		{"a trial over ten years", token, 86400, subscriptions.MaxTrialPeriodSeconds + 1},
		{"the longest possible trial", token, 86400, math.MaxUint64},
		{"a price of one wei", big.NewInt(1), 86400, 0},
		{"a price a wei under one token", new(big.Int).Sub(token, big.NewInt(1)), 86400, 0},
	}
}

// Plan creation refuses every term outside the bounds, as an ordinary
// transaction error that the block builder prunes, and stores nothing; the
// bounds themselves are allowed.
func TestCreatePlanTransactionRefusesTermsOutsideTheBounds(t *testing.T) {
	treasury := [20]byte{0xFE, 0x01}
	sp := selfDealingSubscriptionsProcessor(t, treasury, 100)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	merchant := toAddress(key)
	if err := sp.setAccount(merchant[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	sp.BeginBlock(1, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	defer sp.EndBlock()
	registry := subscriptions.NewRegistry(nhbstate.NewManager(sp.Trie))
	nonce := func() uint64 {
		acc, err := sp.getAccount(merchant[:])
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		return acc.Nonce
	}

	for _, tc := range refusedPlanTerms() {
		err := sp.ApplyTransaction(planTermsTx(t, key, nonce(), tc.price, tc.interval, tc.trial))
		if !errors.Is(err, subscriptions.ErrInvalidPlan) {
			t.Fatalf("%s: err = %v, want ErrInvalidPlan", tc.name, err)
		}
		if got := classifyProposalError(err); got != proposalDispositionPrune {
			t.Fatalf("%s: a plan outside the bounds can never become valid: want prune, got disposition %d", tc.name, got)
		}
	}
	if ids, err := registry.ListPlansByMerchant(merchant); err != nil || len(ids) != 0 {
		t.Fatalf("a refused plan must not be stored: ids=%v err=%v", ids, err)
	}

	token := subscriptions.MinPlanPriceWei()
	for _, tc := range []planTerms{
		{"the shortest interval at the smallest price", token, subscriptions.MinPlanIntervalSeconds, 0},
		{"the longest interval and trial", token, subscriptions.MaxPlanIntervalSeconds, subscriptions.MaxTrialPeriodSeconds},
	} {
		if err := sp.ApplyTransaction(planTermsTx(t, key, nonce(), tc.price, tc.interval, tc.trial)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	ids, err := registry.ListPlansByMerchant(merchant)
	if err != nil || len(ids) != 2 {
		t.Fatalf("expected the two plans within the bounds to be stored, got ids=%v err=%v", ids, err)
	}
	if plan, ok := registry.GetPlan(ids[1]); !ok || plan.IntervalSeconds != subscriptions.MaxPlanIntervalSeconds || plan.TrialPeriodSeconds != subscriptions.MaxTrialPeriodSeconds {
		t.Fatalf("the stored plan does not carry the requested terms: %+v", plan)
	}
}

// A block proposer handed a plan outside the bounds (for example from a mempool
// that skipped admission simulation) must drop just that transaction and still
// build the block with the others in it, never fail the whole proposal.
func TestCreateBlockPrunesAPlanOutsideTheBoundsWithoutAborting(t *testing.T) {
	badKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	goodKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	validatorKeyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validatorKeyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: validatorKeyA.PubKey().Address().String(), Power: 11440},
			{Address: validatorKeyB.PubKey().Address().String(), Power: 11336},
		},
		Alloc: map[string]map[string]string{
			badKey.PubKey().Address().String():  {"NHB": "1000", "ZNHB": "0"},
			goodKey.PubKey().Address().String(): {"NHB": "1000", "ZNHB": "0"},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	nodeKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	node, err := NewNode(db, nodeKey, genesisPath, false, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	if err := node.SetSubscriptionsConfig(subscriptions.Config{MaxRetries: 3, RetryIntervalSeconds: 86400}); err != nil {
		t.Fatalf("configure subscriptions: %v", err)
	}

	token := subscriptions.MinPlanPriceWei()
	bad := planTermsTx(t, badKey, 0, big.NewInt(1), 1, 0) // one wei, every second
	good := planTermsTx(t, goodKey, 0, token, subscriptions.MinPlanIntervalSeconds, 0)
	block, err := node.CreateBlock([]*types.Transaction{bad, good})
	if err != nil {
		t.Fatalf("CreateBlock must not abort the proposal because of a plan outside the bounds: %v", err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected only the plan within the bounds in the block, got %d transactions", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if ids, err := node.SubscriptionPlansByMerchant(toAddress(badKey)); err != nil || len(ids) != 0 {
		t.Fatalf("no plan may exist for the refused transaction: ids=%v err=%v", ids, err)
	}
	if ids, err := node.SubscriptionPlansByMerchant(toAddress(goodKey)); err != nil || len(ids) != 1 {
		t.Fatalf("the plan within the bounds must exist: ids=%v err=%v", ids, err)
	}
}
