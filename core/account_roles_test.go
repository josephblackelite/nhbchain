package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
	"nhbchain/native/market"
	"nhbchain/native/subscriptions"
)

// Several engines load the accounts a transaction touches one by one, change
// them and write each back separately. Where two roles are played by one
// address (a plan subscribed to by its own merchant, a listing filled by its
// own seller, a paymaster that is also the spender) the two loads are two
// objects, and whichever is written last overwrites the other's change: value
// is minted or destroyed by a transaction that applies without error.
//
// A plan subscribed to by its own merchant and a listing filled by its own
// seller are refused outright (subscriptions.ErrSelfSubscription,
// market.ErrSelfFill), so for those two assignments the tests assert the
// refusal; the settlement code still has to be conservative for a
// subscription record that exists in state, so that one is stored directly.
//
// These tests drive each such transaction through every assignment of its
// roles to accounts -- including the treasury wallet in every role -- and,
// for each of them, assert that the total of every asset across the touched
// accounts is unchanged, that every account moved by exactly what its roles
// entitle it to, and (through treasuryFixture.apply and lifecycle) that the
// supply invariant holds. They use the fixture from
// znhb_treasury_pool_test.go, which watches the module accounts that hold
// funds while a transaction is in flight.

// --- balance helpers ---------------------------------------------------------

func (f *treasuryFixture) balanceNHB(addr [20]byte) *big.Int {
	f.t.Helper()
	account, err := f.sp.getAccount(addr[:])
	if err != nil {
		f.t.Fatalf("load balance of %x: %v", addr, err)
	}
	return new(big.Int).Set(account.BalanceNHB)
}

// totalNHB sums the NHB of every watched account.
func (f *treasuryFixture) totalNHB() *big.Int {
	f.t.Helper()
	total := new(big.Int)
	for addr := range f.watched {
		total.Add(total, f.balanceNHB(addr))
	}
	return total
}

func (f *treasuryFixture) balanceOf(asset string, addr [20]byte) *big.Int {
	f.t.Helper()
	if asset == "NHB" {
		return f.balanceNHB(addr)
	}
	return f.balanceZNHB(addr)
}

// addrDeltas is what each address should gain (or lose) from a transaction,
// keyed by address so that two roles played by one account add up on it.
type addrDeltas map[[20]byte]*big.Int

func (d addrDeltas) add(addr [20]byte, amount *big.Int) {
	if current, ok := d[addr]; ok {
		current.Add(current, amount)
		return
	}
	d[addr] = new(big.Int).Set(amount)
}

// snapshot records the balance of asset for each address.
func (f *treasuryFixture) snapshot(asset string, addrs ...[20]byte) map[[20]byte]*big.Int {
	f.t.Helper()
	out := make(map[[20]byte]*big.Int, len(addrs))
	for _, addr := range addrs {
		out[addr] = f.balanceOf(asset, addr)
	}
	return out
}

// assertDeltas compares how far each snapshotted balance has moved with what
// its roles entitle it to.
func (f *treasuryFixture) assertDeltas(label, asset string, before map[[20]byte]*big.Int, want addrDeltas) {
	f.t.Helper()
	for addr, was := range before {
		wantDelta := big.NewInt(0)
		if d, ok := want[addr]; ok {
			wantDelta = d
		}
		gotDelta := new(big.Int).Sub(f.balanceOf(asset, addr), was)
		if gotDelta.Cmp(wantDelta) != 0 {
			f.t.Fatalf("%s: %s balance of %x moved by %s, want %s", label, asset, addr, gotDelta, wantDelta)
		}
	}
}

// --- subscription billing ----------------------------------------------------

const (
	subscriptionTestPrice   = 1_000_000_000_000_000_000 // the smallest price a plan may have: one whole token
	subscriptionTestFee     = 10_000_000_000_000_000    // the 1% management fee on the price
	subscriptionTestFunding = 5 * subscriptionTestPrice // what each account starts with
)

// subscriptionTestSupply is the ZNHB the treasury wallet holds in these tests,
// enough for it to pay a plan's price when it is the payer.
var subscriptionTestSupply = new(big.Int).Mul(big.NewInt(subscriptionTestPrice), big.NewInt(100))

