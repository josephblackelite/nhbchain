package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/p2p"
)

// A block must fit one p2p frame (1 MiB by default) in every message it travels
// in, or the peers refuse the message. Nothing bounded the size of a block, and a
// transfer ignores its Data field, so a few transactions carrying a lot of Data
// made a block that could not be proposed, gossiped or served to a peer that was
// catching up. These tests cover the proposer's byte budget, the admission limit
// that goes with it, and the sync reply for a block no frame can hold.

// testFrameLimit is the default p2p frame limit (MaxMsgBytes).
const testFrameLimit = 1 << 20

// proposalWrapperAllowance stands in for what a proposal adds around its block
// (the signature, the proposer and, on a re-proposal, the proof votes).
const proposalWrapperAllowance = 4 << 10

// largeDataTransfer builds a signed native transfer that carries dataBytes of
// Data, which the transfer ignores.
func largeDataTransfer(t testing.TB, key *crypto.PrivateKey, nonce uint64, dataBytes int) *types.Transaction {
	t.Helper()
	to := make([]byte, 20)
	copy(to, livenessKey(t).PubKey().Address().Bytes())
	return livenessSign(t, key, types.TxTypeTransfer, nonce, bytes.Repeat([]byte{0x5A}, dataBytes), to, 1)
}

// fundedLargeTransfers returns n large transfers from n distinct funded senders,
// injected into the mempool.
func fundedLargeTransfers(t testing.TB, node *Node, n, dataBytes int) []*types.Transaction {
	t.Helper()
	out := make([]*types.Transaction, 0, n)
	for i := 0; i < n; i++ {
		key := livenessKey(t)
		livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
		out = append(out, largeDataTransfer(t, key, 0, dataBytes))
	}
	livenessInject(node, out...)
	return out
}

func frameBytes(t testing.TB, msg *p2p.Message, err error) int {
	t.Helper()
	if err != nil {
		t.Fatalf("build message: %v", err)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	return len(raw)
}

// requireBlockFitsFrame fails unless block can be sent to a peer in every
// message it travels in: the proposal that carries it, the gossip of the
// committed block and a reply to a block request.
func requireBlockFitsFrame(t testing.TB, block *types.Block) {
	t.Helper()
	raw, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("encode block: %v", err)
	}
	proposal := &p2p.Message{Type: p2p.MsgTypeProposal, Payload: append(raw, make([]byte, proposalWrapperAllowance)...)}
	gossip, gossipErr := p2p.NewBlockMessage(block)
	reply, replyErr := p2p.NewBlocksMessage([]*types.Block{block})
	for name, size := range map[string]int{
		"proposal":   frameBytes(t, proposal, nil),
		"block":      frameBytes(t, gossip, gossipErr),
		"sync reply": frameBytes(t, reply, replyErr),
	} {
		if size > testFrameLimit {
			t.Fatalf("the %s frame is %d bytes, over the %d byte frame limit (block JSON %d bytes, %d transactions)",
				name, size, testFrameLimit, len(raw), len(block.Transactions))
		}
	}
}

// Large transactions queued together are spread over several blocks, each of
// which fits a frame; none is lost and none is stuck behind the others.
func TestQueuedLargeTransactionsNeverMakeABlockTooBigForAFrame(t *testing.T) {
	node := newTestNode(t)
	const queued = 4
	txs := fundedLargeTransfers(t, node, queued, 250<<10)

	committed := make(map[*types.Transaction]int)
	for round := 1; round <= queued; round++ {
		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("round %d: CreateBlock: %v", round, err)
		}
		if len(block.Transactions) == 0 {
			t.Fatalf("round %d: an empty block while %d large transactions are queued", round, queued-round+1)
		}
		requireBlockFitsFrame(t, block)
		if err := node.ValidateBlock(block); err != nil {
			t.Fatalf("round %d: the block does not validate: %v", round, err)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("round %d: CommitBlock: %v", round, err)
		}
		for _, tx := range block.Transactions {
			committed[tx]++
		}
	}
	for i, tx := range txs {
		if committed[tx] != 1 {
			t.Fatalf("transaction %d was committed %d times over %d rounds, want once", i, committed[tx], queued)
		}
	}
	if left := node.MempoolSize(); left != 0 {
		t.Fatalf("%d transactions are still queued", left)
	}
}

