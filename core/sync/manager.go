package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/triedb"

	"nhbchain/core/types"
)

// Manager coordinates snapshot production and fast-sync verification for a node instance.
type Manager struct {
	mu         sync.Mutex
	chainID    uint64
	height     uint64
	trieDB     *triedb.Database
	writer     *SnapshotWriter
	loader     *SnapshotLoader
	validators *ValidatorSet
	governance GovernanceVerifier
}

// NewManager wires a fast-sync manager on top of the provided trie database.
func NewManager(chainID uint64, height uint64, trieDB *triedb.Database) *Manager {
	if trieDB == nil {
		return nil
	}
	return &Manager{
		chainID: chainID,
		height:  height,
		trieDB:  trieDB,
		writer:  NewSnapshotWriter(trieDB, defaultChunkSize),
		loader:  NewSnapshotLoader(trieDB),
	}
}

// SetValidatorSet configures the quorum verifier used for snapshot and block verification.
func (m *Manager) SetValidatorSet(set *ValidatorSet) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.validators = set
	m.mu.Unlock()
}

// SetGovernanceVerifier registers an optional governance anchor validator for manifest imports.
func (m *Manager) SetGovernanceVerifier(verifier GovernanceVerifier) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.governance = verifier
	m.mu.Unlock()
}

// ExportSnapshot writes a snapshot into the destination directory. The manifest is returned for signing.
func (m *Manager) ExportSnapshot(ctx context.Context, height uint64, root common.Hash, outDir string) (*SnapshotManifest, error) {
	if m == nil || m.writer == nil {
		return nil, fmt.Errorf("snapshot writer not configured")
	}
	m.mu.Lock()
	writer := m.writer
	chainID := m.chainID
	m.mu.Unlock()
	return writer.Write(ctx, chainID, height, root, outDir)
}

// ImportSnapshot verifies and applies the provided manifest and chunk directory into the active trie database.
func (m *Manager) ImportSnapshot(ctx context.Context, manifest *SnapshotManifest, chunkDir string) (common.Hash, error) {
	if m == nil || m.loader == nil {
		return common.Hash{}, fmt.Errorf("snapshot loader not configured")
	}
	if manifest == nil {
		return common.Hash{}, fmt.Errorf("nil manifest")
	}
	m.mu.Lock()
	validators := m.validators
	governance := m.governance
	loader := m.loader
	chainID := m.chainID
	m.mu.Unlock()
	if manifest.ChainID != 0 && manifest.ChainID != chainID {
		return common.Hash{}, fmt.Errorf("snapshot chain mismatch: manifest=%d local=%d", manifest.ChainID, chainID)
	}
	if err := VerifyManifest(manifest, validators, governance); err != nil {
		return common.Hash{}, err
	}
	return loader.Apply(ctx, manifest, chunkDir, manifest.Height)
}

// InstallSnapshotDir performs a full download+install flow using atomic swap semantics.
func (m *Manager) InstallSnapshotDir(ctx context.Context, manifest *SnapshotManifest, baseURL string, downloadDir string, targetPath string, fetcher *HTTPFetcher) error {
	if manifest == nil {
		return fmt.Errorf("nil manifest")
	}
	if downloadDir == "" {
		downloadDir = filepath.Join(targetPath, "snapshot")
	}
	if err := EnsureChunks(ctx, manifest, baseURL, downloadDir, fetcher); err != nil {
		return err
	}
	if err := InstallSnapshot(ctx, manifest, downloadDir, targetPath, nil); err != nil {
		return err
	}
	return nil
}

// RangeSync verifies block proofs from the provided checkpoint using the configured validator set.
func (m *Manager) RangeSync(ctx context.Context, checkpoint *SnapshotCheckpoint, fetcher ProofFetcher, applier HeaderApplier) (*types.BlockHeader, error) {
	if m == nil {
		return nil, fmt.Errorf("sync manager not configured")
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("nil checkpoint")
	}
	m.mu.Lock()
	validators := m.validators
	chainID := m.chainID
	m.mu.Unlock()
	if validators == nil {
		return nil, fmt.Errorf("validator set unavailable")
	}
	rangeSyncer := NewRangeSyncer(chainID, validators, applier)
	return rangeSyncer.Sync(ctx, checkpoint.Header, fetcher)
}

// SnapshotCheckpoint bundles the snapshot height and checkpoint header required to resume range sync.
type SnapshotCheckpoint struct {
	Height uint64
	Header *types.BlockHeader
}

// SetHeight updates the latest synced block height, used as metadata for future snapshot exports.
func (m *Manager) SetHeight(height uint64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.height = height
	m.mu.Unlock()
}

// Height returns the most recent synced block height recorded by the manager.
func (m *Manager) Height() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.height
}

// Close releases underlying resources. Currently a no-op but kept for API symmetry.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writer = nil
	m.loader = nil
	m.validators = nil
	m.governance = nil
}
