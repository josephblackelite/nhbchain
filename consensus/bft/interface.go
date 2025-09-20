package bft

import (
	"math/big"
	"nhbchain/core/types"
)

// NodeInterface defines the methods the BFT engine needs to interact with the parent node.
type NodeInterface interface {
	GetMempool() []*types.Transaction
	CreateBlock(txs []*types.Transaction) *types.Block
	CommitBlock(block *types.Block)
	GetValidatorSet() map[string]*big.Int
	GetAccount(addr []byte) (*types.Account, error)
}
