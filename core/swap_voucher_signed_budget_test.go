package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/swap"
)

// PL-EXT-35. The USD budget the treasury's curve-price guard holds a voucher mint to used
// to be the submission's own usdAmount, which no signed digest covers. These tests drive
// real submissions through the node (submit, build a block, commit) behind an unrelated
// good voucher in the same block, so a refused voucher also proves the block is still
// built. The node validates a voucher when it is submitted as well as when it is put in a
// block, so a refusal can come at either point; both count as "not minted".

const budgetTestProvider = "test-provider"

// budgetCase describes the voucher under test.
type budgetCase struct {
	fiat       string // "USD" or another currency
	fiatAmount string // the signed fiat amount
	rate       string // the signed rate (fiat per ZNHB), also the price proof's
	usdAmount  string // the submission's own, unsigned, budget ("" = none, "curve" = the curve's exact cost at its place in the sale)
	allowEUR   bool
}

type budgetOutcome struct {
	refusedAtSubmission bool
	included            bool     // in the block
	balance             *big.Int // what its recipient holds after the commit
	usdStored           string   // the USD figure on its ledger record
	mempoolLeft         int      // transactions still waiting after the commit
}

func (o budgetOutcome) minted() bool { return o.included && o.balance.Sign() > 0 }

// runBudgetCase submits a good control voucher and then the voucher under test, builds one
// block of both and commits it. submitActivation is the setting while they are submitted,
// blockActivation the setting when the block is built (they differ in the test that
// switches the rule on while a voucher waits).
func runBudgetCase(t *testing.T, submitActivation, blockActivation uint64, tc budgetCase) budgetOutcome {
	t.Helper()
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	// The provider of these vouchers is a neutral test name with the same price signer.
	registerSwapPriceSignerCore(t, node, budgetTestProvider, oracleKey)
	if tc.allowEUR {
		node.SetSwapConfig(swap.Config{
			AllowedFiat:        []string{"USD", "EUR"},
			MaxQuoteAgeSeconds: 120,
			SlippageBps:        50,
			OraclePriority:     []string{"manual"},
		})
	}
	node.SetSwapVoucherSignedBudget(submitActivation)

	newRecipient := func() [20]byte {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("recipient key: %v", err)
		}
		return toAddress(key)
	}
	recipient := newRecipient()

	// The control: an ordinary transfer that is valid whatever the rule says, in the same
	// block as the voucher under test.
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
	transfer := livenessTransfer(t, honest, 0)

	rat, ok := new(big.Rat).SetString(tc.rate)
	if !ok {
		t.Fatalf("bad rate %q", tc.rate)
	}
	amount, err := swap.ComputeMintAmount(tc.fiatAmount, rat, 18)
	if err != nil {
		t.Fatalf("compute amount: %v", err)
	}
	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, tc.rate, "ORDER-BUDGET-A")
	voucher.Fiat = tc.fiat
	voucher.FiatAmount = tc.fiatAmount
	voucher.Amount = amount

	usd := tc.usdAmount
	if usd == "curve" {
		cost, err := curve.Default().Cost(big.NewInt(0), amount)
		if err != nil {
			t.Fatalf("curve cost: %v", err)
		}
		usd = cost.FloatString(12)
	}
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signSwapVoucherCore(t, minterKey, voucher),
		Provider:     budgetTestProvider,
		ProviderTxID: "BUDGET-A",
		USDAmount:    usd,
		PriceProof:   signedPriceProofCore(t, oracleKey, budgetTestProvider, tc.rate, time.Now()),
	}
	out := budgetOutcome{}
	if _, _, err := node.SwapSubmitVoucher(submission); err != nil {
		out.refusedAtSubmission = true
	}

	node.SetSwapVoucherSignedBudget(blockActivation)
	pending := append(append([]*types.Transaction(nil), node.mempool...), transfer)
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort for a refused voucher: %v", err)
	}
	if !livenessBlockContains(t, block, transfer) {
		t.Fatalf("the ordinary transfer is missing from the block: it was not built around the other transaction")
	}
	for _, tx := range block.Transactions {
		sub, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			continue
		}
		if sub.ProviderTxID == "BUDGET-A" {
			out.included = true
		}
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	account, err := node.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	out.balance = new(big.Int).Set(account.BalanceZNHB)
	node.stateMu.Lock()
	record, found, err := swap.NewLedger(nhbstate.NewManager(node.state.Trie)).Get("BUDGET-A")
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("ledger get: %v", err)
	}
	if found {
		out.usdStored = record.USD
	}
	node.mempoolMu.Lock()
	out.mempoolLeft = len(node.mempool)
	node.mempoolMu.Unlock()
	return out
}

