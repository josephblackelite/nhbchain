package core

import (
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

// The proposer refuses to propose a transaction that reads the wall clock while
// it applies (see installClockGuard): a value taken from the clock and written
// into state differs from one execution to the next, so a block containing it
// cannot be re-executed identically by another validator. Escrow and trade
// timestamps used to be such a value; they now come from the block time, so no
// shipped transaction type reads the clock and the detector is only the guard
// against a change that brings a read back. These tests pin both halves: the
// escrow transactions are proposed and included, and the detector, with its
// environment override, still works for a transaction that does read the clock.

// clockProbe makes exactly the transactions in probes read the process clock
// while they apply on the proposer, the way a transaction that stamped a
// wall-clock value into state would.
func clockProbe(t *testing.T, node *Node, probes ...*types.Transaction) {
	t.Helper()
	want := make(map[string]struct{}, len(probes))
	for _, tx := range probes {
		key, err := transactionKey(tx)
		if err != nil {
			t.Fatalf("key of probe transaction: %v", err)
		}
		want[key] = struct{}{}
	}
	node.setProposalAppliedHook(func(sp *StateProcessor, tx *types.Transaction) {
		key, err := transactionKey(tx)
		if err != nil {
			return
		}
		if _, ok := want[key]; ok {
			sp.now()
		}
	})
}

// The escrow, delegated and realm transactions the detector used to hold out
// (create, refund, delegated create and refund, realm create and update) apply
// without a single read of the process clock, next to the ones that never read
// it (lock, dispute), even when the state processor keeps the real clock.
func TestEscrowTransactionsDoNotReadTheWallClock(t *testing.T) {
	fx := newEscrowClockFixture(t)
	// A nil clock is the process wall clock, the one the detector watches.
	sp := fx.newProcessor(t, escrowClockWallA)
	sp.nowFunc = nil
	reads := installClockGuard(sp, false)
	if reads == nil {
		t.Fatalf("the detector's clock guard must be installed on a state processor that keeps the real clock")
	}

	payer, payee := escrowClockAddr(fx.payer), escrowClockAddr(fx.payee)
	deadline := escrowClockBlockTime + 7200
	realmData := func(profile string) []byte {
		data, err := jsonMarshal(struct {
			ID              string   `json:"id"`
			Threshold       uint32   `json:"threshold"`
			Scheme          uint8    `json:"scheme"`
			Members         []string `json:"members"`
			Scope           uint8    `json:"scope"`
			ProviderProfile string   `json:"providerProfile"`
		}{
			ID:              "clock-realm",
			Threshold:       1,
			Scheme:          uint8(escrow.ArbitrationSchemeSingle),
			Members:         []string{fx.arbitrator.PubKey().Address().String()},
			Scope:           uint8(escrow.EscrowRealmScopePlatform),
			ProviderProfile: profile,
		})
		if err != nil {
			t.Fatalf("marshal realm payload: %v", err)
		}
		return data
	}
	id1 := escrowClockID(payer, payee, 1)
	id2 := escrowClockID(payer, payee, 2)
	steps := []struct {
		name string
		tx   *types.Transaction
	}{
		{"create realm", escrowClockTx(t, fx.admin, types.TxTypeEscrowCreateRealm, 0, realmData("core-team"))},
		{"update realm", escrowClockTx(t, fx.admin, types.TxTypeEscrowUpdateRealm, 1, realmData("core-team-v2"))},
		{"create escrow", escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 0, escrowClockCreateData(t, payee, deadline, 1))},
		{"lock escrow", escrowClockTx(t, fx.payer, types.TxTypeLockEscrow, 1, id1[:])},
		{"dispute escrow", escrowClockTx(t, fx.payer, types.TxTypeDisputeEscrow, 2, id1[:])},
		{"create second escrow", escrowClockTx(t, fx.payer, types.TxTypeCreateEscrow, 3, escrowClockCreateData(t, payee, deadline, 2))},
		{"lock second escrow", escrowClockTx(t, fx.payer, types.TxTypeLockEscrow, 4, id2[:])},
		{"refund second escrow", escrowClockTx(t, fx.payer, types.TxTypeRefundEscrow, 5, id2[:])},
	}
	for _, step := range steps {
		before := clockReads(reads)
		if err := sp.ApplyTransaction(step.tx); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := clockReads(reads); got != before {
			t.Fatalf("%s read the wall clock %d time(s) while applying", step.name, got-before)
		}
	}
}

