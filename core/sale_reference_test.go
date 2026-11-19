package core

import (
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/curve"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-BC-D: the treasury sale follows the outside market. These tests drive the real
// applyBuyZNHB and applyBuybackRefPrice through a state processor, not the helpers.

var saleRefStart = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

type saleRefEnv struct {
	sp       *StateProcessor
	admin    [20]byte
	signer   *crypto.PrivateKey
	buyer    *crypto.PrivateKey
	buyerAdr []byte
}

func newSaleRefEnv(t *testing.T, rules ValidatorRules) *saleRefEnv {
	t.Helper()
	sp, admin := newBuyZNHBStateProcessor(t)
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 0x55
	}
	signer, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		t.Fatalf("signer key: %v", err)
	}
	var signerAddr [20]byte
	copy(signerAddr[:], signer.PubKey().Address().Bytes())
	if err := sp.SetBuybackConfig(buyback.Config{FeeShareBps: 2000, SignerThreshold: 1, Signers: [][20]byte{signerAddr}}); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	sp.SetBuybackAccrualAddress(deriveModuleAddress("module/tokenomics/buybackAccrual", crypto.NHBPrefix))
	sp.SetValidatorRules(rules)

	buyer, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("buyer key: %v", err)
	}
	buyerAdr := buyer.PubKey().Address().Bytes()
	funding := new(big.Int).Mul(big.NewInt(1_000_000), curveWeiPerToken())
	if err := sp.setAccount(buyerAdr, &types.Account{BalanceNHB: funding, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	return &saleRefEnv{sp: sp, admin: admin, signer: signer, buyer: buyer, buyerAdr: buyerAdr}
}

// epochHeight is a block height inside buyback epoch e.
func (e *saleRefEnv) epochHeight(epoch uint64) uint64 {
	return epoch * e.sp.epochConfig.Length
}

// postRef records a reference price struck at `struck` (rate = num/denom NHB per whole ZNHB) in the
// block of the given height and time, through the real TxTypeBuybackRefPrice handler.
func (e *saleRefEnv) postRef(t *testing.T, height uint64, now time.Time, num, denom int64, struck time.Time) error {
	t.Helper()
	e.sp.BeginBlock(height, now)
	defer e.sp.EndBlock()
	epoch, ok := e.sp.currentBuybackEpoch()
	if !ok {
		t.Fatalf("no buyback epoch at height %d", height)
	}
	rp := &buyback.ReferencePrice{Rate: new(big.Rat).SetFrac64(num, denom), Epoch: epoch, Timestamp: struck}
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], e.signer.PrivateKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	data, err := rlp.EncodeToBytes(buybackRefPricePayload{RateNum: big.NewInt(num), RateDenom: big.NewInt(denom), Epoch: epoch, Timestamp: uint64(struck.Unix()), Signatures: [][]byte{sig}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return e.sp.applyBuybackRefPrice(&types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: data})
}

// buy submits a signed buyZNHB of `whole` ZNHB in the block of the given height and time.
func (e *saleRefEnv) buy(t *testing.T, height uint64, now time.Time, whole int64, maxNHBWhole int64) error {
	t.Helper()
	e.sp.BeginBlock(height, now)
	defer e.sp.EndBlock()
	account, err := e.sp.getAccount(e.buyerAdr)
	if err != nil {
		t.Fatalf("buyer: %v", err)
	}
	amount := new(big.Int).Mul(big.NewInt(whole), curveWeiPerToken())
	maxNHB := new(big.Int).Mul(big.NewInt(maxNHBWhole), curveWeiPerToken())
	tx := buyZNHBTx(t, account.Nonce, amount, maxNHB)
	if err := tx.Sign(e.buyer.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return e.sp.ApplyTransaction(tx)
}

func (e *saleRefEnv) buyerNHB(t *testing.T) *big.Int {
	t.Helper()
	account, err := e.sp.getAccount(e.buyerAdr)
	if err != nil {
		t.Fatalf("buyer: %v", err)
	}
	return new(big.Int).Set(account.BalanceNHB)
}

func (e *saleRefEnv) buyerZNHB(t *testing.T) *big.Int {
	t.Helper()
	account, err := e.sp.getAccount(e.buyerAdr)
	if err != nil {
		t.Fatalf("buyer: %v", err)
	}
	return new(big.Int).Set(account.BalanceZNHB)
}

// wholeToWei converts whole tokens to wei.
func wholeToWei(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), curveWeiPerToken())
}

