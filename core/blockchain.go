package core

import (
	"encoding/json"
	"fmt"
	"sync"

	"nhbchain/core/types"
	"nhbchain/storage"
)

// Blockchain manages the collection of blocks.
type Blockchain struct {
	db      storage.Database // CHANGED: Now uses the generic Database interface
	tip     []byte
	height  uint64
	heights map[uint64][]byte
	mu      sync.RWMutex
}

// NewBlockchain creates a new blockchain using the provided Database interface.
func NewBlockchain(db storage.Database) (*Blockchain, error) { // CHANGED: Accepts the interface
	bc := &Blockchain{
		db:      db, // Use the provided database
		heights: make(map[uint64][]byte),
	}

	genesisHash, err := db.Get([]byte("genesis"))
	if err != nil {
		fmt.Println("No genesis block found. Creating a new one.")
		genesis := createGenesisBlock()
		genesisBytes, _ := json.Marshal(genesis)
		genesisHash, _ = genesis.Header.Hash()

		db.Put(genesisHash, genesisBytes)
		db.Put([]byte("genesis"), genesisHash)
		db.Put([]byte("tip"), genesisHash)

		bc.tip = genesisHash
		bc.height = 0
		bc.heights[0] = genesisHash
	} else {
		// A full implementation would load the entire heights map from the DB on startup.
		fmt.Println("Found existing genesis block.")
		tipHash, _ := db.Get([]byte("tip"))
		bc.tip = tipHash
		// To make this fully persistent, we would also need to load the last known height
		// and rebuild the heights map here. For now, this is sufficient for the simulation.
	}

	return bc, nil
}

// AddBlock validates a new block and adds it to the chain.
func (bc *Blockchain) AddBlock(b *types.Block) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if string(b.Header.PrevHash) != string(bc.tip) {
		return fmt.Errorf("block prevhash mismatch")
	}

	blockBytes, _ := json.Marshal(b)
	blockHash, _ := b.Header.Hash()

	bc.db.Put(blockHash, blockBytes)
	bc.db.Put([]byte("tip"), blockHash)
	bc.tip = blockHash
	bc.height++
	bc.heights[bc.height] = blockHash

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
		StateRoot: []byte{},
	}
	return types.NewBlock(header, []*types.Transaction{})
}
