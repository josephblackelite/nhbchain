package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/storage"
)

// Blockchain manages the collection of blocks.
type Blockchain struct {
	db      storage.Database // Uses the generic Database interface
	tip     []byte
	height  uint64
	heights map[uint64][]byte
	mu      sync.RWMutex
	chainID uint64
}

var (
	tipKey        = []byte("tip")
	genesisKey    = []byte("genesis")
	heightKeyName = []byte("height")
	heightPrefix  = []byte("height:")
	hashPrefix    = []byte("hash:")
)

func encodeUint64(v uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, v)
	return buf
}

func decodeUint64(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}

func heightKey(height uint64) []byte {
	key := make([]byte, len(heightPrefix)+8)
	copy(key, heightPrefix)
	binary.BigEndian.PutUint64(key[len(heightPrefix):], height)
	return key
}

func hashKey(hash []byte) []byte {
	key := make([]byte, len(hashPrefix)+len(hash))
	copy(key, hashPrefix)
	copy(key[len(hashPrefix):], hash)
	return key
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// NewBlockchain creates a new blockchain using the provided Database interface.
func NewBlockchain(db storage.Database, genesisPath string, allowAutogenesis bool) (*Blockchain, error) {
	bc := &Blockchain{
		db:      db,
		heights: make(map[uint64][]byte),
	}

	genesisHash, err := db.Get(genesisKey)
	if err != nil {
		trimmedPath := strings.TrimSpace(genesisPath)
		genesis, spec, loadErr := genesisFromSource(trimmedPath, allowAutogenesis, db)
		if loadErr != nil {
			return nil, loadErr
		}

		genesisHash, err = persistGenesisBlock(db, genesis)
		if err != nil {
			return nil, err
		}

		if len(genesisHash) < 8 {
			return nil, fmt.Errorf("genesis hash too short: %d", len(genesisHash))
		}
		bc.tip = cloneBytes(genesisHash)
		bc.height = 0
		bc.heights[0] = cloneBytes(genesisHash)
		bc.chainID = binary.BigEndian.Uint64(genesisHash[:8])
		if spec != nil {
			fmt.Printf("Loaded genesis from %s  hash=0x%x  chainID=%d  accounts=%d  validators=%d\n",
				trimmedPath, genesisHash, bc.chainID, len(spec.Alloc), len(spec.Validators))
		}
		return bc, nil
	}

	// Existing chain: load tip, height, and the height index.
	fmt.Println("Found existing genesis block.")
	tipHash, err := db.Get(tipKey)
	if err != nil {
		return nil, fmt.Errorf("load tip: %w", err)
	}
	bc.tip = cloneBytes(tipHash)

	heightBytes, err := db.Get(heightKeyName)
	if err != nil {
		return nil, fmt.Errorf("load height: %w", err)
	}
	bc.height = decodeUint64(heightBytes)

	for i := uint64(0); i <= bc.height; i++ {
		hashBytes, err := db.Get(heightKey(i))
		if err != nil {
			return nil, fmt.Errorf("load height index %d: %w", i, err)
		}
		bc.heights[i] = cloneBytes(hashBytes)
	}

	genesisHash, ok := bc.heights[0]
	if !ok {
		return nil, fmt.Errorf("missing genesis hash in height index")
	}
	if len(genesisHash) < 8 {
		return nil, fmt.Errorf("genesis hash too short: %d", len(genesisHash))
	}
	bc.chainID = binary.BigEndian.Uint64(genesisHash[:8])

	return bc, nil
}

// AddBlock validates a new block and adds it to the chain.
func (bc *Blockchain) AddBlock(b *types.Block) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// Basic linkage check: parent hash must match current tip.
	if !bytes.Equal(b.Header.PrevHash, bc.tip) {
		return fmt.Errorf("block prevhash mismatch")
	}

	// Verify TxRoot against the computed root for this block's tx list.
	expectedTxRoot, err := ComputeTxRoot(b.Transactions)
	if err != nil {
		return fmt.Errorf("compute tx root: %w", err)
	}
	if !bytes.Equal(expectedTxRoot, b.Header.TxRoot) {
		return fmt.Errorf("transaction root mismatch")
	}

	// Persist block
	blockBytes, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal block: %w", err)
	}
	blockHash, err := b.Header.Hash()
	if err != nil {
		return fmt.Errorf("hash block: %w", err)
	}

	if err := bc.db.Put(blockHash, blockBytes); err != nil {
		return fmt.Errorf("store block: %w", err)
	}
	if err := bc.db.Put(tipKey, blockHash); err != nil {
		return fmt.Errorf("store tip: %w", err)
	}

	newHeight := bc.height + 1
	if err := bc.db.Put(heightKeyName, encodeUint64(newHeight)); err != nil {
		return fmt.Errorf("store height: %w", err)
	}
	if err := bc.db.Put(heightKey(newHeight), blockHash); err != nil {
		return fmt.Errorf("store height index: %w", err)
	}
	if err := bc.db.Put(hashKey(blockHash), encodeUint64(newHeight)); err != nil {
		return fmt.Errorf("store hash index: %w", err)
	}

	// Update in-memory pointers after successful persistence.
	bc.tip = cloneBytes(blockHash)
	bc.height = newHeight
	bc.heights[newHeight] = cloneBytes(blockHash)

	fmt.Printf("Added new block! Height: %d, Hash: %x\n", bc.height, blockHash)
	return nil
}

