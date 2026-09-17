package crypto

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/accounts/keystore"
)

// SaveToKeystore writes the provided private key to an Ethereum v3 keystore file at the given path.
// If the parent directory does not exist it will be created with 0700 permissions.
func SaveToKeystore(path string, key *PrivateKey, passphrase string) error {
	if key == nil {
		return errors.New("crypto: nil private key")
	}
	if path == "" {
		return errors.New("crypto: empty keystore path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp(dir, "keystore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	ks := keystore.NewKeyStore(tmpDir, keystore.StandardScryptN, keystore.StandardScryptP)
	if _, err := ks.ImportECDSA(key.PrivateKey, passphrase); err != nil {
		return err
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("crypto: failed to create keystore file")
	}

	src := filepath.Join(tmpDir, entries[0].Name())
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(src, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// LoadFromKeystore decrypts an Ethereum v3 keystore file using the supplied passphrase.
func LoadFromKeystore(path, passphrase string) (*PrivateKey, error) {
	if path == "" {
		return nil, errors.New("crypto: empty keystore path")
	}

	keyJSON, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	decrypted, err := keystore.DecryptKey(keyJSON, passphrase)
	if err != nil {
		return nil, err
	}

	return &PrivateKey{PrivateKey: decrypted.PrivateKey}, nil
}
