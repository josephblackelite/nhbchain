package core

import (
	"encoding/hex"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"nhbchain/config"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/loyalty"
	"nhbchain/native/swap"

	"github.com/ethereum/go-ethereum/common"
)

func TestNodeMempoolConcurrentAdds(t *testing.T) {
	node := newTestNode(t)
	node.SetMempoolUnlimitedOptIn(true)
	node.SetMempoolLimit(0)
	node.SetTransactionSimulationEnabled(false)

	const producers = 2
	const perProducer = 2
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Errorf("generate key: %v", err)
					return
				}
				tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
				if err := node.AddTransaction(tx); err != nil {
					t.Errorf("add transaction: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	txs := node.GetMempool()
	expected := producers * perProducer
	if len(txs) != expected {
		t.Fatalf("expected %d transactions, got %d", expected, len(txs))
	}
}

func TestNodeMempoolLimitEnforcedConcurrently(t *testing.T) {
	node := newTestNode(t)
	const limit = 3
	node.SetMempoolLimit(limit)
	node.SetTransactionSimulationEnabled(false)

	const workers = 2
	const perWorker = 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fullCount int
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Errorf("generate key: %v", err)
					return
				}
				tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
				err = node.AddTransaction(tx)
				if err != nil {
					if !errors.Is(err, ErrMempoolFull) {
						t.Errorf("unexpected error: %v", err)
						return
					}
					mu.Lock()
					fullCount++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	txs := node.GetMempool()
	if len(txs) != limit {
		t.Fatalf("expected %d transactions in mempool, got %d", limit, len(txs))
	}
	if fullCount == 0 {
		t.Fatalf("expected ErrMempoolFull under load")
	}
}

func TestCommitBlockFailureRetainsMempool(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transaction: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction from initial proposal, got %d", len(proposed))
	}

	// Subsequent proposal attempts before the commit result should yield no new transactions.
	if again := node.GetMempool(); len(again) != 0 {
		t.Fatalf("expected no additional transactions while proposal in-flight, got %d", len(again))
	}

	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: node.currentTime().Unix(),
		PrevHash:  node.chain.Tip(),
		Validator: node.validatorKey.PubKey().Address().Bytes(),
		TxRoot:    []byte("mismatch"),
	}
	block := types.NewBlock(header, proposed)
	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected commit failure due to tx root mismatch")
	}

	reproposed := node.GetMempool()
	if len(reproposed) != 1 {
		t.Fatalf("expected transaction to remain after failed commit, got %d", len(reproposed))
	}
	if reproposed[0] != tx {
		t.Fatalf("expected same transaction pointer after failure")
	}
}

func TestCommitBlockSuccessPrunesMempool(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transaction: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction to propose, got %d", len(proposed))
	}

	block, err := node.CreateBlock(proposed)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	if remaining := node.GetMempool(); len(remaining) != 0 {
		t.Fatalf("expected mempool to be empty after successful commit, got %d", len(remaining))
	}
}

func TestCreateBlockCommitBlockSettlesTransferBalances(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	ensureAccountState(t, node, senderKey, 0)
	ensureAccountBytesState(t, node, recipientKey.PubKey().Address().Bytes(), 0, 0)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(100),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction to propose, got %d", len(proposed))
	}

	block, err := node.CreateBlock(proposed)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	senderAccount, err := node.GetAccount(senderKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get sender account: %v", err)
	}
	recipientAccount, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get recipient account: %v", err)
	}

	if senderAccount.BalanceNHB.Cmp(big.NewInt(1_000_000_000_000-100)) != 0 {
		t.Fatalf("unexpected sender NHB balance: got %s", senderAccount.BalanceNHB.String())
	}
	if recipientAccount.BalanceNHB.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("unexpected recipient NHB balance: got %s", recipientAccount.BalanceNHB.String())
	}
	if senderAccount.Nonce != 1 {
		t.Fatalf("expected sender nonce to advance to 1, got %d", senderAccount.Nonce)
	}
	if remaining := node.GetMempool(); len(remaining) != 0 {
		t.Fatalf("expected mempool to be empty after successful transfer commit, got %d", len(remaining))
	}
}

