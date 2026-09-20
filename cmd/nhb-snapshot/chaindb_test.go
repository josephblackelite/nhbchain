package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gethtrie "github.com/ethereum/go-ethereum/trie"
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
