package fees_test

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/fees"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestApplyFreeTierThreshold(t *testing.T) {
	value := big.NewInt(1_000)
	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
	}
	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 100,
		MDRBasisPoints:     150,
		OwnerWallet:        owner,
		Assets: map[string]fees.AssetPolicy{
			fees.AssetNHB: {MDRBasisPoints: 150, OwnerWallet: owner},
		},
	}
	monthStart := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	usage := uint64(0)
	for i := 0; i < 102; i++ {
		result := fees.Apply(fees.ApplyInput{
			Domain:        fees.DomainPOS,
			Gross:         value,
			UsageCount:    usage,
			PolicyVersion: 1,
			Config:        policy,
			WindowStart:   monthStart,
			Asset:         fees.AssetNHB,
		})
		usage = result.Counter
		if i < 100 {
			if !result.FreeTierApplied {
				t.Fatalf("expected free tier for tx %d", i)
			}
			if result.Fee.Sign() != 0 {
				t.Fatalf("expected zero fee during free tier")
			}
		} else {
			if result.FreeTierApplied {
				t.Fatalf("expected fee to apply after free tier exhaustion")
			}
			expectedFee := new(big.Int).Mul(value, big.NewInt(int64(policy.MDRBasisPoints)))
			expectedFee.Div(expectedFee, big.NewInt(10_000))
			if result.Fee.Cmp(expectedFee) != 0 {
				t.Fatalf("unexpected fee amount: got %s want %s", result.Fee, expectedFee)
			}
			if result.OwnerWallet != owner {
				t.Fatalf("unexpected owner wallet: %x", result.OwnerWallet)
			}
			if result.FreeTierRemaining != 0 {
				t.Fatalf("expected free tier to be exhausted, remaining=%d", result.FreeTierRemaining)
			}
		}
	}
}

func TestMonthlyCounterReset(t *testing.T) {
	db := storage.NewMemDB()
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := nhbstate.NewManager(trie)

	var payer [20]byte
	for i := range payer {
		payer[i] = 0xAA
	}
	jan := time.Date(2024, time.January, 10, 0, 0, 0, 0, time.UTC)
	janStart := monthStartUTC(jan)
	if err := manager.FeesPutCounter(fees.DomainPOS, payer, janStart, fees.FreeTierScopeAggregate, 50); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	count, windowStart, ok, err := manager.FeesGetCounter(fees.DomainPOS, payer, janStart, fees.FreeTierScopeAggregate)
	if err != nil || !ok {
		t.Fatalf("load counter: %v (ok=%v)", err, ok)
	}
	if count != 50 {
		t.Fatalf("unexpected count: %d", count)
	}
	if !windowStart.Equal(janStart) {
		t.Fatalf("unexpected window start: %s", windowStart)
	}

	feb := time.Date(2024, time.February, 2, 0, 0, 0, 0, time.UTC)
	febStart := monthStartUTC(feb)
	usage := count
	if !sameMonth(windowStart, febStart) {
		usage = 0
		windowStart = febStart
	}

	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 100,
		MDRBasisPoints:     150,
		Assets: map[string]fees.AssetPolicy{
			fees.AssetNHB: {MDRBasisPoints: 150},
		},
	}
	result := fees.Apply(fees.ApplyInput{
		Domain:        fees.DomainPOS,
		Gross:         big.NewInt(500),
		UsageCount:    usage,
		PolicyVersion: 2,
		Config:        policy,
		WindowStart:   windowStart,
		Asset:         fees.AssetNHB,
	})
	if !result.FreeTierApplied {
		t.Fatalf("expected free tier to apply after rollover")
	}
	if result.Counter != 1 {
		t.Fatalf("expected counter 1 after rollover, got %d", result.Counter)
	}
	if result.FreeTierRemaining != 99 {
		t.Fatalf("expected 99 remaining, got %d", result.FreeTierRemaining)
	}
	if !result.WindowStart.Equal(febStart) {
		t.Fatalf("unexpected window start in result: %s", result.WindowStart)
	}

	if err := manager.FeesPutCounter(fees.DomainPOS, payer, result.WindowStart, fees.FreeTierScopeAggregate, result.Counter); err != nil {
		t.Fatalf("update counter: %v", err)
	}
	stored, newWindow, ok, err := manager.FeesGetCounter(fees.DomainPOS, payer, febStart, fees.FreeTierScopeAggregate)
	if err != nil || !ok {
		t.Fatalf("reload counter: %v (ok=%v)", err, ok)
	}
	if stored != 1 {
		t.Fatalf("unexpected stored count: %d", stored)
	}
	if !newWindow.Equal(febStart) {
		t.Fatalf("unexpected stored window start: %s", newWindow)
	}
}

