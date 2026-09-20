package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nhbchain/crypto"
)

// fixture is a real chain, staged and packed the way the producer does it.
type fixture struct {
	t         *testing.T
	stage     string
	outDir    string
	m         *manifest
	manifest  string
	archive   string
	genesis   []byte
	chainID   uint64
	validator crypto.Address
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := filepath.Join(t.TempDir(), "db")
	validator := testAddress(0x33)
	b := newChainBuilder(t, db, true, validator)
	for i := 0; i < 120; i++ {
		b.addBlock(12)
	}
	genesis := b.bc.GenesisHash()
	chainID := b.bc.ChainID()
	b.close()

	f := &fixture{t: t, genesis: genesis, chainID: chainID, validator: validator}
	f.stage = stageChainFiles(t, db)
	f.outDir = filepath.Join(t.TempDir(), "out")
	m, err := packSnapshot(packOptions{
		DataDir:   f.stage,
		OutDir:    f.outDir,
		Producer:  producerInfo{BinaryVersion: "test", BinaryCommit: "0123456789abcdef", BinarySha256: strings.Repeat("ab", 32)},
		CreatedAt: time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
		Latest:    true,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	f.m = m
	f.archive = filepath.Join(f.outDir, m.Archive.Name)
	f.manifest = strings.TrimSuffix(f.archive, ".tar.gz") + ".manifest.json"
	return f
}

func (f *fixture) expect() expectations {
	return expectations{ChainID: f.chainID, GenesisHash: f.genesis}
}

func (f *fixture) extract(target string) (*manifest, *chainIdentity, error) {
	return extractSnapshot(extractOptions{
		ManifestPath: f.manifest,
		ArchivePath:  f.archive,
		Target:       target,
		Expect:       f.expect(),
		Now:          time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
	})
}

// rawEntry is one tar entry, written exactly as given.
type rawEntry struct {
	hdr  tar.Header
	data []byte
}

func regEntry(name string, data []byte) rawEntry {
	return rawEntry{hdr: tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(data)), Mode: 0o644}, data: data}
}

// stagedEntries returns the staged files as archive entries, in name order.
func (f *fixture) stagedEntries() []rawEntry {
	var out []rawEntry
	for _, name := range dbFiles(f.t, f.stage) {
		if !allowedFileName(name) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.stage, name))
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, regEntry(name, data))
	}
	return out
}

func tarGz(t *testing.T, entries []rawEntry, trailer []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		hdr := e.hdr
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatalf("write header %q: %v", hdr.Name, err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatalf("write %q: %v", hdr.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	raw.Write(trailer)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// hostile writes an archive built from entries next to a manifest that is
// consistent with it: the manifest's archive size and sha256 describe the
// hostile archive, so only the content rules can stop it. mutate may change
// the manifest further.
func (f *fixture) hostile(name string, archive []byte, mutate func(*manifest)) (manifestPath, archivePath string) {
	f.t.Helper()
	dir := filepath.Join(f.t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	m := *f.m
	m.Archive.Files = append([]fileEntry(nil), f.m.Archive.Files...)
	sum := sha256.Sum256(archive)
	m.Archive.Size = int64(len(archive))
	m.Archive.Sha256 = hex.EncodeToString(sum[:])
	if mutate != nil {
		mutate(&m)
	}
	archivePath = filepath.Join(dir, m.Archive.Name)
	manifestPath = filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		f.t.Fatal(err)
	}
	encoded, err := m.marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return manifestPath, archivePath
}

func (f *fixture) extractFrom(manifestPath, archivePath, target string) error {
	_, _, err := extractSnapshot(extractOptions{
		ManifestPath: manifestPath,
		ArchivePath:  archivePath,
		Target:       target,
		Expect:       f.expect(),
		Now:          time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC),
	})
	return err
}

// mustNotLeaveTraces checks that a refused extraction left the target as it was
// and no staging directory behind.
func mustNotLeaveTraces(t *testing.T, target string) {
	t.Helper()
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		t.Fatalf("the refused extraction left %d entries in the target", len(entries))
	}
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nhb-snapshot-extract-") {
			t.Fatalf("staging directory %s was left behind", e.Name())
		}
	}
}

