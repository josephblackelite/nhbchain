package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-EXT-38. A sponsored NHB transfer debits the sponsor the protocol fee; the fee used
// to be credited to the collector only while the transfer gas policy was enabled, and
// the policy is disabled whenever its free-tier spend limit is zero, whatever the fee
// rate. These tests run one sponsored transfer through the real state processor with
// the policy disabled and a fee rate set, before and after the activation height.

type sponsoredFeeOutcome struct {
	sponsorLost    *big.Int // what the sponsor was debited
	collectorGain  *big.Int // what the fee collector was credited
	totalNHBChange *big.Int // after - before over every account touched (negative = NHB destroyed)
}

// runSponsoredFee applies one sponsored transfer of 10,000 NHB at the given fee rate
// (basis points) in a block of the given height. policyEnabled sets the transfer gas
// policy's Enabled flag; activation is the setting under test (zero = off).
func runSponsoredFee(t *testing.T, policyEnabled bool, feeBps uint32, activation, height uint64) sponsoredFeeOutcome {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetPaymasterEnabled(true)
	sp.SetSponsoredTransferFeeCredit(activation)
	sp.BeginBlock(height, time.Unix(1_800_000_000, 0))

	keys := map[string]*crypto.PrivateKey{}
	addrs := map[string][]byte{}
	for _, name := range []string{"sender", "recipient", "sponsor"} {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate %s key: %v", name, err)
		}
		keys[name] = key
		addrs[name] = key.PubKey().Address().Bytes()
	}
	addrs["collector"] = make([]byte, 20)
	addrs["collector"][19] = 0x55
	var collector [20]byte
	copy(collector[:], addrs["collector"])

	sp.SetTransferGasPolicy(TransferGasPolicy{
		Enabled:           policyEnabled,
		FreeSpendLimitWei: big.NewInt(0),
		Window:            TransferGasWindowLifetime,
		FeeCollector:      collector,
		FeeBps:            feeBps,
	})
	for _, role := range []string{"sender", "recipient", "sponsor", "collector"} {
		if err := sp.setAccount(addrs[role], &types.Account{BalanceNHB: big.NewInt(sponsoredTransferStart), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	balance := func(role string) *big.Int {
		account, err := sp.getAccount(addrs[role])
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		return new(big.Int).Set(account.BalanceNHB)
	}
	total := func() *big.Int {
		sum := new(big.Int)
		for _, role := range []string{"sender", "recipient", "sponsor", "collector"} {
			sum.Add(sum, balance(role))
		}
		return sum
	}
	before := total()

	tx := &types.Transaction{
		ChainID:   types.NHBChainID(),
		Type:      types.TxTypeTransfer,
		Nonce:     0,
		To:        append([]byte(nil), addrs["recipient"]...),
		Value:     big.NewInt(sponsoredTransferValue),
		GasLimit:  21_000,
		GasPrice:  big.NewInt(1),
		Paymaster: append([]byte(nil), addrs["sponsor"]...),
	}
	signPaymaster(t, tx, keys["sponsor"])
	signTransaction(t, tx, keys["sender"])
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply the sponsored transfer: %v", err)
	}
	if got, want := balance("recipient"), big.NewInt(sponsoredTransferStart+sponsoredTransferValue); got.Cmp(want) != 0 {
		t.Fatalf("recipient holds %s, want %s", got, want)
	}
	start := big.NewInt(sponsoredTransferStart)
	return sponsoredFeeOutcome{
		sponsorLost:    new(big.Int).Sub(start, balance("sponsor")),
		collectorGain:  new(big.Int).Sub(balance("collector"), start),
		totalNHBChange: new(big.Int).Sub(total(), before),
	}
}

func eq(got *big.Int, want int64) bool { return got.Cmp(big.NewInt(want)) == 0 }

// The older behaviour, kept below the height and with the setting off: the sponsor
// pays the fee and nobody receives it.
func TestSponsoredFeeIsDestroyedWithTheSettingOffWhenThePolicyIsDisabled(t *testing.T) {
	out := runSponsoredFee(t, false, 1_000, 0, 500)
	if !eq(out.sponsorLost, sponsoredTransferFee) || !eq(out.collectorGain, 0) || !eq(out.totalNHBChange, -sponsoredTransferFee) {
		t.Fatalf("setting off, policy disabled: sponsor lost %s, collector gained %s, total changed by %s; want %d, 0, -%d (the unchanged behaviour)",
			out.sponsorLost, out.collectorGain, out.totalNHBChange, sponsoredTransferFee, sponsoredTransferFee)
	}
}

func TestSponsoredFeeIsDestroyedBelowTheHeight(t *testing.T) {
	out := runSponsoredFee(t, false, 1_000, 600, 599)
	if !eq(out.sponsorLost, sponsoredTransferFee) || !eq(out.collectorGain, 0) || !eq(out.totalNHBChange, -sponsoredTransferFee) {
		t.Fatalf("one block below the height: sponsor lost %s, collector gained %s, total changed by %s; want the unchanged behaviour (%d, 0, -%d)",
			out.sponsorLost, out.collectorGain, out.totalNHBChange, sponsoredTransferFee, sponsoredTransferFee)
	}
}

func TestSponsoredFeeIsCreditedFromTheHeightWhenThePolicyIsDisabled(t *testing.T) {
	for _, height := range []uint64{600, 601, 10_000} {
		out := runSponsoredFee(t, false, 1_000, 600, height)
		if !eq(out.sponsorLost, sponsoredTransferFee) || !eq(out.collectorGain, sponsoredTransferFee) || !eq(out.totalNHBChange, 0) {
			t.Fatalf("height %d: sponsor lost %s, collector gained %s, total changed by %s; want %d, %d, 0 (the fee collected, nothing destroyed)",
				height, out.sponsorLost, out.collectorGain, out.totalNHBChange, sponsoredTransferFee, sponsoredTransferFee)
		}
	}
}

// With the policy enabled the fee was always credited: the rule changes nothing there,
// at any height and with the setting on or off.
func TestSponsoredFeeWithThePolicyEnabledIsUnchangedByTheSetting(t *testing.T) {
	for _, tc := range []struct{ activation, height uint64 }{{0, 500}, {600, 500}, {600, 700}} {
		out := runSponsoredFee(t, true, 1_000, tc.activation, tc.height)
		if !eq(out.sponsorLost, sponsoredTransferFee) || !eq(out.collectorGain, sponsoredTransferFee) || !eq(out.totalNHBChange, 0) {
			t.Fatalf("policy enabled, activation %d height %d: sponsor lost %s, collector gained %s, total changed by %s; want %d, %d, 0",
				tc.activation, tc.height, out.sponsorLost, out.collectorGain, out.totalNHBChange, sponsoredTransferFee, sponsoredTransferFee)
		}
	}
}

// No fee rate, no fee: the rule credits a fee that was debited, never one that was not.
func TestSponsoredTransferWithNoFeeRateMovesNoFeeAtAnyHeight(t *testing.T) {
	for _, height := range []uint64{599, 600, 700} {
		out := runSponsoredFee(t, false, 0, 600, height)
		if !eq(out.sponsorLost, 0) || !eq(out.collectorGain, 0) || !eq(out.totalNHBChange, 0) {
			t.Fatalf("height %d, fee rate 0: sponsor lost %s, collector gained %s, total changed by %s; want all zero",
				height, out.sponsorLost, out.collectorGain, out.totalNHBChange)
		}
	}
}

func TestSponsoredTransferFeeCreditedTable(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSponsoredTransferFeeCredit(600)
	at := func(height uint64) { sp.BeginBlock(height, time.Unix(1_800_000_000, 0)) }
	fee := big.NewInt(5)
	zero := big.NewInt(0)

	at(599)
	if sp.sponsoredTransferFeeCredited(false, fee) {
		t.Fatalf("below the height a disabled policy must not credit")
	}
	at(600)
	if !sp.sponsoredTransferFeeCredited(false, fee) {
		t.Fatalf("at the height a disabled policy with a positive fee must credit")
	}
	if sp.sponsoredTransferFeeCredited(false, zero) || sp.sponsoredTransferFeeCredited(false, nil) {
		t.Fatalf("a zero or missing fee is never credited by the rule")
	}
	if !sp.sponsoredTransferFeeCredited(true, zero) {
		t.Fatalf("an enabled policy credits as it always did, even a zero fee")
	}
	sp.SetSponsoredTransferFeeCredit(0)
	at(10_000)
	if sp.sponsoredTransferFeeCredited(false, fee) {
		t.Fatalf("with the setting at zero the rule is off at every height")
	}
	var nilSP *StateProcessor
	nilSP.SetSponsoredTransferFeeCredit(5)
	if nilSP.sponsoredTransferFeeCredited(false, fee) {
		t.Fatalf("a nil state processor credits nothing")
	}
}

// The setting survives the copy a block's execution is built on, and the node carries it
// to a state processor it rebuilds.
func TestSponsoredTransferFeeCreditSettingIsCopied(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSponsoredTransferFeeCredit(777)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.sponsoredFeeCreditFromHeight != 777 {
		t.Fatalf("the copy holds %d, want 777: a block built on a copy would run with the rule off", cp.sponsoredFeeCreditFromHeight)
	}
}

func TestNodeSponsoredTransferFeeCredit_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetSponsoredTransferFeeCredit(9)
	node.stateMu.Lock()
	gotH := node.state.sponsoredFeeCreditFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.sponsoredFeeCreditFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuiltH)
	}
}