// refCostWei is rate (num/denom NHB per whole ZNHB) times `whole` ZNHB, rounded up to attoNHB the way the
// sale rounds.
func refCostWei(num, denom, whole int64) *big.Int {
	r := new(big.Rat).SetFrac64(num*whole, denom)
	return curve.RoundCostUp(r)
}

func saleRules(from uint64) ValidatorRules {
	return ValidatorRules{SaleReferenceActivationHeight: from, SaleReferenceMaxAgeSeconds: 3600}
}

func TestSaleReference_OffPricesByTheCurveAtEveryHeight(t *testing.T) {
	e := newSaleRefEnv(t, ValidatorRules{})
	before := e.buyerNHB(t)
	if err := e.buy(t, 1_000_000_000, saleRefStart, 1000, 100); err != nil {
		t.Fatalf("buy with the rule off: %v", err)
	}
	charged := new(big.Int).Sub(before, e.buyerNHB(t))
	want := expectedCost(t, big.NewInt(0), wholeToWei(1000))
	if charged.Cmp(want) != 0 {
		t.Fatalf("charged %s, the curve alone asks %s", charged, want)
	}
	if e.buyerZNHB(t).Cmp(wholeToWei(1000)) != 0 {
		t.Fatalf("buyer ZNHB = %s", e.buyerZNHB(t))
	}
}

func TestSaleReference_OffIgnoresAReferenceAndRecordsNothing(t *testing.T) {
	e := newSaleRefEnv(t, ValidatorRules{})
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 1, saleRefStart); err != nil {
		t.Fatalf("post reference: %v", err)
	}
	if _, found, err := nhbstate.NewManager(e.sp.Trie).SaleReference(); err != nil || found {
		t.Fatalf("rule off: a sale reference was recorded (found=%v err=%v)", found, err)
	}
	before := e.buyerNHB(t)
	if err := e.buy(t, e.epochHeight(1)+1, saleRefStart, 1000, 100); err != nil {
		t.Fatalf("buy: %v", err)
	}
	charged := new(big.Int).Sub(before, e.buyerNHB(t))
	if want := expectedCost(t, big.NewInt(0), wholeToWei(1000)); charged.Cmp(want) != 0 {
		t.Fatalf("charged %s, want the curve cost %s", charged, want)
	}
}

func TestSaleReference_OnWithNoReferencePausesSalesFromTheHeight(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1000))
	if err := e.buy(t, 999, saleRefStart, 10, 100); err != nil {
		t.Fatalf("buy one block before the height must price by the curve: %v", err)
	}
	znhb := e.buyerZNHB(t)
	nhb := e.buyerNHB(t)
	err := e.buy(t, 1000, saleRefStart, 10, 100)
	if !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("buy at the height with no reference: %v, want ErrSaleReferenceUnavailable", err)
	}
	if e.buyerZNHB(t).Cmp(znhb) != 0 || e.buyerNHB(t).Cmp(nhb) != 0 {
		t.Fatalf("a paused sale moved balances")
	}
}

func TestSaleReference_AReferenceAboveTheCurveLiftsThePrice(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart); err != nil { // 2.5 NHB per ZNHB
		t.Fatalf("post reference: %v", err)
	}
	before := e.buyerNHB(t)
	if err := e.buy(t, e.epochHeight(1)+1, saleRefStart.Add(10*time.Second), 1000, 5000); err != nil {
		t.Fatalf("buy at the reference price: %v", err)
	}
	charged := new(big.Int).Sub(before, e.buyerNHB(t))
	if want := refCostWei(5, 2, 1000); charged.Cmp(want) != 0 {
		t.Fatalf("charged %s, the reference asks %s", charged, want)
	}
	curveOnly := expectedCost(t, big.NewInt(0), wholeToWei(1000))
	if charged.Cmp(curveOnly) <= 0 {
		t.Fatalf("charged %s is not above the curve cost %s", charged, curveOnly)
	}
	admin, err := e.sp.getAccount(e.admin[:])
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	if admin.BalanceNHB.Cmp(charged) != 0 {
		t.Fatalf("treasury received %s, buyer paid %s", admin.BalanceNHB, charged)
	}
}

