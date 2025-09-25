package sync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"

	"nhbchain/storage"
)

// SnapshotLoader restores a trie database from chunked snapshot files.
type SnapshotLoader struct {
	trieDB *triedb.Database
}

// NewSnapshotLoader creates a loader bound to a trie database.
func NewSnapshotLoader(trieDB *triedb.Database) *SnapshotLoader {
	return &SnapshotLoader{trieDB: trieDB}
}

// Apply reads snapshot chunks, verifies their hashes, and installs the resulting
// state root into the destination trie database. The returned root must match the
// manifest state root or an error is produced.
func (l *SnapshotLoader) Apply(ctx context.Context, manifest *SnapshotManifest, chunkDir string, height uint64) (common.Hash, error) {
	if l == nil || l.trieDB == nil {
		return common.Hash{}, fmt.Errorf("snapshot loader not initialised")
	}
	if manifest == nil {
		return common.Hash{}, fmt.Errorf("nil manifest")
	}
	trie, err := gethtrie.New(gethtrie.TrieID(gethtypes.EmptyRootHash), l.trieDB)
	if err != nil {
		return common.Hash{}, fmt.Errorf("build empty trie: %w", err)
	}

	chunks := manifest.SortedChunks()
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return common.Hash{}, ctx.Err()
		default:
		}
		path := filepath.Join(chunkDir, chunk.Path)
		if err := verifyChunk(path, chunk.Hash); err != nil {
			return common.Hash{}, fmt.Errorf("verify chunk %s: %w", chunk.Path, err)
		}
		file, err := os.Open(path)
		if err != nil {
			return common.Hash{}, fmt.Errorf("open chunk %s: %w", chunk.Path, err)
		}
		reader := bufio.NewReader(file)
		count := uint64(0)
		for {
			key, value, err := readRecord(reader)
			if err != nil {
				if err == io.EOF {
					break
				}
				file.Close()
				return common.Hash{}, fmt.Errorf("read chunk %s: %w", chunk.Path, err)
			}
			if len(key) == 0 {
				continue
			}
			if err := trie.Update(key, value); err != nil {
				file.Close()
				return common.Hash{}, fmt.Errorf("update trie: %w", err)
			}
			count++
		}
		file.Close()
		if chunk.Entries > 0 && count != chunk.Entries {
			return common.Hash{}, fmt.Errorf("chunk %s entry mismatch: expected %d got %d", chunk.Path, chunk.Entries, count)
		}
	}

	root, nodes := trie.Commit(false)
	merged := trienode.NewMergedNodeSet()
	if nodes != nil {
		if err := merged.Merge(nodes); err != nil {
			return common.Hash{}, fmt.Errorf("merge trie nodes: %w", err)
		}
	}
	if err := l.trieDB.Update(root, common.Hash{}, height, merged, nil); err != nil {
		return common.Hash{}, fmt.Errorf("flush trie: %w", err)
	}
	if !bytes.Equal(root[:], manifest.StateRoot) {
		return common.Hash{}, fmt.Errorf("state root mismatch: manifest %x computed %x", manifest.StateRoot, root)
	}
	return root, nil
}

func verifyChunk(path string, expectedHash []byte) error {
	hash, err := fileHash(path)
	if err != nil {
		return err
	}
	if len(expectedHash) > 0 && !bytes.Equal(hash, expectedHash) {
		return fmt.Errorf("hash mismatch: expected %x got %x", expectedHash, hash)
	}
	return nil
}

func fileHash(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	hasher := sha256Pool.Get()
	defer sha256Pool.Put(hasher)
	h := hasher.(hash.Hash)
	h.Reset()
	if _, err := io.Copy(h, file); err != nil {
		return nil, err
	}
	sum := h.Sum(nil)
	return sum, nil
}

func readRecord(r *bufio.Reader) ([]byte, []byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, nil, err
	}
	keyLen := binary.BigEndian.Uint32(header)
	if keyLen == 0 {
		return nil, nil, nil
	}
	key := make([]byte, keyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, nil, err
	}
	valueLen := binary.BigEndian.Uint32(header)
	value := make([]byte, valueLen)
	if _, err := io.ReadFull(r, value); err != nil {
		return nil, nil, err
	}
	return key, value, nil
}

// HTTPFetcher retrieves snapshot chunks and manifests from remote peers with TLS
// pinning.
type HTTPFetcher struct {
	Client       *http.Client
	PinnedSHA256 []byte
}

// Fetch downloads the provided URL and enforces the configured TLS fingerprint.
func (f *HTTPFetcher) Fetch(ctx context.Context, url string) ([]byte, error) {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.TLS != nil && len(f.PinnedSHA256) > 0 {
		if len(resp.TLS.PeerCertificates) == 0 {
			return nil, fmt.Errorf("missing TLS peer certificate for pinning")
		}
		cert := resp.TLS.PeerCertificates[0]
		sum := sha256.Sum256(cert.Raw)
		if !bytes.Equal(sum[:], f.PinnedSHA256) {
			return nil, fmt.Errorf("TLS fingerprint mismatch")
		}
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// EnsureChunks verifies existing files and resumes downloads if they are missing
// or corrupted. It provides resumable semantics by skipping already verified
// chunks.
func EnsureChunks(ctx context.Context, manifest *SnapshotManifest, baseURL string, outDir string, fetcher *HTTPFetcher) error {
	if manifest == nil {
		return fmt.Errorf("nil manifest")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for _, chunk := range manifest.SortedChunks() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		path := filepath.Join(outDir, chunk.Path)
		if err := verifyChunk(path, chunk.Hash); err == nil {
			continue
		}
		if fetcher == nil {
			return fmt.Errorf("missing fetcher for chunk %s", chunk.Path)
		}
		url := fmt.Sprintf("%s/%s", strings.TrimRight(baseURL, "/"), chunk.Path)
		data, err := fetcher.Fetch(ctx, url)
		if err != nil {
			return fmt.Errorf("download chunk %s: %w", chunk.Path, err)
		}
		if err := os.WriteFile(path, data, chunkFilePermission); err != nil {
			return fmt.Errorf("write chunk %s: %w", chunk.Path, err)
		}
		if err := verifyChunk(path, chunk.Hash); err != nil {
			return fmt.Errorf("post-download verification failed for %s: %w", chunk.Path, err)
		}
	}
	return nil
}

// InstallSnapshot downloads (if required) and installs a snapshot into the target
// database directory using an atomic swap strategy.
func InstallSnapshot(ctx context.Context, manifest *SnapshotManifest, chunkDir string, targetPath string, factory func(string) (storage.Database, error)) error {
	if manifest == nil {
		return fmt.Errorf("nil manifest")
	}
	if factory == nil {
		factory = func(path string) (storage.Database, error) {
			return storage.NewLevelDB(path)
		}
	}
	tmpPath := targetPath + ".tmp"
	backupPath := targetPath + ".bak"

	_ = os.RemoveAll(tmpPath)
	_ = os.RemoveAll(backupPath)

	db, err := factory(tmpPath)
	if err != nil {
		return fmt.Errorf("open temp db: %w", err)
	}
	loader := NewSnapshotLoader(db.TrieDB())
	if _, err := loader.Apply(ctx, manifest, chunkDir, manifest.Height); err != nil {
		db.Close()
		_ = os.RemoveAll(tmpPath)
		return err
	}
	db.Close()
	if err := os.Rename(targetPath, backupPath); err != nil && !os.IsNotExist(err) {
		_ = os.RemoveAll(tmpPath)
		return fmt.Errorf("rename existing db: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("activate snapshot: %w", err)
	}
	return nil
}
