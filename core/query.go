package core

import (
	"errors"
	"math/big"

	"nhbchain/core/types"
)

// QueryResult encapsulates the raw value and optional proof returned by state queries.
type QueryResult struct {
	Value []byte
	Proof []byte
}

// QueryRecord represents an individual key/value pair returned from a prefix query.
type QueryRecord struct {
	Key   string
	Value []byte
	Proof []byte
}

// SimulationResult captures execution metadata produced when applying a transaction.
type SimulationResult struct {
	GasUsed uint64
	GasCost *big.Int
	Events  []types.Event
}

// ErrQueryNotSupported indicates the requested namespace/path is not handled by the state router.
var ErrQueryNotSupported = errors.New("query: not supported")
