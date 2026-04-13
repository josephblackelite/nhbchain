package core

import (
	"math/big"

	"nhbchain/core/types"
	consensusv1 "nhbchain/proto/consensus/v1"
)

// ConsensusAPI defines the subset of node capabilities required by consensus services.
type ConsensusAPI interface {
	SubmitTransaction(tx *types.Transaction) error
	SubmitTxEnvelope(tx *consensusv1.SignedTxEnvelope) error
	GetValidatorSet() map[string]*big.Int
	GetBlockByHeight(height uint64) (*types.Block, error)
	GetHeight() uint64
	GetMempool() []*types.Transaction
	CreateBlock(txs []*types.Transaction) (*types.Block, error)
	CommitBlock(block *types.Block) error
	GetLastCommitHash() []byte
	QueryState(namespace, key string) (*QueryResult, error)
	QueryPrefix(namespace, prefix string) ([]QueryRecord, error)
	SimulateTx(txBytes []byte) (*SimulationResult, error)
}
