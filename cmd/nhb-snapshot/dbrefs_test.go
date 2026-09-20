package main

// Tests of what the MANIFEST of a database says the database is made of
// (dbrefs.go), which is how scripts/make-snapshot.sh decides what to copy.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/journal"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// buildCompactedDB writes a database with small buffers, so that it flushes many
// tables, then compacts it by hand, so that tables move between levels and others
// are deleted: the records of its MANIFEST add, move and delete tables.
func buildCompactedDB(t *testing.T, dir string) {
	t.Helper()
	db := openWritableDB(t, dir, true)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 9000; i++ {
		key := []byte(fmt.Sprintf("key-%06d", (i*7919)%3100))
		// Noise: it does not compress, so the tables are as large as the buffers.
		value := make([]byte, 120)
		rng.Read(value)
		if err := db.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3100; i += 3 {
		if err := db.disk.Delete([]byte(fmt.Sprintf("key-%06d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.disk.Compact(nil, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

// libraryTables is the list of tables the LevelDB library itself finds when it
// opens dir.
func libraryTables(t *testing.T, dir string) map[int64]int64 {
	t.Helper()
	db, err := leveldb.OpenFile(dir, &opt.Options{ReadOnly: true, ErrorIfMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, err := liveTables(db)
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

// The tables this tool reads out of a MANIFEST are the ones the LevelDB library
// finds, on databases that have flushed and compacted, and every one of them is a
// file of the size the MANIFEST records.
func TestRefsMatchWhatLevelDBReports(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"a database compacted by hand": buildCompactedDB,
		"a chain database": func(t *testing.T, dir string) {
			b := newChainBuilder(t, dir, true, testAddress(0x51))
			for i := 0; i < 150; i++ {
				b.addBlock(12)
			}
			b.close()
		},
		"a database that never flushed": func(t *testing.T, dir string) { buildChainDir(t, dir, 20, 4, testAddress(0x52)) },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "db")
			build(t, dir)
			refs, err := readDBRefs(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := libraryTables(t, dir)
			if len(refs.Tables) != len(want) {
				t.Fatalf("this tool reads %d tables, the library %d: %v vs %v", len(refs.Tables), len(want), refs.Tables, want)
			}
			for num, size := range want {
				if got, ok := refs.Tables[num]; !ok || got != size {
					t.Fatalf("table %d: this tool reads %d (present %v), the library %d", num, got, ok, size)
				}
				info, err := os.Stat(filepath.Join(dir, tableFileName(num)))
				if err != nil || info.Size() != size {
					t.Fatalf("table %d is not the file the MANIFEST records: %v", num, err)
				}
				if num >= refs.NextFileNum {
					t.Fatalf("table %d is not below the next file number %d", num, refs.NextFileNum)
				}
			}
			if name == "a database compacted by hand" && len(want) < 2 {
				t.Fatalf("the fixture has %d tables: the compaction did not happen", len(want))
			}
			if refs.JournalNum >= refs.NextFileNum {
				t.Fatalf("the journal %d is not below the next file number %d", refs.JournalNum, refs.NextFileNum)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "CURRENT"))
			if err != nil || strings.TrimSpace(string(raw)) != refs.Manifest {
				t.Fatalf("the MANIFEST is %q, CURRENT says %q", refs.Manifest, raw)
			}
		})
	}
}

// Only CURRENT and the MANIFEST it names are needed to find the rest: what the
// script copies first, and then asks about.
func TestRefsOnlyNeedCURRENTAndTheMANIFEST(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	buildCompactedDB(t, dir)
	full, err := readDBRefs(dir)
	if err != nil {
		t.Fatal(err)
	}
	two := filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(two, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CURRENT", full.Manifest} {
		copyTestFile(t, filepath.Join(dir, name), filepath.Join(two, name))
	}
	got, err := readDBRefs(two)
	if err != nil {
		t.Fatal(err)
	}
	if got.Manifest != full.Manifest || got.JournalNum != full.JournalNum || len(got.Tables) != len(full.Tables) {
		t.Fatalf("the two files say %+v, the directory %+v", got, full)
	}
	// The command prints them as the script reads them.
	out, code := runToolCode(builtTool(t), "refs", "--data-dir", two)
	if code != 0 {
		t.Fatalf("refs: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "manifest "+full.Manifest || lines[1] != "journal "+journalFileName(full.JournalNum) {
		t.Fatalf("refs printed %q", lines)
	}
	tables := 0
	for _, line := range lines[2:] {
		if strings.HasPrefix(line, "table ") {
			tables++
		}
	}
	if tables != len(full.Tables) {
		t.Fatalf("refs printed %d tables, the MANIFEST lists %d", tables, len(full.Tables))
	}
}

// manifestBytes writes records into a MANIFEST-format file.
func manifestBytes(t *testing.T, records ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := journal.NewWriter(&buf)
	for _, rec := range records {
		rw, err := w.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rw.Write(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uvarints(values ...uint64) []byte {
	var out []byte
	for _, v := range values {
		out = binary.AppendUvarint(out, v)
	}
	return out
}

func withBytes(b []byte) []byte { return append(uvarints(uint64(len(b))), b...) }

// header is the fields of a MANIFEST record the database cannot do without.
func header(journalNum, nextFile uint64) []byte {
	out := uvarints(recComparer)
	out = append(out, withBytes([]byte("leveldb.BytewiseComparator"))...)
	out = append(out, uvarints(recJournalNum, journalNum, recNextFileNum, nextFile, recSeqNum, 100)...)
	return out
}

func addTable(level, num, size uint64) []byte {
	out := uvarints(recAddTable, level, num, size)
	out = append(out, withBytes([]byte("a"))...)
	return append(out, withBytes([]byte("z"))...)
}

func writeStage(t *testing.T, current string, manifest []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "CURRENT"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, "MANIFEST-000007"), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// As in the LevelDB library, what one record deletes is taken out before what it
// adds is put in, whatever the order of its fields, and a table moved from one
// level to another is a delete and an add.
func TestRefsApplyARecordLikeTheLibrary(t *testing.T) {
	first := append(header(9, 20), addTable(0, 5, 111)...)
	first = append(first, addTable(0, 6, 222)...)
	// Deletes table 5 and adds it again (in the other order), moves table 6 to
	// level 1.
	second := append(addTable(0, 5, 111), uvarints(recDelTable, 0, 5, recDelTable, 0, 6)...)
	second = append(second, addTable(1, 6, 222)...)
	dir := writeStage(t, "MANIFEST-000007\n", manifestBytes(t, first, second))
	refs, err := readDBRefs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Tables) != 2 || refs.Tables[5] != 111 || refs.Tables[6] != 222 || refs.JournalNum != 9 || refs.NextFileNum != 20 {
		t.Fatalf("got %+v", refs)
	}
	// A later record deletes a table for good.
	third := uvarints(recDelTable, 0, 5)
	dir = writeStage(t, "MANIFEST-000007\n", manifestBytes(t, first, second, third))
	if refs, err = readDBRefs(dir); err != nil || len(refs.Tables) != 1 || refs.Tables[6] != 222 {
		t.Fatalf("got %+v, %v", refs, err)
	}
	// A table in two levels is not a database.
	dup := append(header(9, 20), addTable(0, 5, 1)...)
	dup = append(dup, addTable(1, 5, 1)...)
	if _, err := readDBRefs(writeStage(t, "MANIFEST-000007\n", manifestBytes(t, dup))); err == nil {
		t.Fatalf("a table in two levels was accepted")
	}
	// A previous journal is named when the MANIFEST says so.
	prev := append(header(9, 20), uvarints(recPrevJournalNum, 8)...)
	refs, err = readDBRefs(writeStage(t, "MANIFEST-000007\n", manifestBytes(t, prev)))
	if err != nil {
		t.Fatal(err)
	}
	if got := refs.journalNames(); len(got) != 2 || got[0] != "000009.log" || got[1] != "000008.log" {
		t.Fatalf("journals %v", got)
	}
}

func TestRefsRefuseWhatIsNotAMANIFEST(t *testing.T) {
	good := manifestBytes(t, append(header(9, 20), addTable(0, 5, 111)...), uvarints(recDelTable, 0, 5))
	if _, err := readDBRefs(writeStage(t, "MANIFEST-000007\n", good)); err != nil {
		t.Fatalf("the reference MANIFEST was refused: %v", err)
	}
	flip := func(at int) []byte {
		out := append([]byte(nil), good...)
		out[at] ^= 0x40
		return out
	}
	cases := []struct {
		name     string
		manifest []byte
		want     string
	}{
		{"an empty file", []byte{}, "no record"},
		{"a file that is text", []byte("root:$6$SECRETSHADOWLINE$abcdefghijklmnop:19000:0:99999:7:::\n"), "MANIFEST is damaged"},
		{"a record cut short", good[:len(good)-3], "MANIFEST is damaged"},
		{"a byte changed in the middle", flip(len(good) / 2), "MANIFEST is damaged"},
		{"a byte changed in the checksum", flip(1), "MANIFEST is damaged"},
		{"three stray bytes after the last record", append(append([]byte(nil), good...), 'x', 'y', 'z'), "MANIFEST is damaged"},
		{"stray text after the last record", append(append([]byte(nil), good...), []byte("root:$6$SECRET")...), "MANIFEST is damaged"},
		{"a field type nobody knows", manifestBytes(t, append(header(9, 20), uvarints(12, 1)...)), "does not know"},
		{"a record that ends inside a field", manifestBytes(t, append(header(9, 20), uvarints(recAddTable, 0)...)), "cannot be read"},
		{"a deleted table with no number", manifestBytes(t, append(header(9, 20), uvarints(recDelTable, 0)...)), "cannot be read"},
		{"a level that does not exist", manifestBytes(t, append(header(9, 20), addTable(5000, 5, 1)...)), "cannot be read"},
		{"no journal", manifestBytes(t, append(append(uvarints(recComparer), withBytes([]byte("c"))...), uvarints(recNextFileNum, 3, recSeqNum, 1)...)), "names no journal"},
		{"no comparer", manifestBytes(t, uvarints(recJournalNum, 1, recNextFileNum, 3, recSeqNum, 1)), "no comparer"},
		{"no next file number", manifestBytes(t, append(append(uvarints(recComparer), withBytes([]byte("c"))...), uvarints(recJournalNum, 1, recSeqNum, 1)...)), "no next file number"},
		{"a file number that is not one", manifestBytes(t, append(header(9, 20), uvarints(recDelTable, 0, 1<<63)...)), "not a file number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDBRefs(writeStage(t, "MANIFEST-000007\n", tc.manifest))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRefsRefuseAnOddCURRENT(t *testing.T) {
	good := manifestBytes(t, header(9, 20))
	for _, tc := range []struct{ name, current, want string }{
		{"no newline", "MANIFEST-000007", "one file name and a newline"},
		{"two lines", "MANIFEST-000007\nMANIFEST-000008\n", "not a MANIFEST file"},
		{"a path", "../MANIFEST-000007\n", "not a MANIFEST file"},
		{"a number that is too short", "MANIFEST-7\n", "not a MANIFEST file"},
		{"a carriage return", "MANIFEST-000007\r\n", "not a MANIFEST file"},
		{"nothing", "", "one file name and a newline"},
		{"too long", strings.Repeat("MANIFEST-000007\n", 8), "more than the"},
		{"another file", "000009.log\n", "not a MANIFEST file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDBRefs(writeStage(t, tc.current, good))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected an error containing %q, got %v", tc.want, err)
			}
		})
	}
	// The MANIFEST CURRENT names is not there.
	if _, err := readDBRefs(writeStage(t, "MANIFEST-000008\n", good)); err == nil {
		t.Fatalf("a missing MANIFEST was accepted")
	}
}

// modeStub wraps a FileInfo and reports another mode: the way these tests give a
// file the kind of a link without the privilege to make one.
type modeStub struct {
	os.FileInfo
	mode os.FileMode
}

func (m modeStub) Mode() os.FileMode { return m.mode }

// stubLstat makes lstatFile report the given mode for the file at path.
func stubLstat(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	real := lstatFile
	lstatFile = func(name string) (os.FileInfo, error) {
		info, err := real(name)
		if err == nil && filepath.Clean(name) == filepath.Clean(path) {
			return modeStub{info, mode}, nil
		}
		return info, err
	}
	t.Cleanup(func() { lstatFile = real })
}

// Every file that is read to find out what to copy is a regular file that is
// opened without following a link: a link is refused however it got there.
func TestRefsNeverReadThroughALink(t *testing.T) {
	good := manifestBytes(t, header(9, 20))
	for _, name := range []string{"CURRENT", "MANIFEST-000007"} {
		for kind, mode := range map[string]os.FileMode{"a symbolic link": os.ModeSymlink | 0o777, "a directory": os.ModeDir | 0o755, "a named pipe": os.ModeNamedPipe | 0o644, "a device": os.ModeDevice | 0o644} {
			t.Run(name+" is "+kind, func(t *testing.T) {
				dir := writeStage(t, "MANIFEST-000007\n", good)
				stubLstat(t, filepath.Join(dir, name), mode)
				_, err := readDBRefs(dir)
				if err == nil || !strings.Contains(err.Error(), kind+", not a regular file") {
					t.Fatalf("expected a refusal because %s is %s, got %v", name, kind, err)
				}
			})
		}
	}
	t.Run("a file that is not the one lstat saw", func(t *testing.T) {
		dir := writeStage(t, "MANIFEST-000007\n", good)
		other := filepath.Join(t.TempDir(), "other")
		if err := os.WriteFile(other, []byte("MANIFEST-000007\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		real := lstatFile
		lstatFile = func(name string) (os.FileInfo, error) {
			if filepath.Clean(name) == filepath.Join(dir, "CURRENT") {
				return real(other)
			}
			return real(name)
		}
		t.Cleanup(func() { lstatFile = real })
		if _, err := readDBRefs(dir); err == nil || !strings.Contains(err.Error(), "not the file it was a moment ago") {
			t.Fatalf("a file other than the one lstat saw was read: %v", err)
		}
	})
	t.Run("a real link", func(t *testing.T) {
		canSymlink(t)
		dir := writeStage(t, "MANIFEST-000007\n", good)
		secret := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(secret, []byte("MANIFEST-000007\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "CURRENT")); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, secret, filepath.Join(dir, "CURRENT"))
		if _, err := readDBRefs(dir); err == nil {
			t.Fatalf("a CURRENT that is a link was read")
		}
	})
}
