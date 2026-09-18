package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"
	"nhbchain/storage"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// TestSwapAdminTxTypeByteValues pins down the two new TxType byte values so
// a future merge can never silently reassign or collide them, mirroring
// TestSwapVoucherMintTxTypeByteValue's precedent.
func TestSwapAdminTxTypeByteValues(t *testing.T) {
	if types.TxTypeSwapVoucherReverse != 0x4A {
		t.Fatalf("expected TxTypeSwapVoucherReverse == 0x4A, got 0x%02X", byte(types.TxTypeSwapVoucherReverse))
	}
	if types.TxTypeSwapMarkReconciled != 0x4B {
		t.Fatalf("expected TxTypeSwapMarkReconciled == 0x4B, got 0x%02X", byte(types.TxTypeSwapMarkReconciled))
	}
	if types.TxTypeSwapVoucherReverse == types.TxTypeSwapMarkReconciled {
		t.Fatalf("TxTypeSwapVoucherReverse must not collide with TxTypeSwapMarkReconciled")
	}
	// Senderless, like TxTypeSwapVoucherMint -- see core/types/transaction.go's
	// doc comment for why (the RoleSwapAdmin-holding key is not this node's
	// own, so authorization is via an embedded signature, not tx.From()).
	if types.RequiresSignature(types.TxTypeSwapVoucherReverse) {
		t.Fatalf("expected TxTypeSwapVoucherReverse to be senderless (RequiresSignature=false)")
	}
	if types.RequiresSignature(types.TxTypeSwapMarkReconciled) {
		t.Fatalf("expected TxTypeSwapMarkReconciled to be senderless (RequiresSignature=false)")
	}
}

// writeSwapAdminGenesis writes a fixed, explicit genesis spec (never
// autogenesis) to a temp file and returns its path. Two nodes built from
// the SAME returned path start from a byte-identical trie -- required for
// the state-root equality assertions below, mirroring
// TestStakeClaimRewardsBlock_ProposerAndValidatorAgree's identical
// reasoning (core/staking_claim_rewards_consensus_test.go).
func writeSwapAdminGenesis(t *testing.T) string {
	t.Helper()
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
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(genesisPath, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}
	return genesisPath
}

// buildSwapAdminTestNode builds one node against genesisPath -- call it
// twice with the SAME path to get two independently-constructed nodes that
// nonetheless start from byte-identical state.
func buildSwapAdminTestNode(t *testing.T, genesisPath string) *Node {
	t.Helper()
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
	return node
}

// seedSwapAdminVoucher performs the identical, deterministic sequence of
// direct trie writes needed to exercise a reversal/reconciliation: register
// ZNHB, grant RoleSwapAdmin to adminAddr, record a minted voucher, and fund
// the recipient's balance. Calling it with the exact same arguments on two
// independently-built nodes (see buildSwapAdminTestNode) keeps their
// resulting tries byte-identical going into the transaction under test.
func seedSwapAdminVoucher(t *testing.T, node *Node, adminAddr, recipient [20]byte, providerTxID string, amount *big.Int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	if meta, err := manager.Token("ZNHB"); err != nil {
		t.Fatalf("load token: %v", err)
	} else if meta == nil {
		if err := manager.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
			t.Fatalf("register token: %v", err)
		}
	}
	if err := manager.SetRole(RoleSwapAdmin, adminAddr[:]); err != nil {
		t.Fatalf("grant RoleSwapAdmin: %v", err)
	}
	ledger := swap.NewLedger(manager)
	ledger.SetClock(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })
	record := &swap.VoucherRecord{
		Provider:      "nowpayments",
		ProviderTxID:  providerTxID,
		Token:         "ZNHB",
		MintAmountWei: new(big.Int).Set(amount),
		Recipient:     recipient,
		Status:        swap.VoucherStatusMinted,
	}
	if err := ledger.Put(record); err != nil {
		t.Fatalf("seed voucher record: %v", err)
	}
	if err := manager.SetBalance(recipient[:], "ZNHB", new(big.Int).Set(amount)); err != nil {
		t.Fatalf("seed recipient balance: %v", err)
	}
}

