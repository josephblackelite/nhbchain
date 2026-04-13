package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backend enumerates supported secret backends.
type Backend string

const (
	// BackendEnv loads secrets from environment variables.
	BackendEnv Backend = "env"
	// BackendFilesystem loads secrets from files under a root directory.
	BackendFilesystem Backend = "filesystem"
)

// Config describes the secret backend wiring for the OTC gateway.
type Config struct {
	Backend Backend
	// BasePath is used by filesystem backends to locate secret files.
	BasePath string
}

// Manager implements auth.SecretProvider using the configured backend.
type Manager struct {
	backend Backend
	baseDir string
}

// NewManager constructs a Manager for the supplied configuration.
func NewManager(cfg Config) (*Manager, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = BackendEnv
	}

	switch backend {
	case BackendEnv:
		return &Manager{backend: backend}, nil
	case BackendFilesystem:
		base := strings.TrimSpace(cfg.BasePath)
		if base == "" {
			return nil, errors.New("filesystem secret backend requires base path")
		}
		info, err := os.Stat(base)
		if err != nil {
			return nil, fmt.Errorf("stat secret directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("secret base path %s is not a directory", base)
		}
		return &Manager{backend: backend, baseDir: base}, nil
	default:
		return nil, fmt.Errorf("unsupported secret backend %q", backend)
	}
}

// GetSecret resolves the value associated with name using the configured backend.
func (m *Manager) GetSecret(ctx context.Context, name string) (string, error) {
	_ = ctx
	if m == nil {
		return "", errors.New("secret manager not configured")
	}
	switch m.backend {
	case BackendEnv:
		if name == "" {
			return "", errors.New("secret name required")
		}
		return strings.TrimSpace(os.Getenv(name)), nil
	case BackendFilesystem:
		if name == "" {
			return "", errors.New("secret name required")
		}
		clean := filepath.Clean(name)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, `..`+string(os.PathSeparator)) {
			return "", fmt.Errorf("secret name %q is invalid", name)
		}
		if filepath.IsAbs(clean) {
			return "", fmt.Errorf("secret name %q must be relative", name)
		}
		path := filepath.Join(m.baseDir, clean)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	default:
		return "", fmt.Errorf("unsupported secret backend %q", m.backend)
	}
}