// Escrow creation and refund are proposed and included with the state processors
// on the process wall clock and the detector on (the default), and the block is
// re-executed identically by the validator a wall-clock second later. Before
// escrow timestamps came from the block time such a block could not be
// validated a second after it was built.
func TestEscrowCreateAndRefundAreProposedAndIncluded(t *testing.T) {
	payerKey, payeeKey := livenessKey(t), livenessKey(t)
	// deterministicClock is deliberately left off: the state processors keep the
	// process wall clock, which is what the detector watches.
	pair := newLivenessPair(t, livenessPairOptions{funded: []*crypto.PrivateKey{payerKey, payeeKey}})
	payer, payee := payerKey.PubKey().Address().Bytes(), payeeKey.PubKey().Address().Bytes()
	id := escrowID(payer, payee, 1)
	deadline := pair.clock.Add(2 * time.Hour).Unix()
	nondeterministic := map[string]string{"disposition": "nondeterministic"}
	before := livenessMetricValue(t, "nhb_mempool_tx_failures_total", nondeterministic)

	create := livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0, escrowCreatePayload(t, payee, 100, deadline, 1), nil, 0)
	honest := livenessTransfer(t, payeeKey, 0)
	pair.submit(create)
	pair.submit(honest)
	block := pair.build()
	if !livenessBlockContains(t, block, create) {
		t.Fatalf("the escrow create must be proposed")
	}
	if !livenessBlockContains(t, block, honest) {
		t.Fatalf("the honest transaction must be proposed too")
	}
	if got := livenessMetricValue(t, "nhb_mempool_tx_failures_total", nondeterministic); got != before {
		t.Fatalf("the escrow create was counted as a nondeterministic failure (%v -> %v)", before, got)
	}
	if rec, ok := livenessStrikes(t, pair.proposer, create); ok {
		t.Fatalf("the escrow create must carry no strike, got %+v", rec)
	}
	// Cross a wall-clock second between building and validating.
	time.Sleep(1300 * time.Millisecond)
	pair.commit(block)

	lock := livenessSign(t, payerKey, types.TxTypeLockEscrow, 1, id[:], nil, 0)
	pair.submit(lock)
	block = pair.build()
	if !livenessBlockContains(t, block, lock) {
		t.Fatalf("the escrow lock must be proposed")
	}
	pair.commit(block)

	// A refund compares the deadline with the block time: before it, the refund is
	// proposed and included whatever the wall clock says.
	pair.advance(10 * time.Minute)
	refund := livenessSign(t, payerKey, types.TxTypeRefundEscrow, 2, id[:], nil, 0)
	pair.submit(refund)
	block = pair.build()
	if !livenessBlockContains(t, block, refund) {
		t.Fatalf("the escrow refund must be proposed")
	}
	time.Sleep(1300 * time.Millisecond)
	pair.commit(block)

	if got := livenessMetricValue(t, "nhb_mempool_tx_failures_total", nondeterministic); got != before {
		t.Fatalf("an escrow transaction was counted as a nondeterministic failure (%v -> %v)", before, got)
	}
	if rec, ok := livenessStrikes(t, pair.proposer, refund); ok {
		t.Fatalf("the escrow refund must carry no strike, got %+v", rec)
	}
}

