package main

// The file format a LevelDB journal and a LevelDB MANIFEST share
// (leveldb/journal/journal.go): blocks of 32 KiB holding chunks that each start
// with a checksum, a length and a type. A record is one chunk of the full type, or
// a first chunk, any number of middle chunks and a last chunk. A chunk never
// crosses a block; what is left of a block after its last chunk, when that is less
// than a header, is zero.
//
// The LevelDB library reads these files leniently: it skips what it cannot read,
// and a file with a journal's name and any other content opens without complaint.
// This reads them strictly, because what is read here is packed and published:
// every chunk has to be one, with the checksum it says, in the order a writer
// makes them, and nothing else may follow. The one thing it can be told to accept
// is what a copy taken while the node writes can have and nothing else can: the
// end of the file may cut a chunk, or a record, short.

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	journalBlockSize  = 32 * 1024
	journalHeaderSize = 7

	chunkFull   = 1
	chunkFirst  = 2
	chunkMiddle = 3
	chunkLast   = 4
)

// journalWalk says how a file is read and what is done with what it holds.
type journalWalk struct {
	// TornOK accepts a file that ends inside a chunk or between the chunks of a
	// record (a copy of a file a running node was still writing).
	TornOK bool
	// MaxRecord bounds one record.
	MaxRecord int
	// Whole is called with every complete record. The slice is reused: copy what
	// is kept.
	Whole func(record []byte) error
	// Prefix is called, when TornOK, with what there is of the record the file
	// ends in.
	Prefix func(record []byte) error
}

// walkJournal reads r, which holds a journal-format file, and calls Whole with
// each of its records.
func walkJournal(r io.Reader, w journalWalk) error {
	var (
		block    = make([]byte, journalBlockSize)
		record   []byte
		inRecord bool
		offset   int64
	)
	for {
		n, err := io.ReadFull(r, block)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return err
		}
		if n == 0 {
			break
		}
		// Only the last block can be shorter than a block, and it is the one a
		// copy cut short ends in.
		short := n < journalBlockSize
		pos := 0
		for pos < n {
			at := offset + int64(pos)
			if n-pos < journalHeaderSize {
				// Too little left for a chunk header: the zero padding at the end of a
				// block, or, in the last block of a copy, the start of a header.
				switch {
				case !short && !isZero(block[pos:n]):
					return fmt.Errorf("byte %d: what follows the last chunk of a block is not zero", at)
				case short && !w.TornOK:
					return fmt.Errorf("byte %d: the file ends in the middle of a chunk header", at)
				}
				break
			}
			sum := binary.LittleEndian.Uint32(block[pos : pos+4])
			length := int(binary.LittleEndian.Uint16(block[pos+4 : pos+6]))
			kind := block[pos+6]
			if kind < chunkFull || kind > chunkLast {
				return fmt.Errorf("byte %d: a chunk of type %d, which does not exist: this is not a journal", at, kind)
			}
			end := pos + journalHeaderSize + length
			if end > journalBlockSize {
				return fmt.Errorf("byte %d: a chunk that runs past the end of its block: this is not a journal", at)
			}
			starts := kind == chunkFull || kind == chunkFirst
			if starts && inRecord {
				return fmt.Errorf("byte %d: a record starts inside another", at)
			}
			if !starts && !inRecord {
				return fmt.Errorf("byte %d: a chunk of type %d with no record to continue", at, kind)
			}
			if end > n {
				// Only a copy cut short ends inside a chunk (n is less than a block, and
				// no chunk crosses a block). What there is of the chunk has to be what a
				// record starts or goes on with.
				if !w.TornOK {
					return fmt.Errorf("byte %d: the file ends in the middle of a chunk", at)
				}
				partial := block[pos+journalHeaderSize : n]
				if starts {
					record = record[:0]
				}
				if len(record)+len(partial) > w.MaxRecord {
					return fmt.Errorf("byte %d: a record of more than %d bytes", at, w.MaxRecord)
				}
				record = append(record, partial...)
				return w.Prefix(record)
			}
			if got := util.NewCRC(block[pos+journalHeaderSize-1 : end]).Value(); got != sum {
				return fmt.Errorf("byte %d: the checksum of a chunk is wrong: this is not a journal", at)
			}
			payload := block[pos+journalHeaderSize : end]
			if starts {
				record = append(record[:0], payload...)
			} else {
				if len(record)+len(payload) > w.MaxRecord {
					return fmt.Errorf("byte %d: a record of more than %d bytes", at, w.MaxRecord)
				}
				record = append(record, payload...)
			}
			if len(record) > w.MaxRecord {
				return fmt.Errorf("byte %d: a record of more than %d bytes", at, w.MaxRecord)
			}
			inRecord = kind == chunkFirst || kind == chunkMiddle
			if !inRecord {
				if err := w.Whole(record); err != nil {
					return fmt.Errorf("byte %d: %w", at, err)
				}
			}
			pos = end
		}
		offset += int64(n)
		if short {
			break
		}
	}
	if inRecord {
		if !w.TornOK {
			return fmt.Errorf("the file ends in the middle of a record")
		}
		// The copy ends between two chunks of a record.
		return w.Prefix(record)
	}
	return nil
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
