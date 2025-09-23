package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

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
func NewBlockchain(db storage.Database) (*Blockchain, error) {
	bc := &Blockchain{
		db:      db,
		heights: make(map[uint64][]byte),
	}

	genesisHash, err := db.Get(genesisKey)
	if err != nil {
		// No genesis present; create and persist one.
		fmt.Println("No genesis block found. Creating a new one.")
		genesis := createGenesisBlock()

		genesisBytes, err := json.Marshal(genesis)
		if err != nil {
			return nil, fmt.Errorf("marshal genesis: %w", err)
		}
		genesisHash, err = genesis.Header.Hash()
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

		bc.tip = cloneBytes(genesisHash)
		bc.height = 0
		bc.heights[0] = cloneBytes(genesisHash)
	} else {
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
	}

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