// What is left out of a block is the tail of the offered list, so that a
// sender's transactions still go in nonce order and nothing is taken from behind
// one that did not fit.
func TestTrimmedBlockKeepsOneSendersTransactionsInNonceOrder(t *testing.T) {
	node := newTestNode(t)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
	const queued = 3
	txs := make([]*types.Transaction, 0, queued)
	for nonce := uint64(0); nonce < queued; nonce++ {
		txs = append(txs, largeDataTransfer(t, key, nonce, 250<<10))
	}
	livenessInject(node, txs...)

	for round := 0; round < queued; round++ {
		block, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("round %d: CreateBlock: %v", round, err)
		}
		if len(block.Transactions) != 1 || block.Transactions[0] != txs[round] {
			t.Fatalf("round %d: expected exactly the transaction with nonce %d, got %d transactions", round, round, len(block.Transactions))
		}
		requireBlockFitsFrame(t, block)
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("round %d: CommitBlock: %v", round, err)
		}
	}
}

// A transaction that is too large for any block would sit at the head of every
// build; it is dropped, and the transactions behind it are not held up.
func TestTransactionTooLargeForABlockIsDroppedAndDoesNotHoldUpOthers(t *testing.T) {
	node := newTestNode(t)
	bigKey, smallKey := livenessKey(t), livenessKey(t)
	livenessFund(t, node, bigKey.PubKey().Address().Bytes(), 1_000_000_000, 0)
	livenessFund(t, node, smallKey.PubKey().Address().Bytes(), 1_000_000_000, 0)
	big := largeDataTransfer(t, bigKey, 0, 700<<10)
	small := livenessTransfer(t, smallKey, 0)
	livenessInject(node, big, small)

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if livenessBlockContains(t, block, big) {
		t.Fatalf("a transaction too large for a block was placed in one")
	}
	if !livenessBlockContains(t, block, small) {
		t.Fatalf("the small transaction was held up by the large one")
	}
	requireBlockFitsFrame(t, block)
	if livenessResident(t, node, big) {
		t.Fatalf("a transaction that can never be proposed must leave the mempool")
	}
	if !livenessResident(t, node, small) {
		t.Fatalf("the transaction that was proposed stays resident until its block commits")
	}
}

// smallBudgetNode returns a node whose proposals may hold limit bytes of JSON,
// so the budget mechanics can be tested without moving hundreds of kilobytes.
func smallBudgetNode(t *testing.T, limit int) *Node {
	t.Helper()
	node := newTestNode(t)
	node.setBuildConfig(func(cfg *buildConfig) { cfg.MaxBlockBytes = limit })
	return node
}

const (
	smallBudget     = 20 << 10
	smallBudgetData = 3000 // about 4.7 KB of block JSON per transaction, so four fit
)

func requireWithinBudget(t testing.TB, block *types.Block, limit int) {
	t.Helper()
	raw, err := json.Marshal(block)
	if err != nil {
		t.Fatalf("encode block: %v", err)
	}
	if len(raw) > limit {
		t.Fatalf("the block is %d bytes of JSON with %d transactions, over its %d byte budget", len(raw), len(block.Transactions), limit)
	}
}