func TestPackIsDeterministicAndRoundTrips(t *testing.T) {
	f := newFixture(t)

	// The same staged files packed again give the same bytes.
	again := filepath.Join(t.TempDir(), "again")
	m2, err := packSnapshot(packOptions{
		DataDir:   f.stage,
		OutDir:    again,
		Producer:  f.m.Producer,
		CreatedAt: time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Archive.Sha256 != f.m.Archive.Sha256 || m2.Archive.Size != f.m.Archive.Size {
		t.Fatalf("packing twice gave different archives: %s vs %s", m2.Archive.Sha256, f.m.Archive.Sha256)
	}
	first, _ := os.ReadFile(f.manifest)
	second, _ := os.ReadFile(strings.TrimSuffix(filepath.Join(again, m2.Archive.Name), ".tar.gz") + ".manifest.json")
	if !bytes.Equal(first, second) {
		t.Fatalf("packing twice gave different manifests")
	}

	// verify passes, extract passes, and the result is the packed chain.
	if err := verifyArchiveFile(f.archive, f.m); err != nil {
		t.Fatal(err)
	}
	if err := verifyArchiveContent(f.archive, f.m); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "data")
	m, id, err := f.extract(target)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if id.Height != 120 || m.Height != 120 || m.TipHash != hex0x(id.TipHash) {
		t.Fatalf("extracted height %d (manifest %d)", id.Height, m.Height)
	}
	if id.ChainID != f.chainID {
		t.Fatalf("chain id %d, want %d", id.ChainID, f.chainID)
	}
	if got := dbFiles(t, target); len(got) != len(f.m.Archive.Files) {
		t.Fatalf("extracted %d files, want %d: %v", len(got), len(f.m.Archive.Files), got)
	}
	// The manifest names the producer and the latest pointer was written.
	if f.m.Producer.BinaryCommit != "0123456789abcdef" || f.m.Producer.SnapshotTool != "nhb-snapshot 1" {
		t.Fatalf("producer info: %+v", f.m.Producer)
	}
	if _, err := os.Stat(filepath.Join(f.outDir, "manifest.json")); err != nil {
		t.Fatalf("no manifest.json: %v", err)
	}
	for _, name := range f.m.Archive.Files {
		if !allowedFileName(name.Name) {
			t.Fatalf("the manifest lists %q", name.Name)
		}
	}
}

// The packer refuses anything in the staged directory that is not a chain
// database file: keys, the peer list, sign state and lock files never get in.
func TestPackRefusesNodeStateAndKeys(t *testing.T) {
	for _, name := range []string{
		"node_key.json", "validator.key", "validator.keystore", "bft_sign_state.json",
		"polc_lock.json", "genesis.resolved.json", "wallet.key", "id_rsa.pem", "notes.txt", "p2p", "000001.tmp",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			path := filepath.Join(f.stage, name)
			if name == "p2p" {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "out")
			if _, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: out}); err == nil || !strings.Contains(err.Error(), "refusing to pack") {
				t.Fatalf("packing a directory holding %s: %v", name, err)
			}
			if entries, _ := os.ReadDir(out); len(entries) > 0 {
				t.Fatalf("a refused pack left %d files in the output directory", len(entries))
			}
		})
	}
}

func TestPackSkipsLevelDBHousekeeping(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"LOCK", "LOG", "LOG.old"} {
		if err := os.WriteFile(filepath.Join(f.stage, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: filepath.Join(t.TempDir(), "out")})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range m.Archive.Files {
		switch file.Name {
		case "LOCK", "LOG", "LOG.old":
			t.Fatalf("%s was packed", file.Name)
		}
	}
}

func TestPackRefusesASymlinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.stage, "CURRENT")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(f.stage, "CURRENT")); err != nil {
		t.Fatal(err)
	}
	if _, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: filepath.Join(t.TempDir(), "out")}); err == nil {
		t.Fatalf("a symlinked CURRENT was packed")
	}
}