func subscriptionTestSetup(f *treasuryFixture, asset subscriptions.Asset, payer, merchant, treasury int) {
	f.t.Helper()
	if err := f.sp.SetSubscriptionsConfig(subscriptions.Config{
		ManagementFeeBps:     100,
		ManagementFeeCapBps:  500,
		Treasury:             f.addr(treasury),
		MaxRetries:           3,
		RetryIntervalSeconds: 86400,
	}); err != nil {
		f.t.Fatalf("configure subscriptions: %v", err)
	}
	planData, err := rlp.EncodeToBytes(struct {
		Name               string
		PriceWei           *big.Int
		Asset              string
		IntervalSeconds    uint64
		TrialPeriodSeconds uint64
	}{Name: "plan", PriceWei: big.NewInt(subscriptionTestPrice), Asset: string(asset), IntervalSeconds: 86400})
	if err != nil {
		f.t.Fatalf("encode plan: %v", err)
	}
	f.mustApply("create plan", f.signed(merchant, &types.Transaction{Type: types.TxTypeSubscriptionCreatePlan, Data: planData}))
	subscribeData, err := rlp.EncodeToBytes(struct{ PlanID uint64 }{PlanID: 1})
	if err != nil {
		f.t.Fatalf("encode subscribe: %v", err)
	}
	subscribe := f.signed(payer, &types.Transaction{Type: types.TxTypeSubscriptionSubscribe, Data: subscribeData})
	if payer == merchant {
		// Subscribe refuses a plan's own merchant. Billing must still settle
		// conservatively for a record that exists in state all the same, so the
		// subscription Subscribe would have created is stored directly.
		if err := f.apply("subscribe", subscribe); !errors.Is(err, subscriptions.ErrSelfSubscription) {
			f.t.Fatalf("subscribe: a plan's own merchant: err = %v, want ErrSelfSubscription", err)
		}
		f.storeSubscription(asset, payer, merchant)
		return
	}
	f.mustApply("subscribe", subscribe)
}

// storeSubscription writes the first subscription to plan 1 straight into the
// registry, due immediately, the way Subscribe would have.
func (f *treasuryFixture) storeSubscription(asset subscriptions.Asset, payer, merchant int) {
	f.t.Helper()
	manager := nhbstate.NewManager(f.sp.Trie)
	registry := subscriptions.NewRegistry(manager)
	id, err := manager.SubscriptionsNextSubscriptionID()
	if err != nil {
		f.t.Fatalf("assign subscription id: %v", err)
	}
	now := uint64(f.now.Unix())
	sub := &subscriptions.Subscription{
		ID:              id,
		PlanID:          1,
		Payer:           f.addr(payer),
		Merchant:        f.addr(merchant),
		PriceWei:        big.NewInt(subscriptionTestPrice),
		Asset:           asset,
		IntervalSeconds: 86400,
		Status:          subscriptions.SubscriptionStatusActive,
		StartAt:         now,
		NextChargeAt:    now,
		CreatedAt:       now,
	}
	if err := registry.PutSubscription(sub); err != nil {
		f.t.Fatalf("store subscription: %v", err)
	}
	if err := manager.SubscriptionsAppendDue(now/secondsPerDay, sub.ID); err != nil {
		f.t.Fatalf("schedule first charge: %v", err)
	}
}

// chargeSubscriptionForRoles bills one cycle of a subscription to a plan
// priced in asset, with the payer, the plan's merchant and the management fee
// treasury assigned to accounts as who says (payer, merchant, feeTreasury),
// and asserts the charge moves exactly price - fee to the merchant and fee to
// the treasury out of the payer, with nothing created or destroyed. Payer and
// merchant being one account is a plan subscribed to by its own merchant:
// Subscribe refuses it, so that subscription is stored directly.
func chargeSubscriptionForRoles(t *testing.T, asset subscriptions.Asset, who []int) {
	t.Helper()
	f := newTreasuryFixtureWithSupply(t, subscriptionTestSupply)
	payer, merchant, treasury := who[0], who[1], who[2]
	for _, id := range who {
		f.fund(id, subscriptionTestFunding)
	}
	for _, id := range who {
		f.fundNHB(id, subscriptionTestFunding)
	}
	subscriptionTestSetup(f, asset, payer, merchant, treasury)

	roleAddrs := [][20]byte{f.addr(payer), f.addr(merchant), f.addr(treasury)}
	before := f.snapshot(string(asset), roleAddrs...)
	nhbBefore := f.totalNHB()
	f.lifecycle("first charge", 1)

	want := addrDeltas{}
	want.add(f.addr(payer), big.NewInt(-subscriptionTestPrice))
	want.add(f.addr(merchant), big.NewInt(subscriptionTestPrice-subscriptionTestFee))
	want.add(f.addr(treasury), big.NewInt(subscriptionTestFee))
	f.assertDeltas("first charge", string(asset), before, want)
	if got := f.totalNHB(); got.Cmp(nhbBefore) != 0 {
		t.Fatalf("NHB across the touched accounts changed from %s to %s", nhbBefore, got)
	}
}