func TestSaleReference_ASlippageCapBelowTheReferenceRefusesTheSale(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart); err != nil {
		t.Fatalf("post reference: %v", err)
	}
	znhb := e.buyerZNHB(t)
	// the curve alone would be 50 NHB; the buyer's cap of 100 NHB is under the 2,500 the reference asks
	if err := e.buy(t, e.epochHeight(1)+1, saleRefStart, 1000, 100); err == nil {
		t.Fatalf("a cap under the reference price must refuse the sale")
	}
	if e.buyerZNHB(t).Cmp(znhb) != 0 {
		t.Fatalf("a refused sale moved ZNHB")
	}
}

func TestSaleReference_AReferenceBelowTheCurveLeavesTheCurve(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 1, 100, saleRefStart); err != nil { // 0.01 NHB per ZNHB
		t.Fatalf("post reference: %v", err)
	}
	before := e.buyerNHB(t)
	if err := e.buy(t, e.epochHeight(1)+1, saleRefStart, 1000, 100); err != nil {
		t.Fatalf("buy: %v", err)
	}
	charged := new(big.Int).Sub(before, e.buyerNHB(t))
	if want := expectedCost(t, big.NewInt(0), wholeToWei(1000)); charged.Cmp(want) != 0 {
		t.Fatalf("charged %s, the curve (the floor) asks %s", charged, want)
	}
}

func TestSaleReference_TheCurveIsAlwaysTheFloor(t *testing.T) {
	// For a spread of reference rates the price is never below the curve and never below the reference.
	curveCost := big.NewRat(50, 1) // 1,000 ZNHB at the first tranche, $0.05
	e := newSaleRefEnv(t, saleRules(1))
	manager := nhbstate.NewManager(e.sp.Trie)
	for _, rate := range [][2]int64{{1, 1000}, {1, 20}, {49, 1000}, {1, 20}, {51, 1000}, {1, 1}, {37, 1}, {1000, 1}} {
		if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 1, RateNum: big.NewInt(rate[0]), RateDenom: big.NewInt(rate[1]), TimestampAt: uint64(saleRefStart.Unix())}); err != nil {
			t.Fatalf("set: %v", err)
		}
		got, err := saleCostAt(manager, e.sp.validatorRules, 5, saleRefStart, curveCost, wholeToWei(1000))
		if err != nil {
			t.Fatalf("rate %v: %v", rate, err)
		}
		ref := new(big.Rat).Mul(big.NewRat(rate[0], rate[1]), big.NewRat(1000, 1))
		if got.Cmp(curveCost) < 0 || got.Cmp(ref) < 0 {
			t.Fatalf("rate %v: price %s is below the curve %s or the reference %s", rate, got.FloatString(6), curveCost.FloatString(6), ref.FloatString(6))
		}
		want := curveCost
		if ref.Cmp(curveCost) > 0 {
			want = ref
		}
		if got.Cmp(want) != 0 {
			t.Fatalf("rate %v: price %s, want max(curve, reference) = %s", rate, got.FloatString(6), want.FloatString(6))
		}
	}
}

func TestSaleReference_AStaleReferencePausesAndAFreshOneResumes(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1)) // maximum age 3,600 s
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart); err != nil {
		t.Fatalf("post reference: %v", err)
	}
	h := e.epochHeight(1) + 1
	if err := e.buy(t, h, saleRefStart.Add(3600*time.Second), 10, 1000); err != nil {
		t.Fatalf("a reference exactly at the maximum age still prices the sale: %v", err)
	}
	if err := e.buy(t, h+1, saleRefStart.Add(3601*time.Second), 10, 1000); !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("one second past the maximum age: %v, want ErrSaleReferenceUnavailable (never the curve)", err)
	}
	later := saleRefStart.Add(2 * time.Hour)
	if err := e.postRef(t, e.epochHeight(2), later, 3, 1, later); err != nil {
		t.Fatalf("a new reference after the old one went stale: %v", err)
	}
	before := e.buyerNHB(t)
	if err := e.buy(t, e.epochHeight(2)+1, later.Add(time.Second), 10, 1000); err != nil {
		t.Fatalf("sales resume with a fresh reference: %v", err)
	}
	if charged := new(big.Int).Sub(before, e.buyerNHB(t)); charged.Cmp(refCostWei(3, 1, 10)) != 0 {
		t.Fatalf("charged %s, want the new reference's %s", charged, refCostWei(3, 1, 10))
	}
}