func TestHostileArchivesAreRefused(t *testing.T) {
	f := newFixture(t)
	base := f.stagedEntries()

	with := func(extra ...rawEntry) []rawEntry {
		return append(append([]rawEntry(nil), base...), extra...)
	}
	// replace CURRENT by the given entry
	replaceCurrent := func(e rawEntry) []rawEntry {
		var out []rawEntry
		for _, b := range base {
			if b.hdr.Name != "CURRENT" {
				out = append(out, b)
			}
		}
		return append(out, e)
	}
	someTable := ""
	for _, b := range base {
		if strings.HasSuffix(b.hdr.Name, ".ldb") {
			someTable = b.hdr.Name
			break
		}
	}
	if someTable == "" {
		t.Fatalf("the fixture has no table file (flush did not happen)")
	}
	withoutTable := func() []rawEntry {
		var out []rawEntry
		for _, b := range base {
			if b.hdr.Name != someTable {
				out = append(out, b)
			}
		}
		return out
	}
	biggerCurrent := func() []rawEntry {
		var out []rawEntry
		for _, b := range base {
			if b.hdr.Name == "CURRENT" {
				b = regEntry("CURRENT", append(append([]byte(nil), b.data...), []byte("padding")...))
			}
			out = append(out, b)
		}
		return out
	}

	cases := []struct {
		name    string
		entries []rawEntry
		trailer []byte
		want    string
	}{
		{"path traversal", with(regEntry("../evil", []byte("x"))), nil, "not a chain database file name"},
		{"deep path traversal", with(regEntry("../../../../etc/cron.d/evil", []byte("x"))), nil, "not a chain database file name"},
		{"absolute path", with(regEntry("/etc/passwd", []byte("x"))), nil, "not a chain database file name"},
		{"windows drive path", with(regEntry(`C:\Windows\evil`, []byte("x"))), nil, "not a chain database file name"},
		{"backslash traversal", with(regEntry(`..\evil`, []byte("x"))), nil, "not a chain database file name"},
		{"nested path", with(regEntry("sub/000009.ldb", []byte("x"))), nil, "not a chain database file name"},
		{"dot entry", with(regEntry(".", []byte("x"))), nil, "not a chain database file name"},
		{"node identity file", with(regEntry("p2p/node_key.json", []byte(`{"privateKey":"00"}`))), nil, "not a chain database file name"},
		{"key file at top level", with(regEntry("validator.key", []byte("k"))), nil, "not a chain database file name"},
		{"lock file", with(regEntry("LOCK", nil)), nil, "not a chain database file name"},
		{"sign state file", with(regEntry("bft_sign_state.json", []byte("{}"))), nil, "not a chain database file name"},
		{"symlink", replaceCurrent(rawEntry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: "CURRENT", Linkname: "/etc/passwd", Mode: 0o777}}), nil, "symbolic link"},
		{"hard link out", with(rawEntry{hdr: tar.Header{Typeflag: tar.TypeLink, Name: "000777.ldb", Linkname: "/etc/passwd", Mode: 0o644}}), nil, "hard link"},
		{"character device", with(rawEntry{hdr: tar.Header{Typeflag: tar.TypeChar, Name: "000778.ldb", Devmajor: 1, Devminor: 3, Mode: 0o666}}), nil, "character device"},
		{"block device", with(rawEntry{hdr: tar.Header{Typeflag: tar.TypeBlock, Name: "000779.ldb", Devmajor: 8, Devminor: 0, Mode: 0o660}}), nil, "block device"},
		{"named pipe", with(rawEntry{hdr: tar.Header{Typeflag: tar.TypeFifo, Name: "000780.ldb", Mode: 0o644}}), nil, "named pipe"},
		{"directory entry", with(rawEntry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: "000781.ldb", Mode: 0o755}}), nil, "directory"},
		{"duplicate entry", with(base[0]), nil, "twice"},
		{"unlisted file", with(regEntry("999999.ldb", []byte("x"))), nil, "not listed in the manifest"},
		{"missing file", withoutTable(), nil, "lacks"},
		{"entry larger than the manifest says", biggerCurrent(), nil, "the manifest says"},
		{"data after the tar trailer", base, []byte("trailing garbage"), "data after the end"},
		{"expansion beyond the manifest", base, make([]byte, 1<<20), "expands to more than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := tarGz(t, tc.entries, tc.trailer)
			manifestPath, archivePath := f.hostile(strings.ReplaceAll(tc.name, " ", "-"), archive, nil)
			target := filepath.Join(t.TempDir(), "data")

			err := f.extractFrom(manifestPath, archivePath, target)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got: %v", tc.want, err)
			}
			mustNotLeaveTraces(t, target)
			if _, statErr := os.Lstat(target); statErr == nil {
				t.Fatalf("the refused extraction created the target")
			}
			// verify refuses the same archive without writing anything.
			m, err := readManifestFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyArchiveContent(archivePath, m); err == nil {
				t.Fatalf("verify accepted the hostile archive")
			}
		})
	}
}

