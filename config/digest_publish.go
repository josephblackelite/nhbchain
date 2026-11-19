package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ConsensusDigestFileName is the file a node leaves in its data directory at
// start-up: the text form of its consensus-settings digest.
const ConsensusDigestFileName = "consensus_config_digest.txt"

// PublishConsensusDigest computes the digest of c's consensus settings, writes its
// text form to dir/ConsensusDigestFileName, and logs the root and one hash per
// section, so the operators of two validators can compare them before a block
// proves they disagree (PL-R5T-16, PL-R5T-30). Only hashes are logged and written.
//
// It never stops the node: a directory that cannot be written is a warning, and
// NHB_MASTER_TREASURY being set is reported at error level (it replaces the
// genesis admin wallet on this node alone, PL-DC-04) but the node still starts.
func (c *Config) PublishConsensusDigest(logger *slog.Logger, getenv func(string) string, dir string) (ConsensusDigest, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	d, err := c.ConsensusDigest(getenv)
	if err != nil {
		return ConsensusDigest{}, err
	}
	attrs := []any{slog.String("root", d.Root), slog.Int("version", d.Version)}
	for _, s := range d.Sections {
		attrs = append(attrs, slog.String(s.Name, s.Digest))
	}
	logger.Info("Consensus settings digest: the other validator must log the same root (nhb-snapshot check-parity compares two nodes' files)", attrs...)

	if v := getenv(treasuryOverrideEnv); strings.TrimSpace(v) != "" {
		logger.Error(treasuryOverrideEnv + " is set on this node: it replaces the treasury the genesis names, on this node only. If the other validator does not have the same value, the two compute different state. Unset it unless both validators carry it on purpose.")
	}

	if dir != "" {
		if err := writeFileAtomic(filepath.Join(dir, ConsensusDigestFileName), []byte(d.Text())); err != nil {
			logger.Warn("Could not write the consensus settings digest file", slog.String("dir", dir), slog.Any("error", err))
		}
	}
	return d, nil
}

// writeFileAtomic writes data next to path and renames it into place, so a reader
// never sees half a file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".digest-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
