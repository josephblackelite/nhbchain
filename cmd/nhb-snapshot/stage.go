package main

// Checks of a staged copy of a chain database, before anything of it is packed.
//
// scripts/make-snapshot.sh makes the copy from a directory that the node's own
// user controls, and what it packs is published. The script copies only what the
// copied MANIFEST refers to, but this does not rely on that: a staged copy is
// accepted only if every file in it is one its own MANIFEST refers to (CURRENT,
// the MANIFEST CURRENT names, the tables it lists, the journal it names), each a
// regular file, every journal reads as a journal of write batches, and every
// table reads in full. A file that merely has a name that looks like one of
// those (a journal that is a page of text, a table nothing lists, an older
// MANIFEST) makes the copy unfit to pack.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxJournalRecord bounds one record of a journal, which is one write batch.
const maxJournalRecord = 64 << 20

// verifyStage checks the files of the staged copy in dir and returns what its
// MANIFEST says.
func verifyStage(dir string) (*dbRefs, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	refs, err := readDBRefs(dir)
	if err != nil {
		return nil, fmt.Errorf("refusing to pack %s: %w", dir, err)
	}
	seenTable := map[int64]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if housekeepingFiles[name] {
			continue
		}
		if !allowedFileName(name) {
			return nil, fmt.Errorf("refusing to pack %s: %q is not a chain database file (only CURRENT, MANIFEST-*, *.log, *.ldb and *.sst are ever packed)", dir, name)
		}
		path := filepath.Join(dir, name)
		info, err := lstatFile(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to pack %s: %q is %s, not a regular file", dir, name, describeMode(info.Mode()))
		}
		switch {
		case name == "CURRENT" || name == refs.Manifest:
		case strings.HasPrefix(name, "MANIFEST-"):
			return nil, fmt.Errorf("refusing to pack %s: %s is a MANIFEST that CURRENT does not name (it names %s)", dir, name, refs.Manifest)
		default:
			if num, isTable := tableNumber(name); isTable {
				size, listed := refs.Tables[num]
				if !listed || name != tableFileName(num) {
					return nil, fmt.Errorf("refusing to pack %s: the MANIFEST does not list %s (a table that was still being written, or a file that only has a table's name)", dir, name)
				}
				if info.Size() != size {
					return nil, fmt.Errorf("refusing to pack %s: table %s is %d bytes, the MANIFEST says %d", dir, name, info.Size(), size)
				}
				seenTable[num] = true
				continue
			}
			num, isJournal := journalNumber(name)
			if !isJournal {
				return nil, fmt.Errorf("refusing to pack %s: %q is not a file of the database", dir, name)
			}
			named := false
			for _, want := range refs.journalNames() {
				named = named || want == name
			}
			if !named || name != journalFileName(num) {
				return nil, fmt.Errorf("refusing to pack %s: the MANIFEST names the journal %s, not %s", dir, strings.Join(refs.journalNames(), " and "), name)
			}
			if err := checkJournal(path); err != nil {
				return nil, fmt.Errorf("refusing to pack %s: journal %s: %w", dir, name, err)
			}
		}
	}
	for num := range refs.Tables {
		if !seenTable[num] {
			return nil, fmt.Errorf("refusing to pack %s: table %s, which the MANIFEST lists, is not there", dir, tableFileName(num))
		}
	}
	return refs, nil
}

// sameTables checks that the tables this tool read out of the MANIFEST are the
// ones the LevelDB library reports for the database it opened: if the two ever
// read a MANIFEST differently, nothing is packed on the strength of either.
func sameTables(refs *dbRefs, live map[int64]int64) error {
	if len(refs.Tables) != len(live) {
		return fmt.Errorf("the MANIFEST lists %d tables, the database opens with %d: refusing to trust either", len(refs.Tables), len(live))
	}
	for num, size := range refs.Tables {
		if got, ok := live[num]; !ok || got != size {
			return fmt.Errorf("the MANIFEST lists table %s at %d bytes, the database opens without it or with %d: refusing to trust either", tableFileName(num), size, got)
		}
	}
	return nil
}

// scanTables reads every key and value of the open database once. That reads
// every block of every table file and verifies its checksum, so a file that has
// the name of a table and is listed by the MANIFEST, but is not a table, cannot
// pass. It returns how many entries the database holds.
func scanTables(db *chainDB) (uint64, error) {
	it := db.disk.NewIterator(nil, nil)
	defer it.Release()
	var n uint64
	for it.Next() {
		n++
	}
	if err := it.Error(); err != nil {
		return n, fmt.Errorf("the tables of %s do not read in full: %w", db.dir, err)
	}
	return n, nil
}