// A voucher the issuer signed at 0.025 USD per ZNHB: 100 USD for 4,000 ZNHB, which the
// treasury's curve prices at about 200 USD.
var underpricedUSD = budgetCase{fiat: "USD", fiatAmount: "100.00", rate: "0.025"}

func TestBudgetGuardHoldsAnUnderpricedVoucherToItsSignedFiatAmount(t *testing.T) {
	// No submitted budget: the signed 100 USD is the budget, far from the curve's price,
	// and the slippage guard refuses it. This is the guard working, with the rule off or on.
	for _, activation := range []uint64{0, 1} {
		if out := runBudgetCase(t, activation, activation, underpricedUSD); out.minted() {
			t.Fatalf("activation %d: an underpriced voucher with no submitted budget was minted", activation)
		}
	}
}

// The reported weakness: the submitter writes the budget.
func TestSubmittedBudgetLetsAnUnderpricedVoucherPassWithTheRuleOff(t *testing.T) {
	tc := underpricedUSD
	tc.usdAmount = "curve"
	for _, activation := range []uint64{0, 100000} { // off, and set far above the chain tip
		out := runBudgetCase(t, activation, activation, tc)
		if !out.minted() {
			t.Fatalf("activation %d: the submitter's own budget no longer lets the voucher through (%+v); the rule must change nothing below its height", activation, out)
		}
		if out.usdStored == "" || out.usdStored == "100.00" {
			t.Fatalf("activation %d: the record holds USD %q, want the submitted figure", activation, out.usdStored)
		}
	}
}

func TestSubmittedBudgetNoLongerLetsAnUnderpricedVoucherPassFromTheHeight(t *testing.T) {
	tc := underpricedUSD
	tc.usdAmount = "curve"
	if out := runBudgetCase(t, 1, 1, tc); out.minted() || !out.refusedAtSubmission {
		t.Fatalf("from the height a voucher sold below the curve price was accepted on the submitter's budget (%+v)", out)
	}
}

// A voucher admitted while the rule was off and still waiting when the chain reaches the
// height is dropped from the block without stopping it, and does not stay in the pool.
func TestAVoucherWaitingWhenTheHeightIsReachedIsPrunedAndTheBlockIsStillBuilt(t *testing.T) {
	tc := underpricedUSD
	tc.usdAmount = "curve"
	out := runBudgetCase(t, 0, 1, tc)
	if out.refusedAtSubmission {
		t.Fatalf("with the rule off the voucher should have been admitted")
	}
	if out.included || out.minted() {
		t.Fatalf("a voucher admitted before the height was minted on the submitter's budget after it (%+v)", out)
	}
	if out.mempoolLeft != 0 {
		t.Fatalf("the pruned voucher still waits in the pool (%d left), to be proposed and refused again", out.mempoolLeft)
	}
}

func TestAnHonestVoucherMintsWithTheRuleOnWhetherOrNotItRepeatsTheSignedAmount(t *testing.T) {
	for name, usd := range map[string]string{"no budget": "", "the signed amount": "100.00", "the same number spelt differently": "100"} {
		tc := budgetCase{fiat: "USD", fiatAmount: "100.00", rate: "0.05", usdAmount: usd}
		out := runBudgetCase(t, 1, 1, tc)
		if !out.minted() {
			t.Fatalf("%s: an honest USD voucher was refused with the rule on (%+v)", name, out)
		}
		if out.usdStored != "100.00" {
			t.Fatalf("%s: the record holds USD %q, want the signed 100.00", name, out.usdStored)
		}
	}
}

func TestASubmittedBudgetThatDiffersFromTheSignedAmountIsRefusedWithTheRuleOn(t *testing.T) {
	for _, usd := range []string{"99.00", "100.01", "abc", "0"} {
		tc := budgetCase{fiat: "USD", fiatAmount: "100.00", rate: "0.05", usdAmount: usd}
		if out := runBudgetCase(t, 1, 1, tc); out.minted() {
			t.Fatalf("usdAmount %q beside a signed 100.00 was accepted with the rule on (%+v)", usd, out)
		}
	}
	// With the rule off a differing figure within the curve's tolerance is still the
	// submitter's to write: the older behaviour is unchanged below the height.
	tc := budgetCase{fiat: "USD", fiatAmount: "100.00", rate: "0.05", usdAmount: "100.20"}
	if out := runBudgetCase(t, 0, 0, tc); !out.minted() {
		t.Fatalf("with the rule off the submitted figure is no longer used (%+v)", out)
	}
}

