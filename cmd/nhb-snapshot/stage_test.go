package main

// Tests of the checks a staged copy of a chain database goes through before any
// of it is packed (stage.go, journalfile.go): every file in it has to be one its
// own MANIFEST refers to, a regular file, and what it says it is.

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
)

// stagedCopy makes a chain database with tables and stages what it refers to,
// the way scripts/make-snapshot.sh does.
func stagedCopy(t *testing.T) (stage string, refs *dbRefs) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	b := newChainBuilder(t, dir, true, testAddress(0x61))
	for i := 0; i < 130; i++ {
		b.addBlock(12)
	}
	b.close()
	stage = stageChainFiles(t, dir)
	refs, err := readDBRefs(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Tables) == 0 {
		t.Fatal("the fixture has no table")
	}
	return stage, refs
}

func TestVerifyStageAcceptsWhatTheDatabaseRefersTo(t *testing.T) {
	stage, refs := stagedCopy(t)
	got, err := verifyStage(stage)
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest != refs.Manifest || len(got.Tables) != len(refs.Tables) {
		t.Fatalf("got %+v", got)
	}
	id, err := openAndReadStaged(stage, checkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want, err := openAndReadIdentity(stage, checkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if id.Height != want.Height || !bytes.Equal(id.TipHash, want.TipHash) {
		t.Fatalf("the staged open reads %d/%x, the plain one %d/%x", id.Height, id.TipHash, want.Height, want.TipHash)
	}
	// The command does the same.
	if out, code := runToolCode(builtTool(t), "info", "--staged", "--data-dir", stage); code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("info --staged (%d): %s", code, out)
	}
}

// A file that only has the name of one of the database's, an older MANIFEST, a
// table that is still being written, a journal that is not the one the MANIFEST
// names: each is a file the database does not refer to, and a stage that holds one
// is refused whole, by the tool that reads it and by the packer.
func TestVerifyStageRefusesWhatTheDatabaseDoesNotReferTo(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, stage string, refs *dbRefs)
		want  string
	}{
		{"a journal that is a page of text", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, journalFileName(refs.JournalNum+50)), shadowLine)
		}, "the MANIFEST names the journal"},
		{"a journal with the number of an older one", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, journalFileName(refs.JournalNum-1)), shadowLine)
		}, "the MANIFEST names the journal"},
		{"a journal name with leading zeros", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, "0"+journalFileName(refs.JournalNum)), shadowLine)
		}, "the MANIFEST names the journal"},
		{"the named journal replaced by a page of text", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, journalFileName(refs.JournalNum)), shadowLine)
		}, "not a journal"},
		{"the named journal followed by a page of text", func(t *testing.T, stage string, refs *dbRefs) {
			appendTo(t, filepath.Join(stage, journalFileName(refs.JournalNum)), shadowLine)
		}, "not a journal"},
		{"a table nothing lists", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, tableFileName(refs.NextFileNum+9)), "half written")
		}, "does not list"},
		{"a listed table under the other suffix", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, strings.TrimSuffix(refs.tableNames()[0], ".ldb")+".sst"), "x")
		}, "does not list"},
		{"a listed table that is not there", func(t *testing.T, stage string, refs *dbRefs) {
			if err := os.Remove(filepath.Join(stage, refs.tableNames()[0])); err != nil {
				t.Fatal(err)
			}
		}, "is not there"},
		{"a listed table of another size", func(t *testing.T, stage string, refs *dbRefs) {
			appendTo(t, filepath.Join(stage, refs.tableNames()[0]), "x")
		}, "the MANIFEST says"},
		{"a MANIFEST CURRENT does not name", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, "MANIFEST-999999"), "an older MANIFEST")
		}, "does not name"},
		{"a file that is not the database's", func(t *testing.T, stage string, refs *dbRefs) {
			mustWrite(t, filepath.Join(stage, "validator.key"), "k")
		}, "not a chain database file"},
		{"a directory with the name of a table", func(t *testing.T, stage string, refs *dbRefs) {
			if err := os.MkdirAll(filepath.Join(stage, tableFileName(refs.NextFileNum+3)), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "a directory, not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stage, refs := stagedCopy(t)
			tc.plant(t, stage, refs)
			if _, err := verifyStage(stage); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
			if _, err := openAndReadStaged(stage, checkOptions{}); err == nil {
				t.Fatalf("a staged copy that holds what the database does not refer to was opened")
			}
			out := filepath.Join(t.TempDir(), "out")
			if _, err := packSnapshot(packOptions{DataDir: stage, OutDir: out}); err == nil {
				t.Fatalf("a staged copy that holds what the database does not refer to was packed")
			}
			if files := filesUnder(t, out); len(files) > 0 {
				t.Fatalf("a refused pack left %v", files)
			}
		})
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// No file of the stage is read through a link, whatever put it there.
func TestVerifyStageNeverReadsThroughALink(t *testing.T) {
	stage, refs := stagedCopy(t)
	for _, name := range append([]string{"CURRENT", refs.Manifest, journalFileName(refs.JournalNum)}, refs.tableNames()[0]) {
		t.Run(name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
				t.Skipf("%s is not in this stage: %v", name, err)
			}
			stubLstat(t, filepath.Join(stage, name), os.ModeSymlink|0o777)
			if _, err := verifyStage(stage); err == nil || !strings.Contains(err.Error(), "a symbolic link") {
				t.Fatalf("a link was accepted: %v", err)
			}
			out := filepath.Join(t.TempDir(), "out")
			if _, err := packSnapshot(packOptions{DataDir: stage, OutDir: out}); err == nil {
				t.Fatalf("a link was packed")
			}
		})
	}
}

