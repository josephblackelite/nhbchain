package codec

import (
	"fmt"
	"math/big"

	"nhbchain/core/types"
	consensuspb "nhbchain/proto/consensus"
)

// BigIntToProto converts a big.Int into its protobuf representation.
func BigIntToProto(v *big.Int) *consensuspb.BigInt {
	if v == nil {
		return nil
	}
	return &consensuspb.BigInt{Value: v.String()}
}

// BigIntFromProto parses a protobuf big integer.
func BigIntFromProto(v *consensuspb.BigInt) (*big.Int, error) {
	if v == nil || v.Value == "" {
		return nil, nil
	}
	parsed, ok := new(big.Int).SetString(v.Value, 10)
	if !ok {
		return nil, fmt.Errorf("invalid big integer %q", v.Value)
	}
	return parsed, nil
}

// TransactionToProto converts a core transaction into the protobuf shape.
func TransactionToProto(tx *types.Transaction) (*consensuspb.Transaction, error) {
	if tx == nil {
		return nil, nil
	}
	msg := &consensuspb.Transaction{
		ChainId:    BigIntToProto(tx.ChainID),
		Type:       uint32(tx.Type),
		Nonce:      tx.Nonce,
		To:         append([]byte(nil), tx.To...),
		Value:      BigIntToProto(tx.Value),
		Data:       append([]byte(nil), tx.Data...),
		GasLimit:   tx.GasLimit,
		GasPrice:   BigIntToProto(tx.GasPrice),
		Paymaster:  append([]byte(nil), tx.Paymaster...),
		R:          BigIntToProto(tx.R),
		S:          BigIntToProto(tx.S),
		V:          BigIntToProto(tx.V),
		PaymasterR: BigIntToProto(tx.PaymasterR),
		PaymasterS: BigIntToProto(tx.PaymasterS),
		PaymasterV: BigIntToProto(tx.PaymasterV),
	}
	return msg, nil
}

// TransactionFromProto converts a protobuf transaction into the core type.
func TransactionFromProto(msg *consensuspb.Transaction) (*types.Transaction, error) {
	if msg == nil {
		return nil, nil
	}
	tx := &types.Transaction{
		Type:      types.TxType(msg.Type),
		Nonce:     msg.Nonce,
		To:        append([]byte(nil), msg.To...),
		Data:      append([]byte(nil), msg.Data...),
		GasLimit:  msg.GasLimit,
		Paymaster: append([]byte(nil), msg.Paymaster...),
	}
	var err error
	if tx.ChainID, err = BigIntFromProto(msg.ChainId); err != nil {
		return nil, err
	}
	if tx.Value, err = BigIntFromProto(msg.Value); err != nil {
		return nil, err
	}
	if tx.GasPrice, err = BigIntFromProto(msg.GasPrice); err != nil {
		return nil, err
	}
	if tx.R, err = BigIntFromProto(msg.R); err != nil {
		return nil, err
	}
	if tx.S, err = BigIntFromProto(msg.S); err != nil {
		return nil, err
	}
	if tx.V, err = BigIntFromProto(msg.V); err != nil {
		return nil, err
	}
	if tx.PaymasterR, err = BigIntFromProto(msg.PaymasterR); err != nil {
		return nil, err
	}
	if tx.PaymasterS, err = BigIntFromProto(msg.PaymasterS); err != nil {
		return nil, err
	}
	if tx.PaymasterV, err = BigIntFromProto(msg.PaymasterV); err != nil {
		return nil, err
	}
	return tx, nil
}

// BlockHeaderToProto converts a block header.
func BlockHeaderToProto(header *types.BlockHeader) *consensuspb.BlockHeader {
	if header == nil {
		return nil
	}
	return &consensuspb.BlockHeader{
		Height:    header.Height,
		Timestamp: header.Timestamp,
		PrevHash:  append([]byte(nil), header.PrevHash...),
		StateRoot: append([]byte(nil), header.StateRoot...),
		TxRoot:    append([]byte(nil), header.TxRoot...),
		Validator: append([]byte(nil), header.Validator...),
	}
}

// BlockHeaderFromProto converts a protobuf header to the core type.
func BlockHeaderFromProto(msg *consensuspb.BlockHeader) *types.BlockHeader {
	if msg == nil {
		return nil
	}
	return &types.BlockHeader{
		Height:    msg.Height,
		Timestamp: msg.Timestamp,
		PrevHash:  append([]byte(nil), msg.PrevHash...),
		StateRoot: append([]byte(nil), msg.StateRoot...),
		TxRoot:    append([]byte(nil), msg.TxRoot...),
		Validator: append([]byte(nil), msg.Validator...),
	}
}

// BlockToProto converts a block, including its transactions.
func BlockToProto(block *types.Block) (*consensuspb.Block, error) {
	if block == nil {
		return nil, nil
	}
	txs := make([]*consensuspb.Transaction, len(block.Transactions))
	for i, tx := range block.Transactions {
		converted, err := TransactionToProto(tx)
		if err != nil {
			return nil, err
		}
		txs[i] = converted
	}
	return &consensuspb.Block{
		Header:       BlockHeaderToProto(block.Header),
		Transactions: txs,
	}, nil
}

// BlockFromProto converts the protobuf message into a core block instance.
func BlockFromProto(msg *consensuspb.Block) (*types.Block, error) {
	if msg == nil {
		return nil, nil
	}
	txs := make([]*types.Transaction, len(msg.Transactions))
	for i, protoTx := range msg.Transactions {
		tx, err := TransactionFromProto(protoTx)
		if err != nil {
			return nil, err
		}
		txs[i] = tx
	}
	return &types.Block{
		Header:       BlockHeaderFromProto(msg.Header),
		Transactions: txs,
	}, nil
}

// TransactionsToProto converts a slice of transactions.
func TransactionsToProto(txs []*types.Transaction) ([]*consensuspb.Transaction, error) {
	if len(txs) == 0 {
		return nil, nil
	}
	out := make([]*consensuspb.Transaction, len(txs))
	for i, tx := range txs {
		converted, err := TransactionToProto(tx)
		if err != nil {
			return nil, err
		}
		out[i] = converted
	}
	return out, nil
}

// TransactionsFromProto converts protobuf transactions into core transactions.
func TransactionsFromProto(msgs []*consensuspb.Transaction) ([]*types.Transaction, error) {
	if len(msgs) == 0 {
		return nil, nil
	}
	out := make([]*types.Transaction, len(msgs))
	for i, msg := range msgs {
		tx, err := TransactionFromProto(msg)
		if err != nil {
			return nil, err
		}
		out[i] = tx
	}
	return out, nil
}
