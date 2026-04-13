package store

import (
	"fmt"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/storage"
)

// Store persists consensus-related metadata such as the validator set.
type Store struct {
	db storage.Database
}

// New creates a consensus store backed by the provided database.
func New(db storage.Database) *Store {
	return &Store{db: db}
}

// Validator captures the minimal information required by consensus for a
// validator at genesis.
type Validator struct {
	Address []byte
	PubKey  []byte
	Power   uint64
	Moniker string
}

var validatorSetKey = []byte("consensus/validatorset")

// SaveValidators persists the provided validator list. The caller must ensure
// deterministic ordering of the slice.
func (s *Store) SaveValidators(validators []Validator) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("consensus store uninitialised")
	}
	encoded, err := rlp.EncodeToBytes(validators)
	if err != nil {
		return err
	}
	return s.db.Put(validatorSetKey, encoded)
}