// A voucher in another currency has no signed USD value: the submitter supplied the whole
// budget before; from the height it is refused.
func TestAVoucherInAnotherCurrencyIsRefusedFromTheHeight(t *testing.T) {
	tc := budgetCase{fiat: "EUR", fiatAmount: "92.00", rate: "0.046", usdAmount: "curve", allowEUR: true}
	if out := runBudgetCase(t, 0, 0, tc); !out.minted() {
		t.Fatalf("with the rule off a EUR voucher on a submitted budget no longer mints (%+v)", out)
	}
	if out := runBudgetCase(t, 1, 1, tc); out.minted() || !out.refusedAtSubmission {
		t.Fatalf("with the rule on a EUR voucher was accepted on a budget nothing signed (%+v)", out)
	}

	// A signed amount that happens to equal the curve's price in USD must not be taken for
	// dollars: 100 EUR for 2,000 ZNHB at 0.05 is a figure in euros, whether or not the
	// submission repeats it.
	for _, usd := range []string{"", "100.00"} {
		same := budgetCase{fiat: "EUR", fiatAmount: "100.00", rate: "0.05", usdAmount: usd, allowEUR: true}
		if out := runBudgetCase(t, 1, 1, same); out.minted() {
			t.Fatalf("with the rule on a EUR voucher whose number equals the curve price in USD was minted as if it were dollars (usdAmount %q, %+v)", usd, out)
		}
	}
}

func TestSwapVoucherSignedBudgetPredicateTable(t *testing.T) {
	sp := newStakingStateProcessor(t)
	usd := &swap.VoucherV1{Fiat: "usd", FiatAmount: " 100.00 "}
	eur := &swap.VoucherV1{Fiat: "EUR", FiatAmount: "92.00"}
	at := func(height uint64) { sp.BeginBlock(height, time.Unix(1_800_000_000, 0)) }

	sp.SetSwapVoucherSignedBudget(50)
	at(49)
	if got, err := sp.swapVoucherUSDBudget(eur, " 77 "); err != nil || got != "77" {
		t.Fatalf("below the height a EUR voucher takes the submitted figure: %q, %v", got, err)
	}
	if got, err := sp.swapVoucherUSDBudget(usd, ""); err != nil || got != "100.00" {
		t.Fatalf("below the height a USD voucher with no figure takes the signed amount: %q, %v", got, err)
	}
	if got, err := sp.swapVoucherUSDBudget(usd, "150"); err != nil || got != "150" {
		t.Fatalf("below the height a submitted figure wins: %q, %v", got, err)
	}
	at(50)
	if _, err := sp.swapVoucherUSDBudget(eur, "77"); err == nil {
		t.Fatalf("at the height a EUR voucher is refused")
	}
	if _, err := sp.swapVoucherUSDBudget(usd, "150"); err == nil {
		t.Fatalf("at the height a figure that differs from the signed amount is refused")
	}
	if got, err := sp.swapVoucherUSDBudget(usd, "100"); err != nil || got != "100.00" {
		t.Fatalf("at the height an equal figure is accepted and the signed text is kept: %q, %v", got, err)
	}
	sp.SetSwapVoucherSignedBudget(0)
	at(1000)
	if _, err := sp.swapVoucherUSDBudget(eur, "77"); err != nil {
		t.Fatalf("with the setting at zero the rule is off at every height: %v", err)
	}
	var nilSP *StateProcessor
	nilSP.SetSwapVoucherSignedBudget(1)
	if nilSP.swapVoucherSignedBudgetActive() {
		t.Fatalf("a nil state processor is never active")
	}
}

func TestSwapVoucherSignedBudgetSettingIsCopied(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherSignedBudget(321)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if cp.swapVoucherSignedBudgetFromHeight != 321 {
		t.Fatalf("the copy holds %d, want 321", cp.swapVoucherSignedBudgetFromHeight)
	}
}

func TestNodeSwapVoucherSignedBudget_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetSwapVoucherSignedBudget(9)
	node.stateMu.Lock()
	gotH := node.state.swapVoucherSignedBudgetFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.swapVoucherSignedBudgetFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 9 || rebuiltH != 9 {
		t.Fatalf("setter reached the processor at %d, rebuilt at %d, want 9 and 9", gotH, rebuiltH)
	}
}