func TestCreateBlockPrunesStaleNonceTransactions(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	staleSenderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate stale sender key: %v", err)
	}
	validSenderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate valid sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	ensureAccountState(t, node, staleSenderKey, 0)
	ensureAccountState(t, node, validSenderKey, 0)
	ensureAccountBytesState(t, node, recipientKey.PubKey().Address().Bytes(), 0, 0)

	staleTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(25),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := staleTx.Sign(staleSenderKey.PrivateKey); err != nil {
		t.Fatalf("sign stale transfer: %v", err)
	}
	if err := node.AddTransaction(staleTx); err != nil {
		t.Fatalf("add stale transfer: %v", err)
	}

	// Advance the sender nonce in state after admission so the queued transaction
	// becomes stale and would previously poison proposer block construction.
	ensureAccountState(t, node, staleSenderKey, 1)

	validTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(75),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := validTx.Sign(validSenderKey.PrivateKey); err != nil {
		t.Fatalf("sign valid transfer: %v", err)
	}
	if err := node.AddTransaction(validTx); err != nil {
		t.Fatalf("add valid transfer: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 2 {
		t.Fatalf("expected 2 transactions in proposal set, got %d", len(proposed))
	}

	block, err := node.CreateBlock(proposed)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if got := len(block.Transactions); got != 1 {
		t.Fatalf("expected 1 transaction after stale nonce pruning, got %d", got)
	}
	if block.Transactions[0] != validTx {
		t.Fatalf("expected valid transaction to remain in block")
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	recipientAccount, err := node.GetAccount(recipientKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get recipient account: %v", err)
	}
	if recipientAccount.BalanceNHB.Cmp(big.NewInt(75)) != 0 {
		t.Fatalf("unexpected recipient NHB balance after stale prune: got %s", recipientAccount.BalanceNHB.String())
	}
	if remaining := node.GetMempool(); len(remaining) != 0 {
		t.Fatalf("expected stale transaction to be pruned from mempool, got %d transactions", len(remaining))
	}
}

func TestCreateBlockCommitBlockSettlesFounderLoyaltyTransfer(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	var treasury [20]byte
	treasury[19] = 0x7a

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	cfg := (&loyalty.GlobalConfig{
		Active:       true,
		Treasury:     treasury[:],
		BaseBps:      50,
		MinSpend:     new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil),
		CapPerTx:     new(big.Int).Mul(big.NewInt(50), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		DailyCapUser: new(big.Int).Mul(big.NewInt(200), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		Dynamic: loyalty.DynamicConfig{
			TargetBps:                      50,
			MinBps:                         25,
			MaxBps:                         100,
			SmoothingStepBps:               5,
			CoverageMaxBps:                 5000,
			CoverageLookbackDays:           7,
			DailyCapPctOf7dFeesBps:         6000,
			DailyCapUsd:                    5000,
			YearlyCapPctOfInitialSupplyBps: 1000,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled:                  true,
				PricePair:                "ZNHB/USD",
				TwapWindowSeconds:        7200,
				MaxDeviationBps:          300,
				PriceMaxAgeSeconds:       600,
				FallbackMinEmissionZNHB:  big.NewInt(0),
				UseLastGoodPriceFallback: true,
			},
		},
	}).Normalize()
	if err := manager.SetLoyaltyGlobalConfig(cfg); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("set loyalty config: %v", err)
	}
	now := time.Unix(node.currentTime().Unix(), 0).UTC()
	tracker := nhbstate.NewRollingFees(manager)
	if err := tracker.AddDay(now, big.NewInt(0), new(big.Int).Mul(big.NewInt(1_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed rolling fees: %v", err)
	}
	if err := manager.SwapPutPriceProof("ZNHB", &swap.PriceProofRecord{Rate: big.NewRat(1, 1), Timestamp: now}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed price proof: %v", err)
	}
	if err := manager.PutAccount(treasury[:], &types.Account{BalanceZNHB: new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)), Stake: big.NewInt(0), BalanceNHB: big.NewInt(0)}); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("put treasury account: %v", err)
	}
	node.stateMu.Unlock()

	setAccountBalanceNHB(t, node, senderKey.PubKey().Address().Bytes(), 0, new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)))
	setAccountBalanceNHB(t, node, recipientKey.PubKey().Address().Bytes(), 0, big.NewInt(0))

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    new(big.Int).Mul(big.NewInt(2), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction to propose, got %d", len(proposed))
	}

	block, err := node.CreateBlock(proposed)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if got := node.state.CurrentRoot().Bytes(); string(got) != string(block.Header.StateRoot) {
		t.Fatalf("state root drifted after commit: got %x want %x", got, block.Header.StateRoot)
	}
	senderAccount, err := node.GetAccount(senderKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("get sender account: %v", err)
	}
	if senderAccount.BalanceZNHB.Sign() <= 0 {
		t.Fatalf("expected sender to receive founder loyalty reward, got %s", senderAccount.BalanceZNHB.String())
	}
}

