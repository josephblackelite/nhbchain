package main

// What the MANIFEST of a LevelDB directory says the database is made of.
//
// A chain database directory holds more than the database: leftovers of a
// compaction, an older MANIFEST, a table that is still being written, and, in
// a directory the node's own user controls, anything that user chose to put
// there under a name that looks like one of the database's. A snapshot must
// hold exactly the files the database refers to, so this reads them out of the
// MANIFEST instead of trusting names: CURRENT names the MANIFEST, the MANIFEST
// lists the tables and names the journal.
//
// The record layout is goleveldb's (leveldb/session_record.go, whose constants
// are "written to disk and should not be changed"); every MANIFEST written by
// the node's own LevelDB library reads back exactly as that library reads it,
// and TestRefsMatchWhatLevelDBReports checks the table list against the
// library's own on databases that have compacted. Unlike the library this
// refuses what it does not understand: a record type it does not know, a
// record that does not end where its fields end, a damaged chunk.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

const (
	recComparer       = 1
	recJournalNum     = 2
	recNextFileNum    = 3
	recSeqNum         = 4
	recCompPtr        = 5
	recDelTable       = 6
	recAddTable       = 7
	recPrevJournalNum = 9

	// maxCurrentBytes bounds CURRENT, which holds one file name and a newline.
	maxCurrentBytes = 64
	// maxManifestFileBytes bounds a MANIFEST that is read to find the tables:
	// the live chain's is a few hundred kilobytes, and a running node starts a
	// new one every time it starts.
	maxManifestFileBytes = 128 << 20
	// maxManifestRecord bounds one record of it.
	maxManifestRecord = 64 << 20
	// maxLevel is far above the seven levels LevelDB has.
	maxLevel = 1024
)

var (
	manifestNamePattern = regexp.MustCompile(`^MANIFEST-[0-9]{6,}$`)
	journalNamePattern  = regexp.MustCompile(`^([0-9]{6,})\.log$`)
)

// lstatFile is os.Lstat. Tests replace it to report a symbolic link, or a
// different file than the one that is opened, without needing the privilege
// to make one (Windows asks for one).
var lstatFile = os.Lstat

// describeMode says what kind of file a mode is, for a message that starts
// "... is ".
func describeMode(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "a symbolic link"
	case m.IsDir():
		return "a directory"
	case m&os.ModeNamedPipe != 0:
		return "a named pipe"
	case m&os.ModeSocket != 0:
		return "a socket"
	case m&os.ModeDevice != 0:
		return "a device"
	}
	return "not a regular file"
}

// openPlain opens the regular file at path for reading. A symbolic link, or
// anything else that is not a regular file, is refused, and so is a file that
// is not the one lstat saw: the last component of the path is never followed,
// by the open itself where the platform can do that (O_NOFOLLOW) and by the
// comparison of what was opened with what lstat reported where it cannot.
func openPlain(path string) (*os.File, os.FileInfo, error) {
	before, err := lstatFile(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is %s, not a regular file", path, describeMode(before.Mode()))
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s is not the file it was a moment ago: refusing to read it", path)
	}
	return f, after, nil
}

// readPlainFile reads the regular file at path, which may not be longer than
// max bytes.
func readPlainFile(path string, max int64) ([]byte, error) {
	f, info, err := openPlain(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, more than the %d it may have", path, info.Size(), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s grew past %d bytes while it was read", path, max)
	}
	return data, nil
}

func journalFileName(num int64) string { return fmt.Sprintf("%06d.log", num) }

