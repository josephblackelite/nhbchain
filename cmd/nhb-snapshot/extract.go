package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// defaultMaxBytes bounds the uncompressed size a manifest may announce.
const defaultMaxBytes int64 = 64 << 30

// checkUnpackSize refuses a manifest that announces more uncompressed bytes than
// maxBytes (defaultMaxBytes when it is not positive). verify and extract both
// call it before they read the archive, so an archive is never unpacked, or even
// decompressed, on the strength of a size nobody bounded.
func checkUnpackSize(m *manifest, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	if m.Archive.UncompressedSize > maxBytes {
		return fmt.Errorf("the manifest announces %d bytes, above the allowed %d", m.Archive.UncompressedSize, maxBytes)
	}
	return nil
}

// hashRegularFile returns the size and sha256 of the regular file at path. A
// symbolic link or any other kind of file is refused.
func hashRegularFile(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return n, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// verifyArchiveFile checks the archive file against the size and sha256 in the
// manifest.
func verifyArchiveFile(path string, m *manifest) error {
	size, sum, err := hashRegularFile(path)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	if size != m.Archive.Size {
		return fmt.Errorf("the archive is %d bytes, the manifest says %d (a truncated or replaced download)", size, m.Archive.Size)
	}
	if sum != m.Archive.Sha256 {
		return fmt.Errorf("the archive sha256 is %s, the manifest says %s", sum, m.Archive.Sha256)
	}
	return nil
}

// archiveSink receives the content of each accepted archive entry.
type archiveSink interface {
	create(name string, size int64) (io.WriteCloser, error)
}

type discardSink struct{}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (discardSink) create(string, int64) (io.WriteCloser, error) {
	return nopWriteCloser{io.Discard}, nil
}

// dirSink writes entries as new files of one directory. Entry names have
// already been reduced to chain database file names, which cannot hold a path
// separator, so a file can only ever be created directly inside dir, and
// O_EXCL refuses to write through anything already there.
type dirSink struct{ dir string }

func (s dirSink) create(name string, _ int64) (io.WriteCloser, error) {
	if !allowedFileName(name) {
		return nil, fmt.Errorf("refusing to create %q", name)
	}
	return os.OpenFile(filepath.Join(s.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

// zeroChecker accepts only zero bytes: the padding a tar archive may end in.
type zeroChecker struct{}

func (zeroChecker) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, errors.New("data after the end of the tar archive")
		}
	}
	return len(p), nil
}

func describeType(t byte) string {
	switch t {
	case tar.TypeSymlink:
		return "a symbolic link"
	case tar.TypeLink:
		return "a hard link"
	case tar.TypeChar:
		return "a character device"
	case tar.TypeBlock:
		return "a block device"
	case tar.TypeFifo:
		return "a named pipe"
	case tar.TypeDir:
		return "a directory"
	case tar.TypeXGlobalHeader:
		return "a global header"
	default:
		return fmt.Sprintf("of type %q", string(rune(t)))
	}
}

// walkArchive reads the archive and hands each entry to sink, applying every
// rule on the way: only regular files, only the file names the manifest lists,
// each exactly once, with exactly the size and sha256 the manifest gives, and
// nothing after the tar trailer. Nothing is trusted from the archive itself.
func walkArchive(r io.Reader, m *manifest, sink archiveSink) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("the archive is not a gzip stream: %w", err)
	}
	defer gz.Close()

	// The tar stream is the file contents plus a 512-byte header and padding
	// per file plus the trailer; anything beyond that is not a snapshot.
	limit := m.Archive.UncompressedSize + int64(len(m.Archive.Files))*1024 + 64*1024
	lr := &io.LimitedReader{R: gz, N: limit + 1}
	tr := tar.NewReader(lr)

	seen := make(map[string]bool, len(m.Archive.Files))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("the archive entry %q is %s; only regular files are allowed", hdr.Name, describeType(hdr.Typeflag))
		}
		if hdr.Linkname != "" {
			return fmt.Errorf("the archive entry %q carries a link target", hdr.Name)
		}
		if !allowedFileName(hdr.Name) {
			return fmt.Errorf("the archive entry %q is not a chain database file name (paths, absolute names, key material and node state files are refused)", hdr.Name)
		}
		want, ok := m.file(hdr.Name)
		if !ok {
			return fmt.Errorf("the archive entry %q is not listed in the manifest", hdr.Name)
		}
		if seen[hdr.Name] {
			return fmt.Errorf("the archive holds %q twice", hdr.Name)
		}
		seen[hdr.Name] = true
		if hdr.Size != want.Size {
			return fmt.Errorf("the archive entry %q is %d bytes, the manifest says %d", hdr.Name, hdr.Size, want.Size)
		}

		w, err := sink.create(hdr.Name, hdr.Size)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(w, h), tr)
		closeErr := w.Close()
		if copyErr != nil {
			return fmt.Errorf("read %s from the archive: %w", hdr.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("write %s: %w", hdr.Name, closeErr)
		}
		if n != want.Size {
			return fmt.Errorf("the archive entry %q ended after %d of %d bytes", hdr.Name, n, want.Size)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want.Sha256 {
			return fmt.Errorf("the archive entry %q has sha256 %s, the manifest says %s", hdr.Name, got, want.Sha256)
		}
	}

	if _, err := io.Copy(zeroChecker{}, lr); err != nil {
		return err
	}
	if lr.N <= 0 {
		return fmt.Errorf("the archive expands to more than the %d bytes the manifest allows", limit)
	}
	for _, f := range m.Archive.Files {
		if !seen[f.Name] {
			return fmt.Errorf("the archive lacks %q, which the manifest lists", f.Name)
		}
	}
	return nil
}

