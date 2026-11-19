package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/fees"
)

// W11 part A: from UniformTransferFeeActivationHeight a transfer that names a fee domain pays
// exactly what an ordinary transfer pays. These tests run real signed transfers through the
// state processor. The numbers are small round ones: the allowance is 1,000 wei, the ordinary
// rates are 20 (NHB) and 10 (ZNHB) basis points and the domain's merchant rates, which the rule
// stops charging, are 150 (NHB) and 200 (ZNHB); a transfer is 100,000, so the ordinary fee is 200
// NHB / 100 ZNHB and the merchant fee 1,500 NHB / 2,000 ZNHB.

const (
	uniformValue     = int64(100_000)
	uniformAllowance = int64(1_000)
)

type uniformAsset struct {
	name   string
	kind   types.TxType
	ordBps int64
	mdrBps int64
	znhb   bool
}

var uniformAssets = []uniformAsset{
	{"NHB", types.TxTypeTransfer, 20, 150, false},
	{"ZNHB", types.TxTypeTransferZNHB, 10, 200, true},
}

func (a uniformAsset) ordinary() int64 { return uniformValue * a.ordBps / 10_000 }
func (a uniformAsset) merchant() int64 { return uniformValue * a.mdrBps / 10_000 }

type uniformEnv struct {
	t    *testing.T
	sp   *StateProcessor
	keys map[string]*crypto.PrivateKey
	addr map[string][20]byte
}

// newUniformEnv builds a state processor with the transfer policy and a "pos" domain whose route
// wallet is the "route" role (none when withRoute is false). activation is the setting under test.
func newUniformEnv(t *testing.T, activation uint64, withRoute bool) *uniformEnv {
	t.Helper()
	e := &uniformEnv{t: t, sp: newStakingStateProcessor(t), keys: map[string]*crypto.PrivateKey{}, addr: map[string][20]byte{}}
	for _, role := range []string{"sender", "recipient", "collector", "route", "sponsor"} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate %s key: %v", role, err)
		}
		e.keys[role] = key
		var a [20]byte
		copy(a[:], key.PubKey().Address().Bytes())
		e.addr[role] = a
		balance := big.NewInt(0)
		if role == "sender" || role == "sponsor" {
			balance = big.NewInt(100_000_000)
		}
		if err := e.sp.setAccount(a[:], &types.Account{BalanceNHB: new(big.Int).Set(balance), BalanceZNHB: new(big.Int).Set(balance), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed %s: %v", role, err)
		}
	}
	e.sp.SetUniformTransferFee(activation)
	// The live chain has the NHB route-alias fix in force (FeeRouteAliasActivationHeight 1720900);
	// a bare state processor has it off, which would make the aliasing tests measure a bug that is
	// already fixed where it matters.
	e.sp.SetFeeRouteAliasActivationHeight(1)
	e.sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           true,
		FreeSpendLimitWei: big.NewInt(uniformAllowance),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      e.addr["collector"],
		FeeBps:            20,
		FeeBpsZNHB:        10,
	})
	var route [20]byte
	if withRoute {
		route = e.addr["route"]
	}
	e.sp.SetFeePolicy(fees.Policy{Version: 1, Domains: map[string]fees.DomainPolicy{
		"pos": {
			FreeTierTxPerMonth:    0,
			FreeTierTxPerMonthSet: true,
			MDRBasisPoints:        150,
			OwnerWallet:           route,
			Assets: map[string]fees.AssetPolicy{
				fees.AssetNHB:  {MDRBasisPoints: 150, OwnerWallet: route},
				fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: route},
			},
		},
	}})
	return e
}

func (e *uniformEnv) at(height uint64) { e.sp.BeginBlock(height, time.Unix(1_800_000_000, 0).UTC()) }

func (e *uniformEnv) balance(role string, znhb bool) *big.Int {
	e.t.Helper()
	a := e.addr[role]
	acc, err := e.sp.getAccount(a[:])
	if err != nil {
		e.t.Fatalf("load %s: %v", role, err)
	}
	if znhb {
		return new(big.Int).Set(acc.BalanceZNHB)
	}
	return new(big.Int).Set(acc.BalanceNHB)
}

// flows are the balance changes one transfer caused, in the transferred asset.
type flows struct{ sender, recipient, collector, route, sponsor int64 }

func (e *uniformEnv) snapshot(znhb bool) map[string]*big.Int {
	out := map[string]*big.Int{}
	for _, role := range []string{"sender", "recipient", "collector", "route", "sponsor"} {
		out[role] = e.balance(role, znhb)
	}
	return out
}