// TestSwapVoucherReverseApply_IndependentStatesAgree is the direct
// regression test for the consensus-safety property this fix establishes:
// applying the exact same TxTypeSwapVoucherReverse transaction against two
// independently-constructed StateProcessor instances (seeded identically,
// never sharing memory) must produce byte-identical resulting state roots.
// Before this fix, SwapReverseVoucher mutated only the handling validator's
// own local trie directly under n.stateMu.Lock(), completely outside
// ApplyTransaction -- a different validator that never independently
// received/replayed that exact RPC call would never apply the reversal at
// all, a real fork risk (NHB-AUDIT-C4 follow-up).
func TestSwapVoucherReverseApply_IndependentStatesAgree(t *testing.T) {
	genesisPath := writeSwapAdminGenesis(t)
	nodeA := buildSwapAdminTestNode(t, genesisPath)
	nodeB := buildSwapAdminTestNode(t, genesisPath)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	// The refund sink defaults to each node's own genesis-declared
	// treasury/admin wallet -- which itself falls back to each node's own
	// (different, randomly generated per test node) validator address when
	// genesis declares no admin wallet, exactly as production does (see
	// NewNode's treasury fallback). Since this genesis spec deliberately
	// declares none (this test needs nothing else admin-wallet-related),
	// nodeA/nodeB's default sinks would otherwise differ -- pin them to the
	// SAME address explicitly, the same way a real network's genesis
	// AdminWallet keeps every validator's default in agreement.
	sinkKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sink key: %v", err)
	}
	var sinkAddr [20]byte
	copy(sinkAddr[:], sinkKey.PubKey().Address().Bytes())
	nodeA.SetSwapRefundSink(sinkAddr)
	nodeB.SetSwapRefundSink(sinkAddr)

	providerTxID := "order-determinism-reverse-1"
	amount := big.NewInt(1_000_000)

	seedSwapAdminVoucher(t, nodeA, adminAddr, recipient, providerTxID, amount)
	seedSwapAdminVoucher(t, nodeB, adminAddr, recipient, providerTxID, amount)

	preRootA := nodeA.state.PendingRoot()
	preRootB := nodeB.state.PendingRoot()
	if preRootA != preRootB {
		t.Fatalf("expected identical starting roots after identical seeding, got %s vs %s", preRootA, preRootB)
	}

	signature, err := ethcrypto.Sign(SwapVoucherReverseSigningHash(providerTxID), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reversal: %v", err)
	}
	payload, err := encodeSwapVoucherReverseTransaction(providerTxID, signature)
	if err != nil {
		t.Fatalf("encode reversal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapVoucherReverse,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		applyErr := node.state.ApplyTransaction(tx)
		node.stateMu.Unlock()
		if applyErr != nil {
			t.Fatalf("apply reversal transaction: %v", applyErr)
		}
	}

	postRootA := nodeA.state.PendingRoot()
	postRootB := nodeB.state.PendingRoot()
	if postRootA != postRootB {
		t.Fatalf("state roots diverged after applying the identical reversal transaction: %s vs %s", postRootA, postRootB)
	}
	if postRootA == preRootA {
		t.Fatalf("expected the reversal to actually change state -- root did not move")
	}

	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		manager := nhbstate.NewManager(node.state.Trie)
		ledger := swap.NewLedger(manager)
		record, ok, getErr := ledger.Get(providerTxID)
		balance, balErr := manager.Balance(recipient[:], "ZNHB")
		node.stateMu.Unlock()
		if getErr != nil || !ok {
			t.Fatalf("expected voucher record to exist: ok=%v err=%v", ok, getErr)
		}
		if record.Status != swap.VoucherStatusReversed {
			t.Fatalf("expected reversed status, got %s", record.Status)
		}
		if balErr != nil {
			t.Fatalf("read balance: %v", balErr)
		}
		if balance.Sign() != 0 {
			t.Fatalf("expected recipient balance zero after reversal, got %s", balance)
		}
	}
}

