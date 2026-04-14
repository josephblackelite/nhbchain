package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/p2p"
	"nhbchain/storage"
)

type testBroadcaster struct {
	messages []*p2p.Message
}

func (b *testBroadcaster) Broadcast(msg *p2p.Message) error {
	b.messages = append(b.messages, msg)
	return nil
}

func TestProcessNetworkMessageRejectsInvalidTransaction(t *testing.T) {
	node := newTestNode(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	invalidChainID := big.NewInt(0xdead)
	tx := prepareSignedTransaction(t, node, senderKey, 0, invalidChainID)
	payload, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	msg := &p2p.Message{Type: p2p.MsgTypeTx, Payload: payload}
	err = node.ProcessNetworkMessage(msg)
	if err == nil {
		t.Fatalf("expected invalid payload error")
	}
	if !errors.Is(err, p2p.ErrInvalidPayload) {
		t.Fatalf("expected ErrInvalidPayload, got %v", err)
	}

	if mempool := node.GetMempool(); len(mempool) != 0 {
		t.Fatalf("expected empty mempool, got %d", len(mempool))
	}

	if _, err := node.CreateBlock(nil); err != nil {
		t.Fatalf("create block after invalid tx: %v", err)
	}
}

func TestGetAccountConcurrentWithCommit(t *testing.T) {
	node := newTestNode(t)

	address := node.validatorKey.PubKey().Address().Bytes()
	const commitRounds = 5

	errCh := make(chan error, 2)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < commitRounds; i++ {
			block, err := node.CreateBlock(nil)
			if err != nil {
				errCh <- fmt.Errorf("create block: %w", err)
				return
			}
			if err := node.CommitBlock(block); err != nil {
				errCh <- fmt.Errorf("commit block: %w", err)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := node.GetAccount(address); err != nil {
				errCh <- fmt.Errorf("get account: %w", err)
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent access failed: %v", err)
		}
	}
}

func TestProcessNetworkMessageGetStatusBroadcastsStatus(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	if err := node.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeGetStatus, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("process get status: %v", err)
	}
	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected 1 status broadcast, got %d", len(broadcaster.messages))
	}
	if broadcaster.messages[0].Type != p2p.MsgTypeStatus {
		t.Fatalf("expected status message, got %d", broadcaster.messages[0].Type)
	}
}

func TestProcessNetworkMessageGetBlocksBroadcastsChunk(t *testing.T) {
	node := newTestNode(t)
	broadcaster := &testBroadcaster{}
	node.SetNetworkBroadcaster(broadcaster)

	for i := 0; i < 2; i++ {
		block, err := node.CreateBlock(nil)
		if err != nil {
			t.Fatalf("create block %d: %v", i, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("commit block %d: %v", i, err)
		}
	}

	payload, err := json.Marshal(p2p.GetBlocksPayload{From: 1})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := node.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeGetBlocks, Payload: payload}); err != nil {
		t.Fatalf("process get blocks: %v", err)
	}
	if len(broadcaster.messages) != 1 {
		t.Fatalf("expected 1 blocks broadcast, got %d", len(broadcaster.messages))
	}
	if broadcaster.messages[0].Type != p2p.MsgTypeBlocks {
		t.Fatalf("expected blocks message, got %d", broadcaster.messages[0].Type)
	}
}

func TestProcessNetworkMessageBlocksCatchUpCommitsMissingBlocks(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	sourceDB := storage.NewMemDB()
	t.Cleanup(func() { sourceDB.Close() })
	source, err := NewNode(sourceDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new source node: %v", err)
	}

	targetDB := storage.NewMemDB()
	t.Cleanup(func() { targetDB.Close() })
	target, err := NewNode(targetDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new target node: %v", err)
	}
	target.SetNetworkBroadcaster(&testBroadcaster{})

	var blocks []*types.Block
	for i := 0; i < 2; i++ {
		block, err := source.CreateBlock(nil)
		if err != nil {
			t.Fatalf("source create block %d: %v", i, err)
		}
		if err := source.CommitBlock(block); err != nil {
			t.Fatalf("source commit block %d: %v", i, err)
		}
		blocks = append(blocks, block)
	}

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: blocks})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}
	if err := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload}); err != nil {
		t.Fatalf("process blocks: %v", err)
	}
	if got := target.GetHeight(); got != source.GetHeight() {
		t.Fatalf("expected target height %d, got %d", source.GetHeight(), got)
	}
}

