package genesis

import (
	"fmt"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/core/types"
	"nhbchain/storage"
)

func BuildGenesisFromSpec(spec *GenesisSpec, db storage.Database) (*types.Block, error) {
	if spec == nil {
		return nil, fmt.Errorf("genesis spec must not be nil")
	}
	if db == nil {
		return nil, fmt.Errorf("database must not be nil")
	}

	ts := spec.GenesisTimestamp()
	if ts.IsZero() {
		parsed, err := parseGenesisTime(spec.GenesisTime)
		if err != nil {
			return nil, err
		}
		ts = parsed
	}

	header := &types.BlockHeader{
		Height:    0,
		Timestamp: ts.Unix(),
		PrevHash:  []byte{},
		StateRoot: gethtypes.EmptyRootHash.Bytes(),
		TxRoot:    gethtypes.EmptyRootHash.Bytes(),
	}

	// TODO: initialize the token registry using spec.NativeTokens once state management is available.
	// TODO: credit account balances from spec.Alloc.
	// TODO: assign account roles from spec.Roles.
	// TODO: register validators from spec.Validators.
	// TODO: persist the initialized state into db.
	_ = db

	block := types.NewBlock(header, nil)
	return block, nil
}
