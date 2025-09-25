package sync

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

const (
	defaultChunkSize    = 16 * 1024 * 1024
	chunkFilePermission = 0o644
)

// SnapshotWriter walks the canonical state trie and persists compacted chunks to
// disk. Chunks are length-prefixed records composed of hashed trie keys and
// values. The format is intentionally simple so auditors can re-implement it in
// other languages.
type SnapshotWriter struct {
	trieDB    *triedb.Database
	chunkSize int64
}

// NewSnapshotWriter builds a writer bound to the provided trie database.
func NewSnapshotWriter(trieDB *triedb.Database, chunkSize int64) *SnapshotWriter {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	return &SnapshotWriter{trieDB: trieDB, chunkSize: chunkSize}
}

// Write produces a manifest by traversing the state trie rooted at stateRoot and
// persisting chunk files under outDir. The manifest is returned but not signed.
func (w *SnapshotWriter) Write(ctx context.Context, chainID uint64, height uint64, stateRoot common.Hash, outDir string) (*SnapshotManifest, error) {
	if w == nil || w.trieDB == nil {
		return nil, fmt.Errorf("snapshot writer not initialised")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create snapshot directory: %w", err)
	}

	trie, err := gethtrie.New(gethtrie.TrieID(stateRoot), w.trieDB)
	if err != nil {
		return nil, fmt.Errorf("open trie: %w", err)
	}
	nodeIter, err := trie.NodeIterator(nil)
	if err != nil {
		return nil, fmt.Errorf("node iterator: %w", err)
	}
	iterator := gethtrie.NewIterator(nodeIter)

	manifest := &SnapshotManifest{
		Version:   ManifestVersion,
		ChainID:   chainID,
		Height:    height,
		StateRoot: common.CopyBytes(stateRoot[:]),
		ChunkSize: w.chunkSize,
		Chunks:    make([]ChunkMeta, 0),
		Metadata:  map[string]string{"createdAt": time.Now().UTC().Format(time.RFC3339)},
	}

	var (
		index     int
		chunkPath string
		chunkFile *os.File
		writer    *bufio.Writer
		written   int64
		entries   uint64
	)

	flushChunk := func(final bool) error {
		if chunkFile == nil {
			return nil
		}
		if writer != nil {
			if err := writer.Flush(); err != nil {
				_ = chunkFile.Close()
				return err
			}
		}
		if err := chunkFile.Sync(); err != nil {
			_ = chunkFile.Close()
			return err
		}
		if err := chunkFile.Close(); err != nil {
			return err
		}
		hash, size, err := hashFile(chunkPath)
		if err != nil {
			return err
		}
		manifest.TotalBytes += uint64(size)
		manifest.TotalEntries += entries
		manifest.Chunks = append(manifest.Chunks, ChunkMeta{
			Index:   index,
			Path:    filepath.Base(chunkPath),
			Entries: entries,
			Bytes:   uint64(size),
			Hash:    hash,
		})
		if final {
			return nil
		}
		index++
		chunkFile = nil
		writer = nil
		written = 0
		entries = 0
		return nil
	}

	defer func() {
		_ = flushChunk(true)
	}()

	for iterator.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if chunkFile == nil {
			chunkPath = filepath.Join(outDir, fmt.Sprintf("chunk-%04d.bin", index))
			file, err := os.OpenFile(chunkPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, chunkFilePermission)
			if err != nil {
				return nil, fmt.Errorf("open chunk %d: %w", index, err)
			}
			chunkFile = file
			writer = bufio.NewWriter(chunkFile)
			written = 0
			entries = 0
		}

		key := iterator.Key
		value := iterator.Value
		if len(key) == 0 {
			continue
		}
		if err := writeRecord(writer, key, value); err != nil {
			return nil, fmt.Errorf("write chunk record: %w", err)
		}
		entries++
		written += int64(8 + len(key) + len(value))
		if written >= w.chunkSize {
			if err := flushChunk(false); err != nil {
				return nil, fmt.Errorf("flush chunk: %w", err)
			}
		}
	}

	if iterator.Err != nil {
		return nil, fmt.Errorf("iterate trie: %w", iterator.Err)
	}

	if err := flushChunk(true); err != nil {
		return nil, err
	}

	// Persist the state root to the manifest metadata for sanity checks.
	manifest.Metadata["stateRootHex"] = stateRoot.Hex()
	return manifest, nil
}

func writeRecord(w io.Writer, key []byte, value []byte) error {
	if err := binary.Write(w, binary.BigEndian, uint32(len(key))); err != nil {
		return err
	}
	if _, err := w.Write(key); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(value))); err != nil {
		return err
	}
	if _, err := w.Write(value); err != nil {
		return err
	}
	return nil
}

func hashFile(path string) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	hasher := sha256Pool.Get()
	defer sha256Pool.Put(hasher)
	h := hasher.(hash.Hash)
	h.Reset()
	size, err := io.Copy(h, file)
	if err != nil {
		return nil, 0, err
	}
	sum := h.Sum(nil)
	return sum, size, nil
}

var sha256Pool = sync.Pool{New: func() interface{} { return sha256.New() }}