func TestNothingIsWrittenOutsideTheTarget(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	canary := filepath.Join(root, "evil")
	base := f.stagedEntries()
	archive := tarGz(t, append(append([]rawEntry(nil), base...), regEntry("../evil", []byte("x"))), nil)
	manifestPath, archivePath := f.hostile("outside", archive, nil)
	target := filepath.Join(root, "sub", "data")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.extractFrom(manifestPath, archivePath, target); err == nil {
		t.Fatalf("hostile archive accepted")
	}
	for _, p := range []string{canary, filepath.Join(filepath.Dir(target), "evil"), filepath.Join(root, "sub", "evil")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s was written", p)
		}
	}
}

func TestTruncatedArchiveIsRefused(t *testing.T) {
	f := newFixture(t)
	data, err := os.ReadFile(f.archive)
	if err != nil {
		t.Fatal(err)
	}
	cut := data[:len(data)/2]

	t.Run("manifest describes the full archive", func(t *testing.T) {
		manifestPath := filepath.Join(t.TempDir(), "m.json")
		archivePath := filepath.Join(t.TempDir(), f.m.Archive.Name)
		encoded, _ := f.m.marshal()
		if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, cut, 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "data")
		err := f.extractFrom(manifestPath, archivePath, target)
		if err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("got: %v", err)
		}
		mustNotLeaveTraces(t, target)
	})

	t.Run("manifest matches the truncated bytes", func(t *testing.T) {
		manifestPath, archivePath := f.hostile("truncated", cut, nil)
		target := filepath.Join(t.TempDir(), "data")
		if err := f.extractFrom(manifestPath, archivePath, target); err == nil {
			t.Fatalf("a truncated archive with a matching manifest was accepted")
		}
		mustNotLeaveTraces(t, target)
	})
}

func TestWrongChecksumIsRefused(t *testing.T) {
	f := newFixture(t)
	data, err := os.ReadFile(f.archive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/3] ^= 0xff
	archivePath := filepath.Join(t.TempDir(), f.m.Archive.Name)
	if err := os.WriteFile(archivePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "data")
	err = f.extractFrom(f.manifest, archivePath, target)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("got: %v", err)
	}
	mustNotLeaveTraces(t, target)
}

func TestNotAGzipStreamIsRefused(t *testing.T) {
	f := newFixture(t)
	// Long enough that its size is a believable one for the manifest to give.
	manifestPath, archivePath := f.hostile("notgzip", bytes.Repeat([]byte("this is not an archive"), 2000), nil)
	target := filepath.Join(t.TempDir(), "data")
	if err := f.extractFrom(manifestPath, archivePath, target); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("got: %v", err)
	}
	mustNotLeaveTraces(t, target)
}

