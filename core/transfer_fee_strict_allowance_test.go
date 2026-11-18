package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
)

// W11 second part: the strict free allowance. The free part of a transfer is what is left of the allowance (never below
// zero, never more than the amount) and the fee is the rate on the rest. Numbers: allowance 1,000,000 units, 20 basis
// points NHB and 10 ZNHB; sender balance 100,000,000 in each asset.

const strictLimit = int64(1_000_000)

func bigS(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad integer " + s)
	}
	return v
}

// newStrictEnv is the uniform-fee environment with an allowance of strictLimit and the strict allowance from strictAt.
func newStrictEnv(t *testing.T, strictAt uint64) *uniformEnv {
	t.Helper()
	e := newUniformEnv(t, 0, true)
	policy := e.sp.TransferGasPolicy()
	policy.FreeSpendLimitWei = big.NewInt(strictLimit)
	e.sp.SetTransferGasPolicy(policy)
	e.sp.SetTransferFeeStrictAllowance(strictAt)
	return e
}

func rateFee(a uniformAsset, chargeable int64) int64 { return chargeable * a.ordBps / 10_000 }

func TestFeeWithAllowanceTable(t *testing.T) {
	p := TransferGasPolicy{Enabled: true, FreeSpendLimitWei: big.NewInt(1000), FeeBps: 20, FeeBpsZNHB: 10}
	type want struct{ free, chargeable, fee string }
	for name, tc := range map[string]struct {
		asset               string
		amount, left        string
		strict              bool
		wantStrict, wantOld want
	}{
		"nothing left":                {"NHB", "100000", "0", true, want{"0", "100000", "200"}, want{"0", "100000", "200"}},
		"more than the amount left":   {"NHB", "100000", "5000000", true, want{"100000", "0", "0"}, want{"100000", "0", "0"}},
		"exactly the amount left":     {"NHB", "100000", "100000", true, want{"100000", "0", "0"}, want{"100000", "0", "0"}},
		"one wei short of the amount": {"NHB", "100000", "99999", true, want{"99999", "1", "0"}, want{"100000", "0", "0"}},
		"crossing the line":           {"NHB", "1100000", "1000000", true, want{"1000000", "100000", "200"}, want{"1100000", "0", "0"}},
		"one wei left":                {"NHB", "20001", "1", true, want{"1", "20000", "40"}, want{"20001", "0", "0"}},
		"rounds down to nothing":      {"NHB", "1000499", "1000000", true, want{"1000000", "499", "0"}, want{"1000499", "0", "0"}},
		"ZNHB rate":                   {"ZNHB", "1100000", "1000000", true, want{"1000000", "100000", "100"}, want{"1100000", "0", "0"}},
		"asset name case":             {"znhb", "1100000", "1000000", true, want{"1000000", "100000", "100"}, want{"1100000", "0", "0"}},
		"unknown asset uses NHB rate": {"", "1100000", "1000000", true, want{"1000000", "100000", "200"}, want{"1100000", "0", "0"}},
		"far beyond 128 bits": {"NHB", "1000000000000000000000000000000000000000001", "1000000000000000000000", true,
			want{"1000000000000000000000", "999999999999999999999000000000000000000001", "1999999999999999999998000000000000000000"},
			want{"1000000000000000000000000000000000000000001", "0", "0"}},
	} {
		amount, left := bigS(tc.amount), bigS(tc.left)
		amountBefore, leftBefore := new(big.Int).Set(amount), new(big.Int).Set(left)
		for _, strict := range []bool{true, false} {
			free, chargeable, fee := p.FeeWithAllowance(tc.asset, amount, left, strict)
			w := tc.wantStrict
			if !strict {
				w = tc.wantOld
			}
			if free.String() != w.free || chargeable.String() != w.chargeable || fee.String() != w.fee {
				t.Errorf("%s strict=%v: free %s chargeable %s fee %s, want %s %s %s", name, strict, free, chargeable, fee, w.free, w.chargeable, w.fee)
			}
			if new(big.Int).Add(free, chargeable).Cmp(amount) != 0 {
				t.Errorf("%s strict=%v: free + chargeable = %s, want the amount %s", name, strict, new(big.Int).Add(free, chargeable), amount)
			}
		}
		if amount.Cmp(amountBefore) != 0 || left.Cmp(leftBefore) != 0 {
			t.Errorf("%s: the inputs were modified (%s %s)", name, amount, left)
		}
	}
	// Degenerate inputs: nothing to charge, never a nil, never a panic, never a negative figure.
	for name, in := range map[string][2]*big.Int{
		"nil amount": {nil, big.NewInt(5)}, "zero amount": {big.NewInt(0), big.NewInt(5)}, "negative amount": {big.NewInt(-5), big.NewInt(5)},
	} {
		for _, strict := range []bool{true, false} {
			free, chargeable, fee := p.FeeWithAllowance("NHB", in[0], in[1], strict)
			if free == nil || chargeable == nil || fee == nil || free.Sign() != 0 || chargeable.Sign() != 0 || fee.Sign() != 0 {
				t.Errorf("%s strict=%v: %v %v %v", name, strict, free, chargeable, fee)
			}
		}
	}
	for name, left := range map[string]*big.Int{"nil remaining": nil, "negative remaining": big.NewInt(-9)} {
		for _, strict := range []bool{true, false} {
			free, chargeable, fee := p.FeeWithAllowance("NHB", big.NewInt(100000), left, strict)
			if free.Sign() != 0 || chargeable.Cmp(big.NewInt(100000)) != 0 || fee.Cmp(big.NewInt(200)) != 0 {
				t.Errorf("%s strict=%v: %s %s %s, want 0 100000 200", name, strict, free, chargeable, fee)
			}
		}
	}
	// A zero rate has no fee at any split.
	zero := TransferGasPolicy{Enabled: true}
	if _, chargeable, fee := zero.FeeWithAllowance("NHB", big.NewInt(500), big.NewInt(100), true); chargeable.Cmp(big.NewInt(400)) != 0 || fee.Sign() != 0 {
		t.Errorf("zero rate: chargeable %s fee %s", chargeable, fee)
	}
}