// TestSubscriptionChargeRolesConserveNHB is the NHB twin of
// TestSubscriptionChargeRolesKeepSupplyInvariant: the same charge, every
// assignment of the payer, the merchant and the fee treasury (the treasury
// wallet included, any two of them one account), on a plan priced in NHB.
func TestSubscriptionChargeRolesConserveNHB(t *testing.T) {
	names := []string{"payer", "merchant", "feeTreasury"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) { chargeSubscriptionForRoles(t, subscriptions.AssetNHB, who) })
	}
}

// TestSelfSubscriptionDoesNotMint is the reported exploit: an account creates
// a plan naming itself as the merchant, subscribes to it, and lets billing
// run. The merchant's write used to overwrite the payer's debit, so every
// cycle credited the payer with the whole plan price (less the fee) and the
// balance compounded from nothing. Subscribe now refuses it, and billing of a
// subscription that exists in state anyway (stored directly, as
// subscriptionTestSetup does) must cost the account exactly the management
// fee each cycle, in either asset.
func TestSelfSubscriptionDoesNotMint(t *testing.T) {
	for _, asset := range []subscriptions.Asset{subscriptions.AssetZNHB, subscriptions.AssetNHB} {
		t.Run(string(asset), func(t *testing.T) {
			f := newTreasuryFixtureWithSupply(t, subscriptionTestSupply)
			const user, feeTreasury = 0, 1
			for _, id := range []int{user, feeTreasury} {
				f.fund(id, subscriptionTestFunding)
				f.fundNHB(id, subscriptionTestFunding)
			}
			subscriptionTestSetup(f, asset, user, user, feeTreasury)

			totalZNHB, totalNHB := f.totalZNHB(), f.totalNHB()
			userAddr, treasuryAddr := f.addr(user), f.addr(feeTreasury)
			for cycle := 1; cycle <= 3; cycle++ {
				userBefore := f.balanceOf(string(asset), userAddr)
				treasuryBefore := f.balanceOf(string(asset), treasuryAddr)
				f.lifecycleAt(fmt.Sprintf("cycle %d", cycle), uint64(cycle), f.now.Unix()+int64(cycle-1)*86400)

				spent := new(big.Int).Sub(userBefore, f.balanceOf(string(asset), userAddr))
				if spent.Cmp(big.NewInt(subscriptionTestFee)) != 0 {
					t.Fatalf("cycle %d: the account paying itself lost %s, want exactly the %d management fee", cycle, spent, subscriptionTestFee)
				}
				earned := new(big.Int).Sub(f.balanceOf(string(asset), treasuryAddr), treasuryBefore)
				if earned.Cmp(big.NewInt(subscriptionTestFee)) != 0 {
					t.Fatalf("cycle %d: the fee treasury gained %s, want %d", cycle, earned, subscriptionTestFee)
				}
				if got := f.totalZNHB(); got.Cmp(totalZNHB) != 0 {
					t.Fatalf("cycle %d: total ZNHB moved from %s to %s", cycle, totalZNHB, got)
				}
				if got := f.totalNHB(); got.Cmp(totalNHB) != 0 {
					t.Fatalf("cycle %d: total NHB moved from %s to %s", cycle, totalNHB, got)
				}
			}
		})
	}
}

// --- market fills, cancellations --------------------------------------------

// setMarketFeeCollector points the market's flat-fee collector at an account
// (in production it is a dedicated module address) so the collector can share
// an address with the buyer or the seller.
func (f *treasuryFixture) setMarketFeeCollector(id int) {
	f.t.Helper()
	addr := f.addr(id)
	f.sp.marketFeeCollectorAddr = crypto.MustNewAddress(crypto.NHBPrefix, addr[:])
}

const (
	marketTestListed  = 1_000 // ZNHB listed and bought
	marketTestRateDen = 3     // NHB per ZNHB, so the fill costs 3,000
)