func TestSaleReference_AZeroMaximumAgePausesSales(t *testing.T) {
	e := newSaleRefEnv(t, ValidatorRules{SaleReferenceActivationHeight: 1})
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart); err != nil {
		t.Fatalf("post reference: %v", err)
	}
	if err := e.buy(t, e.epochHeight(1)+1, saleRefStart, 10, 1000); !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("with no maximum age set: %v, want ErrSaleReferenceUnavailable", err)
	}
}

func TestSaleReference_RefusesAReferenceDatedTooFarAheadOrAlreadyTooOld(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart.Add(301*time.Second)); !errors.Is(err, ErrSaleReferenceTooFarAhead) {
		t.Fatalf("301 s ahead: %v, want ErrSaleReferenceTooFarAhead", err)
	}
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart.Add(-3601*time.Second)); !errors.Is(err, ErrSaleReferenceTooOld) {
		t.Fatalf("3,601 s old: %v, want ErrSaleReferenceTooOld", err)
	}
	if _, found, _ := nhbstate.NewManager(e.sp.Trie).SaleReference(); found {
		t.Fatalf("a refused reference left a record")
	}
	if _, exists, _ := nhbstate.NewManager(e.sp.Trie).BuybackRefPriceForEpoch(1); exists {
		t.Fatalf("a refused reference left the buyback record of its epoch")
	}
	// the two edges are accepted (and the epoch is still free, the refusals wrote nothing)
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, saleRefStart.Add(300*time.Second)); err != nil {
		t.Fatalf("300 s ahead is the edge and is accepted: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(2), saleRefStart, 5, 2, saleRefStart.Add(-3600*time.Second)); err != nil {
		t.Fatalf("3,600 s old is the edge and is accepted: %v", err)
	}
}

func TestSaleReference_AFutureDatedReferenceCannotKeepItselfFreshPastTheMaximumAge(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	struck := saleRefStart.Add(300 * time.Second) // the furthest ahead a reference may be dated
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 5, 2, struck); err != nil {
		t.Fatalf("post: %v", err)
	}
	// 3,900 s after the block: the reference is 3,600 s old at its signed time, so it is still fresh; one more second is stale
	h := e.epochHeight(1) + 1
	if err := e.buy(t, h, struck.Add(3600*time.Second), 10, 1000); err != nil {
		t.Fatalf("fresh at its signed age limit: %v", err)
	}
	if err := e.buy(t, h+1, struck.Add(3601*time.Second), 10, 1000); !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("stale: %v", err)
	}
}

func TestSaleReference_StepBoundRefusesALargeMoveWhileThePreviousIsFresh(t *testing.T) {
	rules := saleRules(1)
	rules.SaleReferenceMaxStepBps = 1000 // 10 percent
	e := newSaleRefEnv(t, rules)
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 100, 100, saleRefStart); err != nil { // 1.00
		t.Fatalf("first reference (nothing to compare with): %v", err)
	}
	now := saleRefStart.Add(60 * time.Second)
	if err := e.postRef(t, e.epochHeight(2), now, 110, 100, now); err != nil { // +10 percent exactly
		t.Fatalf("a move of exactly the bound: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(3), now, 15, 10, now); !errors.Is(err, ErrSaleReferenceStepTooLarge) { // 1.10 -> 1.50
		t.Fatalf("a move over the bound: %v, want ErrSaleReferenceStepTooLarge", err)
	}
	// 1.10 - 0.99 = 0.11, exactly 10 percent of 1.10: a downward move of exactly the bound is accepted
	if err := e.postRef(t, e.epochHeight(3), now, 99, 100, now); err != nil {
		t.Fatalf("a downward move of exactly the bound: %v", err)
	}
}