func (e *uniformEnv) delta(before map[string]*big.Int, znhb bool, role string) int64 {
	return new(big.Int).Sub(e.balance(role, znhb), before[role]).Int64()
}

// send applies one signed transfer from the sender and returns what moved.
func (e *uniformEnv) send(a uniformAsset, value int64, domain string, sponsored bool) flows {
	e.t.Helper()
	before := e.snapshot(a.znhb)
	senderAddr := e.addr["sender"]
	acc, err := e.sp.getAccount(senderAddr[:])
	if err != nil {
		e.t.Fatalf("load sender: %v", err)
	}
	recipient := e.addr["recipient"]
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            a.kind,
		Nonce:           acc.Nonce,
		To:              append([]byte(nil), recipient[:]...),
		Value:           big.NewInt(value),
		GasLimit:        21_000,
		GasPrice:        big.NewInt(1),
		MerchantAddress: domain,
	}
	if sponsored {
		sponsor := e.addr["sponsor"]
		tx.Paymaster = append([]byte(nil), sponsor[:]...)
		signPaymaster(e.t, tx, e.keys["sponsor"])
	}
	signTransaction(e.t, tx, e.keys["sender"])
	if err := e.sp.ApplyTransaction(tx); err != nil {
		e.t.Fatalf("apply %s transfer (domain %q): %v", a.name, domain, err)
	}
	return flows{
		sender:    e.delta(before, a.znhb, "sender"),
		recipient: e.delta(before, a.znhb, "recipient"),
		collector: e.delta(before, a.znhb, "collector"),
		route:     e.delta(before, a.znhb, "route"),
		sponsor:   e.delta(before, a.znhb, "sponsor"),
	}
}

func (e *uniformEnv) expect(label string, got, want flows) {
	e.t.Helper()
	if got != want {
		e.t.Fatalf("%s: moved %+v, want %+v (sender, recipient, collector, route, sponsor)", label, got, want)
	}
}

// Below the height, and with the setting off, a transfer naming a domain pays the ordinary fee AND
// the domain's merchant fee: the behaviour every committed block was produced with.
func TestDomainTransferPaysBothFeesWhileTheRuleIsOff(t *testing.T) {
	for _, a := range uniformAssets {
		for name, tc := range map[string]struct{ activation, height uint64 }{
			"setting off":         {0, 10_000},
			"one block below":     {600, 599},
			"far below":           {600, 1},
			"setting off, early":  {0, 1},
			"setting off, late":   {0, 1 << 40},
			"activation at max":   {^uint64(0), 1 << 40},
			"height zero context": {600, 0},
		} {
			e := newUniformEnv(t, tc.activation, true)
			e.at(tc.height)
			// First transfer: inside the allowance, no ordinary fee; the merchant fee is charged.
			e.expect(a.name+" "+name+" first", e.send(a, uniformValue, "pos", false),
				flows{sender: -(uniformValue + a.merchant()), recipient: uniformValue, route: a.merchant()})
			// Second: both fees.
			e.expect(a.name+" "+name+" second", e.send(a, uniformValue, "pos", false),
				flows{sender: -(uniformValue + a.ordinary() + a.merchant()), recipient: uniformValue, collector: a.ordinary(), route: a.merchant()})
		}
	}
}

// From the height the same transfer pays the ordinary fee only, and the fee goes to the
// domain's route wallet instead of the collector.
func TestDomainTransferPaysOnlyTheOrdinaryFeeFromTheHeight(t *testing.T) {
	for _, a := range uniformAssets {
		for _, height := range []uint64{600, 601, 5_000_000} {
			e := newUniformEnv(t, 600, true)
			e.at(height)
			e.expect(a.name+" first (inside the allowance)", e.send(a, uniformValue, "pos", false),
				flows{sender: -uniformValue, recipient: uniformValue})
			e.expect(a.name+" second", e.send(a, uniformValue, "pos", false),
				flows{sender: -(uniformValue + a.ordinary()), recipient: uniformValue, route: a.ordinary()})
			e.expect(a.name+" third", e.send(a, uniformValue, "pos", false),
				flows{sender: -(uniformValue + a.ordinary()), recipient: uniformValue, route: a.ordinary()})
		}
	}
}

