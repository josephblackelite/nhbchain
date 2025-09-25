package sync

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// ManifestVersion is the snapshot manifest format version exported by the fast-sync
// pipeline. Versioning allows wire compatibility without breaking older
// deployments.
const ManifestVersion = 1

// SnapshotManifest describes the content of a state snapshot.
type SnapshotManifest struct {
	Version      int               `json:"version"`
	ChainID      uint64            `json:"chainId"`
	Height       uint64            `json:"height"`
	StateRoot    []byte            `json:"stateRoot"`
	Checkpoint   []byte            `json:"checkpoint"`
	ChunkSize    int64             `json:"chunkSize"`
	TotalEntries uint64            `json:"totalEntries"`
	TotalBytes   uint64            `json:"totalBytes"`
	Chunks       []ChunkMeta       `json:"chunks"`
	Signatures   []ValidatorSig    `json:"signatures"`
	Governance   *GovernanceAnchor `json:"governance,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ChunkMeta describes a single chunk file in the snapshot.
type ChunkMeta struct {
	Index   int    `json:"index"`
	Path    string `json:"path"`
	Entries uint64 `json:"entries"`
	Bytes   uint64 `json:"bytes"`
	Hash    []byte `json:"hash"`
}

// ValidatorSig represents a validator signature over the manifest digest.
type ValidatorSig struct {
	Address   []byte `json:"address"`
	Signature []byte `json:"signature"`
	Weight    uint64 `json:"weight"`
}

// GovernanceAnchor allows shipping a governance approved checkpoint root when the
// validator quorum is unavailable (e.g. for testnets).
type GovernanceAnchor struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// Digest returns the canonical hash for the manifest used by validators when
// signing snapshot releases.
func (m *SnapshotManifest) Digest() ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("nil manifest")
	}
	copy := *m
	copy.Signatures = nil
	serialized, err := json.Marshal(copy)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(serialized)
	return hash[:], nil
}

// SortedChunks returns a copy of the chunk list ordered by index.
func (m *SnapshotManifest) SortedChunks() []ChunkMeta {
	if m == nil {
		return nil
	}
	out := make([]ChunkMeta, len(m.Chunks))
	copy(out, m.Chunks)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Index == out[j].Index {
			return out[i].Path < out[j].Path
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// Copy returns a deep copy of the manifest.
func (m *SnapshotManifest) Copy() *SnapshotManifest {
	if m == nil {
		return nil
	}
	dup := *m
	dup.StateRoot = append([]byte(nil), m.StateRoot...)
	dup.Checkpoint = append([]byte(nil), m.Checkpoint...)
	dup.Chunks = append([]ChunkMeta(nil), m.Chunks...)
	dup.Signatures = append([]ValidatorSig(nil), m.Signatures...)
	if m.Metadata != nil {
		dup.Metadata = make(map[string]string, len(m.Metadata))
		for k, v := range m.Metadata {
			dup.Metadata[k] = v
		}
	}
	if m.Governance != nil {
		anchor := *m.Governance
		anchor.Payload = append([]byte(nil), m.Governance.Payload...)
		anchor.Signature = append([]byte(nil), m.Governance.Signature...)
		dup.Governance = &anchor
	}
	return &dup
}