// fillMarketListingForRoles lists ZNHB from the seller and fills the whole
// listing as the buyer, with the fee collector assigned as who says (buyer,
// seller, feeCollector). Both the listing and the fill are applied through
// the real dispatch and state, and the fill must move exactly: the cost from
// buyer to seller, the flat fee from buyer to collector, and the listed ZNHB
// from the escrow to the buyer.
func fillMarketListingForRoles(t *testing.T, who []int) {
	t.Helper()
	f := newTreasuryFixture(t)
	buyer, seller, collector := who[0], who[1], who[2]
	for _, id := range who {
		f.fund(id, 1_000_000)
	}
	for _, id := range who {
		f.fundNHBWei(id, big.NewInt(5_000_000_000_000_000_000))
	}
	f.setMarketFeeCollector(collector)

	f.mustApply("create listing", f.signed(seller, marketCreateListingTx(t, 0, big.NewInt(marketTestListed), big.NewInt(1), big.NewInt(marketTestRateDen), true)))
	listingID := soleOpenListingID(t, f.sp)

	fee, ok := new(big.Int).SetString(defaultMarketFlatFeeWei, 10)
	if !ok {
		t.Fatalf("parse default flat fee")
	}
	cost := big.NewInt(marketTestListed * marketTestRateDen)

	roleAddrs := [][20]byte{f.addr(buyer), f.addr(seller), f.addr(collector)}
	nhbBefore := f.snapshot("NHB", roleAddrs...)
	znhbBefore := f.snapshot("ZNHB", roleAddrs...)
	totalNHB := f.totalNHB()
	fill := f.signed(buyer, marketFillListingTx(t, 0, listingID, big.NewInt(marketTestListed)))
	if buyer == seller {
		// A buyer filling its own listing is refused, and the refusal moves
		// nothing.
		if err := f.apply("fill listing", fill); !errors.Is(err, market.ErrSelfFill) {
			t.Fatalf("fill listing: a buyer filling its own listing: err = %v, want ErrSelfFill", err)
		}
		f.assertDeltas("refused fill", "NHB", nhbBefore, addrDeltas{})
		f.assertDeltas("refused fill", "ZNHB", znhbBefore, addrDeltas{})
		return
	}
	f.mustApply("fill listing", fill)

	wantNHB := addrDeltas{}
	wantNHB.add(f.addr(buyer), new(big.Int).Neg(new(big.Int).Add(cost, fee)))
	wantNHB.add(f.addr(seller), cost)
	wantNHB.add(f.addr(collector), fee)
	f.assertDeltas("fill listing", "NHB", nhbBefore, wantNHB)
	wantZNHB := addrDeltas{}
	wantZNHB.add(f.addr(buyer), big.NewInt(marketTestListed))
	f.assertDeltas("fill listing", "ZNHB", znhbBefore, wantZNHB)
	if got := f.totalNHB(); got.Cmp(totalNHB) != 0 {
		t.Fatalf("NHB across the touched accounts changed from %s to %s", totalNHB, got)
	}
}

// TestMarketFillRolesConserveNHBAndZNHB fills a listing with the buyer, the
// seller and the fee collector assigned to accounts in every way, the treasury
// wallet included. A buyer filling its own listing used to have the seller's
// write overwrite its own: it kept its NHB, gained the cost on top, and lost
// the ZNHB the escrow paid out. That assignment is now refused with
// market.ErrSelfFill and must leave every balance as it was.
func TestMarketFillRolesConserveNHBAndZNHB(t *testing.T) {
	names := []string{"buyer", "seller", "feeCollector"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) { fillMarketListingForRoles(t, who) })
	}
}