// journalNumber returns the file number of a journal file name.
func journalNumber(name string) (int64, bool) {
	m := journalNamePattern.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// dbRefs is what a MANIFEST says about the files of its database.
type dbRefs struct {
	// Manifest is the MANIFEST file CURRENT names.
	Manifest string
	// JournalNum is the journal the database recovers from: the one write-ahead
	// journal that holds what no table does.
	JournalNum int64
	// PrevJournalNum is the journal before it, when the MANIFEST names one (zero
	// when it does not).
	PrevJournalNum int64
	NextFileNum    int64
	// Tables are the table files the database is made of: file number to the
	// size the MANIFEST records for it.
	Tables map[int64]int64
}

// journalNames returns the journal files the MANIFEST names.
func (r *dbRefs) journalNames() []string {
	names := []string{journalFileName(r.JournalNum)}
	if r.PrevJournalNum != 0 && r.PrevJournalNum != r.JournalNum {
		names = append(names, journalFileName(r.PrevJournalNum))
	}
	return names
}

// tableNames returns the table files the MANIFEST lists, in file number order.
func (r *dbRefs) tableNames() []string {
	nums := make([]int64, 0, len(r.Tables))
	for n := range r.Tables {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	names := make([]string, 0, len(nums))
	for _, n := range nums {
		names = append(names, tableFileName(n))
	}
	return names
}

// refsBuilder replays the records of a MANIFEST.
type refsBuilder struct {
	haveComparer, haveJournal, haveNext, haveSeq bool
	journal, prev, next                          int64
	// levels holds the tables of each level. A table is added to and deleted
	// from one level: a move to another level is a delete and an add.
	levels map[uint64]map[int64]int64
}

// tableRecord is one "add table" or "delete table" field of a record.
type tableRecord struct {
	level     uint64
	num, size int64
}

func newRefsBuilder() *refsBuilder {
	return &refsBuilder{levels: map[uint64]map[int64]int64{}}
}

func unexpectedEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// apply reads one record: a series of fields, each a varint tag and its
// values. The record has to end exactly where a field ends. As in the LevelDB
// library, the tables a record deletes are removed first and the tables it adds
// are added after that, whatever the order of the fields.
func (b *refsBuilder) apply(rec []byte) error {
	var dels, adds []tableRecord
	r := bytes.NewReader(rec)
	uvarint := func(field string) (uint64, error) {
		v, err := binary.ReadUvarint(r)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", field, unexpectedEOF(err))
		}
		return v, nil
	}
	fileNum := func(field string) (int64, error) {
		v, err := uvarint(field)
		if err != nil {
			return 0, err
		}
		if v > math.MaxInt64 {
			return 0, fmt.Errorf("%s: %d is not a file number", field, v)
		}
		return int64(v), nil
	}
	level := func(field string) (uint64, error) {
		v, err := uvarint(field)
		if err != nil {
			return 0, err
		}
		if v > maxLevel {
			return 0, fmt.Errorf("%s: level %d does not exist", field, v)
		}
		return v, nil
	}
	skipBytes := func(field string) error {
		n, err := uvarint(field)
		if err != nil {
			return err
		}
		if n > uint64(r.Len()) {
			return fmt.Errorf("%s: %w", field, io.ErrUnexpectedEOF)
		}
		_, _ = r.Seek(int64(n), io.SeekCurrent)
		return nil
	}
	for {
		tag, err := binary.ReadUvarint(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("field header: %w", err)
		}
		switch tag {
		case recComparer:
			if err := skipBytes("comparer"); err != nil {
				return err
			}
			b.haveComparer = true
		case recJournalNum:
			if b.journal, err = fileNum("journal number"); err != nil {
				return err
			}
			b.haveJournal = true
		case recPrevJournalNum:
			if b.prev, err = fileNum("previous journal number"); err != nil {
				return err
			}
		case recNextFileNum:
			if b.next, err = fileNum("next file number"); err != nil {
				return err
			}
			b.haveNext = true
		case recSeqNum:
			if _, err = uvarint("sequence number"); err != nil {
				return err
			}
			b.haveSeq = true
		case recCompPtr:
			if _, err = level("compaction pointer level"); err != nil {
				return err
			}
			if err := skipBytes("compaction pointer key"); err != nil {
				return err
			}
		case recDelTable:
			lvl, err := level("deleted table level")
			if err != nil {
				return err
			}
			num, err := fileNum("deleted table number")
			if err != nil {
				return err
			}
			dels = append(dels, tableRecord{level: lvl, num: num})
		case recAddTable:
			lvl, err := level("added table level")
			if err != nil {
				return err
			}
			num, err := fileNum("added table number")
			if err != nil {
				return err
			}
			size, err := fileNum("added table size")
			if err != nil {
				return err
			}
			if err := skipBytes("added table smallest key"); err != nil {
				return err
			}
			if err := skipBytes("added table largest key"); err != nil {
				return err
			}
			adds = append(adds, tableRecord{level: lvl, num: num, size: size})
		default:
			return fmt.Errorf("a field of type %d, which this tool does not know", tag)
		}
	}
	for _, d := range dels {
		delete(b.levels[d.level], d.num)
	}
	for _, a := range adds {
		if b.levels[a.level] == nil {
			b.levels[a.level] = map[int64]int64{}
		}
		b.levels[a.level][a.num] = a.size
	}
	return nil
}

// refs returns what the records said, once every one has been applied.
func (b *refsBuilder) refs() (*dbRefs, error) {
	switch {
	case !b.haveComparer:
		return nil, fmt.Errorf("it names no comparer")
	case !b.haveNext:
		return nil, fmt.Errorf("it holds no next file number")
	case !b.haveJournal:
		return nil, fmt.Errorf("it names no journal")
	case !b.haveSeq:
		return nil, fmt.Errorf("it holds no sequence number")
	}
	out := &dbRefs{JournalNum: b.journal, PrevJournalNum: b.prev, NextFileNum: b.next, Tables: map[int64]int64{}}
	for _, tables := range b.levels {
		for num, size := range tables {
			if _, dup := out.Tables[num]; dup {
				return nil, fmt.Errorf("table %d is in two levels", num)
			}
			out.Tables[num] = size
		}
	}
	return out, nil
}

// decodeManifest reads the records of a MANIFEST. It is strict (see
// journalfile.go): a chunk that does not check out, a stray byte after the last
// record, a file that ends inside a record, is an error, not something to skip.
func decodeManifest(r io.Reader) (*dbRefs, error) {
	b := newRefsBuilder()
	records := 0
	err := walkJournal(r, journalWalk{
		MaxRecord: maxManifestRecord,
		Whole: func(rec []byte) error {
			records++
			if err := b.apply(rec); err != nil {
				return fmt.Errorf("a record that cannot be read: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("the MANIFEST is damaged: %w", err)
	}
	if records == 0 {
		return nil, fmt.Errorf("the MANIFEST holds no record")
	}
	out, err := b.refs()
	if err != nil {
		return nil, fmt.Errorf("the MANIFEST is incomplete: %w", err)
	}
	return out, nil
}

// parseCurrent returns the MANIFEST file name CURRENT holds: one name and a
// newline, the form the LevelDB library writes.
func parseCurrent(data []byte) (string, error) {
	if len(data) < 2 || data[len(data)-1] != '\n' {
		return "", fmt.Errorf("CURRENT does not hold one file name and a newline")
	}
	name := string(data[:len(data)-1])
	if !manifestNamePattern.MatchString(name) {
		return "", fmt.Errorf("CURRENT names %q, which is not a MANIFEST file", name)
	}
	return name, nil
}

// readDBRefs reads CURRENT and the MANIFEST it names from dir. Nothing else has
// to be there: a directory that holds only these two is enough to find out
// which tables and which journal the rest of the database has to hold.
func readDBRefs(dir string) (*dbRefs, error) {
	current, err := readPlainFile(filepath.Join(dir, "CURRENT"), maxCurrentBytes)
	if err != nil {
		return nil, fmt.Errorf("CURRENT: %w", err)
	}
	name, err := parseCurrent(current)
	if err != nil {
		return nil, err
	}
	f, info, err := openPlain(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("the MANIFEST %s that CURRENT names: %w", name, err)
	}
	defer f.Close()
	if info.Size() > maxManifestFileBytes {
		return nil, fmt.Errorf("the MANIFEST %s is %d bytes, more than the %d it may have", name, info.Size(), int64(maxManifestFileBytes))
	}
	refs, err := decodeManifest(io.LimitReader(f, maxManifestFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	refs.Manifest = name
	return refs, nil
}
