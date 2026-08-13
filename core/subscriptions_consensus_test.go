package core

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/subscriptions"
	"nhbchain/storage"
)

// TestSubscriptionsCreatePlanSubscribeAndCharge_ProposerAndValidatorAgree
// drives the real CreateBlock/ValidateBlock/CommitBlock production code
// paths for a block containing a real, validly-signed
// TxTypeSubscriptionCreatePlan followed by a TxTypeSubscriptionSubscribe --
// proving two independently constructed nodes derive the same state root,
// the same regression shape as the governance/POTSO-stake/CreatePool tests.
//
// The plan has a zero trial period, so the subscription's very first charge
// is due "today" -- meaning settleSubscriptionCharges (called from
// ProcessBlockLifecycle at the end of this same block's execution) fires
// within the SAME block as the Subscribe transaction, with zero further
// signature from the payer. This is the direct regression test for this
// module's entire safety argument: the payer's one signature on Subscribe
// is enough to authorize the chain debiting them, deterministically and
// identically, on every independently-constructed validator.
func TestSubscriptionsCreatePlanSubscribeAndCharge_ProposerAndValidatorAgree(t *testing.T) {
	merchantKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate merchant key: %v", err)
	}
	payerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate payer key: %v", err)
	}
	merchantAddr := toAddress(merchantKey)
	payerAddr := toAddress(payerKey)
	payerAddrStr := payerKey.PubKey().Address().String()

	genesisValidatorKeyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key A: %v", err)
	}
	genesisValidatorKeyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate genesis validator key B: %v", err)
	}
	spec := genesis.GenesisSpec{
		GenesisTime:  "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{{Symbol: "NHB", Name: "NHBCoin", Decimals: 18}, {Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18}},
		Validators: []genesis.ValidatorSpec{
			{Address: genesisValidatorKeyA.PubKey().Address().String(), Power: 11440},
			{Address: genesisValidatorKeyB.PubKey().Address().String(), Power: 11336},
		},
		// Payer's NHB balance must come from genesis, not a direct
		// post-construction trie write -- see potso_stake_consensus_test.go's
		// comment on resetDriftUnlessSelfProposedLocked for why.
		Alloc: map[string]map[string]string{
			payerAddrStr: {"NHB": "1000", "ZNHB": "0"},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}

	build := func() *Node {
		db := storage.NewMemDB()
		t.Cleanup(func() { db.Close() })
		validatorKey, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate node validator key: %v", err)
		}
		node, err := NewNode(db, validatorKey, genesisPath, false, false)
		if err != nil {
			t.Fatalf("new node: %v", err)
		}
		// ManagementFeeBps: 0 keeps this test focused on the core
		// mandate/settlement mechanism without also needing to fund and
		// verify a treasury account -- the fee math itself is covered by
		// native/subscriptions' own unit tests.
		if err := node.SetSubscriptionsConfig(subscriptions.Config{
			MaxRetries:           3,
			RetryIntervalSeconds: 86400,
		}); err != nil {
			t.Fatalf("configure subscriptions engine: %v", err)
		}
		return node
	}
	proposer := build()
	validator := build()

	createPlanData, err := rlp.EncodeToBytes(struct {
		Name               string
		PriceWei           *big.Int
		Asset              string
		IntervalSeconds    uint64
		TrialPeriodSeconds uint64
	}{
		Name:            "Pro Monthly",
		PriceWei:        big.NewInt(100),
		Asset:           "NHB",
		IntervalSeconds: 86400,
	})
	if err != nil {
		t.Fatalf("encode create-plan payload: %v", err)
	}
	createPlanTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSubscriptionCreatePlan,
		Nonce:    0,
		Data:     createPlanData,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := createPlanTx.Sign(merchantKey.PrivateKey); err != nil {
		t.Fatalf("sign create-plan tx: %v", err)
	}
	if err := proposer.AddTransaction(createPlanTx); err != nil {
		t.Fatalf("add create-plan tx: %v", err)
	}

	// CreatePlan settles in its own block first -- AddTransaction validates
	// a new mempool entry against currently COMMITTED state, so a
	// Subscribe referencing this plan cannot be admitted to the mempool
	// alongside CreatePlan in the same batch; the plan must actually be
	// committed first. This also gives clean cross-block regression
	// coverage: the second block's Subscribe reads a plan that landed in
	// an earlier, already-committed block.
	planBlock, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create plan block: %v", err)
	}
	if len(planBlock.Transactions) != 1 {
		t.Fatalf("expected the create-plan tx to survive into the proposed block, got %d txs", len(planBlock.Transactions))
	}
	if err := validator.ValidateBlock(planBlock); err != nil {
		t.Fatalf("validator rejected proposer's plan block: %v", err)
	}
	if err := proposer.CommitBlock(planBlock); err != nil {
		t.Fatalf("proposer commit plan block: %v", err)
	}
	if err := validator.CommitBlock(planBlock); err != nil {
		t.Fatalf("validator commit plan block: %v", err)
	}

	// The first assigned PlanID is always 1 (state.Manager's
	// SubscriptionsNextPlanID starts its counter at 1) -- deterministic,
	// so the Subscribe payload below can reference it directly without
	// needing to first query the chain for the ID CreatePlan will assign.
	subscribeData, err := rlp.EncodeToBytes(struct{ PlanID uint64 }{PlanID: 1})
	if err != nil {
		t.Fatalf("encode subscribe payload: %v", err)
	}
	subscribeTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSubscriptionSubscribe,
		Nonce:    0,
		Data:     subscribeData,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := subscribeTx.Sign(payerKey.PrivateKey); err != nil {
		t.Fatalf("sign subscribe tx: %v", err)
	}
	if err := proposer.AddTransaction(subscribeTx); err != nil {
		t.Fatalf("add subscribe tx: %v", err)
	}

	subBlock, err := proposer.CreateBlock(append([]*types.Transaction(nil), proposer.mempool...))
	if err != nil {
		t.Fatalf("create subscribe block: %v", err)
	}
	if len(subBlock.Transactions) != 1 {
		t.Fatalf("expected the subscribe tx to survive into the proposed block, got %d txs", len(subBlock.Transactions))
	}
	if err := validator.ValidateBlock(subBlock); err != nil {
		t.Fatalf("validator rejected proposer's subscribe block: %v", err)
	}
	if err := proposer.CommitBlock(subBlock); err != nil {
		t.Fatalf("proposer commit subscribe block: %v", err)
	}
	if err := validator.CommitBlock(subBlock); err != nil {
		t.Fatalf("validator commit subscribe block: %v", err)
	}

	// ValidateBlock/CommitBlock above already fail loudly on any
	// state-root mismatch -- this final check confirms the plan (from the
	// first block), the subscription, and its same-block first charge
	// (both from the second block) all landed identically on both nodes.
	for _, node := range []*Node{proposer, validator} {
		plan, ok := node.SubscriptionPlanByID(subscriptions.PlanID(1))
		if !ok {
			t.Fatalf("plan 1 not found")
		}
		if plan.Merchant != merchantAddr {
			t.Fatalf("plan merchant mismatch")
		}
		if plan.PriceWei.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("plan price = %s, want 100", plan.PriceWei)
		}

		sub, ok := node.SubscriptionByID(subscriptions.SubscriptionID(1))
		if !ok {
			t.Fatalf("subscription 1 not found")
		}
		if sub.Payer != payerAddr || sub.Merchant != merchantAddr {
			t.Fatalf("subscription payer/merchant mismatch")
		}
		if sub.Status != subscriptions.SubscriptionStatusActive {
			t.Fatalf("subscription status = %v, want active", sub.Status)
		}
		if sub.CycleCount != 1 {
			t.Fatalf("subscription cycle count = %d, want 1 (same-block first charge)", sub.CycleCount)
		}
		wantNextChargeAt := uint64(subBlock.Header.Timestamp) + 86400
		if sub.NextChargeAt != wantNextChargeAt {
			t.Fatalf("next charge at = %d, want %d", sub.NextChargeAt, wantNextChargeAt)
		}

		charges, err := node.SubscriptionCharges(subscriptions.SubscriptionID(1))
		if err != nil {
			t.Fatalf("list charges: %v", err)
		}
		if len(charges) != 1 {
			t.Fatalf("expected exactly one charge, got %d", len(charges))
		}
		if charges[0].Status != subscriptions.ChargeStatusPaid {
			t.Fatalf("charge status = %v, want paid", charges[0].Status)
		}
		if charges[0].AmountWei.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("charge amount = %s, want 100", charges[0].AmountWei)
		}

		merchantAcc, err := node.GetAccount(merchantAddr[:])
		if err != nil {
			t.Fatalf("load merchant account: %v", err)
		}
		if merchantAcc.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("merchant NHB balance = %s, want 100", merchantAcc.BalanceNHB)
		}

		payerAcc, err := node.GetAccount(payerAddr[:])
		if err != nil {
			t.Fatalf("load payer account: %v", err)
		}
		if payerAcc.BalanceNHB.Cmp(big.NewInt(900)) != 0 {
			t.Fatalf("payer NHB balance = %s, want 900", payerAcc.BalanceNHB)
		}
	}
}