// TestMarketSelfFillCannotMintNHB is the reported exploit: an account holding
// NHB and one wei of ZNHB lists that wei at a rate that costs nearly all its
// NHB and fills the listing itself. The fill is refused (market.ErrSelfFill),
// so the account keeps exactly the NHB it had -- nothing minted, and not even
// the flat fee charged -- and gets the escrowed ZNHB back when it cancels.
func TestMarketSelfFillCannotMintNHB(t *testing.T) {
	f := newTreasuryFixture(t)
	const user = 0
	f.fund(user, 1)
	balance := new(big.Int).Mul(big.NewInt(1_000), big.NewInt(1_000_000_000_000_000_000))
	f.fundNHBWei(user, balance)
	f.setMarketFeeCollector(1)
	fee, _ := new(big.Int).SetString(defaultMarketFlatFeeWei, 10)
	cost := new(big.Int).Sub(balance, fee)

	f.mustApply("create listing", f.signed(user, marketCreateListingTx(t, 0, big.NewInt(1), big.NewInt(1), cost, false)))
	listingID := soleOpenListingID(t, f.sp)
	if err := f.apply("fill own listing", f.signed(user, marketFillListingTx(t, 0, listingID, big.NewInt(1)))); !errors.Is(err, market.ErrSelfFill) {
		t.Fatalf("fill own listing: err = %v, want ErrSelfFill", err)
	}

	addr := f.addr(user)
	if got := f.balanceNHB(addr); got.Cmp(balance) != 0 {
		t.Fatalf("NHB after a refused fill of its own listing = %s, want the %s it started with", got, balance)
	}
	if got := f.balanceZNHB(addr); got.Sign() != 0 {
		t.Fatalf("ZNHB after a refused fill = %s, want the wei to stay in escrow", got)
	}
	f.mustApply("cancel listing", f.signed(user, marketCancelListingTx(t, 0, listingID)))
	if got := f.balanceZNHB(addr); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("ZNHB after cancelling = %s, want the 1 wei it listed", got)
	}
}

// TestMarketCancelRolesConserveZNHB cancels a listing with the seller being an
// ordinary account or the treasury wallet.
func TestMarketCancelRolesConserveZNHB(t *testing.T) {
	for _, seller := range []int{0, treasuryID} {
		t.Run(describeAssignment([]string{"seller"}, []int{seller}), func(t *testing.T) {
			f := newTreasuryFixture(t)
			f.fund(seller, 1_000_000)
			before := f.snapshot("ZNHB", f.addr(seller))
			f.mustApply("create listing", f.signed(seller, marketCreateListingTx(t, 0, big.NewInt(marketTestListed), big.NewInt(1), big.NewInt(marketTestRateDen), true)))
			listingID := soleOpenListingID(t, f.sp)
			f.mustApply("cancel listing", f.signed(seller, marketCancelListingTx(t, 0, listingID)))
			f.assertDeltas("cancel listing", "ZNHB", before, addrDeltas{})
		})
	}
}

// --- loyalty program rewards -------------------------------------------------

const (
	loyaltyTestAdmin    = 90 // holds ROLE_LOYALTY_ADMIN and takes no other role
	loyaltyTestTreasury = 91 // the base-reward treasury, which never pays (base rate zero)

	loyaltyTestSpend  = 100_000
	loyaltyTestReward = 10_000 // 10% of the spend
)

// setUpLoyaltyProgram registers a business owned by the merchant, names the
// paymaster (through a loyalty admin, once the paymaster has recorded its own
// opt-in: an owner may only name its own wallet), registers the merchant and
// creates a program paying 10% of every NHB spend in ZNHB.
func (f *treasuryFixture) setUpLoyaltyProgram(merchant, paymaster int) {
	f.t.Helper()
	manager := nhbstate.NewManager(f.sp.Trie)
	// The tokens may already be registered; either way they must exist.
	_ = manager.RegisterToken("NHB", "Native", 18)
	_ = manager.RegisterToken("ZNHB", "ZapNHB", 18)
	treasury := f.addr(loyaltyTestTreasury)
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     append([]byte(nil), treasury[:]...),
		BaseBps:      0,
		MinSpend:     big.NewInt(0),
		CapPerTx:     big.NewInt(0),
		DailyCapUser: big.NewInt(0),
		Dynamic: loyalty.DynamicConfig{
			DailyCapPctOf7dFeesBps: 10_000,
			PriceGuard:             loyalty.PriceGuardConfig{Enabled: false},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		f.t.Fatalf("set loyalty config: %v", err)
	}
	admin := f.addr(loyaltyTestAdmin)
	if err := manager.SetRole(RoleLoyaltyAdmin, admin[:]); err != nil {
		f.t.Fatalf("grant loyalty admin role: %v", err)
	}
	merchantAddr, paymasterAddr := f.addr(merchant), f.addr(paymaster)
	registry := loyalty.NewRegistry(manager)
	businessID, err := registry.RegisterBusiness(merchantAddr, "business")
	if err != nil {
		f.t.Fatalf("register business: %v", err)
	}
	if err := registry.SetPaymaster(businessID, paymasterAddr, paymasterAddr); err != nil {
		f.t.Fatalf("paymaster opt-in: %v", err)
	}
	if err := registry.SetPaymaster(businessID, admin, paymasterAddr); err != nil {
		f.t.Fatalf("set paymaster: %v", err)
	}
	if err := registry.AddMerchantAddress(businessID, merchantAddr); err != nil {
		f.t.Fatalf("add merchant: %v", err)
	}
	var programID loyalty.ProgramID
	programID[31] = 1
	if err := registry.CreateProgram(merchantAddr, &loyalty.Program{
		ID:              programID,
		Owner:           merchantAddr,
		TokenSymbol:     "ZNHB",
		AccrualBps:      1000,
		MinSpendWei:     big.NewInt(0),
		CapPerTx:        big.NewInt(100_000),
		DailyCapUser:    big.NewInt(1_000_000),
		DailyCapProgram: big.NewInt(10_000_000),
		StartTime:       uint64(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()),
		Active:          true,
	}); err != nil {
		f.t.Fatalf("create program: %v", err)
	}
}