// TestSwapMarkReconciledApply_IndependentStatesAgree is
// TestSwapVoucherReverseApply_IndependentStatesAgree's counterpart for
// TxTypeSwapMarkReconciled.
func TestSwapMarkReconciledApply_IndependentStatesAgree(t *testing.T) {
	genesisPath := writeSwapAdminGenesis(t)
	nodeA := buildSwapAdminTestNode(t, genesisPath)
	nodeB := buildSwapAdminTestNode(t, genesisPath)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)

	providerTxID := "order-determinism-reconcile-1"
	amount := big.NewInt(500)

	seedSwapAdminVoucher(t, nodeA, adminAddr, recipient, providerTxID, amount)
	seedSwapAdminVoucher(t, nodeB, adminAddr, recipient, providerTxID, amount)

	preRootA := nodeA.state.PendingRoot()
	preRootB := nodeB.state.PendingRoot()
	if preRootA != preRootB {
		t.Fatalf("expected identical starting roots after identical seeding, got %s vs %s", preRootA, preRootB)
	}

	ids := []string{providerTxID}
	signature, err := ethcrypto.Sign(SwapMarkReconciledSigningHash(ids), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reconciliation: %v", err)
	}
	payload, err := encodeSwapMarkReconciledTransaction(ids, signature)
	if err != nil {
		t.Fatalf("encode reconciliation: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapMarkReconciled,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		applyErr := node.state.ApplyTransaction(tx)
		node.stateMu.Unlock()
		if applyErr != nil {
			t.Fatalf("apply reconciliation transaction: %v", applyErr)
		}
	}

	postRootA := nodeA.state.PendingRoot()
	postRootB := nodeB.state.PendingRoot()
	if postRootA != postRootB {
		t.Fatalf("state roots diverged after applying the identical reconciliation transaction: %s vs %s", postRootA, postRootB)
	}
	if postRootA == preRootA {
		t.Fatalf("expected the reconciliation to actually change state -- root did not move")
	}

	for _, node := range []*Node{nodeA, nodeB} {
		node.stateMu.Lock()
		manager := nhbstate.NewManager(node.state.Trie)
		ledger := swap.NewLedger(manager)
		record, ok, getErr := ledger.Get(providerTxID)
		node.stateMu.Unlock()
		if getErr != nil || !ok {
			t.Fatalf("expected voucher record to exist: ok=%v err=%v", ok, getErr)
		}
		if record.Status != swap.VoucherStatusReconciled {
			t.Fatalf("expected reconciled status, got %s", record.Status)
		}
	}
}

// TestSwapVoucherReverseApply_RejectsNonAdminSignerAtApplyTime is the
// direct regression test for the authorization half of this fix: the
// RoleSwapAdmin check must be enforced by
// applySwapVoucherReverseTransaction itself -- reachable from
// StateProcessor.ApplyTransaction with zero HTTP/RPC layer involved -- not
// merely by the old RPC handler's bearer-auth middleware. A transaction
// signed by a key that was never granted RoleSwapAdmin must be rejected
// deterministically, before it ever touches the voucher ledger or moves
// any balance.
func TestSwapVoucherReverseApply_RejectsNonAdminSignerAtApplyTime(t *testing.T) {
	node := newTestNode(t)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)
	providerTxID := "order-unauthorized-apply"
	amount := big.NewInt(750)

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("register token: %v", err)
	}
	ledger := swap.NewLedger(manager)
	record := &swap.VoucherRecord{
		Provider:      "nowpayments",
		ProviderTxID:  providerTxID,
		Token:         "ZNHB",
		MintAmountWei: new(big.Int).Set(amount),
		Recipient:     recipient,
		Status:        swap.VoucherStatusMinted,
	}
	if err := ledger.Put(record); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed voucher record: %v", err)
	}
	if err := manager.SetBalance(recipient[:], "ZNHB", new(big.Int).Set(amount)); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed recipient balance: %v", err)
	}
	node.stateMu.Unlock()

	// rogueKey is never granted RoleSwapAdmin.
	rogueKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate rogue key: %v", err)
	}
	signature, err := ethcrypto.Sign(SwapVoucherReverseSigningHash(providerTxID), rogueKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reversal: %v", err)
	}
	payload, err := encodeSwapVoucherReverseTransaction(providerTxID, signature)
	if err != nil {
		t.Fatalf("encode reversal: %v", err)
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeSwapVoucherReverse,
		Data:     payload,
		GasLimit: 0,
		GasPrice: big.NewInt(0),
	}

	node.stateMu.Lock()
	applyErr := node.state.ApplyTransaction(tx)
	node.stateMu.Unlock()
	if !errors.Is(applyErr, ErrSwapAdminUnauthorized) {
		t.Fatalf("SECURITY REGRESSION: expected ErrSwapAdminUnauthorized for a non-admin signer, got %v", applyErr)
	}

	// Confirm the rejected transaction had zero observable side effect.
	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	ledger2 := swap.NewLedger(manager2)
	after, _, getErr := ledger2.Get(providerTxID)
	balance, balErr := manager2.Balance(recipient[:], "ZNHB")
	node.stateMu.Unlock()
	if getErr != nil {
		t.Fatalf("read voucher record: %v", getErr)
	}
	if after.Status != swap.VoucherStatusMinted {
		t.Fatalf("expected voucher status unchanged (still minted), got %s", after.Status)
	}
	if balErr != nil {
		t.Fatalf("read balance: %v", balErr)
	}
	if balance.Cmp(amount) != 0 {
		t.Fatalf("expected recipient balance unchanged at %s, got %s", amount, balance)
	}
}

