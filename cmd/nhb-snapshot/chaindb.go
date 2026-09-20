package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	ethdbleveldb "github.com/ethereum/go-ethereum/ethdb/leveldb"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nhbcrypto "nhbchain/crypto"
)

// The keys below are the ones core.Blockchain keeps in the chain database
// (core/blockchain.go). They are repeated here so this command stays a small,
// read-only reader; TestReaderMatchesCoreBlockchain builds a chain with the
// real code and fails if the two ever disagree.
var (
	keyTip       = []byte("tip")
	keyGenesis   = []byte("genesis")
	keyHeight    = []byte("height")
	prefixHeight = []byte("height:")

	// validatorSetKey is where core/state_transition.go keeps the validator set
	// inside the state trie.
	validatorSetKey = crypto.Keccak256([]byte("validator-set"))
)

func heightKey(h uint64) []byte {
	key := make([]byte, len(prefixHeight)+8)
	copy(key, prefixHeight)
	binary.BigEndian.PutUint64(key[len(prefixHeight):], h)
	return key
}

// chainDB is a read-only view of a node's chain database.
type chainDB struct {
	dir    string
	disk   ethdb.Database
	trieDB *triedb.Database
	// tables are the table files the database's current version refers to
	// (file number -> size). Only these are ever read; any other .ldb file in
	// the directory is an unfinished or obsolete leftover.
	tables map[int64]int64
}

var tableLinePattern = regexp.MustCompile(`^([0-9]+):([0-9]+)\[`)

// liveTables reads the tables of the current version from an open database.
func liveTables(db *leveldb.DB) (map[int64]int64, error) {
	prop, err := db.GetProperty("leveldb.sstables")
	if err != nil {
		return nil, err
	}
	tables := make(map[int64]int64)
	for _, line := range strings.Split(prop, "\n") {
		m := tableLinePattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		num, _ := strconv.ParseInt(m[1], 10, 64)
		size, _ := strconv.ParseInt(m[2], 10, 64)
		tables[num] = size
	}
	return tables, nil
}

// tableNumber returns the file number of a table file name.
func tableNumber(name string) (int64, bool) {
	for _, suffix := range []string{".ldb", ".sst"} {
		if strings.HasSuffix(name, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(name, suffix), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func tableFileName(num int64) string { return fmt.Sprintf("%06d.ldb", num) }

// openChainDB opens the LevelDB directory dir strictly read-only. It never
// repairs anything: go-ethereum's LevelDB wrapper falls back to a repairing
// (writing) open when it meets a corrupt database, so the directory is first
// opened directly with the same read-only options, and a corrupt one is
// reported as it is.
func openChainDB(dir string) (*chainDB, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("chain database %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("chain database %s is not a directory", dir)
	}
	current := filepath.Join(dir, "CURRENT")
	if st, err := os.Stat(current); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s does not look like a LevelDB directory: no CURRENT file", dir)
	}

	probe, err := leveldb.OpenFile(dir, &opt.Options{ReadOnly: true, ErrorIfMissing: true})
	if err != nil {
		return nil, fmt.Errorf("chain database %s does not open: %w", dir, err)
	}
	tables, tablesErr := liveTables(probe)
	if err := probe.Close(); err != nil {
		return nil, fmt.Errorf("close probe of %s: %w", dir, err)
	}
	if tablesErr != nil {
		return nil, fmt.Errorf("chain database %s: list tables: %w", dir, tablesErr)
	}
	// The manifest names the table files the database is made of; each must
	// be there and as long as the manifest says. LevelDB itself only finds a
	// missing or short table when a read happens to need it.
	for num, size := range tables {
		path := filepath.Join(dir, tableFileName(num))
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("chain database %s is incomplete: table file %s is missing", dir, tableFileName(num))
		}
		if info.Size() != size {
			return nil, fmt.Errorf("chain database %s is damaged: table file %s is %d bytes, the manifest says %d", dir, tableFileName(num), info.Size(), size)
		}
	}

	backend, err := ethdbleveldb.New(dir, 16, 16, "nhb-snapshot/", true)
	if err != nil {
		return nil, fmt.Errorf("chain database %s does not open: %w", dir, err)
	}
	disk := rawdb.NewDatabase(backend)
	return &chainDB{dir: dir, disk: disk, trieDB: triedb.NewDatabase(disk, triedb.HashDefaults), tables: tables}, nil
}

func (c *chainDB) close() {
	if c == nil {
		return
	}
	_ = c.trieDB.Close()
	_ = c.disk.Close()
}

func (c *chainDB) get(key []byte) ([]byte, error) {
	return c.disk.Get(key)
}

// chainIdentity is what a snapshot says about the chain it holds.
type chainIdentity struct {
	ChainID      uint64
	GenesisHash  []byte
	Height       uint64
	TipHash      []byte
	StateRoot    []byte
	TipTimestamp int64
	Validators   []string // bech32 addresses of the validator set in the tip's state
	HeadersRead  uint64   // how many headers were linked and hashed
	StateNodes   uint64   // how many state trie nodes were re-hashed (0 when skipped)
}

// checkOptions selects how much of the database is verified.
type checkOptions struct {
	// HeaderWindow is how many of the newest blocks are fully decoded, hashed
	// and linked to their parent. Zero means every block back to genesis.
	HeaderWindow uint64
	// SkipState skips re-hashing every node of the tip's state trie.
	SkipState bool
}

const defaultHeaderWindow = 256

func decodeBlock(raw []byte) (*types.Block, error) {
	var block types.Block
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, err
	}
	if block.Header == nil {
		return nil, errors.New("block has no header")
	}
	return &block, nil
}