func TestProcessNetworkMessageBlocksCatchUpAllowsHistoricalTimestamps(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	sourceDB := storage.NewMemDB()
	t.Cleanup(func() { sourceDB.Close() })
	source, err := NewNode(sourceDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new source node: %v", err)
	}
	baseTime := time.Unix(1_776_115_000, 0).UTC()
	source.SetTimeSource(func() time.Time { return baseTime })

	targetDB := storage.NewMemDB()
	t.Cleanup(func() { targetDB.Close() })
	target, err := NewNode(targetDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new target node: %v", err)
	}
	target.SetNetworkBroadcaster(&testBroadcaster{})
	target.SetTimeSource(func() time.Time { return baseTime.Add(30 * time.Minute) })

	var blocks []*types.Block
	for i := 0; i < 2; i++ {
		block, err := source.CreateBlock(nil)
		if err != nil {
			t.Fatalf("source create block %d: %v", i, err)
		}
		if err := source.CommitBlock(block); err != nil {
			t.Fatalf("source commit block %d: %v", i, err)
		}
		blocks = append(blocks, block)
	}

	payload, err := json.Marshal(p2p.BlocksPayload{Blocks: blocks})
	if err != nil {
		t.Fatalf("marshal blocks payload: %v", err)
	}
	if err := target.ProcessNetworkMessage(&p2p.Message{Type: p2p.MsgTypeBlocks, Payload: payload}); err != nil {
		t.Fatalf("process blocks: %v", err)
	}
	if got := target.GetHeight(); got != source.GetHeight() {
		t.Fatalf("expected target height %d, got %d", source.GetHeight(), got)
	}
}

func TestValidateBlockAllowsPeerClockSkewWhenTimestampIsMonotonic(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	sourceDB := storage.NewMemDB()
	t.Cleanup(func() { sourceDB.Close() })
	source, err := NewNode(sourceDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new source node: %v", err)
	}

	targetDB := storage.NewMemDB()
	t.Cleanup(func() { targetDB.Close() })
	target, err := NewNode(targetDB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new target node: %v", err)
	}

	baseTime := time.Unix(1_776_121_660, 0).UTC()
	source.SetTimeSource(func() time.Time { return baseTime.Add(7 * time.Second) })
	target.SetTimeSource(func() time.Time { return baseTime.Add(30 * time.Second) })

	block, err := source.CreateBlock(nil)
	if err != nil {
		t.Fatalf("source create block: %v", err)
	}
	if err := target.ValidateBlock(block); err != nil {
		t.Fatalf("expected target to accept monotonic peer block despite local clock skew: %v", err)
	}
}

func TestCommitBlockDuplicateCommittedBlockIsIdempotent(t *testing.T) {
	node := newTestNode(t)

	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("duplicate commit should be idempotent, got %v", err)
	}
	if got := node.GetHeight(); got != 1 {
		t.Fatalf("expected height 1 after duplicate commit, got %d", got)
	}
}