// The block is the longest prefix of the offered list that fits; the rest stay
// resident and are released from the in-flight lease so the next build offers
// them again.
func TestOverBudgetBlockKeepsThePrefixAndReleasesTheRest(t *testing.T) {
	node := smallBudgetNode(t, smallBudget)
	fundedLargeTransfers(t, node, 10, smallBudgetData)
	offered := node.GetMempool()

	block, err := node.CreateBlock(offered)
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	got := len(block.Transactions)
	if got < 2 || got >= len(offered) {
		t.Fatalf("expected a block holding some but not all of the %d offered transactions, got %d", len(offered), got)
	}
	requireWithinBudget(t, block, smallBudget)
	for i, tx := range block.Transactions {
		if tx != offered[i] {
			t.Fatalf("position %d is not the %dth offered transaction: the prefix must keep the offered order", i, i)
		}
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("the trimmed block must validate: %v", err)
	}
	for _, tx := range offered[got:] {
		if !livenessResident(t, node, tx) {
			t.Fatalf("a transaction left out of the block must stay resident")
		}
	}
	if inFlight := livenessInFlight(node); inFlight != got {
		t.Fatalf("expected only the %d included transactions leased, got %d", got, inFlight)
	}
	// The ones left out are offered again, and all of them go in over later
	// blocks.
	remaining := len(offered) - got
	for round := 0; remaining > 0; round++ {
		if round > len(offered) {
			t.Fatalf("%d transactions are still queued after %d rounds", remaining, round)
		}
		if err := node.CommitBlock(block); err != nil {
			t.Fatalf("CommitBlock: %v", err)
		}
		next, err := node.CreateBlock(node.GetMempool())
		if err != nil {
			t.Fatalf("CreateBlock: %v", err)
		}
		requireWithinBudget(t, next, smallBudget)
		remaining -= len(next.Transactions)
		block = next
		if len(next.Transactions) == 0 {
			t.Fatalf("an empty block while %d transactions are queued", remaining)
		}
	}
}

// When the wave cap runs out, the clean prefix of the last wave is used. That
// prefix is trimmed to the budget too.
func TestPrefixFallbackBlockIsTrimmedToTheBudget(t *testing.T) {
	node := smallBudgetNode(t, smallBudget)
	node.setBuildConfig(func(cfg *buildConfig) { cfg.MaxWaves = 2 })
	fundedLargeTransfers(t, node, 10, smallBudgetData)
	offered := node.GetMempool()
	// Each of the two exploratory waves loses one more transaction from the tail,
	// so the cap is reached with eight clean transactions, which is more than the
	// budget holds.
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if wave >= 1 && wave <= 2 && tx == offered[len(offered)-wave] {
			return errors.New("injected failure")
		}
		return nil
	})

	block, err := node.CreateBlock(offered)
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if len(block.Transactions) == 0 {
		t.Fatalf("the block must carry the prefix that fits, not nothing")
	}
	requireWithinBudget(t, block, smallBudget)
	for i, tx := range block.Transactions {
		if tx != offered[i] {
			t.Fatalf("position %d is not the %dth offered transaction", i, i)
		}
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("the block must validate: %v", err)
	}
}

// Transactions that cannot apply are still found and left out when the rest is
// over budget; the trim does not narrow the set that is tried.
func TestOverBudgetBuildStillExcludesTransactionsThatCannotApply(t *testing.T) {
	node := smallBudgetNode(t, smallBudget)
	valid := fundedLargeTransfers(t, node, 8, smallBudgetData)
	poison := make([]*types.Transaction, 0, 3)
	for i := 0; i < 3; i++ {
		// An unfunded sender cannot pay for the transfer.
		to := make([]byte, 20)
		to[19] = byte(i + 1)
		poison = append(poison, livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, bytes.Repeat([]byte{0x11}, smallBudgetData), to, 1_000_000))
	}
	livenessInject(node, poison...)

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	requireWithinBudget(t, block, smallBudget)
	if len(block.Transactions) == 0 {
		t.Fatalf("an empty block while valid transactions are queued")
	}
	for _, tx := range block.Transactions {
		for _, bad := range poison {
			if tx == bad {
				t.Fatalf("a transaction that cannot apply was placed in the block")
			}
		}
		found := false
		for _, ok := range valid {
			found = found || tx == ok
		}
		if !found {
			t.Fatalf("the block holds a transaction that was never queued")
		}
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("the block must validate: %v", err)
	}
}