func TestAggregateFreeTierAcrossAssets(t *testing.T) {
	valueNHB := big.NewInt(1_000)
	valueZNHB := big.NewInt(2_000)
	var owner [20]byte
	for i := range owner {
		owner[i] = byte(0x10 + i)
	}
	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 100,
		MDRBasisPoints:     150,
		OwnerWallet:        owner,
		Assets: map[string]fees.AssetPolicy{
			fees.AssetNHB:  {MDRBasisPoints: 150, OwnerWallet: owner},
			fees.AssetZNHB: {MDRBasisPoints: 220, OwnerWallet: owner},
		},
	}
	monthStart := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	usage := uint64(0)
	for i := 0; i < 100; i++ {
		res := fees.Apply(fees.ApplyInput{
			Domain:        fees.DomainPOS,
			Gross:         valueNHB,
			UsageCount:    usage,
			PolicyVersion: 3,
			Config:        policy,
			WindowStart:   monthStart,
			Asset:         fees.AssetNHB,
		})
		if !res.FreeTierApplied {
			t.Fatalf("expected NHB tx %d to be free", i)
		}
		if res.Counter != uint64(i+1) {
			t.Fatalf("unexpected NHB counter after tx %d: %d", i, res.Counter)
		}
		usage = res.Counter
	}
	znhb := fees.Apply(fees.ApplyInput{
		Domain:        fees.DomainPOS,
		Gross:         valueZNHB,
		UsageCount:    usage,
		PolicyVersion: 3,
		Config:        policy,
		WindowStart:   monthStart,
		Asset:         fees.AssetZNHB,
	})
	if znhb.Counter != 101 {
		t.Fatalf("expected aggregate counter to reach 101, got %d", znhb.Counter)
	}
	if znhb.FreeTierApplied {
		t.Fatalf("expected ZNHB transfer to be charged after free tier exhaustion")
	}
	expectedFee := new(big.Int).Mul(valueZNHB, big.NewInt(220))
	expectedFee.Div(expectedFee, big.NewInt(10_000))
	if znhb.Fee.Cmp(expectedFee) != 0 {
		t.Fatalf("unexpected ZNHB fee: got %s want %s", znhb.Fee, expectedFee)
	}
	if znhb.FeeBasisPoints != 220 {
		t.Fatalf("unexpected ZNHB MDR: %d", znhb.FeeBasisPoints)
	}
}

func TestFreeTierPerAssetScope(t *testing.T) {
	monthStart := time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC)
	value := big.NewInt(1_000)
	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 2,
		FreeTierPerAsset:   true,
		MDRBasisPoints:     150,
		Assets: map[string]fees.AssetPolicy{
			fees.AssetNHB:  {MDRBasisPoints: 150},
			fees.AssetZNHB: {MDRBasisPoints: 200},
		},
	}
	usage := map[string]uint64{
		fees.AssetNHB:  0,
		fees.AssetZNHB: 0,
	}
	for i := 0; i < 2; i++ {
		res := fees.Apply(fees.ApplyInput{
			Domain:        fees.DomainPOS,
			Gross:         value,
			UsageCount:    usage[fees.AssetNHB],
			PolicyVersion: 4,
			Config:        policy,
			WindowStart:   monthStart,
			Asset:         fees.AssetNHB,
		})
		if !res.FreeTierApplied {
			t.Fatalf("expected NHB tx %d to be free", i)
		}
		usage[fees.AssetNHB] = res.Counter
	}
	charged := fees.Apply(fees.ApplyInput{
		Domain:        fees.DomainPOS,
		Gross:         value,
		UsageCount:    usage[fees.AssetNHB],
		PolicyVersion: 4,
		Config:        policy,
		WindowStart:   monthStart,
		Asset:         fees.AssetNHB,
	})
	if charged.FreeTierApplied {
		t.Fatalf("expected NHB third transfer to be charged")
	}
	if charged.Counter != 3 {
		t.Fatalf("unexpected NHB counter: %d", charged.Counter)
	}

	znhb := fees.Apply(fees.ApplyInput{
		Domain:        fees.DomainPOS,
		Gross:         value,
		UsageCount:    usage[fees.AssetZNHB],
		PolicyVersion: 4,
		Config:        policy,
		WindowStart:   monthStart,
		Asset:         fees.AssetZNHB,
	})
	if !znhb.FreeTierApplied {
		t.Fatalf("expected ZNHB transfer to use its own free tier")
	}
	if znhb.Counter != 1 {
		t.Fatalf("unexpected ZNHB counter: %d", znhb.Counter)
	}
}

func monthStartUTC(ts time.Time) time.Time {
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func sameMonth(a, b time.Time) bool {
	ua := a.UTC()
	ub := b.UTC()
	return ua.Year() == ub.Year() && ua.Month() == ub.Month()
}

func TestApplySelectsAssetPolicy(t *testing.T) {
	var nhbWallet, znhbWallet [20]byte
	for i := range nhbWallet {
		nhbWallet[i] = byte(i + 1)
		znhbWallet[i] = byte(0xF0 + i)
	}
	gross := big.NewInt(10_000)
	policy := fees.DomainPolicy{
		FreeTierTxPerMonth: 0,
		MDRBasisPoints:     150,
		OwnerWallet:        nhbWallet,
		Assets: map[string]fees.AssetPolicy{
			fees.AssetNHB:  {MDRBasisPoints: 180, OwnerWallet: nhbWallet},
			fees.AssetZNHB: {MDRBasisPoints: 220, OwnerWallet: znhbWallet},
		},
	}
	nhb := fees.Apply(fees.ApplyInput{Domain: fees.DomainPOS, Gross: gross, PolicyVersion: 1, Config: policy, Asset: fees.AssetNHB})
	if nhb.FeeBasisPoints != 180 {
		t.Fatalf("unexpected NHB basis points: %d", nhb.FeeBasisPoints)
	}
	if nhb.OwnerWallet != nhbWallet {
		t.Fatalf("unexpected NHB wallet: %x", nhb.OwnerWallet)
	}
	znhb := fees.Apply(fees.ApplyInput{Domain: fees.DomainPOS, Gross: gross, PolicyVersion: 1, Config: policy, Asset: fees.AssetZNHB})
	if znhb.FeeBasisPoints != 220 {
		t.Fatalf("unexpected ZNHB basis points: %d", znhb.FeeBasisPoints)
	}
	if znhb.OwnerWallet != znhbWallet {
		t.Fatalf("unexpected ZNHB wallet: %x", znhb.OwnerWallet)
	}
	if znhb.Asset != fees.AssetZNHB {
		t.Fatalf("unexpected asset tag: %s", znhb.Asset)
	}
}