func TestSyncStakingParamsUsesDeterministicReferenceTime(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	dbA := storage.NewMemDB()
	t.Cleanup(func() { dbA.Close() })
	nodeA, err := NewNode(dbA, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node A: %v", err)
	}
	dbB := storage.NewMemDB()
	t.Cleanup(func() { dbB.Close() })
	nodeB, err := NewNode(dbB, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node B: %v", err)
	}

	baseTime := time.Unix(1_776_121_660, 0).UTC()
	nodeA.SetTimeSource(func() time.Time { return baseTime.Add(7 * time.Second) })
	nodeB.SetTimeSource(func() time.Time { return baseTime.Add(37 * time.Second) })

	nodeA.stateMu.Lock()
	managerA := nhbstate.NewManager(nodeA.state.Trie)
	if err := managerA.ParamStoreSet(governance.ParamKeyStakingAprBps, []byte("1750")); err != nil {
		nodeA.stateMu.Unlock()
		t.Fatalf("set staking apr A: %v", err)
	}
	rootABefore := nodeA.state.CurrentRoot()
	nodeA.stateMu.Unlock()

	nodeB.stateMu.Lock()
	managerB := nhbstate.NewManager(nodeB.state.Trie)
	if err := managerB.ParamStoreSet(governance.ParamKeyStakingAprBps, []byte("1750")); err != nil {
		nodeB.stateMu.Unlock()
		t.Fatalf("set staking apr B: %v", err)
	}
	rootBBefore := nodeB.state.CurrentRoot()
	nodeB.stateMu.Unlock()

	if err := nodeA.SyncStakingParams(); err != nil {
		t.Fatalf("sync staking params A: %v", err)
	}
	if err := nodeB.SyncStakingParams(); err != nil {
		t.Fatalf("sync staking params B: %v", err)
	}

	nodeA.stateMu.RLock()
	rootA := nodeA.state.CurrentRoot()
	aprA := nodeA.state.StakeRewardAPR()
	nodeA.stateMu.RUnlock()

	nodeB.stateMu.RLock()
	rootB := nodeB.state.CurrentRoot()
	aprB := nodeB.state.StakeRewardAPR()
	nodeB.stateMu.RUnlock()

	if rootA != rootB {
		t.Fatalf("expected deterministic staking sync roots, got %x vs %x", rootA.Bytes(), rootB.Bytes())
	}
	if rootA != rootABefore {
		t.Fatalf("expected staking sync A to keep canonical root stable, got %x want %x", rootA.Bytes(), rootABefore.Bytes())
	}
	if rootB != rootBBefore {
		t.Fatalf("expected staking sync B to keep canonical root stable, got %x want %x", rootB.Bytes(), rootBBefore.Bytes())
	}
	if aprA != 1750 || aprB != 1750 {
		t.Fatalf("expected runtime staking APR 1750, got %d and %d", aprA, aprB)
	}
}

func TestSyncValidatorThresholdsDoesNotMutateCanonicalState(t *testing.T) {
	t.Setenv("NHB_ENV", "dev")
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}

	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	cfg := node.globalConfigSnapshot()
	cfg.Staking.MinStakeWei = "10000000000000000000000"
	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("set global config: %v", err)
	}

	node.stateMu.RLock()
	rootBefore := node.state.CurrentRoot()
	node.stateMu.RUnlock()

	if err := node.SyncValidatorThresholds(); err != nil {
		t.Fatalf("sync validator thresholds: %v", err)
	}

	node.stateMu.RLock()
	rootAfter := node.state.CurrentRoot()
	node.stateMu.RUnlock()

	if rootAfter != rootBefore {
		t.Fatalf("expected validator threshold sync to keep canonical root stable, got %x want %x", rootAfter.Bytes(), rootBefore.Bytes())
	}
}

func TestCommitBlockFailureRollbackPreservesNextProposalStateRoot(t *testing.T) {
	node := newTestNode(t)
	fixedTime := time.Unix(1_776_117_900, 0).UTC()
	node.SetTimeSource(func() time.Time { return fixedTime })

	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create initial block: %v", err)
	}
	brokenHeader := *block.Header
	brokenHeader.StateRoot = []byte{0x01}
	broken := &types.Block{Header: &brokenHeader, Transactions: block.Transactions}
	if err := node.CommitBlock(broken); err == nil {
		t.Fatalf("expected state root mismatch commit failure")
	}

	nextBlock, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create next block: %v", err)
	}
	if !bytes.Equal(nextBlock.Header.StateRoot, block.Header.StateRoot) {
		t.Fatalf("expected recreated proposal state root %x, got %x", block.Header.StateRoot, nextBlock.Header.StateRoot)
	}
}
