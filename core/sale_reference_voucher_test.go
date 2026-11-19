package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/swap"
)

// PL-BC-D, the second sale path: the swap voucher mint holds its USD budget to the curve's price. From
// SaleReferenceActivationHeight it holds it to the higher of the curve and the outside reference price. These
// tests run real voucher submissions through the node (submit, build a block, commit), like the PL-EXT-35 tests
// in swap_voucher_signed_budget_test.go.

// runSaleRefVoucher submits a voucher (fiat USD, signed amount fiatAmount at rate fiat per ZNHB) with the
// reference rule set as given and an optional recorded reference (rate refNum/refDenom, struck refAgo before
// now), builds one block and commits it. The voucher's budget is its signed amount (the PL-EXT-35 rule is on).
func runSaleRefVoucher(t *testing.T, rules ValidatorRules, refNum, refDenom int64, refAgo time.Duration, fiatAmount, rate string) budgetOutcome {
	t.Helper()
	node, minterKey, oracleKey := setupSwapVoucherTestNode(t)
	registerSwapPriceSignerCore(t, node, budgetTestProvider, oracleKey)
	node.SetSwapVoucherSignedBudget(1)
	node.SetValidatorRules(rules)
	if refNum > 0 {
		err := node.WithState(func(m *nhbstate.Manager) error {
			return m.SetSaleReference(nhbstate.SaleReferenceRecord{
				Epoch:       1,
				RateNum:     big.NewInt(refNum),
				RateDenom:   big.NewInt(refDenom),
				TimestampAt: uint64(time.Now().Add(-refAgo).Unix()),
			})
		})
		if err != nil {
			t.Fatalf("record the reference: %v", err)
		}
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("recipient key: %v", err)
	}
	recipient := toAddress(key)
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
	transfer := livenessTransfer(t, honest, 0)

	rat, ok := new(big.Rat).SetString(rate)
	if !ok {
		t.Fatalf("bad rate %q", rate)
	}
	amount, err := swap.ComputeMintAmount(fiatAmount, rat, 18)
	if err != nil {
		t.Fatalf("compute amount: %v", err)
	}
	voucher := swapVoucherTestVoucher(node.chain.ChainID(), recipient, rate, "ORDER-SALEREF-A")
	voucher.Fiat = "USD"
	voucher.FiatAmount = fiatAmount
	voucher.Amount = amount
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signSwapVoucherCore(t, minterKey, voucher),
		Provider:     budgetTestProvider,
		ProviderTxID: "SALEREF-A",
		PriceProof:   signedPriceProofCore(t, oracleKey, budgetTestProvider, rate, time.Now()),
	}
	out := budgetOutcome{}
	if _, _, err := node.SwapSubmitVoucher(submission); err != nil {
		out.refusedAtSubmission = true
	}
	pending := append(append([]*types.Transaction(nil), node.mempool...), transfer)
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("CreateBlock must not abort for a refused voucher: %v", err)
	}
	if !livenessBlockContains(t, block, transfer) {
		t.Fatalf("the ordinary transfer is missing from the block")
	}
	for _, tx := range block.Transactions {
		sub, err := decodeSwapVoucherMintTransaction(tx.Data)
		if err != nil {
			continue
		}
		if sub.ProviderTxID == "SALEREF-A" {
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
	return out
}

var saleRefVoucherRules = ValidatorRules{SaleReferenceActivationHeight: 1, SaleReferenceMaxAgeSeconds: 3600}

func TestSaleReferenceVoucher_OffThePriceIsTheCurve(t *testing.T) {
	// $100 at 0.05 USD per ZNHB = 2,000 ZNHB = exactly the curve's price, with a huge reference on record and the rule off.
	out := runSaleRefVoucher(t, ValidatorRules{}, 500, 1, time.Minute, "100.00", "0.05")
	if !out.minted() {
		t.Fatalf("rule off: an honest voucher at the curve price was refused (%+v)", out)
	}
}

func TestSaleReferenceVoucher_AVoucherAtTheCurvePriceIsRefusedWhileTheReferenceIsHigher(t *testing.T) {
	// the reference asks 2.50 NHB per ZNHB; the voucher is priced at the curve's 0.05
	out := runSaleRefVoucher(t, saleRefVoucherRules, 5, 2, time.Minute, "100.00", "0.05")
	if out.minted() {
		t.Fatalf("a voucher at the curve price was minted while the reference price is 50 times higher (%+v)", out)
	}
}

func TestSaleReferenceVoucher_AVoucherAtTheReferencePriceMints(t *testing.T) {
	// $5,000 at 2.50 USD per ZNHB = 2,000 ZNHB, which the reference prices at exactly 5,000
	out := runSaleRefVoucher(t, saleRefVoucherRules, 5, 2, time.Minute, "5000.00", "2.5")
	if !out.minted() {
		t.Fatalf("a voucher priced at the reference was refused (%+v)", out)
	}
}

func TestSaleReferenceVoucher_AVoucherAboveTheCurveBelowTheReferenceIsRefused(t *testing.T) {
	// $1,000 for 2,000 ZNHB (0.50 per ZNHB) is far above the curve and still under the reference's 5,000
	out := runSaleRefVoucher(t, saleRefVoucherRules, 5, 2, time.Minute, "1000.00", "0.5")
	if out.minted() {
		t.Fatalf("a voucher priced under the reference was minted (%+v)", out)
	}
}

func TestSaleReferenceVoucher_AReferenceBelowTheCurveLeavesTheCurveAsTheFloor(t *testing.T) {
	out := runSaleRefVoucher(t, saleRefVoucherRules, 1, 100, time.Minute, "100.00", "0.05")
	if !out.minted() {
		t.Fatalf("with the reference under the curve an honest voucher at the curve price was refused (%+v)", out)
	}
}

func TestSaleReferenceVoucher_NoReferencePausesTheMintAndTheBlockIsStillBuilt(t *testing.T) {
	out := runSaleRefVoucher(t, saleRefVoucherRules, 0, 0, 0, "100.00", "0.05")
	if out.minted() {
		t.Fatalf("a voucher was minted with the rule on and no reference on record (%+v)", out)
	}
}

func TestSaleReferenceVoucher_AStaleReferencePausesTheMint(t *testing.T) {
	out := runSaleRefVoucher(t, saleRefVoucherRules, 1, 100, 2*time.Hour, "100.00", "0.05")
	if out.minted() {
		t.Fatalf("a voucher was minted against a reference two hours old with a one-hour maximum (%+v)", out)
	}
}