// Off, below the height and at the largest activation: the whole-transfer rule, byte for byte what every committed
// block was produced with: a first transfer of any size is free.
func TestStrictAllowanceOffKeepsTheWholeTransferRule(t *testing.T) {
	for _, a := range uniformAssets {
		for name, tc := range map[string]struct{ activation, height uint64 }{
			"setting off":     {0, 10_000},
			"one block below": {600, 599},
			"far below":       {600, 1},
			"activation max":  {^uint64(0), 1 << 40},
		} {
			e := newStrictEnv(t, tc.activation)
			e.at(tc.height)
			e.expect(a.name+" "+name+" first of 1,100,000", e.send(a, 1_100_000, "", false), flows{sender: -1_100_000, recipient: 1_100_000})
			e.expect(a.name+" "+name+" second", e.send(a, 100_000, "", false), flows{sender: -(100_000 + rateFee(a, 100_000)), recipient: 100_000, collector: rateFee(a, 100_000)})
			big := newStrictEnv(t, tc.activation)
			big.at(tc.height)
			big.expect(a.name+" "+name+" first of 50,000,000", big.send(a, 50_000_000, "", false), flows{sender: -50_000_000, recipient: 50_000_000})
		}
	}
}

// From the height the fee is on the part above what is left of the allowance, in both assets.
func TestStrictAllowanceChargesOnlyThePartAboveTheLine(t *testing.T) {
	for _, a := range uniformAssets {
		for _, height := range []uint64{600, 601, 5_000_000} {
			// A first transfer that crosses the line: 100,000 above it pays; the next pays in full (nothing is left).
			e := newStrictEnv(t, 600)
			e.at(height)
			fee := rateFee(a, 100_000)
			e.expect(a.name+" crossing", e.send(a, 1_100_000, "", false), flows{sender: -(1_100_000 + fee), recipient: 1_100_000, collector: fee})
			e.expect(a.name+" next", e.send(a, 100_000, "", false), flows{sender: -(100_000 + fee), recipient: 100_000, collector: fee})

			// Exactly the allowance: free; then a transfer of the same size pays in full.
			e = newStrictEnv(t, 600)
			e.at(height)
			e.expect(a.name+" exactly the allowance", e.send(a, 1_000_000, "", false), flows{sender: -1_000_000, recipient: 1_000_000})
			e.expect(a.name+" then the same", e.send(a, 1_000_000, "", false), flows{sender: -(1_000_000 + rateFee(a, 1_000_000)), recipient: 1_000_000, collector: rateFee(a, 1_000_000)})

			// One wei short of the allowance, then a transfer that crosses by 20,000: only those 20,000 pay.
			e = newStrictEnv(t, 600)
			e.at(height)
			e.expect(a.name+" 999,999", e.send(a, 999_999, "", false), flows{sender: -999_999, recipient: 999_999})
			f := rateFee(a, 20_000)
			e.expect(a.name+" crossing by 20,000", e.send(a, 20_001, "", false), flows{sender: -(20_001 + f), recipient: 20_001, collector: f})
			e.expect(a.name+" then 10 (rounds down to no fee)", e.send(a, 10, "", false), flows{sender: -10, recipient: 10})

			// A first transfer many times the allowance pays on all but the allowance.
			e = newStrictEnv(t, 600)
			e.at(height)
			f = rateFee(a, 49_000_000)
			e.expect(a.name+" 50,000,000 first", e.send(a, 50_000_000, "", false), flows{sender: -(50_000_000 + f), recipient: 50_000_000, collector: f})

			// A chargeable part so small that the fee rounds down to nothing: nothing is charged.
			e = newStrictEnv(t, 600)
			e.at(height)
			e.expect(a.name+" crossing by 499", e.send(a, 1_000_499, "", false), flows{sender: -1_000_499, recipient: 1_000_499})
		}
	}
}