// The property the owner asked for: naming a merchant domain costs the sender exactly what the
// same transfer costs with no domain, at every step of the allowance, in both assets.
func TestDomainTransferCostsTheSenderNoMoreThanAnOrdinaryTransfer(t *testing.T) {
	for _, a := range uniformAssets {
		domain := newUniformEnv(t, 600, true)
		plain := newUniformEnv(t, 600, true)
		domain.at(700)
		plain.at(700)
		for step, value := range []int64{50, 100_000, 100_000, 7, 100_000} {
			d := domain.send(a, value, "pos", false)
			p := plain.send(a, value, "", false)
			if d.sender != p.sender || d.recipient != p.recipient {
				t.Fatalf("%s step %d (value %d): a transfer naming a domain moved sender %d recipient %d, an ordinary one sender %d recipient %d",
					a.name, step, value, d.sender, d.recipient, p.sender, p.recipient)
			}
			// The fee itself lands on the route wallet for the domain and on the collector for the plain one.
			if d.route != p.collector || d.collector != 0 || p.route != 0 {
				t.Fatalf("%s step %d: domain route %d collector %d; plain collector %d route %d", a.name, step, d.route, d.collector, p.collector, p.route)
			}
		}
	}
}

// With no route wallet configured for the asset the fee goes to the collector, as for any transfer.
func TestDomainWithoutARouteWalletPaysTheCollector(t *testing.T) {
	for _, a := range uniformAssets {
		e := newUniformEnv(t, 600, false)
		e.at(700)
		e.send(a, uniformValue, "pos", false) // inside the allowance
		e.expect(a.name, e.send(a, uniformValue, "pos", false),
			flows{sender: -(uniformValue + a.ordinary()), recipient: uniformValue, collector: a.ordinary()})
	}
}

// A name the node has no policy for was never charged anything extra and is not now.
func TestUnknownDomainIsAnOrdinaryTransferAtEveryHeight(t *testing.T) {
	for _, a := range uniformAssets {
		for _, tc := range []struct{ activation, height uint64 }{{0, 700}, {600, 599}, {600, 700}} {
			e := newUniformEnv(t, tc.activation, true)
			e.at(tc.height)
			e.send(a, uniformValue, "shop", false)
			e.expect(a.name, e.send(a, uniformValue, "shop", false),
				flows{sender: -(uniformValue + a.ordinary()), recipient: uniformValue, collector: a.ordinary()})
		}
	}
}

// The domain is matched as before: surrounding space and case do not matter.
func TestDomainNameIsMatchedTrimmedAndCaseInsensitive(t *testing.T) {
	a := uniformAssets[0]
	for _, name := range []string{"pos", " pos", "POS ", "Pos"} {
		e := newUniformEnv(t, 600, true)
		e.at(700)
		e.send(a, uniformValue, name, false)
		e.expect(name, e.send(a, uniformValue, name, false),
			flows{sender: -(uniformValue + a.ordinary()), recipient: uniformValue, route: a.ordinary()})
	}
}

// A sponsored NHB transfer: the sponsor pays the ordinary fee to the collector as before; naming a
// domain adds nothing for the sender from the height (before it the sender also paid the merchant fee).
func TestSponsoredDomainTransfer(t *testing.T) {
	a := uniformAssets[0]
	for name, tc := range map[string]struct {
		activation uint64
		want       flows
	}{
		"rule off":  {0, flows{sender: -(uniformValue + a.merchant()), recipient: uniformValue, collector: a.ordinary(), route: a.merchant(), sponsor: -a.ordinary()}},
		"rule from": {600, flows{sender: -uniformValue, recipient: uniformValue, collector: a.ordinary(), sponsor: -a.ordinary()}},
	} {
		e := newUniformEnv(t, tc.activation, true)
		e.sp.SetPaymasterEnabled(true)
		e.at(700)
		e.expect(name, e.send(a, uniformValue, "pos", true), tc.want)
	}
}

// With the ordinary rate at zero there is no fee to route and none is invented.
func TestUniformRuleWithAZeroRateMovesNoFee(t *testing.T) {
	for _, a := range uniformAssets {
		e := newUniformEnv(t, 600, true)
		policy := e.sp.TransferGasPolicy()
		policy.FeeBps, policy.FeeBpsZNHB = 0, 0
		e.sp.SetTransferGasPolicy(policy)
		e.at(700)
		e.send(a, uniformValue, "pos", false)
		e.expect(a.name, e.send(a, uniformValue, "pos", false), flows{sender: -uniformValue, recipient: uniformValue})
	}
}

