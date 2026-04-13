package storage

import (
	"fmt"
	"path/filepath"
	"strings"
)

const defaultFilePragmas = "mode=rwc&_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on"

// FileDSN converts a filesystem path into an on-disk SQLite DSN with sensible
// defaults. Callers must ensure the path is non-empty.
func FileDSN(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", ErrPathRequired
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve storage path: %w", err)
	}
	return fmt.Sprintf("file:%s?%s", abs, defaultFilePragmas), nil
}