// Exact integers well beyond 64 and 128 bits: the live allowance of 1,000 tokens and a first transfer of 1e30 + 1 wei.
func TestStrictAllowanceIsExactBeyond128Bits(t *testing.T) {
	for _, a := range uniformAssets {
		for _, strict := range []bool{true, false} {
			e := newUniformEnv(t, 0, true)
			policy := e.sp.TransferGasPolicy()
			policy.FreeSpendLimitWei = bigS("1000000000000000000000")
			e.sp.SetTransferGasPolicy(policy)
			if strict {
				e.sp.SetTransferFeeStrictAllowance(1)
			}
			rich := bigS("2000000000000000000000000000000")
			senderAddr := e.addr["sender"]
			if err := e.sp.setAccount(senderAddr[:], &types.Account{BalanceNHB: new(big.Int).Set(rich), BalanceZNHB: new(big.Int).Set(rich), Stake: big.NewInt(0)}); err != nil {
				t.Fatal(err)
			}
			e.at(10)
			amount := bigS("1000000000000000000000000000001") // 1e30 + 1 wei
			before := e.balance("sender", a.znhb)
			recipientBefore := e.balance("recipient", a.znhb)
			collectorBefore := e.balance("collector", a.znhb)
			// send with an amount that does not fit an int64: build the transfer here.
			recipient := e.addr["recipient"]
			tx := &types.Transaction{ChainID: types.NHBChainID(), Type: a.kind, Nonce: 0, To: append([]byte(nil), recipient[:]...), Value: new(big.Int).Set(amount), GasLimit: 21_000, GasPrice: big.NewInt(1)}
			signTransaction(t, tx, e.keys["sender"])
			if err := e.sp.ApplyTransaction(tx); err != nil {
				t.Fatalf("%s strict=%v: %v", a.name, strict, err)
			}
			wantFee := "0"
			if strict {
				// chargeable = 1e30 + 1 - 1e21 = 999,999,999,000,000,000,000,000,000,001; NHB 20 bps, ZNHB 10 bps (rounded down)
				wantFee = map[string]string{"NHB": "1999999998000000000000000000", "ZNHB": "999999999000000000000000000"}[a.name]
			}
			gotFee := new(big.Int).Sub(e.balance("collector", a.znhb), collectorBefore)
			if gotFee.String() != wantFee {
				t.Fatalf("%s strict=%v: collector gained %s, want %s", a.name, strict, gotFee, wantFee)
			}
			lost := new(big.Int).Sub(before, e.balance("sender", a.znhb))
			if want := new(big.Int).Add(amount, bigS(wantFee)); lost.Cmp(want) != 0 {
				t.Fatalf("%s strict=%v: sender lost %s, want %s", a.name, strict, lost, want)
			}
			if got := new(big.Int).Sub(e.balance("recipient", a.znhb), recipientBefore); got.Cmp(amount) != 0 {
				t.Fatalf("%s strict=%v: recipient gained %s, want %s", a.name, strict, got, amount)
			}
		}
	}
}

