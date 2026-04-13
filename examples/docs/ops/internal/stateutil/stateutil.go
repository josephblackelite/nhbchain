package stateutil

import (
	"context"
	"fmt"
	"time"

	consclient "nhbchain/consensus/client"
	"nhbchain/core/state"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

// Snapshot captures a read-only view of the latest application state along with
// metadata describing the block it was sourced from.
type Snapshot struct {
	Manager   *state.Manager
	Height    uint64
	Timestamp time.Time

	db     *storage.LevelDB
	client *consclient.Client
}

// Load connects to the consensus gRPC endpoint, fetches the latest block, and
// opens the application state trie from the provided data directory.
func Load(ctx context.Context, dbPath, consensusEndpoint string) (*Snapshot, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("data directory is required")
	}
	if consensusEndpoint == "" {
		consensusEndpoint = "localhost:9090"
	}

	client, err := consclient.Dial(ctx, consensusEndpoint)
	if err != nil {
		return nil, fmt.Errorf("dial consensus: %w", err)
	}

	height, err := client.GetHeight(ctx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("get height: %w", err)
	}
	block, err := client.GetBlockByHeight(ctx, height)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("get block %d: %w", height, err)
	}
	if block == nil || block.Header == nil {
		_ = client.Close()
		return nil, fmt.Errorf("block %d missing header", height)
	}

	db, err := storage.NewLevelDB(dbPath)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("open leveldb: %w", err)
	}

	trie, err := trie.NewTrie(db, block.Header.StateRoot)
	if err != nil {
		db.Close()
		_ = client.Close()
		return nil, fmt.Errorf("open state trie: %w", err)
	}

	snapshot := &Snapshot{
		Manager:   state.NewManager(trie),
		Height:    height,
		Timestamp: time.Unix(block.Header.Timestamp, 0).UTC(),
		db:        db,
		client:    client,
	}
	return snapshot, nil
}

// Close releases the underlying consensus and database handles.
func (s *Snapshot) Close() {
	if s == nil {
		return
	}
	if s.db != nil {
		s.db.Close()
	}
	if s.client != nil {
		_ = s.client.Close()
	}
}