func TestManifestMustMatchPinnedIdentity(t *testing.T) {
	f := newFixture(t)
	target := func() string { return filepath.Join(t.TempDir(), "data") }

	wrongChain := f.expect()
	wrongChain.ChainID++
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: wrongChain}); err == nil || !strings.Contains(err.Error(), "chain id") {
		t.Fatalf("wrong pinned chain id: %v", err)
	}
	wrongGenesis := f.expect()
	wrongGenesis.GenesisHash = bytes.Repeat([]byte{0x99}, 32)
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: wrongGenesis}); err == nil || !strings.Contains(err.Error(), "genesis hash") {
		t.Fatalf("wrong pinned genesis hash: %v", err)
	}
	noGenesis := expectations{ChainID: f.chainID}
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: noGenesis}); err == nil || !strings.Contains(err.Error(), "was pinned") {
		t.Fatalf("no pinned genesis hash: %v", err)
	}
	tooHigh := f.expect()
	tooHigh.MinHeight = f.m.Height + 1
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: tooHigh}); err == nil || !strings.Contains(err.Error(), "below the required minimum") {
		t.Fatalf("minimum height: %v", err)
	}
	tooOld := f.expect()
	tooOld.MaxAge = time.Hour
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: tooOld, Now: time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)}); err == nil || !strings.Contains(err.Error(), "older than") {
		t.Fatalf("maximum age: %v", err)
	}
	wrongTip := f.expect()
	wrongTip.TipHash = bytes.Repeat([]byte{0x01}, 32)
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: wrongTip}); err == nil || !strings.Contains(err.Error(), "tip hash") {
		t.Fatalf("pinned tip hash: %v", err)
	}
	wrongRoot := f.expect()
	wrongRoot.StateRoot = bytes.Repeat([]byte{0x02}, 32)
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target(), Expect: wrongRoot}); err == nil || !strings.Contains(err.Error(), "state root") {
		t.Fatalf("pinned state root: %v", err)
	}
}

// A manifest that lies about the block it describes is caught when the
// database is opened: the archive is genuine, the claim is not.
func TestManifestThatLiesAboutTheChainIsRefused(t *testing.T) {
	f := newFixture(t)
	data, err := os.ReadFile(f.archive)
	if err != nil {
		t.Fatal(err)
	}
	other := hex0x(bytes.Repeat([]byte{0x07}, 32))
	lies := map[string]func(*manifest){
		"tip hash":   func(m *manifest) { m.TipHash = other },
		"state root": func(m *manifest) { m.StateRoot = other },
		"height":     func(m *manifest) { m.Height++ },
		"timestamp":  func(m *manifest) { m.TipTimestamp++ },
	}
	for name, mutate := range lies {
		t.Run(name, func(t *testing.T) {
			manifestPath, archivePath := f.hostile("lie-"+strings.ReplaceAll(name, " ", "-"), data, mutate)
			target := filepath.Join(t.TempDir(), "data")
			err := f.extractFrom(manifestPath, archivePath, target)
			if err == nil || !strings.Contains(err.Error(), "the manifest says") {
				t.Fatalf("got: %v", err)
			}
			mustNotLeaveTraces(t, target)
		})
	}
}

func TestExtractRefusesANonEmptyTarget(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "precious.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.extract(target); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("got: %v", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "mine" {
		t.Fatalf("the existing file was touched: %q, %v", got, err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 1 {
		t.Fatalf("the target now has %d entries", len(entries))
	}
	mustNotLeaveTraces2(t, filepath.Dir(target))
}

func mustNotLeaveTraces2(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".nhb-snapshot-extract-") {
			t.Fatalf("staging directory %s was left behind", e.Name())
		}
	}
}

func TestExtractIntoAnEmptyDirectoryAndOnlyOnce(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.extract(target); err != nil {
		t.Fatalf("an empty directory is a valid target: %v", err)
	}
	// Running it again is idempotent in the only safe way: it refuses.
	if _, _, err := f.extract(target); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("second extraction: %v", err)
	}
}

func TestExtractRefusesBadTargets(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.extract(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file as target: %v", err)
	}
	if _, _, err := f.extract(filepath.Join(root, "missing", "data")); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("a missing parent: %v", err)
	}
	if _, _, err := f.extract(filepath.Join(file, "data")); err == nil {
		t.Fatalf("a parent that is a file")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.extract(link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("a symlink as target: %v", err)
		}
	}
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: string(filepath.Separator), Expect: f.expect()}); err == nil {
		t.Fatalf("extracting into the filesystem root was accepted")
	}
}