// The strict rule changes only the fee: what is recorded as spent is still the whole amount.
func TestStrictAllowanceRecordsTheWholeAmountAsSpent(t *testing.T) {
	for _, a := range uniformAssets {
		e := newStrictEnv(t, 1)
		e.at(10)
		e.send(a, 1_100_000, "", false)
		sender := e.addr["sender"]
		status, err := nhbstate.NewManager(e.sp.Trie).TransferGasSpendStatus(sender, nhbstate.TransferGasWindowLifetime, time.Unix(1_800_000_000, 0).UTC(), big.NewInt(strictLimit), a.name)
		if err != nil {
			t.Fatal(err)
		}
		if status.Spent.Cmp(big.NewInt(1_100_000)) != 0 || status.Remaining.Sign() != 0 || status.Eligible {
			t.Fatalf("%s after a crossing transfer: spent %s remaining %s eligible %v", a.name, status.Spent, status.Remaining, status.Eligible)
		}
	}
}

// A disabled policy (no allowance) charges the whole amount under either rule.
func TestStrictAllowanceWithNoAllowanceChargesTheWholeAmount(t *testing.T) {
	for _, a := range uniformAssets {
		for _, strictAt := range []uint64{0, 1} {
			e := newStrictEnv(t, strictAt)
			policy := e.sp.TransferGasPolicy()
			policy.Enabled = false
			policy.FreeSpendLimitWei = big.NewInt(0)
			e.sp.SetTransferGasPolicy(policy)
			e.at(10)
			f := rateFee(a, 100_000)
			e.expect(a.name, e.send(a, 100_000, "", false), flows{sender: -(100_000 + f), recipient: 100_000, collector: f})
		}
	}
}

// A transfer that names a fee domain under the uniform rule pays what a plain transfer pays under the strict allowance,
// and the fee still goes to the domain's route wallet.
func TestStrictAllowanceAppliesToDomainTransfersUnderTheUniformRule(t *testing.T) {
	for _, a := range uniformAssets {
		domain := newStrictEnv(t, 1)
		domain.sp.SetUniformTransferFee(1)
		plain := newStrictEnv(t, 1)
		plain.sp.SetUniformTransferFee(1)
		domain.at(10)
		plain.at(10)
		for step, value := range []int64{1_100_000, 100_000, 7, 2_000_000} {
			d := domain.send(a, value, "pos", false)
			p := plain.send(a, value, "", false)
			if d.sender != p.sender || d.recipient != p.recipient || d.route != p.collector || d.collector != 0 {
				t.Fatalf("%s step %d (value %d): domain %+v, plain %+v", a.name, step, value, d, p)
			}
		}
		// The first step charged only the part above the allowance: 100,000 of 1,100,000.
		e := newStrictEnv(t, 1)
		e.sp.SetUniformTransferFee(1)
		e.at(10)
		f := rateFee(a, 100_000)
		e.expect(a.name+" first domain transfer", e.send(a, 1_100_000, "pos", false), flows{sender: -(1_100_000 + f), recipient: 1_100_000, route: f})
	}
}