// A file that is listed by the MANIFEST, has the size the MANIFEST says, and is not
// a table is refused too: verifyStage looks at names and sizes, and reading the
// tables in full is what finds it. LevelDB itself reads a table only when a key
// lands in it.
func TestAFileThatIsNotATableIsRefusedEvenWhenTheMANIFESTListsIt(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, path string){
		"a page of text of the size of the table": func(t *testing.T, path string) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			text := bytes.Repeat([]byte(shadowLine), int(info.Size())/len(shadowLine)+1)[:info.Size()]
			if err := os.WriteFile(path, text, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"one byte changed inside a block": func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)/4] ^= 0x55
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			stage, refs := stagedCopy(t)
			damage(t, filepath.Join(stage, refs.tableNames()[0]))
			if _, err := verifyStage(stage); err != nil {
				t.Fatalf("verifyStage looks at names and sizes only, but refused: %v", err)
			}
			if _, err := openStaged(stage); err == nil {
				t.Fatalf("a table that does not read was accepted")
			}
			out := filepath.Join(t.TempDir(), "out")
			if _, err := packSnapshot(packOptions{DataDir: stage, OutDir: out}); err == nil {
				t.Fatalf("a table that does not read was packed")
			}
		})
	}
}

// The packer packs the files the database refers to, and only those.
func TestPackPacksExactlyTheFilesTheDatabaseRefersTo(t *testing.T) {
	stage, refs := stagedCopy(t)
	m, err := packSnapshot(packOptions{DataDir: stage, OutDir: filepath.Join(t.TempDir(), "out")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"CURRENT": true, refs.Manifest: true}
	for _, name := range refs.tableNames() {
		want[name] = true
	}
	for _, name := range refs.journalNames() {
		if _, err := os.Stat(filepath.Join(stage, name)); err == nil {
			want[name] = true
		}
	}
	if len(m.Archive.Files) != len(want) {
		t.Fatalf("packed %d files, the database refers to %d", len(m.Archive.Files), len(want))
	}
	for _, f := range m.Archive.Files {
		if !want[f.Name] {
			t.Fatalf("packed %s, which the database does not refer to", f.Name)
		}
	}
}

// journalDB makes a database whose writes are all in its journal, in records
// larger than a block of the journal, and returns the journal file.
func journalDB(t *testing.T) (dir, journalPath string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "jdb")
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 40; i++ {
		value := make([]byte, 10<<10+rng.Intn(50<<10))
		rng.Read(value)
		if err := db.Put([]byte{byte(i), 'k'}, value, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	refs, err := readDBRefs(dir)
	if err != nil {
		t.Fatal(err)
	}
	journalPath = filepath.Join(dir, journalFileName(refs.JournalNum))
	info, err := os.Stat(journalPath)
	if err != nil || info.Size() < 4*journalBlockSize {
		t.Fatalf("the journal is not the multi-block file this test needs: %v", err)
	}
	return dir, journalPath
}

// A copy of a journal that the node was still writing ends wherever the copy
// happened to stop: in a chunk header, in a payload, between the chunks of a
// record, between records. Every one of those is a journal the script has to
// accept, or it would refuse a snapshot of a healthy node.
func TestCheckJournalAcceptsEveryPrefixOfARealJournal(t *testing.T) {
	_, journalPath := journalDB(t)
	whole, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var cuts []int
	for n := 0; n <= 40; n++ {
		cuts = append(cuts, n)
	}
	for at := journalBlockSize; at < len(whole); at += journalBlockSize {
		for d := -8; d <= 8; d++ {
			if n := at + d; n > 0 && n <= len(whole) {
				cuts = append(cuts, n)
			}
		}
	}
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 150; i++ {
		cuts = append(cuts, rng.Intn(len(whole)+1))
	}
	cuts = append(cuts, len(whole))
	path := filepath.Join(t.TempDir(), "copy.log")
	for _, n := range cuts {
		if err := os.WriteFile(path, whole[:n], 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkJournal(path); err != nil {
			t.Fatalf("a copy cut at byte %d of %d was refused: %v", n, len(whole), err)
		}
	}
}

// What is not a journal is refused: text, zeros, noise, a journal with a block
// gone, a byte changed inside it, or anything after its last record.
func TestCheckJournalRefusesWhatIsNotAJournal(t *testing.T) {
	_, journalPath := journalDB(t)
	whole, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 5*journalBlockSize)
	rand.New(rand.NewSource(5)).Read(noise)
	blockGone := append(append([]byte(nil), whole[:journalBlockSize]...), whole[2*journalBlockSize:]...)
	zeroBlock := append([]byte(nil), whole...)
	for i := journalBlockSize; i < 2*journalBlockSize; i++ {
		zeroBlock[i] = 0
	}
	// A cut journal, so that its end is the start of a record, followed by text.
	cases := map[string][]byte{
		"a page of text":                        []byte(shadowLine),
		"a longer page of text":                 bytes.Repeat([]byte(shadowLine), 2000),
		"zeros":                                 make([]byte, 3*journalBlockSize),
		"noise":                                 noise,
		"a block of the journal gone":           blockGone,
		"a block of the journal zeroed":         zeroBlock,
		"the journal followed by text":          append(append([]byte(nil), whole...), []byte(shadowLine)...),
		"the journal followed by a zero header": append(append([]byte(nil), whole...), make([]byte, 40)...),
	}
	// A byte changed anywhere in the first block.
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 60; i++ {
		flipped := append([]byte(nil), whole...)
		at := rng.Intn(journalBlockSize)
		flipped[at] ^= byte(1 << uint(rng.Intn(8)))
		cases["a byte changed at "+strconv.Itoa(at)] = flipped
	}
	path := filepath.Join(t.TempDir(), "junk.log")
	for name, data := range cases {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkJournal(path); err == nil {
			t.Fatalf("%s was accepted as a journal", name)
		}
	}
}

// A journal that is a series of chunks the right way but does not hold write
// batches is refused as well.
func TestCheckJournalRefusesRecordsThatAreNotWriteBatches(t *testing.T) {
	batch := func(entries ...[]byte) []byte {
		out := make([]byte, 12)
		out[8] = byte(len(entries))
		for _, e := range entries {
			out = append(out, e...)
		}
		return out
	}
	put := []byte{1, 1, 'k', 1, 'v'}
	del := []byte{0, 1, 'k'}
	for name, tc := range map[string]struct {
		records [][]byte
		want    bool
	}{
		"batches":                          {[][]byte{batch(put), batch(put, del), batch()}, true},
		"a record with a wrong count":      {[][]byte{append(batch(put), 0, 1, 'k')}, false},
		"a record of an unknown type":      {[][]byte{batch([]byte{2, 1, 'k'})}, false},
		"a record with a key cut short":    {[][]byte{batch([]byte{1, 9, 'k'})}, false},
		"a record shorter than its header": {[][]byte{[]byte("short")}, false},
		"text":                             {[][]byte{[]byte(shadowLine)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.log")
			if err := os.WriteFile(path, manifestBytes(t, tc.records...), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := checkJournal(path); (err == nil) != tc.want {
				t.Fatalf("checkJournal said %v, want accepted=%v", err, tc.want)
			}
		})
	}
}

// A MANIFEST is a journal-format file that must be whole: what a journal may end
// in, a MANIFEST may not.
func TestWalkJournalIsStrictForAFileThatMustBeWhole(t *testing.T) {
	records := [][]byte{bytes.Repeat([]byte{1}, 50), bytes.Repeat([]byte{2}, 70000), []byte("last")}
	whole := manifestBytes(t, records...)
	count := func(data []byte, torn bool) (int, error) {
		n := 0
		err := walkJournal(bytes.NewReader(data), journalWalk{
			TornOK: torn, MaxRecord: 1 << 20,
			Whole:  func([]byte) error { n++; return nil },
			Prefix: func([]byte) error { return nil },
		})
		return n, err
	}
	if n, err := count(whole, false); err != nil || n != 3 {
		t.Fatalf("a whole file: %d records, %v", n, err)
	}
	for _, cut := range []int{len(whole) - 1, len(whole) - 5, 3, 20, 60} {
		if _, err := count(whole[:cut], false); err == nil {
			t.Fatalf("a file cut at %d of %d was accepted as whole", cut, len(whole))
		}
		if _, err := count(whole[:cut], true); err != nil {
			t.Fatalf("a copy cut at %d of %d was refused: %v", cut, len(whole), err)
		}
	}
	// A record that is larger than the bound is refused, not kept in memory.
	err := walkJournal(bytes.NewReader(whole), journalWalk{MaxRecord: 60000, Whole: func([]byte) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "of more than") {
		t.Fatalf("a record over the bound was accepted: %v", err)
	}
}