// Every way the four accounts of a transfer can coincide (the route wallet being the sender, the
// recipient or the collector, in every combination): nothing is created or destroyed.
func TestUniformFeeConservesBalancesInEveryRoleAssignment(t *testing.T) {
	roles := []string{"sender", "recipient", "collector", "route"}
	for _, a := range uniformAssets {
		for _, who := range roleAssignments(len(roles)) {
			e := newUniformEnv(t, 600, true)
			ids := map[int][20]byte{}
			addrFor := func(id int) [20]byte {
				if addr, ok := ids[id]; ok {
					return addr
				}
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Fatal(err)
				}
				var addr [20]byte
				copy(addr[:], key.PubKey().Address().Bytes())
				ids[id] = addr
				e.keys[idName(id)] = key
				return addr
			}
			sender, recipient, collector, route := addrFor(who[0]), addrFor(who[1]), addrFor(who[2]), addrFor(who[3])
			for _, addr := range ids {
				if err := e.sp.setAccount(addr[:], &types.Account{BalanceNHB: big.NewInt(50_000_000), BalanceZNHB: big.NewInt(50_000_000), Stake: big.NewInt(0)}); err != nil {
					t.Fatal(err)
				}
			}
			e.addr["sender"], e.addr["recipient"], e.addr["collector"], e.addr["route"] = sender, recipient, collector, route
			e.keys["sender"] = e.keys[idName(who[0])]
			policy := e.sp.TransferGasPolicy()
			policy.FeeCollector = collector
			e.sp.SetTransferGasPolicy(policy)
			e.sp.SetFeePolicy(fees.Policy{Version: 1, Domains: map[string]fees.DomainPolicy{"pos": {
				FreeTierTxPerMonthSet: true, MDRBasisPoints: 150, OwnerWallet: route,
				Assets: map[string]fees.AssetPolicy{fees.AssetNHB: {MDRBasisPoints: 150, OwnerWallet: route}, fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: route}},
			}}})
			e.at(700)
			total := func() *big.Int {
				sum := new(big.Int)
				for _, addr := range ids {
					acc, err := e.sp.getAccount(addr[:])
					if err != nil {
						t.Fatal(err)
					}
					if a.znhb {
						sum.Add(sum, acc.BalanceZNHB)
					} else {
						sum.Add(sum, acc.BalanceNHB)
					}
				}
				return sum
			}
			before := total()
			for i := 0; i < 3; i++ {
				acc, _ := e.sp.getAccount(sender[:])
				tx := &types.Transaction{ChainID: types.NHBChainID(), Type: a.kind, Nonce: acc.Nonce, To: append([]byte(nil), recipient[:]...), Value: big.NewInt(uniformValue), GasLimit: 21_000, GasPrice: big.NewInt(1), MerchantAddress: "pos"}
				signTransaction(t, tx, e.keys["sender"])
				if err := e.sp.ApplyTransaction(tx); err != nil {
					t.Fatalf("%s assignment %v transfer %d: %v", a.name, who, i, err)
				}
			}
			if after := total(); after.Cmp(before) != 0 {
				t.Fatalf("%s assignment %v: the accounts held %s before and %s after three transfers", a.name, who, before, after)
			}
		}
	}
}

func idName(id int) string { return "role-" + big.NewInt(int64(id)).String() }

// The domain's fee totals and the fee event are kept under the rule, with the fee the route wallet
// really received; the per-domain monthly counters are not advanced.
func TestUniformFeeKeepsTheDomainTotalsAndEventAndSkipsTheCounters(t *testing.T) {
	for _, a := range uniformAssets {
		e := newUniformEnv(t, 600, true)
		e.at(700)
		e.send(a, uniformValue, "pos", false)
		e.send(a, uniformValue, "pos", false)

		manager := nhbstate.NewManager(e.sp.Trie)
		totals, err := manager.FeesListTotals("pos")
		if err != nil {
			t.Fatalf("list totals: %v", err)
		}
		var found bool
		for _, total := range totals {
			if total.Asset != a.name {
				continue
			}
			found = true
			if total.Gross.Cmp(big.NewInt(2*uniformValue)) != 0 || total.Fee.Cmp(big.NewInt(a.ordinary())) != 0 || total.Net.Cmp(big.NewInt(2*uniformValue-a.ordinary())) != 0 {
				t.Fatalf("%s totals: gross %s fee %s net %s, want %d, %d, %d", a.name, total.Gross, total.Fee, total.Net, 2*uniformValue, a.ordinary(), 2*uniformValue-a.ordinary())
			}
			if total.Wallet != e.addr["route"] {
				t.Fatalf("%s totals are kept under wallet %x, want the route wallet", a.name, total.Wallet)
			}
		}
		if !found {
			t.Fatalf("%s: no totals recorded for the domain: %+v", a.name, totals)
		}

		var feeEvents []map[string]string
		for _, evt := range e.sp.Events() {
			if evt.Type == events.TypeFeeApplied {
				feeEvents = append(feeEvents, evt.Attributes)
			}
		}
		if len(feeEvents) != 2 {
			t.Fatalf("%s: %d fee events, want 2", a.name, len(feeEvents))
		}
		if feeEvents[0]["feeWei"] != "0" || feeEvents[0]["freeTierApplied"] != "true" {
			t.Fatalf("%s first event (inside the allowance): %v", a.name, feeEvents[0])
		}
		second := feeEvents[1]
		if second["feeWei"] != big.NewInt(a.ordinary()).String() || second["freeTierApplied"] != "false" || second["domain"] != "pos" || second["asset"] != a.name || second["feeBps"] != big.NewInt(a.ordBps).String() {
			t.Fatalf("%s second event: %v", a.name, second)
		}

		var payer [20]byte = e.addr["sender"]
		window := feeWindowStart(time.Unix(1_800_000_000, 0).UTC())
		counter, _, _, err := manager.FeesGetCounter("pos", payer, window, fees.FreeTierScopeAggregate)
		if err != nil {
			t.Fatalf("counter: %v", err)
		}
		if counter != 0 {
			t.Fatalf("%s: the domain's monthly counter advanced to %d under the uniform rule", a.name, counter)
		}
	}
	// The same transfers below the height do advance it (the counter is what the rule leaves alone).
	e := newUniformEnv(t, 600, true)
	e.at(10)
	e.send(uniformAssets[0], uniformValue, "pos", false)
	manager := nhbstate.NewManager(e.sp.Trie)
	counter, _, _, err := manager.FeesGetCounter("pos", e.addr["sender"], feeWindowStart(time.Unix(1_800_000_000, 0).UTC()), fees.FreeTierScopeAggregate)
	if err != nil || counter != 1 {
		t.Fatalf("below the height the counter is %d (%v), want 1", counter, err)
	}
}

