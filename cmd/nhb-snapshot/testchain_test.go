package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	ethdbleveldb "github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage/trie"
)

// writableDB is a storage.Database over a LevelDB directory whose buffers can
// be made small, so that a chain built in a test flushes tables and compacts
// the way a long-running node's database does.
type writableDB struct {
	disk   ethdb.Database
	trieDB *triedb.Database
}

func openWritableDB(t testing.TB, dir string, small bool) *writableDB {
	t.Helper()
	backend, err := ethdbleveldb.NewCustom(dir, "test/", func(o *opt.Options) {
		if small {
			o.WriteBuffer = 128 << 10
			o.CompactionTableSize = 128 << 10
			o.CompactionL0Trigger = 2
			o.CompactionTotalSize = 512 << 10
		}
	})
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	disk := rawdb.NewDatabase(backend)
	return &writableDB{disk: disk, trieDB: triedb.NewDatabase(disk, triedb.HashDefaults)}
}

func (d *writableDB) Put(k, v []byte) error        { return d.disk.Put(k, v) }
func (d *writableDB) Get(k []byte) ([]byte, error) { return d.disk.Get(k) }
func (d *writableDB) TrieDB() *triedb.Database     { return d.trieDB }
func (d *writableDB) NewBatch() ethdb.Batch        { return d.disk.NewBatch() }
func (d *writableDB) Close() {
	_ = d.trieDB.Close()
	_ = d.disk.Close()
}

// chainBuilder builds a small chain with real state roots through the same
// code paths a node uses: core.Blockchain for blocks and storage/trie for
// state. It does not run consensus or transactions.
type chainBuilder struct {
	t     testing.TB
	db    *writableDB
	bc    *core.Blockchain
	root  common.Hash
	rng   *rand.Rand
	valid map[string]*big.Int
}

// quietly runs f with standard output discarded: core.Blockchain reports every
// block it adds on standard output.
func quietly(f func()) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		f()
		return
	}
	defer null.Close()
	saved := os.Stdout
	os.Stdout = null
	defer func() { os.Stdout = saved }()
	f()
}

func newChainBuilder(t testing.TB, dir string, small bool, validators ...crypto.Address) *chainBuilder {
	t.Helper()
	return newChainBuilderAt(t, dir, small, "", validators...)
}

// newChainBuilderAt starts from the genesis file at genesisPath (an ephemeral
// genesis when it is empty). With the shipped live genesis the chain has the
// live chain id and genesis hash, and its state holds the live validator set.
func newChainBuilderAt(t testing.TB, dir string, small bool, genesisPath string, validators ...crypto.Address) *chainBuilder {
	t.Helper()
	db := openWritableDB(t, dir, small)
	var bc *core.Blockchain
	var err error
	quietly(func() { bc, err = core.NewBlockchain(db, genesisPath, genesisPath == "") })
	if err != nil {
		db.Close()
		t.Fatalf("new blockchain: %v", err)
	}
	b := &chainBuilder{
		t:     t,
		db:    db,
		bc:    bc,
		root:  common.BytesToHash(bc.CurrentHeader().StateRoot),
		rng:   rand.New(rand.NewSource(7)),
		valid: map[string]*big.Int{},
	}
	for _, v := range validators {
		b.valid[string(v.Bytes())] = big.NewInt(10)
	}
	return b
}

// addBlock commits a block whose state adds n random entries (and, on the
// first block, the validator set).
func (b *chainBuilder) addBlock(n int) {
	b.t.Helper()
	var err error
	quietly(func() { err = b.tryAddBlock(n) })
	if err != nil {
		b.t.Fatal(err)
	}
}

// tryAddBlock is addBlock for callers that are not the test's own goroutine.
func (b *chainBuilder) tryAddBlock(n int) error {
	height := b.bc.Height() + 1
	tr, err := trie.NewTrie(b.db, b.root.Bytes())
	if err != nil {
		return fmt.Errorf("open state at %x: %w", b.root, err)
	}
	if height == 1 && len(b.valid) > 0 {
		encoded, err := nhbstate.EncodeValidatorSet(b.valid)
		if err != nil {
			return fmt.Errorf("encode validators: %w", err)
		}
		if err := tr.Update(validatorSetKey, encoded); err != nil {
			return fmt.Errorf("update validators: %w", err)
		}
	}
	for i := 0; i < n; i++ {
		key := make([]byte, 32)
		val := make([]byte, 96)
		b.rng.Read(key)
		b.rng.Read(val)
		if err := tr.Update(key, val); err != nil {
			return fmt.Errorf("update: %w", err)
		}
	}
	root, err := tr.Commit(b.root, height)
	if err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	header := &types.BlockHeader{
		Height:    height,
		Timestamp: 1_700_000_000 + int64(height),
		PrevHash:  b.bc.Tip(),
		StateRoot: root.Bytes(),
		TxRoot:    gethtypes.EmptyRootHash.Bytes(),
	}
	if err := b.bc.AddBlock(types.NewBlock(header, nil)); err != nil {
		return fmt.Errorf("add block %d: %w", height, err)
	}
	b.root = root
	return nil
}

func (b *chainBuilder) close() { b.db.Close() }

// buildChainDir builds a chain of the given height in dir and closes it.
func buildChainDir(t testing.TB, dir string, height, entriesPerBlock int, validators ...crypto.Address) {
	t.Helper()
	b := newChainBuilder(t, dir, false, validators...)
	for i := 0; i < height; i++ {
		b.addBlock(entriesPerBlock)
	}
	b.close()
}

func testAddress(seed byte) crypto.Address {
	return crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{seed}, 20))
}

// dbFiles lists the regular files of a directory, sorted.
func dbFiles(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// stageChainFiles copies the allowed files of a closed database into a fresh
// directory: what scripts/make-snapshot.sh hands to pack.
func stageChainFiles(t testing.TB, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "stage")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range dbFiles(t, src) {
		if !allowedFileName(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func heightKeyForTest(h uint64) []byte {
	key := append([]byte("height:"), make([]byte, 8)...)
	binary.BigEndian.PutUint64(key[len("height:"):], h)
	return key
}

// chainFiles lists the chain database files of dir, leaving out LevelDB
// housekeeping (an open, even a read-only one, may create the lock file).
func chainFiles(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	for _, name := range dbFiles(t, dir) {
		if !housekeepingFiles[name] {
			out = append(out, name)
		}
	}
	return out
}
