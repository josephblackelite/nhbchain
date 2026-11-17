package core

import (
	"encoding/json"
	"errors"
	"fmt"
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

// selfDealingPartitions returns every way n roles can be split into groups of
// roles that share one address, as role -> group index slices (restricted
// growth strings: [0 0 1] is "roles 0 and 1 share an address, role 2 has its
// own"). n=3 yields 5 partitions, n=4 yields 15.
func selfDealingPartitions(n int) [][]int {
	var out [][]int
	var rec func(cur []int, max int)
	rec = func(cur []int, max int) {
		if len(cur) == n {
			out = append(out, append([]int(nil), cur...))
			return
		}
		for g := 0; g <= max+1; g++ {
			next := max
			if g > max {
				next = g
			}
			rec(append(cur, g), next)
		}
	}
	rec(nil, -1)
	return out
}

func selfDealingAddr(group int) [20]byte {
	return [20]byte{0xC0, byte(group + 1)}
}

func selfDealingAssetBalance(acc *types.Account, asset subscriptions.Asset) *big.Int {
	if asset == subscriptions.AssetZNHB {
		return acc.BalanceZNHB
	}
	return acc.BalanceNHB
}

// Payer, merchant and treasury may resolve to the same address. Whatever the
// combination, one settled charge must move exactly the price out of the
// payer, the net to the merchant and the fee to the treasury, with the total
// of the charged asset across the touched accounts unchanged. Before each
// address was loaded once, the payer/merchant copies of one account made the
// last write win: a self-payer ended with balance + price - fee.
//
// The subscriptions are stored directly in the registry: Subscribe refuses a
// self-subscription, and settlement must stay conservative for any record
// that exists in state.
func TestSubscriptionSettlementRoleCollisionsConserveValue(t *testing.T) {
	const price, feeBps = 10_000, 100
	fee := big.NewInt(price * feeBps / 10_000)
	net := big.NewInt(price - price*feeBps/10_000)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for _, asset := range []subscriptions.Asset{subscriptions.AssetNHB, subscriptions.AssetZNHB} {
		for _, roles := range selfDealingPartitions(3) {
			payerGroup, merchantGroup, treasuryGroup := roles[0], roles[1], roles[2]
			t.Run(fmt.Sprintf("%s/payer=%d merchant=%d treasury=%d", asset, payerGroup, merchantGroup, treasuryGroup), func(t *testing.T) {
				sp := newStakingStateProcessor(t)
				payer, merchant, treasury := selfDealingAddr(payerGroup), selfDealingAddr(merchantGroup), selfDealingAddr(treasuryGroup)
				if err := sp.SetSubscriptionsConfig(subscriptions.Config{
					ManagementFeeBps:     feeBps,
					ManagementFeeCapBps:  500,
					Treasury:             treasury,
					MaxRetries:           3,
					RetryIntervalSeconds: 86400,
				}); err != nil {
					t.Fatalf("configure subscriptions: %v", err)
				}

				// Each distinct address gets its own starting balances.
				initial := map[[20]byte][2]*big.Int{}
				for _, group := range roles {
					addr := selfDealingAddr(group)
					if _, seen := initial[addr]; seen {
						continue
					}
					nhb := big.NewInt(1_000_000 + int64(group)*1_000)
					znhb := big.NewInt(2_000_000 + int64(group)*1_000)
					initial[addr] = [2]*big.Int{nhb, znhb}
					if err := sp.setAccount(addr[:], &types.Account{BalanceNHB: new(big.Int).Set(nhb), BalanceZNHB: new(big.Int).Set(znhb), Stake: big.NewInt(0)}); err != nil {
						t.Fatalf("seed account: %v", err)
					}
				}

				manager := nhbstate.NewManager(sp.Trie)
				registry := subscriptions.NewRegistry(manager)
				sub := &subscriptions.Subscription{
					ID:              1,
					PlanID:          1,
					Payer:           payer,
					Merchant:        merchant,
					PriceWei:        big.NewInt(price),
					Asset:           asset,
					IntervalSeconds: 86400,
					Status:          subscriptions.SubscriptionStatusActive,
					StartAt:         uint64(now.Unix()),
					NextChargeAt:    uint64(now.Unix()),
					CreatedAt:       uint64(now.Unix()),
				}
				if err := registry.PutSubscription(sub); err != nil {
					t.Fatalf("store subscription: %v", err)
				}
				if err := manager.SubscriptionsAppendDue(uint64(now.Unix())/secondsPerDay, sub.ID); err != nil {
					t.Fatalf("schedule charge: %v", err)
				}

				sp.BeginBlock(1, now)
				defer sp.EndBlock()
				if err := sp.settleSubscriptionCharges(now.Unix()); err != nil {
					t.Fatalf("settle: %v", err)
				}

				// Expected balance of the charged asset per address.
				idx := 0
				if asset == subscriptions.AssetZNHB {
					idx = 1
				}
				expected := map[[20]byte]*big.Int{}
				for addr, balances := range initial {
					expected[addr] = new(big.Int).Set(balances[idx])
				}
				expected[payer].Sub(expected[payer], big.NewInt(price))
				expected[merchant].Add(expected[merchant], net)
				expected[treasury].Add(expected[treasury], fee)

				totalBefore, totalAfter := new(big.Int), new(big.Int)
				for addr, balances := range initial {
					acc, err := sp.getAccount(addr[:])
					if err != nil {
						t.Fatalf("load %x: %v", addr, err)
					}
					if got := selfDealingAssetBalance(acc, asset); got.Cmp(expected[addr]) != 0 {
						t.Errorf("address %x: %s balance %s, want %s", addr[:2], asset, got, expected[addr])
					}
					// The other asset is never touched.
					otherAsset := subscriptions.AssetNHB
					if asset == subscriptions.AssetNHB {
						otherAsset = subscriptions.AssetZNHB
					}
					if got, want := selfDealingAssetBalance(acc, otherAsset), balances[1-idx]; got.Cmp(want) != 0 {
						t.Errorf("address %x: %s balance %s changed from %s", addr[:2], otherAsset, got, want)
					}
					totalBefore.Add(totalBefore, balances[idx])
					totalAfter.Add(totalAfter, selfDealingAssetBalance(acc, asset))
				}
				if totalBefore.Cmp(totalAfter) != 0 {
					t.Fatalf("settling one charge changed the total %s supply across the touched accounts: %s -> %s", asset, totalBefore, totalAfter)
				}

				stored, ok := registry.GetSubscription(sub.ID)
				if !ok || stored.CycleCount != 1 || stored.LastChargeStatus != subscriptions.ChargeStatusPaid {
					t.Fatalf("expected one paid cycle recorded, got %+v (found=%v)", stored, ok)
				}
				charges, err := registry.ListCharges(sub.ID)
				if err != nil || len(charges) != 1 || charges[0].FeeWei.Cmp(fee) != 0 {
					t.Fatalf("expected one charge carrying the %s fee, got %+v err=%v", fee, charges, err)
				}
			})
		}
	}
}

func selfDealingSubscriptionsProcessor(t *testing.T, treasury [20]byte, feeBps uint32) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	if err := sp.SetSubscriptionsConfig(subscriptions.Config{
		ManagementFeeBps:     feeBps,
		ManagementFeeCapBps:  500,
		Treasury:             treasury,
		MaxRetries:           3,
		RetryIntervalSeconds: 86400,
	}); err != nil {
		t.Fatalf("configure subscriptions: %v", err)
	}
	return sp
}