// A governed allowance is the allowance the strict rule measures against.
func TestStrictAllowanceUsesTheGovernedAllowance(t *testing.T) {
	e := newStrictEnv(t, 1)
	e.sp.SetTransferFeeGovernance(1, 100)
	record := governance.FeeScheduleRecord{Active: &governance.ScheduledFeeValues{
		FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 50, ZNHBFeeBps: 30, FreeAllowanceWei: "5000000"}, EffectiveHeight: 1, ProposalID: 1}}
	if err := nhbstate.NewManager(e.sp.Trie).ParamStoreSet(governance.ParamKeyTransferFeeSchedule, mustJSON(t, record)); err != nil {
		t.Fatal(err)
	}
	e.at(10)
	// Governed: allowance 5,000,000 and 50 bps NHB / 30 bps ZNHB. A first transfer of 6,000,000 pays on 1,000,000.
	e.expect("NHB", e.send(uniformAssets[0], 6_000_000, "", false), flows{sender: -(6_000_000 + 5_000), recipient: 6_000_000, collector: 5_000})
	e.expect("ZNHB", e.send(uniformAssets[1], 6_000_000, "", false), flows{sender: -(6_000_000 + 3_000), recipient: 6_000_000, collector: 3_000})
}

// The r12 monthly free tier, where it applies, still waives the whole remaining fee, and still counts the transfer.
func TestStrictAllowanceLeavesTheMonthlyFreeTierAlone(t *testing.T) {
	a := uniformAssets[0]
	e := newStrictEnv(t, 1)
	e.sp.SetTransferMonthlyFreeTier(1, 2) // the first 2 plain transfers each month pay no fee
	e.at(10)
	e.expect("first (crossing, in the tier)", e.send(a, 1_100_000, "", false), flows{sender: -1_100_000, recipient: 1_100_000})
	e.expect("second (in the tier)", e.send(a, 100_000, "", false), flows{sender: -100_000, recipient: 100_000})
	f := rateFee(a, 100_000)
	e.expect("third (the tier is used up)", e.send(a, 100_000, "", false), flows{sender: -(100_000 + f), recipient: 100_000, collector: f})
	// Without the tier the first transfer pays on its excess.
	n := newStrictEnv(t, 1)
	n.at(10)
	n.expect("first without the tier", n.send(a, 1_100_000, "", false), flows{sender: -(1_100_000 + f), recipient: 1_100_000, collector: f})
}

// A sponsored transfer: the sponsor pays the fee on the whole amount, the sender's allowance is not consulted, and the
// transfer still counts as the sender's spend, under the strict rule exactly as before.
func TestStrictAllowanceAndSponsoredTransfers(t *testing.T) {
	a := uniformAssets[0]
	for _, strictAt := range []uint64{0, 1} {
		e := newStrictEnv(t, strictAt)
		e.sp.SetPaymasterEnabled(true)
		e.at(10)
		sponsoredFee := rateFee(a, 600_000)
		e.expect("sponsored 600,000", e.send(a, 600_000, "", true), flows{sender: -600_000, recipient: 600_000, collector: sponsoredFee, sponsor: -sponsoredFee})
		// 600,000 of the allowance is spent by the sponsored transfer, 400,000 is left: the sender's own 600,000 transfer
		// is free under the whole-transfer rule and pays on 200,000 under the strict one.
		want := flows{sender: -600_000, recipient: 600_000}
		if strictAt != 0 {
			f := rateFee(a, 200_000)
			want = flows{sender: -(600_000 + f), recipient: 600_000, collector: f}
		}
		e.expect("own 600,000 afterwards", e.send(a, 600_000, "", false), want)
	}
}

