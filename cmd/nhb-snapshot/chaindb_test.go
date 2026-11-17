package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gethtrie "github.com/ethereum/go-ethereum/trie"

	"nhbchain/core"
	"nhbchain/core/types"
)

// The reader repeats the chain database keys of core.Blockchain. This builds a
// chain with the real code and fails if the reader and the node disagree about
// where anything is.
func TestReaderMatchesCoreBlockchain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	validator := testAddress(0x11)
	b := newChainBuilder(t, dir, false, validator)
	for i := 0; i < 40; i++ {
		b.addBlock(5)
	}
	wantHeight := b.bc.Height()
	wantTip := b.bc.Tip()
	wantGenesis := b.bc.GenesisHash()
	wantChain := b.bc.ChainID()
	wantRoot := b.bc.CurrentHeader().StateRoot
	wantTime := b.bc.CurrentHeader().Timestamp
	b.close()

	id, err := openAndReadIdentity(dir, checkOptions{})
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if id.Height != wantHeight || id.Height != 40 {
		t.Fatalf("height %d, core says %d", id.Height, wantHeight)
	}
	if !bytes.Equal(id.TipHash, wantTip) {
		t.Fatalf("tip %x, core says %x", id.TipHash, wantTip)
	}
	if !bytes.Equal(id.GenesisHash, wantGenesis) {
		t.Fatalf("genesis %x, core says %x", id.GenesisHash, wantGenesis)
	}
	if id.ChainID != wantChain {
		t.Fatalf("chain id %d, core says %d", id.ChainID, wantChain)
	}
	if !bytes.Equal(id.StateRoot, wantRoot) {
		t.Fatalf("state root %x, core says %x", id.StateRoot, wantRoot)
	}
	if len(id.Validators) != 1 || id.Validators[0] != validator.String() {
		t.Fatalf("validators %v, want [%s]", id.Validators, validator.String())
	}
	if id.HeadersRead != 41 {
		t.Fatalf("read %d headers, want 41 (the whole chain)", id.HeadersRead)
	}
	if id.StateNodes == 0 {
		t.Fatalf("no state nodes were re-hashed")
	}

	// The node keeps the time of its newest block under a key of its own, and
	// the reader looks for it under its copy of that key: were the two to drift
	// apart the reader would find no record and accept anything.
	db, err := openChainDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.close()
	raw, err := db.get(keyLastTimestamp)
	if err != nil {
		t.Fatalf("the node's last block time record is not under the reader's key: %v", err)
	}
	if len(raw) != 8 || int64(binary.BigEndian.Uint64(raw)) != id.TipTimestamp || id.TipTimestamp != wantTime {
		t.Fatalf("last block time record %x, want the tip header's time %d", raw, wantTime)
	}
}

// The node loads the last block time record as the earliest time it accepts for
// a later block, and cannot read one that is not 8 bytes. The node writes it with
// the tip, so it is the tip's time; every other record is refused.
func TestLastBlockTimeRecordMustBeTheTipTime(t *testing.T) {
	be := func(v int64) []byte {
		out := make([]byte, 8)
		binary.BigEndian.PutUint64(out, uint64(v))
		return out
	}
	cases := []struct {
		name   string
		record func(tip int64) []byte
	}{
		{"far ahead of the tip", func(int64) []byte { return be(4_000_000_000) }},
		{"one second ahead of the tip", func(tip int64) []byte { return be(tip + 1) }},
		{"behind the tip", func(tip int64) []byte { return be(tip - 1) }},
		{"zero", func(int64) []byte { return be(0) }},
		{"empty", func(int64) []byte { return []byte{} }},
		{"three bytes", func(int64) []byte { return []byte{1, 2, 3} }},
		{"nine bytes", func(tip int64) []byte { return append(be(tip), 0) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "db")
			b := newChainBuilder(t, dir, false)
			for i := 0; i < 5; i++ {
				b.addBlock(2)
			}
			tip := b.bc.CurrentHeader().Timestamp
			if err := b.db.disk.Put(keyLastTimestamp, tc.record(tip)); err != nil {
				t.Fatal(err)
			}
			b.close()

			for _, opts := range []checkOptions{{}, {HeaderWindow: 1, SkipState: true}} {
				if _, err := openAndReadIdentity(dir, opts); err == nil || !strings.Contains(err.Error(), "last block time record") {
					t.Fatalf("a database with a %s last block time record was accepted (options %+v): %v", tc.name, opts, err)
				}
			}
		})
	}
}