// openStaged checks that the staged copy in dir holds nothing its MANIFEST does
// not refer to, opens it read-only, checks that the library finds the tables the
// MANIFEST lists and that all of them read in full. The caller closes the result.
func openStaged(dir string) (*chainDB, error) {
	refs, err := verifyStage(dir)
	if err != nil {
		return nil, err
	}
	db, err := openChainDB(dir)
	if err != nil {
		return nil, err
	}
	if err := sameTables(refs, db.tables); err != nil {
		db.close()
		return nil, err
	}
	if _, err := scanTables(db); err != nil {
		db.close()
		return nil, err
	}
	return db, nil
}

// openAndReadStaged is openAndReadIdentity for a staged copy: it first checks
// that the copy holds nothing its MANIFEST does not refer to.
func openAndReadStaged(dir string, opts checkOptions) (*chainIdentity, error) {
	db, err := openStaged(dir)
	if err != nil {
		return nil, err
	}
	defer db.close()
	return db.readIdentity(opts)
}

// checkJournal checks that the file at path is a LevelDB journal, or a copy of
// one that a running node was still writing: a series of records, each of which
// is a write batch, in chunks whose checksums are right (see journalfile.go). The
// one thing it accepts is what such a copy can have and nothing else can: the end
// of the file may cut a chunk, or a record, short. Whatever is left of a record
// there is checked as far as it goes.
func checkJournal(path string) error {
	f, _, err := openPlain(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return walkJournal(f, journalWalk{TornOK: true, MaxRecord: maxJournalRecord, Whole: checkBatch, Prefix: checkBatchPrefix})
}

// batchEntry reads one entry of a write batch (leveldb/batch.go: a type, a key
// and, for a put, a value) from the start of body. It returns how many bytes it
// takes, or 0 and whether the entry is cut short by the end of body.
func batchEntry(body []byte) (used int, cut bool, err error) {
	kind := body[0]
	if kind > 1 {
		return 0, false, fmt.Errorf("an entry of type %d, which a write batch does not have", kind)
	}
	o := uint64(1)
	for part := 0; part < 1+int(kind); part++ {
		if o >= uint64(len(body)) {
			return 0, true, nil
		}
		length, w := binary.Uvarint(body[o:])
		if w == 0 {
			return 0, true, nil
		}
		if w < 0 {
			return 0, false, errors.New("an entry with a length that is not a number")
		}
		o += uint64(w)
		if length > uint64(len(body)) || o+length > uint64(len(body)) {
			return 0, true, nil
		}
		o += length
	}
	return int(o), false, nil
}

// checkBatch checks that data is a LevelDB write batch (leveldb/batch.go): a
// header of the first sequence number and the number of records, then that many
// records of a type, a key and, for a put, a value.
func checkBatch(data []byte) error {
	if len(data) < 12 {
		return errors.New("shorter than a write batch header")
	}
	want := binary.LittleEndian.Uint32(data[8:12])
	body := data[12:]
	var n uint32
	for len(body) > 0 {
		used, cut, err := batchEntry(body)
		if err != nil {
			return fmt.Errorf("record %d: %w", n, err)
		}
		if cut {
			return fmt.Errorf("record %d is cut short", n)
		}
		body = body[used:]
		n++
	}
	if n != want {
		return fmt.Errorf("the header says %d records, the batch holds %d", want, n)
	}
	return nil
}

// checkBatchPrefix checks the start of a write batch, which is all that a copy
// cut short in the middle of a record has: the entries there are right so far,
// and there are not more of them than the header says.
func checkBatchPrefix(data []byte) error {
	if len(data) < 12 {
		return nil
	}
	want := binary.LittleEndian.Uint32(data[8:12])
	body := data[12:]
	var n uint32
	for len(body) > 0 {
		used, cut, err := batchEntry(body)
		if err != nil {
			return fmt.Errorf("the record that the copy ends in: %w", err)
		}
		if cut {
			break
		}
		body = body[used:]
		n++
		if n > want {
			return fmt.Errorf("the record that the copy ends in holds more than the %d entries its header says", want)
		}
	}
	return nil
}