func TestBlockBudgetFollowsTheConfiguredFrameLimit(t *testing.T) {
	if got := blockBytesForFrame(1 << 20); got != defaultMaxBlockBytes {
		t.Fatalf("the default frame limit gives a budget of %d, want %d", got, defaultMaxBlockBytes)
	}
	if got := blockBytesForFrame(64 << 20); got != defaultMaxBlockBytes {
		t.Fatalf("a larger frame limit must not raise the budget above %d, got %d", defaultMaxBlockBytes, got)
	}
	if got := blockBytesForFrame(2048); got != minMaxBlockBytes {
		t.Fatalf("a tiny frame limit gives a budget of %d, want the floor %d", got, minMaxBlockBytes)
	}
	// Whatever the frame limit, a block of exactly the budget, plus what is kept
	// back for the wrapper and the quorum certificate, still fits it once the
	// payload is base64 encoded.
	for _, frame := range []int{256 << 10, 384 << 10, 512 << 10, 768 << 10, 1 << 20, 2 << 20} {
		budget := blockBytesForFrame(frame)
		payload := budget + blockFrameReserve
		wire := (payload+2)/3*4 + frameEnvelopeBytes
		if wire > frame {
			t.Fatalf("frame limit %d: a block of %d bytes with its reserve needs %d bytes on the wire", frame, budget, wire)
		}
	}

	node := newTestNode(t)
	node.SetProposalFrameLimit(256 << 10)
	if got, want := node.buildConfigSnapshot().MaxBlockBytes, blockBytesForFrame(256<<10); got != want {
		t.Fatalf("SetProposalFrameLimit(256 KiB) left the budget at %d, want %d", got, want)
	}
	node.SetProposalFrameLimit(0)
	if got, want := node.buildConfigSnapshot().MaxBlockBytes, blockBytesForFrame(256<<10); got != want {
		t.Fatalf("a zero frame limit must be ignored, the budget became %d", got)
	}
	var nilNode *Node
	nilNode.SetProposalFrameLimit(1 << 20)
}

// signedTransferWithData is a native transfer with a chosen fee, for replacing
// another from the same sender.
func signedTransferWithData(t testing.TB, key *crypto.PrivateKey, nonce uint64, gasPrice int64, dataBytes int) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       make([]byte, 20),
		Value:    big.NewInt(1),
		Data:     bytes.Repeat([]byte{0x33}, dataBytes),
		GasLimit: 21_000,
		GasPrice: big.NewInt(gasPrice),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

// A replacement for a transaction already in the mempool is held to the same
// size limits as a new one: it may not grow the sender's share past the window.
func TestReplacementIsHeldToTheSenderByteWindow(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000_000, 0)

	original := signedTransferWithData(t, key, 0, 1, 0)
	if err := node.AddTransaction(original); err != nil {
		t.Fatalf("add: %v", err)
	}

	tooBig := signedTransferWithData(t, key, 0, 2, 300<<10)
	if err := node.AddTransaction(tooBig); !errors.Is(err, ErrMempoolSenderLimit) {
		t.Fatalf("a replacement that would take the sender past the window must be refused with ErrMempoolSenderLimit, got %v", err)
	}
	if !livenessResident(t, node, original) || livenessResident(t, node, tooBig) {
		t.Fatalf("a refused replacement must leave the original in place")
	}

	fits := signedTransferWithData(t, key, 0, 3, 100<<10)
	if err := node.AddTransaction(fits); err != nil {
		t.Fatalf("a replacement within the window must be accepted: %v", err)
	}
	if livenessResident(t, node, original) || !livenessResident(t, node, fits) {
		t.Fatalf("the accepted replacement must take the original's place")
	}

	// A replacement that does not make the pool larger is never refused for size.
	smaller := signedTransferWithData(t, key, 0, 4, 10<<10)
	if err := node.AddTransaction(smaller); err != nil {
		t.Fatalf("a smaller replacement must be accepted: %v", err)
	}
}