// A database without the record is one the node starts from as well, with the
// tip's time as the earliest it accepts, so it stays acceptable.
func TestAbsentLastBlockTimeRecordIsAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	for i := 0; i < 5; i++ {
		b.addBlock(2)
	}
	if err := b.db.disk.Delete(keyLastTimestamp); err != nil {
		t.Fatal(err)
	}
	b.close()

	id, err := openAndReadIdentity(dir, checkOptions{})
	if err != nil {
		t.Fatalf("a database without a last block time record was refused: %v", err)
	}

	db := openWritableDB(t, dir, false)
	defer db.Close()
	var bc *core.Blockchain
	quietly(func() { bc, err = core.NewBlockchain(db, "", false) })
	if err != nil {
		t.Fatalf("the node does not start from it: %v", err)
	}
	if got := bc.LastTimestamp(); got != id.TipTimestamp {
		t.Fatalf("the node's earliest block time is %d, want the tip's %d", got, id.TipTimestamp)
	}
}

func testTransfer(nonce uint64, value int64) *types.Transaction {
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       bytes.Repeat([]byte{0x33}, 20),
		Value:    big.NewInt(value),
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
}

// rewriteStoredBlock changes the body stored for the block at height h and
// leaves its header, and so the hash it is stored under, as it was.
func rewriteStoredBlock(t *testing.T, b *chainBuilder, h uint64, edit func(*types.Block)) {
	t.Helper()
	hash, err := b.db.disk.Get(heightKeyForTest(h))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.db.disk.Get(hash)
	if err != nil {
		t.Fatal(err)
	}
	var block types.Block
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatal(err)
	}
	edit(&block)
	out, err := json.Marshal(&block)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.db.disk.Put(hash, out); err != nil {
		t.Fatal(err)
	}
}

func TestBlocksWithTransactionsAreAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	for i := uint64(1); i <= 6; i++ {
		if i%2 == 0 {
			b.addBlockWithTxs(2, []*types.Transaction{testTransfer(2*i, 5), testTransfer(2*i+1, 7)})
		} else {
			b.addBlock(2)
		}
	}
	b.close()

	id, err := openAndReadIdentity(dir, checkOptions{})
	if err != nil {
		t.Fatalf("a chain whose blocks carry transactions was refused: %v", err)
	}
	if id.HeadersRead != 7 {
		t.Fatalf("read %d headers, want 7", id.HeadersRead)
	}
}

// The header commits to the transactions of a block only through their root, so
// a body that was changed under an unchanged header must be refused.
func TestBodyThatIsNotTheHeadersTransactionsIsRefused(t *testing.T) {
	cases := []struct {
		name string
		edit func(*types.Block)
	}{
		{"a changed transaction", func(blk *types.Block) { blk.Transactions[0].Value = big.NewInt(99) }},
		{"a transaction left out", func(blk *types.Block) { blk.Transactions = blk.Transactions[:1] }},
		{"no transactions", func(blk *types.Block) { blk.Transactions = nil }},
		{"a transaction added", func(blk *types.Block) {
			blk.Transactions = append(blk.Transactions, testTransfer(9, 1))
		}},
		{"transactions in another order", func(blk *types.Block) {
			blk.Transactions[0], blk.Transactions[1] = blk.Transactions[1], blk.Transactions[0]
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "db")
			b := newChainBuilder(t, dir, false)
			b.addBlock(2)
			b.addBlock(2)
			b.addBlockWithTxs(2, []*types.Transaction{testTransfer(1, 5), testTransfer(2, 7)})
			b.addBlock(2)
			rewriteStoredBlock(t, b, 3, tc.edit)
			b.close()

			if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "transactions give root") {
				t.Fatalf("%s was accepted: %v", tc.name, err)
			}
		})
	}

	// A body that is not the header's is refused wherever the newest blocks
	// are read, including when the tip is all that is.
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	b.addBlock(2)
	b.addBlockWithTxs(2, []*types.Transaction{testTransfer(1, 5)})
	rewriteStoredBlock(t, b, 2, func(blk *types.Block) { blk.Transactions[0].Nonce++ })
	b.close()
	if _, err := openAndReadIdentity(dir, checkOptions{HeaderWindow: 1, SkipState: true}); err == nil || !strings.Contains(err.Error(), "transactions give root") {
		t.Fatalf("a tip with another body was accepted: %v", err)
	}
}

func TestHeaderWindowLimitsWhatIsRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	buildChainDir(t, dir, 30, 2)
	id, err := openAndReadIdentity(dir, checkOptions{HeaderWindow: 5, SkipState: true})
	if err != nil {
		t.Fatal(err)
	}
	if id.HeadersRead != 6 || id.StateNodes != 0 {
		t.Fatalf("read %d headers and %d state nodes, want 6 and 0", id.HeadersRead, id.StateNodes)
	}
}

