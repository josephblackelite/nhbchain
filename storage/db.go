package storage

import (
	"fmt"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
)

// Database is a generic interface for a key-value store.
// This allows our blockchain to use any database backend (in-memory or persistent).
type Database interface {
	Put(key []byte, value []byte) error
	Get(key []byte) ([]byte, error)
	Close() // A way to gracefully shut down the database connection.
}

// --- In-Memory DB (for testing) ---

type MemDB struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemDB() *MemDB {
	return &MemDB{
		data: make(map[string][]byte),
	}
}

func (db *MemDB) Put(key []byte, value []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.data[string(key)] = value
	return nil
}

func (db *MemDB) Get(key []byte) ([]byte, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	value, ok := db.data[string(key)]
	if !ok {
		return nil, fmt.Errorf("key not found")
	}
	return value, nil
}

// Close satisfies the Database interface for MemDB.
func (db *MemDB) Close() {
	// Nothing to close for an in-memory database.
}

// --- Persistent DB (for mainnet) ---

// LevelDB is a persistent key-value store using LevelDB.
type LevelDB struct {
	db *leveldb.DB
}

// NewLevelDB creates or opens a LevelDB database at the specified path.
func NewLevelDB(path string) (*LevelDB, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	return &LevelDB{db: db}, nil
}

// Put inserts or updates a key-value pair.
func (ldb *LevelDB) Put(key []byte, value []byte) error {
	return ldb.db.Put(key, value, nil)
}

// Get retrieves a value for a given key.
func (ldb *LevelDB) Get(key []byte) ([]byte, error) {
	return ldb.db.Get(key, nil)
}

// Close closes the database connection.
func (ldb *LevelDB) Close() {
	ldb.db.Close()
}
