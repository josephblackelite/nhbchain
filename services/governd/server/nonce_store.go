package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// NonceStore persists the next nonce value across service restarts.
type NonceStore interface {
	Load() (uint64, error)
	Save(uint64) error
}

// FileNonceStore stores the next nonce value in a filesystem path.
type FileNonceStore struct {
	path string
}

// NewFileNonceStore constructs a filesystem-backed nonce store rooted at the provided path.
func NewFileNonceStore(path string) (*FileNonceStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("nonce store path required")
	}
	dir := filepath.Dir(trimmed)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create nonce store directory: %w", err)
		}
	}
	return &FileNonceStore{path: trimmed}, nil
}

// Load retrieves the persisted nonce value. A zero return indicates no prior state exists.
func (s *FileNonceStore) Load() (uint64, error) {
	if s == nil {
		return 0, fmt.Errorf("nonce store not initialised")
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read nonce store: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse nonce value: %w", err)
	}
	return value, nil
}

// Save writes the provided nonce value to disk atomically.
func (s *FileNonceStore) Save(value uint64) error {
	if s == nil {
		return fmt.Errorf("nonce store not initialised")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "nonce-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp nonce file: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	if _, err := tmp.WriteString(strconv.FormatUint(value, 10)); err != nil {
		cleanup()
		tmp.Close()
		return fmt.Errorf("write nonce value: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		tmp.Close()
		return fmt.Errorf("chmod nonce file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close nonce file: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		cleanup()
		return fmt.Errorf("replace nonce file: %w", err)
	}
	return nil
}

// RestoreNonce determines the initial nonce to use based on the persisted state and configured baseline.
func RestoreNonce(store NonceStore, baseline uint64) (uint64, error) {
	if baseline == 0 {
		baseline = 1
	}
	if store == nil {
		return baseline, nil
	}
	persisted, err := store.Load()
	if err != nil {
		return 0, err
	}
	if persisted == 0 {
		return baseline, nil
	}
	if persisted < baseline {
		return baseline, nil
	}
	if persisted > baseline {
		// Avoid decreasing the nonce below the persisted value.
		return persisted, nil
	}
	return baseline, nil
}