// GetBlockByHash retrieves a block from the database by its hash.
func (bc *Blockchain) GetBlockByHash(hash []byte) (*types.Block, error) {
	blockBytes, err := bc.db.Get(hash)
	if err != nil {
		return nil, err
	}
	var block types.Block
	if err := json.Unmarshal(blockBytes, &block); err != nil {
		return nil, err
	}
	return &block, nil
}

// GetBlockByHeight retrieves a block by its height.
func (bc *Blockchain) GetBlockByHeight(height uint64) (*types.Block, error) {
	bc.mu.RLock()
	hash, ok := bc.heights[height]
	bc.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("block at height %d not found", height)
	}
	return bc.GetBlockByHash(hash)
}

// GetBlocks retrieves a sequence of blocks from a starting height.
func (bc *Blockchain) GetBlocks(fromHeight uint64) ([]*types.Block, error) {
	bc.mu.RLock()
	currentHeight := bc.height
	bc.mu.RUnlock()

	var blocks []*types.Block
	for i := fromHeight; i <= currentHeight; i++ {
		block, err := bc.GetBlockByHeight(i)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (bc *Blockchain) GetHeight() uint64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.height
}

func (bc *Blockchain) Tip() []byte {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.tip
}

func createGenesisBlock() *types.Block {
	header := &types.BlockHeader{
		Height:    0,
		Timestamp: 1672531200,
		PrevHash:  []byte{},
		StateRoot: gethtypes.EmptyRootHash.Bytes(),
	}
	return types.NewBlock(header, []*types.Transaction{})
}

func genesisFromSource(path string, allowAutogenesis bool, db storage.Database) (*types.Block, *genesis.GenesisSpec, error) {
	if path != "" {
		fmt.Printf("No genesis block found. Loading from %s.\n", path)
		spec, err := genesis.LoadGenesisSpec(path)
		if err != nil {
			return nil, nil, err
		}
		block, finalize, err := genesis.BuildGenesisFromSpec(spec, db)
		if err != nil {
			return nil, nil, err
		}
		hash, err := block.Header.Hash()
		if err != nil {
			return nil, nil, fmt.Errorf("hash genesis: %w", err)
		}
		if len(hash) < 8 {
			return nil, nil, fmt.Errorf("genesis hash too short: %d", len(hash))
		}
		derivedID := binary.BigEndian.Uint64(hash[:8])
		if spec.ChainID != nil && *spec.ChainID != derivedID {
			return nil, nil, fmt.Errorf("chainId mismatch: spec=%d derived=%d", *spec.ChainID, derivedID)
		}
		if finalize != nil {
			if err := finalize(); err != nil {
				return nil, nil, err
			}
		}
		return block, spec, nil
	}

	if !allowAutogenesis {
		return nil, nil, fmt.Errorf("no genesis block present and autogenesis disabled")
	}

	fmt.Println("Auto-genesis created (dev mode)")
	return createGenesisBlock(), nil, nil
}

func persistGenesisBlock(db storage.Database, genesis *types.Block) ([]byte, error) {
	if genesis == nil {
		return nil, fmt.Errorf("genesis block is nil")
	}
	if genesis.Header == nil {
		return nil, fmt.Errorf("genesis block missing header")
	}

	genesisBytes, err := json.Marshal(genesis)
	if err != nil {
		return nil, fmt.Errorf("marshal genesis: %w", err)
	}

	genesisHash, err := genesis.Header.Hash()
	if err != nil {
		return nil, fmt.Errorf("hash genesis: %w", err)
	}

	if err := db.Put(genesisHash, genesisBytes); err != nil {
		return nil, fmt.Errorf("store genesis block: %w", err)
	}
	if err := db.Put(genesisKey, genesisHash); err != nil {
		return nil, fmt.Errorf("store genesis hash: %w", err)
	}
	if err := db.Put(tipKey, genesisHash); err != nil {
		return nil, fmt.Errorf("store tip: %w", err)
	}
	if err := db.Put(heightKeyName, encodeUint64(0)); err != nil {
		return nil, fmt.Errorf("store height: %w", err)
	}
	if err := db.Put(heightKey(0), genesisHash); err != nil {
		return nil, fmt.Errorf("store height index: %w", err)
	}
	if err := db.Put(hashKey(genesisHash), encodeUint64(0)); err != nil {
		return nil, fmt.Errorf("store hash index: %w", err)
	}

	return genesisHash, nil
}

func (bc *Blockchain) CurrentHeader() *types.BlockHeader {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	// A more robust implementation would handle the case where tip is nil
	block, _ := bc.GetBlockByHash(bc.tip)
	if block != nil {
		return block.Header
	}
	return nil
}

// ChainID returns the identifier derived from the genesis block hash.
func (bc *Blockchain) ChainID() uint64 {
	return bc.chainID
}