// With the monthly window the allowance resets with the month: the first transfer of each month crosses again.
func TestStrictAllowanceFollowsTheAllowanceWindow(t *testing.T) {
	a := uniformAssets[0]
	e := newStrictEnv(t, 1)
	policy := e.sp.TransferGasPolicy()
	policy.Window = TransferGasWindowMonthly
	e.sp.SetTransferGasPolicy(policy)
	f := rateFee(a, 100_000)
	e.sp.BeginBlock(10, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	e.expect("November first", e.send(a, 1_100_000, "", false), flows{sender: -(1_100_000 + f), recipient: 1_100_000, collector: f})
	e.expect("November second", e.send(a, 100_000, "", false), flows{sender: -(100_000 + f), recipient: 100_000, collector: f})
	e.sp.BeginBlock(11, time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC))
	e.expect("December first (a new window)", e.send(a, 1_100_000, "", false), flows{sender: -(1_100_000 + f), recipient: 1_100_000, collector: f})
}

func TestStrictAllowanceActiveBoundaries(t *testing.T) {
	sp := newStakingStateProcessor(t)
	at := func(h uint64) { sp.BeginBlock(h, time.Unix(1_800_000_000, 0)) }
	sp.SetTransferFeeStrictAllowance(600)
	for height, want := range map[uint64]bool{0: false, 1: false, 599: false, 600: true, 601: true, 1 << 50: true} {
		at(height)
		if got := sp.strictAllowanceActive(); got != want {
			t.Errorf("activation 600, height %d: active = %v, want %v", height, got, want)
		}
	}
	sp.SetTransferFeeStrictAllowance(0)
	for _, height := range []uint64{0, 1, 600, 1 << 50} {
		at(height)
		if sp.strictAllowanceActive() {
			t.Errorf("setting 0 (off), height %d: active", height)
		}
	}
	var nilSP *StateProcessor
	nilSP.SetTransferFeeStrictAllowance(1)
	if nilSP.strictAllowanceActive() {
		t.Error("a nil state processor is active")
	}
}

func TestStrictAllowanceSettingIsCopiedAndSurvivesARebuild(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetTransferFeeStrictAllowance(777)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatal(err)
	}
	if cp.strictAllowanceFromHeight != 777 {
		t.Fatalf("the copy holds %d, want 777: a block built on a copy would run with the rule off", cp.strictAllowanceFromHeight)
	}
	node := newTestNode(t)
	node.SetTransferFeeStrictAllowance(9)
	node.stateMu.Lock()
	gotH := node.state.strictAllowanceFromHeight
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.strictAllowanceFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if gotH != 9 || rebuilt != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuilt)
	}
}

// The node's quote is the block's own arithmetic for the next block: the strict rule from its height, the governed
// schedule once in force, and the whole-transfer rule otherwise.
func TestNodeTransferFeeQuoteFollowsTheStrictAllowance(t *testing.T) {
	node := newTestNode(t)
	policy := node.TransferGasPolicy()
	policy.Enabled = true
	policy.FreeSpendLimitWei = bigS("1000000000000000000000")
	policy.FeeBps, policy.FeeBpsZNHB = 20, 10
	node.SetTransferGasPolicy(policy)
	next := node.GetHeight() + 1
	var wallet [20]byte
	wallet[19] = 0x42
	amount := bigS("1100000000000000000000") // 1,100 tokens

	for name, tc := range map[string]struct {
		activation            uint64
		strict                bool
		eligible              bool
		free, chargeable, fee string
	}{
		"off":               {0, false, true, "1100000000000000000000", "0", "0"},
		"one block ahead":   {next + 1, false, true, "1100000000000000000000", "0", "0"},
		"at the next block": {next, true, false, "1000000000000000000000", "100000000000000000000", "200000000000000000"},
		"long past":         {1, true, false, "1000000000000000000000", "100000000000000000000", "200000000000000000"},
	} {
		node.SetTransferFeeStrictAllowance(tc.activation)
		quote, err := node.TransferFeeQuote(wallet[:], "NHB", amount)
		if err != nil {
			t.Fatal(err)
		}
		if quote.StrictAllowance != tc.strict || quote.Eligible != tc.eligible || quote.FreeWei.String() != tc.free || quote.ChargeableWei.String() != tc.chargeable || quote.FeeWei.String() != tc.fee || quote.FeeBps != 20 {
			t.Errorf("%s: %+v, want strict %v eligible %v free %s chargeable %s fee %s", name, quote, tc.strict, tc.eligible, tc.free, tc.chargeable, tc.fee)
		}
		active, height := node.TransferFeeStrictAllowanceNextBlock()
		if active != tc.strict || height != tc.activation {
			t.Errorf("%s: next block strict = %v at %d, want %v at %d", name, active, height, tc.strict, tc.activation)
		}
		view, err := node.TransferFeeSchedule()
		if err != nil {
			t.Fatal(err)
		}
		if view.StrictAllowanceActive != tc.strict || view.StrictAllowanceActivationHeight != tc.activation {
			t.Errorf("%s: view strict = %v at %d", name, view.StrictAllowanceActive, view.StrictAllowanceActivationHeight)
		}
	}
	// The ZNHB quote uses the ZNHB rate.
	node.SetTransferFeeStrictAllowance(1)
	z, err := node.TransferFeeQuote(wallet[:], "ZNHB", amount)
	if err != nil {
		t.Fatal(err)
	}
	if z.FeeWei.String() != "100000000000000000" || z.FeeBps != 10 {
		t.Errorf("ZNHB quote: %+v", z)
	}
}