// TestSwapReverseVoucherNoLongerMutatesSynchronously is the direct
// regression test proving the old NHB-AUDIT-C4 direct-mutation code path is
// actually gone: Node.SwapReverseVoucher only enqueues a transaction now
// (via AddTransaction) and must have NO observable effect on committed
// state until a block actually applies that transaction -- the exact
// opposite of the retired behaviour, which mutated n.state.Trie directly
// and synchronously, under n.stateMu.Lock(), before this method ever
// returned.
func TestSwapReverseVoucherNoLongerMutatesSynchronously(t *testing.T) {
	node := newTestNode(t)

	adminKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	var adminAddr [20]byte
	copy(adminAddr[:], adminKey.PubKey().Address().Bytes())
	assignRole(t, node, RoleSwapAdmin, adminAddr)

	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	recipient := toAddress(recipientKey)
	providerTxID := "order-sync-mutation-check"
	amount := big.NewInt(4_200)

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.RegisterToken("ZNHB", "Zero NHB", 18); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("register token: %v", err)
	}
	ledger := swap.NewLedger(manager)
	record := &swap.VoucherRecord{
		Provider:      "nowpayments",
		ProviderTxID:  providerTxID,
		Token:         "ZNHB",
		MintAmountWei: new(big.Int).Set(amount),
		Recipient:     recipient,
		Status:        swap.VoucherStatusMinted,
	}
	if err := ledger.Put(record); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed voucher record: %v", err)
	}
	if err := manager.SetBalance(recipient[:], "ZNHB", new(big.Int).Set(amount)); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed recipient balance: %v", err)
	}
	node.stateMu.Unlock()

	signature, err := ethcrypto.Sign(SwapVoucherReverseSigningHash(providerTxID), adminKey.PrivateKey)
	if err != nil {
		t.Fatalf("sign reversal: %v", err)
	}

	// This call used to mutate n.state.Trie directly, synchronously,
	// before returning. Today it must only submit a transaction.
	txHash, err := node.SwapReverseVoucher(providerTxID, signature)
	if err != nil {
		t.Fatalf("submit reversal: %v", err)
	}
	if txHash == "" {
		t.Fatalf("expected a transaction hash")
	}

	// The old direct-mutation path is gone: committed state must be
	// UNTOUCHED at this point -- still minted, balance still intact.
	node.stateMu.Lock()
	manager2 := nhbstate.NewManager(node.state.Trie)
	ledger2 := swap.NewLedger(manager2)
	preCommit, _, getErr := ledger2.Get(providerTxID)
	preBalance, balErr := manager2.Balance(recipient[:], "ZNHB")
	node.stateMu.Unlock()
	if getErr != nil {
		t.Fatalf("read voucher record: %v", getErr)
	}
	if preCommit.Status != swap.VoucherStatusMinted {
		t.Fatalf("REGRESSION: SwapReverseVoucher mutated state synchronously -- status is %s before any block committed", preCommit.Status)
	}
	if balErr != nil {
		t.Fatalf("read balance: %v", balErr)
	}
	if preBalance.Cmp(amount) != 0 {
		t.Fatalf("REGRESSION: SwapReverseVoucher mutated state synchronously -- balance is %s before any block committed", preBalance)
	}

	pending := node.GetMempool()
	if len(pending) != 1 {
		t.Fatalf("expected exactly 1 enqueued transaction, got %d", len(pending))
	}
	block, err := node.CreateBlock(pending)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	// Only NOW, once a real block has applied the transaction through
	// ApplyTransaction, must the reversal actually take effect.
	node.stateMu.Lock()
	manager3 := nhbstate.NewManager(node.state.Trie)
	ledger3 := swap.NewLedger(manager3)
	postCommit, _, getErr2 := ledger3.Get(providerTxID)
	postBalance, balErr2 := manager3.Balance(recipient[:], "ZNHB")
	node.stateMu.Unlock()
	if getErr2 != nil {
		t.Fatalf("read voucher record: %v", getErr2)
	}
	if postCommit.Status != swap.VoucherStatusReversed {
		t.Fatalf("expected reversed status after commit, got %s", postCommit.Status)
	}
	if balErr2 != nil {
		t.Fatalf("read balance: %v", balErr2)
	}
	if postBalance.Sign() != 0 {
		t.Fatalf("expected recipient balance zero after commit, got %s", postBalance)
	}
}