func selfDealingCreatePlanTx(t *testing.T, key *crypto.PrivateKey, nonce uint64, price int64, asset string) *types.Transaction {
	t.Helper()
	data, err := rlp.EncodeToBytes(struct {
		Name               string
		PriceWei           *big.Int
		Asset              string
		IntervalSeconds    uint64
		TrialPeriodSeconds uint64
	}{Name: "Loop", PriceWei: big.NewInt(price), Asset: asset, IntervalSeconds: 86400})
	if err != nil {
		t.Fatalf("encode create-plan payload: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSubscriptionCreatePlan, Nonce: nonce, Data: data, GasLimit: 100_000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign create-plan: %v", err)
	}
	return tx
}

func selfDealingSubscribeTx(t *testing.T, key *crypto.PrivateKey, nonce, planID uint64) *types.Transaction {
	t.Helper()
	data, err := rlp.EncodeToBytes(struct{ PlanID uint64 }{PlanID: planID})
	if err != nil {
		t.Fatalf("encode subscribe payload: %v", err)
	}
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeSubscriptionSubscribe, Nonce: nonce, Data: data, GasLimit: 100_000, GasPrice: big.NewInt(1)}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign subscribe: %v", err)
	}
	return tx
}

// One account creates a plan priced at its whole balance and subscribes to
// it. That used to mint the price minus the fee every cycle (the debit and
// the credit were two copies of one account and the credit landed last). The
// subscribe transaction is now refused, permanently, and nothing is ever
// scheduled or charged.
func TestSubscriptionSelfSubscribeIsRejectedAndNeverMints(t *testing.T) {
	treasury := [20]byte{0xFE, 0x01}
	sp := selfDealingSubscriptionsProcessor(t, treasury, 100)
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := key.PubKey().Address().Bytes()
	const balance = 1_000_000_000_000_000_000 // one whole token, the smallest price a plan may have
	if err := sp.setAccount(addr, &types.Account{BalanceNHB: big.NewInt(balance), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := sp.setAccount(treasury[:], &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed treasury: %v", err)
	}

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	sp.BeginBlock(1, now)
	defer sp.EndBlock()
	if err := sp.ApplyTransaction(selfDealingCreatePlanTx(t, key, 0, balance, "NHB")); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	subscribeErr := sp.ApplyTransaction(selfDealingSubscribeTx(t, key, 1, 1))

	if err := sp.settleSubscriptionCharges(now.Add(48 * time.Hour).Unix()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	total := new(big.Int)
	for _, a := range [][]byte{addr, treasury[:]} {
		acc, err := sp.getAccount(a)
		if err != nil {
			t.Fatalf("load account: %v", err)
		}
		total.Add(total, acc.BalanceNHB)
	}
	if total.Cmp(big.NewInt(balance)) != 0 {
		t.Fatalf("NHB across the account and the treasury is %s, want the original %d", total, balance)
	}

	if !errors.Is(subscribeErr, subscriptions.ErrSelfSubscription) {
		t.Fatalf("expected the self-subscription to be rejected with ErrSelfSubscription, got %v", subscribeErr)
	}
	if got := classifyProposalError(subscribeErr); got != proposalDispositionPrune {
		t.Fatalf("a self-subscription can never become valid: want prune, got disposition %d", got)
	}
	if _, ok := subscriptions.NewRegistry(nhbstate.NewManager(sp.Trie)).GetSubscription(1); ok {
		t.Fatalf("a rejected self-subscription must leave no subscription behind")
	}
}

// A block proposer handed a self-subscription (for example from a mempool
// that skipped admission simulation) must drop just that transaction and still
// build the block, never fail the whole proposal.
func TestCreateBlockPrunesSelfSubscriptionWithoutAborting(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
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
		Alloc: map[string]map[string]string{key.PubKey().Address().String(): {"NHB": "1000000000000000000", "ZNHB": "0"}},
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

	planTx := selfDealingCreatePlanTx(t, key, 0, 1_000_000_000_000_000_000, "NHB")
	if err := node.AddTransaction(planTx); err != nil {
		t.Fatalf("add create-plan: %v", err)
	}
	planBlock, err := node.CreateBlock(append([]*types.Transaction(nil), node.mempool...))
	if err != nil {
		t.Fatalf("create plan block: %v", err)
	}
	if err := node.CommitBlock(planBlock); err != nil {
		t.Fatalf("commit plan block: %v", err)
	}

	block, err := node.CreateBlock([]*types.Transaction{selfDealingSubscribeTx(t, key, 1, 1)})
	if err != nil {
		t.Fatalf("CreateBlock must not abort the proposal because of a self-subscription: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("the self-subscription must be excluded from the block, got %d txs", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if _, ok := node.SubscriptionByID(subscriptions.SubscriptionID(1)); ok {
		t.Fatalf("no subscription may exist after the self-subscription was pruned")
	}
}