func TestSaleReference_StepBoundIsSymmetricAndExact(t *testing.T) {
	rules := saleRules(1)
	rules.SaleReferenceMaxStepBps = 1000
	e := newSaleRefEnv(t, rules)
	now := saleRefStart
	if err := e.postRef(t, e.epochHeight(1), now, 200, 100, now); err != nil { // 2.00
		t.Fatalf("first: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(2), now, 17999, 10000, now); !errors.Is(err, ErrSaleReferenceStepTooLarge) { // -10.005 percent
		t.Fatalf("just over the downward bound: %v, want ErrSaleReferenceStepTooLarge", err)
	}
	if err := e.postRef(t, e.epochHeight(2), now, 18000, 10000, now); err != nil { // exactly -10 percent
		t.Fatalf("exactly the downward bound: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(3), now, 19801, 10000, now); !errors.Is(err, ErrSaleReferenceStepTooLarge) { // 1.80 -> 1.9801 = +10.005 percent
		t.Fatalf("just over the upward bound: %v, want ErrSaleReferenceStepTooLarge", err)
	}
	if err := e.postRef(t, e.epochHeight(3), now, 19800, 10000, now); err != nil { // exactly +10 percent
		t.Fatalf("exactly the upward bound: %v", err)
	}
}

func TestSaleReference_ARefusedStepLeavesTheLastReferenceAndWritesNothing(t *testing.T) {
	rules := saleRules(1)
	rules.SaleReferenceMaxStepBps = 500
	e := newSaleRefEnv(t, rules)
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 100, 100, saleRefStart); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(2), saleRefStart, 300, 100, saleRefStart); !errors.Is(err, ErrSaleReferenceStepTooLarge) {
		t.Fatalf("a tripling: %v", err)
	}
	manager := nhbstate.NewManager(e.sp.Trie)
	rec, found, err := manager.SaleReference()
	if err != nil || !found || rec.RateNum.Cmp(big.NewInt(100)) != 0 || rec.Epoch != 1 {
		t.Fatalf("the last reference changed: %+v found=%v err=%v", rec, found, err)
	}
	if _, exists, _ := manager.BuybackRefPriceForEpoch(2); exists {
		t.Fatalf("a refused reference left the buyback record of its epoch")
	}
}

func TestSaleReference_AStalePreviousReferenceLetsAnyNewOneIn(t *testing.T) {
	rules := saleRules(1)
	rules.SaleReferenceMaxStepBps = 500
	e := newSaleRefEnv(t, rules)
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 100, 100, saleRefStart); err != nil {
		t.Fatalf("first: %v", err)
	}
	later := saleRefStart.Add(3601 * time.Second)
	if err := e.postRef(t, e.epochHeight(2), later, 1000, 100, later); err != nil { // ten times, but the old one is stale
		t.Fatalf("a stuck feed must be replaceable: %v", err)
	}
	later2 := later.Add(60 * time.Second)
	if err := e.postRef(t, e.epochHeight(3), later2, 5000, 100, later2); !errors.Is(err, ErrSaleReferenceStepTooLarge) {
		t.Fatalf("the replacement is fresh, so the bound applies again: %v", err)
	}
}

func TestSaleReference_ThePreviousReferenceIsStillFreshAtExactlyTheMaximumAge(t *testing.T) {
	rules := saleRules(1) // maximum age 3,600 s
	rules.SaleReferenceMaxStepBps = 500
	e := newSaleRefEnv(t, rules)
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 100, 100, saleRefStart); err != nil {
		t.Fatalf("first: %v", err)
	}
	edge := saleRefStart.Add(3600 * time.Second) // the previous reference is exactly as old as the maximum age: fresh
	if err := e.postRef(t, e.epochHeight(2), edge, 300, 100, edge); !errors.Is(err, ErrSaleReferenceStepTooLarge) {
		t.Fatalf("at exactly the maximum age the previous reference still counts and the bound applies: %v", err)
	}
	past := saleRefStart.Add(3601 * time.Second) // one second more: stale, any new reference is accepted
	if err := e.postRef(t, e.epochHeight(2), past, 300, 100, past); err != nil {
		t.Fatalf("one second past the maximum age the previous reference is stale: %v", err)
	}
}

func TestSaleReference_ZeroStepBoundMeansNoBound(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	if err := e.postRef(t, e.epochHeight(1), saleRefStart, 100, 100, saleRefStart); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := e.postRef(t, e.epochHeight(2), saleRefStart, 100000, 100, saleRefStart); err != nil {
		t.Fatalf("with no bound set a 1,000x move is accepted: %v", err)
	}
}

func TestSaleReference_AnUnsignedOrOneSignatureShortReferenceRecordsNothing(t *testing.T) {
	e := newSaleRefEnv(t, saleRules(1))
	e.sp.BeginBlock(e.epochHeight(1), saleRefStart)
	defer e.sp.EndBlock()
	epoch, _ := e.sp.currentBuybackEpoch()
	data, err := rlp.EncodeToBytes(buybackRefPricePayload{RateNum: big.NewInt(5), RateDenom: big.NewInt(2), Epoch: epoch, Timestamp: uint64(saleRefStart.Unix()), Signatures: nil})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := e.sp.applyBuybackRefPrice(&types.Transaction{Type: types.TxTypeBuybackRefPrice, Data: data}); err == nil {
		t.Fatalf("a reference with no signature was accepted")
	}
	if _, found, _ := nhbstate.NewManager(e.sp.Trie).SaleReference(); found {
		t.Fatalf("an unsigned reference was recorded as the sale reference")
	}
}