func TestOpenRefusesWhatIsNotAChainDatabase(t *testing.T) {
	empty := t.TempDir()
	if _, err := openAndReadIdentity(empty, checkOptions{}); err == nil || !strings.Contains(err.Error(), "no CURRENT") {
		t.Fatalf("empty directory: %v", err)
	}
	if _, err := openAndReadIdentity(filepath.Join(empty, "missing"), checkOptions{}); err == nil {
		t.Fatalf("missing directory opened")
	}

	// A LevelDB that holds no chain.
	dir := filepath.Join(t.TempDir(), "kv")
	db := openWritableDB(t, dir, false)
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "genesis") {
		t.Fatalf("a database without a chain: %v", err)
	}
}

// A copy taken while the node is writing ends in a half-written record. The
// database must still open, at an earlier block than the one being written.
func TestTornJournalTailStillOpens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	buildChainDir(t, dir, 60, 4)
	// Not settled: the point is a journal that still holds blocks.
	stage := stageReferenced(t, dir)

	var journal string
	for _, name := range dbFiles(t, stage) {
		if strings.HasSuffix(name, ".log") {
			journal = filepath.Join(stage, name)
		}
	}
	if journal == "" {
		t.Skip("the chain was flushed to tables entirely; no journal to tear")
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, data[:len(data)-37], 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := openAndReadIdentity(stage, checkOptions{})
	if err != nil {
		t.Fatalf("a copy with a torn journal tail does not open: %v", err)
	}
	if id.Height == 0 || id.Height > 60 {
		t.Fatalf("height %d after the tear", id.Height)
	}
}

// A state trie whose stored bytes do not hash to their address must be refused
// even though the root the header names is present.
func TestStateNodeSubstitutionIsDetected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false, testAddress(0x22))
	for i := 0; i < 20; i++ {
		b.addBlock(8)
	}
	root := common.BytesToHash(b.bc.CurrentHeader().StateRoot)
	tr, err := gethtrie.New(gethtrie.TrieID(root), b.db.trieDB)
	if err != nil {
		t.Fatal(err)
	}
	it, err := tr.NodeIterator(nil)
	if err != nil {
		t.Fatal(err)
	}
	type node struct {
		hash common.Hash
		blob []byte
	}
	var nodes []node
	for it.Next(true) {
		if h := it.Hash(); h != (common.Hash{}) && it.NodeBlob() != nil {
			nodes = append(nodes, node{h, append([]byte(nil), it.NodeBlob()...)})
		}
	}
	if len(nodes) < 3 {
		t.Fatalf("only %d nodes", len(nodes))
	}
	// Store one node's bytes under another node's hash.
	victim, donor := nodes[len(nodes)-1], nodes[1]
	if bytes.Equal(victim.blob, donor.blob) || victim.hash == crypto.Keccak256Hash(donor.blob) {
		t.Fatalf("test setup: nodes are identical")
	}
	if err := b.db.disk.Put(victim.hash.Bytes(), donor.blob); err != nil {
		t.Fatal(err)
	}
	b.close()

	if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("a substituted state node was accepted: %v", err)
	}
	// Skipping the deep check is the only way past it.
	if _, err := openAndReadIdentity(dir, checkOptions{SkipState: true}); err != nil {
		t.Fatalf("with --no-state-check: %v", err)
	}
}

func TestMissingStateRootIsDetected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	for i := 0; i < 5; i++ {
		b.addBlock(3)
	}
	root := b.bc.CurrentHeader().StateRoot
	if err := b.db.disk.Delete(root); err != nil {
		t.Fatal(err)
	}
	b.close()
	if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "state root") {
		t.Fatalf("a database whose tip state is missing was accepted: %v", err)
	}
}

func TestBrokenTipIsDetected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	for i := 0; i < 5; i++ {
		b.addBlock(3)
	}
	// The tip points at a different block than the height index says.
	other := b.bc.GenesisHash()
	if err := b.db.disk.Put(keyTip, other); err != nil {
		t.Fatal(err)
	}
	b.close()
	if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("a mismatched tip was accepted: %v", err)
	}
}

func TestHeightIndexHoleIsDetected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, false)
	for i := 0; i < 8; i++ {
		b.addBlock(2)
	}
	if err := b.db.disk.Delete(heightKeyForTest(4)); err != nil {
		t.Fatal(err)
	}
	b.close()
	if _, err := openAndReadIdentity(dir, checkOptions{}); err == nil || !strings.Contains(err.Error(), "height index") {
		t.Fatalf("a hole in the height index was accepted: %v", err)
	}
}