func TestExtractHonoursTheSizeCeiling(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "data")
	_, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target, Expect: f.expect(), MaxBytes: 1024})
	if err == nil || !strings.Contains(err.Error(), "above the allowed") {
		t.Fatalf("got: %v", err)
	}
	mustNotLeaveTraces(t, target)
}

// A node started with a key that is already a validator in the snapshot would
// vote from a second machine; the extractor is where that is stopped.
func TestExtractRefusesAKeyThatIsAlreadyAValidator(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "data")
	e := f.expect()
	e.RejectValidators = []string{strings.ToLower(f.validator.String())}
	_, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target, Expect: e})
	if err == nil || !strings.Contains(err.Error(), "is a validator in this snapshot") {
		t.Fatalf("got: %v", err)
	}
	mustNotLeaveTraces(t, target)

	// A fresh key is fine.
	e.RejectValidators = []string{strings.ToLower(testAddress(0x44).String())}
	if _, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target, Expect: e}); err != nil {
		t.Fatalf("a fresh key was refused: %v", err)
	}
}

// Corrupt bytes inside a genuine-looking archive (the manifest matches them) are
// caught by opening what was unpacked.
func TestCorruptDatabaseInsideAMatchingArchiveIsRefused(t *testing.T) {
	f := newFixture(t)
	entries := f.stagedEntries()
	var mutated []rawEntry
	var hit bool
	for _, e := range entries {
		if e.hdr.Name == "CURRENT" {
			e = regEntry("CURRENT", []byte("MANIFEST-999999\n"))
			hit = true
		}
		mutated = append(mutated, e)
	}
	if !hit {
		t.Fatal("no CURRENT in the fixture")
	}
	archive := tarGz(t, mutated, nil)
	manifestPath, archivePath := f.hostile("corrupt", archive, func(m *manifest) {
		for i := range m.Archive.Files {
			if m.Archive.Files[i].Name == "CURRENT" {
				sum := sha256.Sum256([]byte("MANIFEST-999999\n"))
				m.Archive.UncompressedSize += int64(len("MANIFEST-999999\n")) - m.Archive.Files[i].Size
				m.Archive.Files[i].Size = int64(len("MANIFEST-999999\n"))
				m.Archive.Files[i].Sha256 = hex.EncodeToString(sum[:])
			}
		}
	})
	target := filepath.Join(t.TempDir(), "data")
	err := f.extractFrom(manifestPath, archivePath, target)
	if err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Fatalf("got: %v", err)
	}
	mustNotLeaveTraces(t, target)
}

// A table file the manifest refers to must be there and as long as it says.
// LevelDB alone finds a missing table only when a read happens to need it.
func TestMissingOrShortTableIsDetected(t *testing.T) {
	f := newFixture(t)
	var table string
	for _, name := range dbFiles(t, f.stage) {
		if strings.HasSuffix(name, ".ldb") {
			table = name
			break
		}
	}
	if table == "" {
		t.Fatal("the fixture has no table file")
	}
	path := filepath.Join(f.stage, table)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, data[:len(data)-100], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openAndReadIdentity(f.stage, checkOptions{}); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("a short table was accepted: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := openAndReadIdentity(f.stage, checkOptions{}); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a missing table was accepted: %v", err)
	}
	if _, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: filepath.Join(t.TempDir(), "out")}); err == nil {
		t.Fatalf("a copy with a missing table was packed")
	}
}

// A copy of a running node may hold a table that was still being written. It
// belongs to no version of the database, so it is left out of the archive.
func TestPackLeavesOutTablesNothingRefersTo(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.stage, "999999.ldb"), []byte("half written"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := packSnapshot(packOptions{DataDir: f.stage, OutDir: filepath.Join(t.TempDir(), "out")})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range m.Archive.Files {
		if file.Name == "999999.ldb" {
			t.Fatalf("an unreferenced table was packed")
		}
	}
	if len(m.Archive.Files) != len(f.m.Archive.Files) {
		t.Fatalf("packed %d files, the same chain packs to %d", len(m.Archive.Files), len(f.m.Archive.Files))
	}
	if m.Archive.Sha256 != f.m.Archive.Sha256 {
		t.Fatalf("an unreferenced table changed the archive")
	}
}