func TestCreateBlockFailureRequeuesProposedTransactions(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	ensureAccountState(t, node, senderKey, 0)
	ensureAccountBytesState(t, node, recipientKey.PubKey().Address().Bytes(), 0, 0)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(100),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction to propose, got %d", len(proposed))
	}

	setAccountBalanceNHB(t, node, senderKey.PubKey().Address().Bytes(), 0, big.NewInt(0))
	if _, err := node.CreateBlock(proposed); err == nil {
		t.Fatalf("expected create block to fail after sender balance dropped")
	}

	reproposed := node.GetMempool()
	if len(reproposed) != 1 {
		t.Fatalf("expected stranded transaction to be requeued after create-block failure, got %d", len(reproposed))
	}
}

func TestMempoolSizeDoesNotConsumeProposalEligibility(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	ensureAccountState(t, node, senderKey, 0)
	ensureAccountBytesState(t, node, recipientKey.PubKey().Address().Bytes(), 0, 0)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(100),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	if got := node.MempoolSize(); got != 1 {
		t.Fatalf("expected mempool size 1, got %d", got)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected transaction to remain proposal-eligible after size inspection, got %d", len(proposed))
	}
}

func TestHasPendingTransactionHashMatchesMempoolEntries(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	ensureAccountState(t, node, senderKey, 0)
	ensureAccountBytesState(t, node, recipientKey.PubKey().Address().Bytes(), 0, 0)

	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    0,
		To:       append([]byte(nil), recipientKey.PubKey().Address().Bytes()...),
		Value:    big.NewInt(100),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign transfer: %v", err)
	}
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transfer: %v", err)
	}

	hashBytes, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash transfer: %v", err)
	}
	hash := hex.EncodeToString(hashBytes)
	if !node.HasPendingTransactionHash(hash) {
		t.Fatalf("expected pending hash lookup to match canonical hash")
	}
	if !node.HasPendingTransactionHash("0x" + hash) {
		t.Fatalf("expected pending hash lookup to match prefixed hash")
	}
	if node.HasPendingTransactionHash("0xdeadbeef") {
		t.Fatalf("did not expect unrelated hash to match")
	}
}

func TestNodeMempoolByteLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(10)

	keyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	txA := prepareSignedTransaction(t, node, keyA, 0, types.NHBChainID())
	sizeA, err := transactionSize(txA)
	if err != nil {
		t.Fatalf("transaction size: %v", err)
	}
	node.globalCfgMu.Lock()
	node.globalCfg.Mempool.MaxBytes = int64(sizeA)
	node.globalCfgMu.Unlock()

	if err := node.AddTransaction(txA); err != nil {
		t.Fatalf("add first transaction: %v", err)
	}

	keyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	txB := prepareSignedTransaction(t, node, keyB, 0, types.NHBChainID())
	if err := node.AddTransaction(txB); err == nil || !errors.Is(err, ErrMempoolByteLimit) {
		t.Fatalf("expected ErrMempoolByteLimit, got %v", err)
	}
}

func TestNodeMempoolPerSenderLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(mempoolMaxSenderTx + 5)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	for i := 0; i < mempoolMaxSenderTx; i++ {
		tx := prepareSignedTransaction(t, node, senderKey, uint64(i), types.NHBChainID())
		if err := node.AddTransaction(tx); err != nil {
			t.Fatalf("unexpected add error %d: %v", i, err)
		}
	}
	blockingTx := prepareSignedTransaction(t, node, senderKey, uint64(mempoolMaxSenderTx), types.NHBChainID())
	if err := node.AddTransaction(blockingTx); err == nil || !errors.Is(err, ErrMempoolSenderLimit) {
		t.Fatalf("expected ErrMempoolSenderLimit, got %v", err)
	}
}

func TestNodeMempoolQuotaLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(10)
	node.globalCfgMu.Lock()
	node.globalCfg.Quotas.Trade = config.Quota{MaxRequestsPerMin: 1, EpochSeconds: 86400}
	node.globalCfgMu.Unlock()

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	first := prepareSignedTransaction(t, node, senderKey, 0, types.NHBChainID())
	if err := node.AddTransaction(first); err != nil {
		t.Fatalf("add first transaction: %v", err)
	}
	second := prepareSignedTransaction(t, node, senderKey, 1, types.NHBChainID())
	if err := node.AddTransaction(second); err == nil || !errors.Is(err, ErrMempoolQuotaExceeded) {
		t.Fatalf("expected ErrMempoolQuotaExceeded, got %v", err)
	}
}

func prepareSignedTransaction(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64, chainID *big.Int) *types.Transaction {
	t.Helper()
	ensureAccountState(t, node, key, nonce)
	if chainID == nil {
		chainID = types.NHBChainID()
	}
	tx := &types.Transaction{
		ChainID: chainID,
		Type:    types.TxTypeHeartbeat,
		Nonce:   nonce,
		Value:   big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

func ensureAccountState(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64) {
	t.Helper()
	ensureAccountBytesState(t, node, key.PubKey().Address().Bytes(), nonce, 1_000_000_000_000)
}

func ensureAccountBytesState(t *testing.T, node *Node, addr []byte, nonce uint64, balanceNHB int64) {
	t.Helper()
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	account := &types.Account{
		Nonce:      nonce,
		BalanceNHB: big.NewInt(balanceNHB),
	}
	if err := manager.PutAccount(addr, account); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("put account: %v", err)
	}
	root, err := node.state.Commit(node.chain.GetHeight())
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("commit seeded account state: %v", err)
	}
	commitStateAsEmptyBlock(t, node, root)
}

func setAccountBalanceNHB(t *testing.T, node *Node, addr []byte, nonce uint64, balanceNHB *big.Int) {
	t.Helper()
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	account := &types.Account{
		Nonce:      nonce,
		BalanceNHB: new(big.Int).Set(balanceNHB),
	}
	if err := manager.PutAccount(addr, account); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("put account: %v", err)
	}
	root, err := node.state.Commit(node.chain.GetHeight())
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("commit seeded account state: %v", err)
	}
	commitStateAsEmptyBlock(t, node, root)
}

func commitStateAsEmptyBlock(t *testing.T, node *Node, root common.Hash) {
	t.Helper()
	txRoot, err := ComputeTxRoot(nil)
	if err != nil {
		t.Fatalf("compute empty tx root: %v", err)
	}
	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: node.currentTime().Unix(),
		PrevHash:  node.chain.Tip(),
		StateRoot: root.Bytes(),
		TxRoot:    txRoot,
		Validator: node.validatorKey.PubKey().Address().Bytes(),
	}
	if err := node.chain.AddBlock(types.NewBlock(header, nil)); err != nil {
		t.Fatalf("commit seeded empty block: %v", err)
	}
}
