package codec

import (
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"

	"nhbchain/core/types"
	consensusv1 "nhbchain/proto/consensus/v1"
)

// BigIntToProto converts a big.Int into its protobuf representation.
func BigIntToProto(v *big.Int) *consensusv1.BigInt {
	if v == nil {
		return nil
	}
	return &consensusv1.BigInt{Value: v.String()}
}

// BigIntFromProto parses a protobuf big integer.
func BigIntFromProto(v *consensusv1.BigInt) (*big.Int, error) {
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
func TransactionToProto(tx *types.Transaction) (*consensusv1.Transaction, error) {
	if tx == nil {
		return nil, nil
	}
	msg := &consensusv1.Transaction{
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
func TransactionFromProto(msg *consensusv1.Transaction) (*types.Transaction, error) {
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

// TransactionFromEnvelope converts a signed envelope into a core transaction after verifying the signature.
func TransactionFromEnvelope(envelope *consensusv1.SignedTxEnvelope) (*types.Transaction, error) {
	if envelope == nil {
		return nil, fmt.Errorf("envelope: transaction required")
	}
	body := envelope.GetEnvelope()
	if body == nil {
		return nil, fmt.Errorf("envelope: missing body")
	}
	signature := envelope.GetSignature()
	if signature == nil {
		return nil, fmt.Errorf("envelope: missing signature")
	}
	sigBytes := signature.GetSignature()
	if len(sigBytes) < 64 {
		return nil, fmt.Errorf("envelope: invalid signature length")
	}
	pubKey := signature.GetPublicKey()
	if len(pubKey) == 0 {
		return nil, fmt.Errorf("envelope: public key required")
	}
	rawBody, err := proto.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("envelope: marshal body: %w", err)
	}
	digest := sha256.Sum256(rawBody)
	if !crypto.VerifySignature(pubKey, digest[:], sigBytes[:64]) {
		return nil, fmt.Errorf("envelope: signature verification failed")
	}
	payload := body.GetPayload()
	if payload == nil {
		return nil, fmt.Errorf("envelope: payload required")
	}
	var protoTx consensusv1.Transaction
	if err := payload.UnmarshalTo(&protoTx); err != nil {
		return nil, fmt.Errorf("envelope: decode payload: %w", err)
	}
	tx, err := TransactionFromProto(&protoTx)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("envelope: decoded transaction nil")
	}
	if nonce := body.GetNonce(); nonce != 0 {
		if tx.Nonce != 0 && tx.Nonce != nonce {
			return nil, fmt.Errorf("envelope: nonce mismatch")
		}
		tx.Nonce = nonce
	}
	return tx, nil
}

// BlockHeaderToProto converts a block header.
func BlockHeaderToProto(header *types.BlockHeader) *consensusv1.BlockHeader {
	if header == nil {
		return nil
	}
	return &consensusv1.BlockHeader{
		Height:    header.Height,
		Timestamp: header.Timestamp,
		PrevHash:  append([]byte(nil), header.PrevHash...),
		StateRoot: append([]byte(nil), header.StateRoot...),
		TxRoot:    append([]byte(nil), header.TxRoot...),
		Validator: append([]byte(nil), header.Validator...),
	}
}

// BlockHeaderFromProto converts a protobuf header to the core type.
func BlockHeaderFromProto(msg *consensusv1.BlockHeader) *types.BlockHeader {
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
func BlockToProto(block *types.Block) (*consensusv1.Block, error) {
	if block == nil {
		return nil, nil
	}
	txs := make([]*consensusv1.Transaction, len(block.Transactions))
	for i, tx := range block.Transactions {
		converted, err := TransactionToProto(tx)
		if err != nil {
			return nil, err
		}
		txs[i] = converted
	}
	return &consensusv1.Block{
		Header:       BlockHeaderToProto(block.Header),
		Transactions: txs,
	}, nil
}

// BlockFromProto converts the protobuf message into a core block instance.
func BlockFromProto(msg *consensusv1.Block) (*types.Block, error) {
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
func TransactionsToProto(txs []*types.Transaction) ([]*consensusv1.Transaction, error) {
	if len(txs) == 0 {
		return nil, nil
	}
	out := make([]*consensusv1.Transaction, len(txs))
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
func TransactionsFromProto(msgs []*consensusv1.Transaction) ([]*types.Transaction, error) {
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