// blockAt reads the block at height h through the height index and checks that
// its header hashes to the indexed hash.
func (c *chainDB) blockAt(h uint64) (*types.Block, []byte, error) {
	hash, err := c.get(heightKey(h))
	if err != nil {
		return nil, nil, fmt.Errorf("height index entry %d: %w", h, err)
	}
	if len(hash) != 32 {
		return nil, nil, fmt.Errorf("height index entry %d is %d bytes, want 32", h, len(hash))
	}
	raw, err := c.get(hash)
	if err != nil {
		return nil, nil, fmt.Errorf("block %d (%x): %w", h, hash, err)
	}
	block, err := decodeBlock(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("block %d (%x): %w", h, hash, err)
	}
	got, err := block.Header.Hash()
	if err != nil {
		return nil, nil, fmt.Errorf("block %d: hash header: %w", h, err)
	}
	if !bytes.Equal(got, hash) {
		return nil, nil, fmt.Errorf("block %d: header hashes to %x but the index says %x", h, got, hash)
	}
	if block.Header.Height != h {
		return nil, nil, fmt.Errorf("block %x claims height %d but is indexed at %d", hash, block.Header.Height, h)
	}
	return block, hash, nil
}

// readIdentity reads the chain identity and checks the database the way the
// node itself would at start-up (tip and height index agree, the index has no
// holes), plus that the newest headers hash and link correctly and that the
// tip's state trie is complete and every node of it hashes to its address.
func (c *chainDB) readIdentity(opts checkOptions) (*chainIdentity, error) {
	genesis, err := c.get(keyGenesis)
	if err != nil {
		return nil, fmt.Errorf("no genesis hash in the database (not a node data directory, or an empty one): %w", err)
	}
	if len(genesis) != 32 {
		return nil, fmt.Errorf("genesis hash is %d bytes, want 32", len(genesis))
	}
	heightRaw, err := c.get(keyHeight)
	if err != nil {
		return nil, fmt.Errorf("no height in the database: %w", err)
	}
	if len(heightRaw) != 8 {
		return nil, fmt.Errorf("height record is %d bytes, want 8", len(heightRaw))
	}
	height := binary.BigEndian.Uint64(heightRaw)
	tip, err := c.get(keyTip)
	if err != nil {
		return nil, fmt.Errorf("no tip in the database: %w", err)
	}
	if len(tip) != 32 {
		return nil, fmt.Errorf("tip hash is %d bytes, want 32", len(tip))
	}

	// The same cross-checks core.NewBlockchain makes when a node starts: the
	// tip must be the hash the height index holds for the height, and the
	// index must have an entry for every height.
	atTip, err := c.get(heightKey(height))
	if err != nil {
		return nil, fmt.Errorf("height index entry %d: %w", height, err)
	}
	if !bytes.Equal(atTip, tip) {
		return nil, fmt.Errorf("crash-recovery integrity check failed: tip %x does not match height-index entry for height %d (%x)", tip, height, atTip)
	}
	atGenesis, err := c.get(heightKey(0))
	if err != nil {
		return nil, fmt.Errorf("height index entry 0: %w", err)
	}
	if !bytes.Equal(atGenesis, genesis) {
		return nil, fmt.Errorf("genesis hash %x does not match height-index entry for height 0 (%x)", genesis, atGenesis)
	}
	for h := uint64(1); h < height; h++ {
		if _, err := c.get(heightKey(h)); err != nil {
			return nil, fmt.Errorf("load height index %d: %w", h, err)
		}
	}

	tipBlock, _, err := c.blockAt(height)
	if err != nil {
		return nil, err
	}
	if len(tipBlock.Header.StateRoot) != 32 {
		return nil, fmt.Errorf("tip header state root is %d bytes, want 32", len(tipBlock.Header.StateRoot))
	}
	id := &chainIdentity{
		ChainID:      binary.BigEndian.Uint64(genesis[:8]),
		GenesisHash:  append([]byte(nil), genesis...),
		Height:       height,
		TipHash:      append([]byte(nil), tip...),
		StateRoot:    append([]byte(nil), tipBlock.Header.StateRoot...),
		TipTimestamp: tipBlock.Header.Timestamp,
	}

	// Link the newest headers to their parents (all of them when the window is
	// zero) and the genesis block to the genesis hash.
	lowest := uint64(0)
	if opts.HeaderWindow > 0 && height > opts.HeaderWindow {
		lowest = height - opts.HeaderWindow
	}
	prev := tipBlock
	id.HeadersRead = 1
	for h := height; h > lowest; h-- {
		parent, parentHash, err := c.blockAt(h - 1)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(prev.Header.PrevHash, parentHash) {
			return nil, fmt.Errorf("block %d does not link to block %d: prevHash %x, parent hash %x", h, h-1, prev.Header.PrevHash, parentHash)
		}
		prev = parent
		id.HeadersRead++
	}
	if lowest == 0 {
		// prev is the genesis block, already hashed against the index above.
		if prev.Header.Height != 0 {
			return nil, fmt.Errorf("first block has height %d, want 0", prev.Header.Height)
		}
	}

	stateTrie, err := gethtrie.New(gethtrie.TrieID(common.BytesToHash(id.StateRoot)), c.trieDB)
	if err != nil {
		return nil, fmt.Errorf("the state root %x of the tip header is not in the database: %w", id.StateRoot, err)
	}
	if !opts.SkipState {
		nodes, err := verifyTrieNodes(stateTrie)
		if err != nil {
			return nil, fmt.Errorf("state trie at %x: %w", id.StateRoot, err)
		}
		id.StateNodes = nodes
	}
	raw, err := stateTrie.Get(validatorSetKey)
	if err != nil {
		return nil, fmt.Errorf("read the validator set: %w", err)
	}
	set, err := nhbstate.DecodeValidatorSet(raw)
	if err != nil {
		return nil, fmt.Errorf("decode the validator set: %w", err)
	}
	for addr := range set {
		encoded, err := nhbcrypto.NewAddress(nhbcrypto.NHBPrefix, []byte(addr))
		if err != nil {
			return nil, fmt.Errorf("validator set holds a malformed address %x: %w", addr, err)
		}
		id.Validators = append(id.Validators, encoded.String())
	}
	sort.Strings(id.Validators)
	return id, nil
}

// verifyTrieNodes walks every node reachable from the trie's root and checks
// that the bytes stored under each node's hash really hash to it, so a state
// trie that was altered under an unchanged root cannot pass.
func verifyTrieNodes(t *gethtrie.Trie) (uint64, error) {
	it, err := t.NodeIterator(nil)
	if err != nil {
		return 0, err
	}
	var nodes uint64
	for it.Next(true) {
		hash := it.Hash()
		if hash == (common.Hash{}) {
			continue
		}
		blob := it.NodeBlob()
		if blob == nil {
			continue
		}
		if crypto.Keccak256Hash(blob) != hash {
			return nodes, fmt.Errorf("node %x holds bytes that hash to %x", hash, crypto.Keccak256Hash(blob))
		}
		nodes++
	}
	if err := it.Error(); err != nil {
		return nodes, err
	}
	return nodes, nil
}

// openAndReadIdentity opens dir read-only and reads its identity.
func openAndReadIdentity(dir string, opts checkOptions) (*chainIdentity, error) {
	db, err := openChainDB(dir)
	if err != nil {
		return nil, err
	}
	defer db.close()
	return db.readIdentity(opts)
}