func (f *treasuryFixture) spendNHB(sender, merchant int) *types.Transaction {
	f.t.Helper()
	to := f.addr(merchant)
	return f.signed(sender, &types.Transaction{
		Type:  types.TxTypeTransfer,
		To:    append([]byte(nil), to[:]...),
		Value: big.NewInt(loyaltyTestSpend),
	})
}

// TestLoyaltyProgramRewardRolesKeepSupplyInvariant pays a program reward on an
// NHB transfer with the spender, the merchant and the paymaster the reward is
// drawn from assigned to accounts in every way, the treasury wallet included.
// The paymaster is debited and the spender credited on separately loaded
// accounts, so a paymaster that was the spender or the merchant had its debit
// overwritten by (or overwrote) the transfer's own write, and in every other
// case the credit was never persisted at all: the reward was minted, or
// destroyed, instead of moved.
func TestLoyaltyProgramRewardRolesKeepSupplyInvariant(t *testing.T) {
	names := []string{"spender", "merchant", "paymaster"}
	for _, who := range roleAssignments(len(names)) {
		name := describeAssignment(names, who)
		t.Run(name, func(t *testing.T) {
			f := newTreasuryFixture(t)
			spender, merchant, paymaster := who[0], who[1], who[2]
			for _, id := range who {
				f.fund(id, 1_000_000)
			}
			f.fundNHB(spender, 10_000_000)
			f.setUpLoyaltyProgram(merchant, paymaster)

			roleAddrs := [][20]byte{f.addr(spender), f.addr(merchant), f.addr(paymaster)}
			before := f.snapshot("ZNHB", roleAddrs...)
			f.mustApply(name, f.spendNHB(spender, merchant))

			want := addrDeltas{}
			want.add(f.addr(spender), big.NewInt(loyaltyTestReward))
			want.add(f.addr(paymaster), big.NewInt(-loyaltyTestReward))
			f.assertDeltas(name, "ZNHB", before, want)
		})
	}
}