// verifyArchiveContent runs the archive through walkArchive without writing
// anything.
func verifyArchiveContent(path string, m *manifest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return walkArchive(f, m, discardSink{})
}

type extractOptions struct {
	ManifestPath string
	ArchivePath  string
	Target       string
	Expect       expectations
	Check        checkOptions
	MaxBytes     int64
	Now          time.Time
}

// prepareTarget checks that the extraction target is a path where a new data
// directory can be put: it does not exist or is an empty directory, and its
// parent is a real directory.
func prepareTarget(target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	parent := filepath.Dir(abs)
	if parent == abs {
		return "", fmt.Errorf("refusing to extract into %s", abs)
	}
	pinfo, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("the parent of the target does not exist: %w", err)
	}
	if !pinfo.IsDir() {
		return "", fmt.Errorf("the parent %s of the target is not a directory", parent)
	}
	info, err := os.Lstat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return abs, nil
	case err != nil:
		return "", err
	case info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("the target %s is a symbolic link", abs)
	case !info.IsDir():
		return "", fmt.Errorf("the target %s exists and is not a directory", abs)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", fmt.Errorf("the target %s is not empty (%d entries); refusing to extract over existing data", abs, len(entries))
	}
	return abs, nil
}

func randomSuffix() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// extractSnapshot verifies the manifest and the archive, unpacks the archive
// into a fresh directory next to the target, opens what it unpacked and checks
// it against the manifest, and only then moves it to the target. On any
// failure nothing is left behind and the target is untouched.
func extractSnapshot(o extractOptions) (*manifest, *chainIdentity, error) {
	m, err := readManifestFile(o.ManifestPath)
	if err != nil {
		return nil, nil, err
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if err := checkManifest(m, o.Expect, o.Now); err != nil {
		return nil, nil, err
	}
	if err := checkUnpackSize(m, o.MaxBytes); err != nil {
		return nil, nil, err
	}
	if err := verifyArchiveFile(o.ArchivePath, m); err != nil {
		return nil, nil, err
	}
	target, err := prepareTarget(o.Target)
	if err != nil {
		return nil, nil, err
	}

	suffix, err := randomSuffix()
	if err != nil {
		return nil, nil, err
	}
	staging := filepath.Join(filepath.Dir(target), ".nhb-snapshot-extract-"+suffix)
	if err := os.Mkdir(staging, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the staging directory: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(staging)
		}
	}()

	f, err := os.Open(o.ArchivePath)
	if err != nil {
		return nil, nil, err
	}
	err = walkArchive(f, m, dirSink{dir: staging})
	_ = f.Close()
	if err != nil {
		return nil, nil, err
	}
	files, err := listStaged(staging)
	if err != nil {
		return nil, nil, err
	}
	if len(files) != len(m.Archive.Files) {
		return nil, nil, fmt.Errorf("unpacked %d files, the manifest lists %d", len(files), len(m.Archive.Files))
	}

	id, err := openAndReadIdentity(staging, o.Check)
	if err != nil {
		return nil, nil, fmt.Errorf("the unpacked snapshot does not open: %w", err)
	}
	_ = os.Remove(filepath.Join(staging, "LOCK"))
	if err := compareIdentity(m, id); err != nil {
		return nil, nil, err
	}
	// The age is checked once more on the block time the database itself holds,
	// so it does not rest on the manifest's word for it.
	if err := checkTipAge(id.TipTimestamp, o.Expect.MaxAge, o.Now); err != nil {
		return nil, nil, err
	}
	for _, addr := range o.Expect.RejectValidators {
		for _, v := range id.Validators {
			if v == addr {
				return nil, nil, fmt.Errorf("%s is a validator in this snapshot's state: a new node must start with a fresh key that is not registered anywhere, never with a key that is already validating", addr)
			}
		}
	}

	if err := os.Chmod(staging, 0o755); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		// prepareTarget saw it empty; a directory cannot be renamed onto on
		// every platform, so remove the empty one first.
		if err := os.Remove(target); err != nil {
			return nil, nil, fmt.Errorf("replace the empty target directory (is it a mount point? extract into a directory that does not exist yet, inside it): %w", err)
		}
	}
	if err := os.Rename(staging, target); err != nil {
		return nil, nil, fmt.Errorf("move the unpacked snapshot into place: %w", err)
	}
	ok = true
	return m, id, nil
}

// compareIdentity checks what the database says against what the manifest
// claims: a manifest that lies about the block it describes is refused.
func compareIdentity(m *manifest, id *chainIdentity) error {
	switch {
	case m.chainIDValue() != id.ChainID:
		return fmt.Errorf("the manifest says chain id %s, the unpacked database says %d", m.ChainID, id.ChainID)
	case m.GenesisHash != hex0x(id.GenesisHash):
		return fmt.Errorf("the manifest says genesis hash %s, the unpacked database says %s", m.GenesisHash, hex0x(id.GenesisHash))
	case m.Height != id.Height:
		return fmt.Errorf("the manifest says height %d, the unpacked database says %d", m.Height, id.Height)
	case m.TipHash != hex0x(id.TipHash):
		return fmt.Errorf("the manifest says tip hash %s, the unpacked database says %s", m.TipHash, hex0x(id.TipHash))
	case m.StateRoot != hex0x(id.StateRoot):
		return fmt.Errorf("the manifest says state root %s, the unpacked database says %s", m.StateRoot, hex0x(id.StateRoot))
	case m.TipTimestamp != id.TipTimestamp:
		return fmt.Errorf("the manifest says tip timestamp %d, the unpacked database says %d", m.TipTimestamp, id.TipTimestamp)
	}
	return nil
}
