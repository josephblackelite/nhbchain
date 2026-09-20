package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type packOptions struct {
	DataDir   string
	OutDir    string
	Producer  producerInfo
	CreatedAt time.Time
	Check     checkOptions
	// Latest also writes OutDir/manifest.json, the fixed name a consumer
	// fetches, after the archive and its own manifest are in place.
	Latest bool
}

// stagedFile is one file of a staged database directory.
type stagedFile struct {
	name string
	size int64
}

// listStaged returns the files of dir that go into a snapshot, sorted by name.
// LevelDB housekeeping files are skipped; anything else that is not a plain
// chain database file is an error, so a directory that still holds a key, a
// peer list or a lock file never gets packed by accident.
func listStaged(dir string) ([]stagedFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []stagedFile
	for _, entry := range entries {
		name := entry.Name()
		if housekeepingFiles[name] {
			continue
		}
		if !allowedFileName(name) {
			return nil, fmt.Errorf("refusing to pack %s: %q is not a chain database file (only CURRENT, MANIFEST-*, *.log, *.ldb and *.sst are ever packed)", dir, name)
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to pack %s: %q is not a regular file", dir, name)
		}
		files = append(files, stagedFile{name: name, size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no chain database files", dir)
	}
	return files, nil
}

func archiveNameFor(id *chainIdentity) string {
	return fmt.Sprintf("nhb-snapshot-%s-h%010d.tar.gz", hex.EncodeToString(id.GenesisHash[:4]), id.Height)
}

// packSnapshot opens the staged database read-only, checks it, and writes a
// deterministic archive and its manifest into OutDir.
func packSnapshot(opts packOptions) (*manifest, error) {
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory: %w", err)
	}
	all, err := listStaged(opts.DataDir)
	if err != nil {
		return nil, err
	}
	db, err := openChainDB(opts.DataDir)
	if err != nil {
		return nil, err
	}
	id, err := db.readIdentity(opts.Check)
	live := db.tables
	db.close()
	if err != nil {
		return nil, err
	}
	// The read-only open may have created an empty LOCK file; it is not packed.
	_ = os.Remove(filepath.Join(opts.DataDir, "LOCK"))

	// Only the tables the database's current version refers to are packed: a
	// copy taken from a running node may also hold a table that was still
	// being written, which nothing refers to and which may still be growing.
	files := all[:0:0]
	for _, f := range all {
		if num, isTable := tableNumber(f.name); isTable {
			if _, isLive := live[num]; !isLive {
				continue
			}
		}
		files = append(files, f)
	}

	// Take the file list again after the open: the staged copy must be still.
	after, err := listStaged(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if len(after) != len(all) {
		return nil, fmt.Errorf("the staged files changed while they were checked")
	}
	for i := range all {
		if all[i] != after[i] {
			return nil, fmt.Errorf("the staged file %s changed while it was checked", all[i].name)
		}
	}

	name := archiveNameFor(id)
	finalArchive := filepath.Join(opts.OutDir, name)
	tmpArchive := finalArchive + ".tmp"
	out, err := os.OpenFile(tmpArchive, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(tmpArchive)
	}
	archiveHash := sha256.New()
	counter := &countingWriter{w: io.MultiWriter(out, archiveHash)}
	gz, err := gzip.NewWriterLevel(counter, gzip.DefaultCompression)
	if err != nil {
		cleanup()
		return nil, err
	}
	// No name and no modification time in the gzip header: the same files
	// always produce the same archive.
	gz.Header.Name = ""
	gz.Header.ModTime = time.Time{}
	tw := tar.NewWriter(gz)

	entries := make([]fileEntry, 0, len(files))
	var total int64
	for _, f := range files {
		entry, err := writeTarFile(tw, filepath.Join(opts.DataDir, f.name), f)
		if err != nil {
			cleanup()
			return nil, err
		}
		entries = append(entries, entry)
		total += entry.Size
	}
	if err := tw.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if err := gz.Close(); err != nil {
		cleanup()
		return nil, err
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return nil, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpArchive)
		return nil, err
	}

	m := &manifest{
		ManifestVersion: manifestVersion,
		ChainID:         fmt.Sprintf("%d", id.ChainID),
		GenesisHash:     hex0x(id.GenesisHash),
		Height:          id.Height,
		TipHash:         hex0x(id.TipHash),
		StateRoot:       hex0x(id.StateRoot),
		TipTimestamp:    id.TipTimestamp,
		CreatedAt:       opts.CreatedAt.UTC().Format(time.RFC3339),
		Producer:        opts.Producer,
		Archive: archiveInfo{
			Name:             name,
			Format:           archiveFormat,
			Size:             counter.n,
			Sha256:           hex.EncodeToString(archiveHash.Sum(nil)),
			UncompressedSize: total,
			Files:            entries,
		},
	}
	m.Producer.SnapshotTool = "nhb-snapshot " + toolVersion
	if err := m.validate(); err != nil {
		_ = os.Remove(tmpArchive)
		return nil, fmt.Errorf("the manifest just built is not valid: %w", err)
	}
	encoded, err := m.marshal()
	if err != nil {
		_ = os.Remove(tmpArchive)
		return nil, err
	}

	if err := os.Rename(tmpArchive, finalArchive); err != nil {
		_ = os.Remove(tmpArchive)
		return nil, err
	}
	manifestPath := strings.TrimSuffix(finalArchive, ".tar.gz") + ".manifest.json"
	if err := writeFileAtomic(manifestPath, encoded); err != nil {
		return nil, err
	}
	if opts.Latest {
		if err := writeFileAtomic(filepath.Join(opts.OutDir, "manifest.json"), encoded); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func writeTarFile(tw *tar.Writer, path string, f stagedFile) (fileEntry, error) {
	src, err := os.Open(path)
	if err != nil {
		return fileEntry{}, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return fileEntry{}, err
	}
	if info.Size() != f.size {
		return fileEntry{}, fmt.Errorf("the staged file %s changed size while it was checked", f.name)
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     f.name,
		Size:     info.Size(),
		Mode:     0o644,
		Format:   tar.FormatUSTAR,
		// Owner, group and time are fixed so the archive does not depend on
		// who packed it or when.
		ModTime: time.Unix(0, 0),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fileEntry{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(src, info.Size()))
	if err != nil {
		return fileEntry{}, err
	}
	if n != info.Size() {
		return fileEntry{}, fmt.Errorf("the staged file %s is shorter than its size", f.name)
	}
	// A byte more than the size read at the start means the file grew.
	var probe [1]byte
	if extra, _ := src.Read(probe[:]); extra > 0 {
		return fileEntry{}, fmt.Errorf("the staged file %s grew while it was packed", f.name)
	}
	return fileEntry{Name: f.name, Size: n, Sha256: hex.EncodeToString(h.Sum(nil))}, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writeFileAtomic writes data to path through a temporary file and a rename,
// so a reader never sees a half-written manifest.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