// TestLoyaltyProgramRewardIsPersistedWithoutDisturbingTheSender runs the same
// NHB transfer with and without a paying program and compares the sender's
// stored account: the reward must be in it, and nothing else about it may
// differ -- the transfer persists the account, then records engagement and
// nonce against it, so a reward written back from the in-memory object would
// silently revert those.
func TestLoyaltyProgramRewardIsPersistedWithoutDisturbingTheSender(t *testing.T) {
	f := newTreasuryFixture(t)
	const spender, merchant, paymaster = 0, 1, 2
	f.fund(spender, 0)
	f.fund(merchant, 0)
	f.fund(paymaster, 1_000_000)
	f.fundNHB(spender, 10_000_000)
	plain, err := f.sp.Copy() // before any program exists
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	f.setUpLoyaltyProgram(merchant, paymaster)

	tx := f.spendNHB(spender, merchant)
	rewarded, err := f.sp.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	if err := rewarded.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer with a paying program: %v", err)
	}
	if err := plain.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transfer without a program: %v", err)
	}

	spenderAddr := f.addr(spender)
	got, err := rewarded.getAccount(spenderAddr[:])
	if err != nil {
		t.Fatalf("load rewarded sender: %v", err)
	}
	base, err := plain.getAccount(spenderAddr[:])
	if err != nil {
		t.Fatalf("load plain sender: %v", err)
	}
	if got.BalanceZNHB.Cmp(big.NewInt(loyaltyTestReward)) != 0 {
		t.Fatalf("stored sender ZNHB = %s, want the %d reward", got.BalanceZNHB, loyaltyTestReward)
	}
	if base.BalanceZNHB.Sign() != 0 {
		t.Fatalf("control run paid a reward: %s", base.BalanceZNHB)
	}
	if base.EngagementTxCount == 0 {
		t.Fatalf("control run recorded no engagement, so the comparison below proves nothing")
	}
	if got.Nonce != base.Nonce || got.BalanceNHB.Cmp(base.BalanceNHB) != 0 ||
		got.EngagementTxCount != base.EngagementTxCount || got.EngagementScore != base.EngagementScore ||
		got.EngagementDay != base.EngagementDay || got.EngagementMinutes != base.EngagementMinutes {
		t.Fatalf("paying the reward disturbed the sender's account: got nonce=%d nhb=%s engagement=%d/%d/%s/%d, control nonce=%d nhb=%s engagement=%d/%d/%s/%d",
			got.Nonce, got.BalanceNHB, got.EngagementTxCount, got.EngagementScore, got.EngagementDay, got.EngagementMinutes,
			base.Nonce, base.BalanceNHB, base.EngagementTxCount, base.EngagementScore, base.EngagementDay, base.EngagementMinutes)
	}
	paymasterAddr := f.addr(paymaster)
	paymasterAcc, err := rewarded.getAccount(paymasterAddr[:])
	if err != nil {
		t.Fatalf("load paymaster: %v", err)
	}
	if want := big.NewInt(1_000_000 - loyaltyTestReward); paymasterAcc.BalanceZNHB.Cmp(want) != 0 {
		t.Fatalf("paymaster ZNHB = %s, want %s", paymasterAcc.BalanceZNHB, want)
	}
}

// --- paymaster consent -------------------------------------------------------

// setPaymasterTx builds the signed transaction naming a paymaster.
func (f *treasuryFixture) setPaymasterTx(sender int, businessID loyalty.BusinessID, paymaster [20]byte) *types.Transaction {
	f.t.Helper()
	data, err := json.Marshal(map[string]string{
		"businessId": fmt.Sprintf("0x%x", businessID[:]),
		"paymaster":  crypto.MustNewAddress(crypto.NHBPrefix, paymaster[:]).String(),
	})
	if err != nil {
		f.t.Fatalf("encode set-paymaster payload: %v", err)
	}
	return f.signed(sender, &types.Transaction{Type: types.TxTypeLoyaltySetPaymaster, Data: data})
}

func (f *treasuryFixture) businessPaymaster(businessID loyalty.BusinessID) [20]byte {
	f.t.Helper()
	business, ok, err := f.sp.LoyaltyBusinessByID(businessID)
	if err != nil || !ok {
		f.t.Fatalf("load business: ok=%v err=%v", ok, err)
	}
	return business.Paymaster
}

