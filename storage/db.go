package storage

import (
	ethdb "github.com/ethereum/go-ethereum/ethdb"
	ethdbleveldb "github.com/ethereum/go-ethereum/ethdb/leveldb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/triedb"
)

// Database is a generic interface for a key-value store that also exposes the
// underlying trie database used for the canonical state.
type Database interface {
	Put(key []byte, value []byte) error
	Get(key []byte) ([]byte, error)
	TrieDB() *triedb.Database
	Close()
}

// --- In-Memory DB (for testing) ---

type MemDB struct {
	db     ethdb.Database
	trieDB *triedb.Database
}

func NewMemDB() *MemDB {
	mem := memorydb.New()
	return &MemDB{
		db:     mem,
		trieDB: triedb.NewDatabase(mem, triedb.HashDefaults),
	}
}

func (db *MemDB) Put(key []byte, value []byte) error {
	return db.db.Put(key, value)
}

func (db *MemDB) Get(key []byte) ([]byte, error) {
	return db.db.Get(key)
}

func (db *MemDB) TrieDB() *triedb.Database {
	return db.trieDB
}

// Close satisfies the Database interface for MemDB.
func (db *MemDB) Close() {
	db.trieDB.Close()
	db.db.Close()
}

// --- Persistent DB (for mainnet) ---

// LevelDB is a persistent key-value store using go-ethereum's LevelDB wrapper.
type LevelDB struct {
	db     ethdb.Database
	trieDB *triedb.Database
}

// NewLevelDB creates or opens a LevelDB database at the specified path.
func NewLevelDB(path string) (*LevelDB, error) {
	db, err := ethdbleveldb.New(path, 128, 64, "nhbchain/db", false)
	if err != nil {
		return nil, err
	}
	return &LevelDB{
		db:     db,
		trieDB: triedb.NewDatabase(db, triedb.HashDefaults),
	}, nil
}

// Put inserts or updates a key-value pair.
func (ldb *LevelDB) Put(key []byte, value []byte) error {
	return ldb.db.Put(key, value)
}

// Get retrieves a value for a given key.
func (ldb *LevelDB) Get(key []byte) ([]byte, error) {
	return ldb.db.Get(key)
}

// TrieDB exposes the trie database handle used for MPT storage.
func (ldb *LevelDB) TrieDB() *triedb.Database {
	return ldb.trieDB
}

// Close closes the database connection.
func (ldb *LevelDB) Close() {
	ldb.trieDB.Close()
	ldb.db.Close()
}