// The quote and the block must agree: the fee the node quotes for a wallet is the fee the state processor charges.
func TestQuoteArithmeticMatchesTheBlockPath(t *testing.T) {
	for _, a := range uniformAssets {
		for _, strict := range []bool{true, false} {
			e := newStrictEnv(t, 0)
			if strict {
				e.sp.SetTransferFeeStrictAllowance(1)
			}
			e.at(10)
			policy := e.sp.TransferGasPolicy()
			for _, value := range []int64{300_000, 800_000, 1_234_567, 5, 2_000_000} {
				sender := e.addr["sender"]
				status, err := nhbstate.NewManager(e.sp.Trie).TransferGasSpendStatus(sender, nhbstate.TransferGasWindowLifetime, time.Unix(1_800_000_000, 0).UTC(), policy.FreeSpendLimitWei, a.name)
				if err != nil {
					t.Fatal(err)
				}
				_, _, quoted := policy.FeeWithAllowance(a.name, big.NewInt(value), status.Remaining, strict)
				got := e.send(a, value, "", false)
				if got.collector != quoted.Int64() || got.sender != -(value+quoted.Int64()) {
					t.Fatalf("%s strict=%v value %d: charged %d (sender %d), quoted %s", a.name, strict, value, got.collector, got.sender, quoted)
				}
			}
		}
	}
}

// A disabled policy has no allowance (the block path never consults it): the node's quote must charge the whole amount
// under either rule, even when the policy still carries a limit (only a direct SetTransferGasPolicy can make that).
func TestNodeTransferFeeQuoteWithoutAnAllowanceChargesTheWholeAmount(t *testing.T) {
	node := newTestNode(t)
	policy := node.TransferGasPolicy()
	policy.Enabled = false
	policy.FreeSpendLimitWei = bigS("1000000000000000000000")
	policy.FeeBps, policy.FeeBpsZNHB = 20, 10
	node.SetTransferGasPolicy(policy)
	var wallet [20]byte
	wallet[19] = 0x43
	amount := bigS("1100000000000000000000")
	for _, activation := range []uint64{0, 1} {
		node.SetTransferFeeStrictAllowance(activation)
		quote, err := node.TransferFeeQuote(wallet[:], "NHB", amount)
		if err != nil {
			t.Fatal(err)
		}
		if quote.Eligible || quote.FreeWei.Sign() != 0 || quote.ChargeableWei.Cmp(amount) != 0 || quote.FeeWei.String() != "2200000000000000000" {
			t.Errorf("activation %d: %+v, want the whole amount charged (fee 2.2 tokens)", activation, quote)
		}
	}
}