// TestLoyaltyPaymasterMustConsent: program rewards are debited from the
// paymaster's balance, and the wallet a business owner names never signs the
// naming transaction. Anyone could therefore point a program at any account --
// an ordinary user's, or the treasury wallet whose ZNHB is the Reward Pool --
// and have every reward paid out of it to a spender of their choosing (with
// the reward persisted correctly, that is straightforward theft). The owner
// may only name its own wallet; only a loyalty admin may name another, and only
// one that has signed its own opt-in. The owner's refusal is prune-safe for the
// block builder, the admin's refusal without an opt-in is skippable (the
// opt-in can still arrive).
func TestLoyaltyPaymasterMustConsent(t *testing.T) {
	f := newTreasuryFixture(t)
	const owner, victim = 0, 1
	f.fund(owner, 0)
	f.fund(victim, 1_000_000)
	admin := f.addr(loyaltyTestAdmin)
	manager := nhbstate.NewManager(f.sp.Trie)
	if err := manager.SetRole(RoleLoyaltyAdmin, admin[:]); err != nil {
		t.Fatalf("grant loyalty admin role: %v", err)
	}
	f.fund(loyaltyTestAdmin, 0)

	nameData, err := json.Marshal(map[string]string{"name": "business"})
	if err != nil {
		t.Fatalf("encode business name: %v", err)
	}
	f.mustApply("create business", f.signed(owner, &types.Transaction{Type: types.TxTypeCreateLoyaltyBusiness, Data: nameData}))
	ids, err := f.sp.LoyaltyBusinessesByOwner(f.addr(owner))
	if err != nil || len(ids) != 1 {
		t.Fatalf("list businesses: ids=%d err=%v", len(ids), err)
	}
	businessID := ids[0]
	var none [20]byte

	for _, target := range []struct {
		name string
		addr [20]byte
	}{
		{"another user's wallet", f.addr(victim)},
		{"the treasury wallet", f.admin},
	} {
		err := f.apply(target.name, f.setPaymasterTx(owner, businessID, target.addr))
		if !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
			t.Fatalf("owner naming %s: err = %v, want ErrPaymasterConsentRequired", target.name, err)
		}
		if got := classifyProposalError(err); got != proposalDispositionPrune {
			t.Fatalf("owner naming %s: classifyProposalError = %v, want prune", target.name, got)
		}
		if got := f.businessPaymaster(businessID); got != none {
			t.Fatalf("owner naming %s left paymaster %x behind", target.name, got)
		}
	}

	f.mustApply("owner names its own wallet", f.setPaymasterTx(owner, businessID, f.addr(owner)))
	if got := f.businessPaymaster(businessID); got != f.addr(owner) {
		t.Fatalf("paymaster = %x, want the owner's own wallet", got)
	}

	// A loyalty admin needs the named wallet's own signed opt-in as well.
	err = f.apply("loyalty admin names a wallet that has not opted in", f.setPaymasterTx(loyaltyTestAdmin, businessID, f.addr(victim)))
	if !errors.Is(err, loyalty.ErrPaymasterConsent) {
		t.Fatalf("loyalty admin naming a wallet without its opt-in: err = %v, want ErrPaymasterConsent", err)
	}
	if got := classifyProposalError(err); got != proposalDispositionSkip {
		t.Fatalf("loyalty admin naming a wallet without its opt-in: classifyProposalError = %v, want skip", got)
	}
	f.mustApply("victim opts in", f.setPaymasterTx(victim, businessID, f.addr(victim)))
	if got := f.businessPaymaster(businessID); got != f.addr(owner) {
		t.Fatalf("an opt-in changed the paymaster to %x", got)
	}
	f.mustApply("loyalty admin names a wallet", f.setPaymasterTx(loyaltyTestAdmin, businessID, f.addr(victim)))
	if got := f.businessPaymaster(businessID); got != f.addr(victim) {
		t.Fatalf("paymaster = %x, want the wallet the loyalty admin named", got)
	}
}

// TestCreateBlockPrunesUnconsentedPaymasterAssignment: a transaction naming a
// wallet the sender does not control as paymaster is refused at mempool
// admission, which simulates the same execution, and, handed to CreateBlock
// directly as a proposal that skipped admission would, must be dropped from
// the block, not abort it for every other sender.
func TestCreateBlockPrunesUnconsentedPaymasterAssignment(t *testing.T) {
	node := newTestNode(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(ownerKey)
	victimKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate victim key: %v", err)
	}
	victim := toAddress(victimKey)

	node.stateMu.Lock()
	registry := loyalty.NewRegistry(nhbstate.NewManager(node.state.Trie))
	businessID, err := registry.RegisterBusiness(owner, "business")
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("register business: %v", err)
	}

	data, err := json.Marshal(map[string]string{
		"businessId": fmt.Sprintf("0x%x", businessID[:]),
		"paymaster":  crypto.MustNewAddress(crypto.NHBPrefix, victim[:]).String(),
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeLoyaltySetPaymaster,
		Nonce:    0,
		Data:     data,
		Value:    big.NewInt(0),
		GasLimit: 50_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(ownerKey.PrivateKey); err != nil {
		t.Fatalf("sign: %v", err)
	}

	if err := node.AddTransaction(tx); !errors.Is(err, loyalty.ErrPaymasterConsentRequired) {
		t.Fatalf("mempool admission of an unconsented paymaster assignment: err = %v, want ErrPaymasterConsentRequired", err)
	}
	block, err := node.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("block building aborted over one refused paymaster assignment: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("the refused assignment was included in the block")
	}
}