// The same holds with the detector switched off: nothing about an escrow block
// depends on when it is re-executed any more.
func TestEscrowCreateBlockValidatesLaterWithTheDetectorOff(t *testing.T) {
	payerKey, payeeKey := livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{funded: []*crypto.PrivateKey{payerKey, payeeKey}})
	pair.proposer.setBuildConfig(func(cfg *buildConfig) { cfg.AllowClockDependent = true })
	create := livenessSign(t, payerKey, types.TxTypeCreateEscrow, 0,
		escrowCreatePayload(t, payeeKey.PubKey().Address().Bytes(), 100, pair.clock.Add(2*time.Hour).Unix(), 1), nil, 0)
	pair.submit(create)

	block := pair.build()
	if !livenessBlockContains(t, block, create) {
		t.Fatalf("the escrow create must be proposed")
	}
	time.Sleep(1300 * time.Millisecond)
	if err := pair.validator.ValidateBlock(block); err != nil {
		t.Fatalf("a block with an escrow create must validate a wall-clock second after it was built: %v", err)
	}
}

// A transaction that does read the wall clock while it applies is never
// proposed, is struck, and is evicted once the strikes are confirmed, without
// hiding the transactions around it.
func TestClockReadingTransactionIsNeverProposed(t *testing.T) {
	probeKey, honestKey := livenessKey(t), livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{funded: []*crypto.PrivateKey{probeKey, honestKey}})
	probe := livenessTransfer(t, probeKey, 0)
	honest := livenessTransfer(t, honestKey, 0)
	clockProbe(t, pair.proposer, probe)
	pair.submit(probe)
	pair.submit(honest)

	nondeterministic := map[string]string{"disposition": "nondeterministic"}
	before := livenessMetricValue(t, "nhb_mempool_tx_failures_total", nondeterministic)
	block := pair.build()
	if livenessBlockContains(t, block, probe) {
		t.Fatalf("a transaction that reads the wall clock must not be proposed")
	}
	if !livenessBlockContains(t, block, honest) {
		t.Fatalf("the honest transaction must still be proposed")
	}
	if got := livenessMetricValue(t, "nhb_mempool_tx_failures_total", nondeterministic); got-before != 1 {
		t.Fatalf("expected one nondeterministic failure to be counted, got %v", got-before)
	}
	if rec, ok := livenessStrikes(t, pair.proposer, probe); !ok || rec.strikes != 1 {
		t.Fatalf("the clock-reading transaction must carry one strike, got %+v ok=%v", rec, ok)
	}
	pair.commit(block)

	// It is evicted after the strike limit, confirmed by a solo dry run that
	// reads the clock as well.
	requireBlockBuildsUntil(t, "eviction of the clock-reading transaction", 12,
		func() { pair.mine() },
		func() bool { return !livenessResident(t, pair.proposer, probe) })
}

// NHB_ALLOW_CLOCK_DEPENDENT_TXS=true, read through loadBuildConfig, switches the
// detector off: the same clock-reading transaction is proposed.
func TestClockDetectorCanBeSwitchedOffFromTheEnvironment(t *testing.T) {
	probeKey := livenessKey(t)
	pair := newLivenessPair(t, livenessPairOptions{funded: []*crypto.PrivateKey{probeKey}})
	env := map[string]string{"NHB_ALLOW_CLOCK_DEPENDENT_TXS": "true"}
	cfg, warnings := loadBuildConfig(defaultBuildConfig(), func(name string) string { return env[name] })
	if len(warnings) != 0 || !cfg.AllowClockDependent {
		t.Fatalf("the override must parse: allow=%v warnings=%v", cfg.AllowClockDependent, warnings)
	}
	pair.proposer.setBuildConfig(func(active *buildConfig) { *active = cfg })

	probe := livenessTransfer(t, probeKey, 0)
	clockProbe(t, pair.proposer, probe)
	pair.submit(probe)
	block := pair.build()
	if !livenessBlockContains(t, block, probe) {
		t.Fatalf("with the detector switched off the clock-reading transaction is proposed")
	}
	if rec, ok := livenessStrikes(t, pair.proposer, probe); ok {
		t.Fatalf("no strike may be recorded with the detector off, got %+v", rec)
	}
}