func TestUniformFeeActiveBoundaries(t *testing.T) {
	sp := newStakingStateProcessor(t)
	at := func(h uint64) { sp.BeginBlock(h, time.Unix(1_800_000_000, 0)) }
	sp.SetUniformTransferFee(600)
	for height, want := range map[uint64]bool{0: false, 1: false, 599: false, 600: true, 601: true, 1 << 50: true} {
		at(height)
		if got := sp.uniformFeeActive(); got != want {
			t.Errorf("activation 600, height %d: active = %v, want %v", height, got, want)
		}
	}
	sp.SetUniformTransferFee(0)
	for _, height := range []uint64{0, 1, 600, 1 << 50} {
		at(height)
		if sp.uniformFeeActive() {
			t.Errorf("setting 0 (off), height %d: active", height)
		}
	}
	var nilSP *StateProcessor
	nilSP.SetUniformTransferFee(1)
	if nilSP.uniformFeeActive() {
		t.Error("a nil state processor is active")
	}
}

// The setting survives the copy a block's execution is built on, and the node carries it to a state
// processor it rebuilds.
func TestUniformTransferFeeSettingIsCopiedAndSurvivesARebuild(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetUniformTransferFee(777)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.uniformFeeFromHeight != 777 {
		t.Fatalf("the copy holds %d, want 777: a block built on a copy would run with the rule off", cp.uniformFeeFromHeight)
	}

	node := newTestNode(t)
	node.SetUniformTransferFee(9)
	node.stateMu.Lock()
	gotH := node.state.uniformFeeFromHeight
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.uniformFeeFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuilt != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuilt)
	}
}

// The role harness of the supply invariant (treasury as sender, recipient, collector and route
// wallet, every aliasing) under the uniform rule: a ZNHB transfer naming the domain keeps the pool
// ledger in step and conserves ZNHB.
func TestUniformFeeZNHBRolesKeepTheSupplyInvariant(t *testing.T) {
	names := []string{"sender", "recipient", "feeCollector", "domainFeeWallet"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.height = 5
			f.sp.SetUniformTransferFee(1)
			sender, recipient, collector, wallet := who[0], who[1], who[2], who[3]
			f.fund(sender, 5_000_000)
			f.fund(recipient, 5_000_000)
			f.sp.SetTransferGasPolicy(TransferGasPolicy{Enabled: false, FeeCollector: f.addr(collector), FeeBps: 20, FeeBpsZNHB: 10})
			f.sp.SetFeePolicy(fees.Policy{Version: 1, Domains: map[string]fees.DomainPolicy{
				"pos": {
					FreeTierTxPerMonthSet: true, MDRBasisPoints: 200, OwnerWallet: f.addr(wallet),
					Assets: map[string]fees.AssetPolicy{fees.AssetZNHB: {MDRBasisPoints: 200, OwnerWallet: f.addr(wallet)}},
				},
			}})
			f.mustApply(name, f.transferZNHB(sender, recipient, 100_000, "pos"))
		})
	}
}