func TestSaleReference_TheQuoteIsThePriceTheNextBlockCharges(t *testing.T) {
	node := newZNHBRPCTestNode(t)
	node.SetValidatorRules(ValidatorRules{SaleReferenceActivationHeight: 1, SaleReferenceMaxAgeSeconds: 3600})
	oneThousand := wholeToWei(1000)

	if _, err := node.QuoteBuyZNHB(oneThousand); !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("quote with the rule on and no reference: %v, want ErrSaleReferenceUnavailable", err)
	}

	now := node.state.blockTimestamp()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 1, RateNum: big.NewInt(5), RateDenom: big.NewInt(2), TimestampAt: uint64(now.Unix())}); err != nil {
		t.Fatalf("set reference: %v", err)
	}
	quote, err := node.QuoteBuyZNHB(oneThousand)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if want := refCostWei(5, 2, 1000); quote.NHBCostWei != want.String() {
		t.Fatalf("quote %s, want the reference cost %s", quote.NHBCostWei, want)
	}

	if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 1, RateNum: big.NewInt(5), RateDenom: big.NewInt(2), TimestampAt: uint64(now.Unix()) - 3601}); err != nil {
		t.Fatalf("set stale reference: %v", err)
	}
	if _, err := node.QuoteBuyZNHB(oneThousand); !errors.Is(err, ErrSaleReferenceUnavailable) {
		t.Fatalf("quote with a stale reference: %v, want ErrSaleReferenceUnavailable", err)
	}
}

func TestSaleReference_TheQuoteIsTheCurveWhileTheRuleIsOff(t *testing.T) {
	node := newZNHBRPCTestNode(t)
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.SetSaleReference(nhbstate.SaleReferenceRecord{Epoch: 1, RateNum: big.NewInt(500), RateDenom: big.NewInt(1), TimestampAt: uint64(node.state.blockTimestamp().Unix())}); err != nil {
		t.Fatalf("set reference: %v", err)
	}
	quote, err := node.QuoteBuyZNHB(wholeToWei(1000))
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	if want := expectedCost(t, big.NewInt(0), wholeToWei(1000)); quote.NHBCostWei != want.String() {
		t.Fatalf("quote %s with the rule off, want the curve cost %s", quote.NHBCostWei, want)
	}
}

func TestSaleReference_RefusalsAreClassifiedForTheProposer(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want proposalTxDisposition
	}{
		{ErrSaleReferenceUnavailable, proposalDispositionSkip},
		{ErrSaleReferenceTooFarAhead, proposalDispositionSkip},
		{ErrSaleReferenceTooOld, proposalDispositionPrune},
		{ErrSaleReferenceStepTooLarge, proposalDispositionPrune},
	} {
		if got := classifyProposalError(tc.err); got != tc.want {
			t.Errorf("%v classified %v, want %v", tc.err, got, tc.want)
		}
		wrapped := errors.Join(errors.New("buyZNHB"), tc.err)
		if got := classifyProposalError(wrapped); got != tc.want {
			t.Errorf("wrapped %v classified %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The sale price must never be read from the native pool or any peer-to-peer price (cheap to move: in a
// constant-product pool the price scales with (1 + net buying / reserve) squared). The reference comes only
// from the signed buyback reference-price channel.
func TestSaleReference_SourceNeverReadsAPoolOrMarketPrice(t *testing.T) {
	raw, err := os.ReadFile("sale_reference.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	src := string(raw)
	for _, banned := range []string{"native/amm", "native/market", "core/amm", "AmmPool", "Reserve0", "Reserve1", "GetPool", "SpotPrice", "p2p"} {
		if strings.Contains(src, banned) {
			t.Fatalf("sale_reference.go mentions %q: the sale price must come only from the signed reference", banned)
		}
	}
	buyback, err := os.ReadFile("buyback_tx.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(buyback), "recordSaleReference(") {
		t.Fatalf("applyBuybackRefPrice no longer records the sale reference")
	}
}