// A transaction whose encoding could not fit a block by itself is refused at
// admission, whether it is new or replaces another.
func TestTransactionTooLargeForABlockIsRefusedAtAdmission(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	key := livenessKey(t)
	livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000_000, 0)

	original := signedTransferWithData(t, key, 0, 1, 0)
	if err := node.AddTransaction(original); err != nil {
		t.Fatalf("add: %v", err)
	}
	huge := signedTransferWithData(t, key, 0, 2, 700<<10)
	err := node.AddTransaction(huge)
	if !errors.Is(err, ErrInvalidTransaction) || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("a replacement too large for any block must be refused as invalid, got %v", err)
	}
	if !livenessResident(t, node, original) || livenessResident(t, node, huge) {
		t.Fatalf("a refused replacement must leave the original in place")
	}

	other := livenessKey(t)
	livenessFund(t, node, other.PubKey().Address().Bytes(), 1_000_000_000_000, 0)
	fresh := signedTransferWithData(t, other, 0, 1, 700<<10)
	if err := node.AddTransaction(fresh); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("a new transaction too large for any block must be refused as invalid, got %v", err)
	}
}

// A request for blocks that no frame can carry is not answered, and that is not
// an error: the peer asking is simply not sent a message it would refuse.
func TestBlockReplyTooLargeForAFrameIsNotSentAndIsNotAnError(t *testing.T) {
	node := newTestNode(t)
	commitEmptyBlocks(t, node, 1)

	var sent int
	tooLarge := func(*p2p.Message) error {
		sent++
		return fmt.Errorf("peer test: %w", p2p.ErrMessageTooLarge)
	}
	if err := node.handleNetworkGetBlocks(p2p.GetBlocksPayload{From: 1}, tooLarge); err != nil {
		t.Fatalf("a reply refused for its size must not be reported as a failure: %v", err)
	}
	if sent != 1 {
		t.Fatalf("expected the reply to be offered once, offered %d times", sent)
	}
	boom := errors.New("peer gone")
	if err := node.handleNetworkGetBlocks(p2p.GetBlocksPayload{From: 1}, func(*p2p.Message) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("any other send failure must still be returned, got %v", err)
	}
}

// A block bigger than a frame that is already committed (an older proposer, or
// a build before the budget existed, could have produced one) can be served to
// nobody. Asking for it must not cost the serving node the connection to the
// peer that asked, nor send the peer a message it would end the connection
// over.
func TestAskingForABlockTooLargeForAFrameDoesNotCostTheConnection(t *testing.T) {
	node := newTestNode(t)
	node.setBuildConfig(func(cfg *buildConfig) { cfg.MaxBlockBytes = 8 << 20 })
	fundedLargeTransfers(t, node, 4, 250<<10)
	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("CommitBlock: %v", err)
	}
	reply, replyErr := p2p.NewBlocksMessage([]*types.Block{block})
	if size := frameBytes(t, reply, replyErr); size <= testFrameLimit {
		t.Fatalf("test setup: the committed block must not fit a frame, its reply is %d bytes", size)
	}

	nodeServer, nodeAddr := startWireServer(t, node, node)
	node.SetNetworkBroadcaster(nodeServer)
	asker := &recordingHandler{}
	askerServer, _ := startWireServer(t, node, asker)
	if err := askerServer.Connect(nodeAddr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitFor(t, 3*time.Second, "the peer to be connected", func() bool {
		return nodeServer.SnapshotNetwork().Counts.Inbound == 1
	})

	getBlocks, err := p2p.NewGetBlocksMessage(1)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := askerServer.Broadcast(getBlocks); err != nil {
		t.Fatalf("send request: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// The connection is still good: a request that can be answered is.
	getStatus, err := p2p.NewGetStatusMessage()
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := askerServer.Broadcast(getStatus); err != nil {
		t.Fatalf("send request: %v", err)
	}
	waitFor(t, 3*time.Second, "the status request to be answered on the same connection", func() bool {
		return asker.count(p2p.MsgTypeStatus) == 1
	})
	if got := asker.count(p2p.MsgTypeBlocks); got != 0 {
		t.Fatalf("a reply no frame can hold was sent: %d blocks messages arrived", got)
	}
	if got := nodeServer.SnapshotNetwork().Counts.Inbound; got != 1 {
		t.Fatalf("the peer that asked is no longer connected (%d inbound peers)", got)
	}
}
