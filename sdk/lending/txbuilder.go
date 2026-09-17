package lending

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const defaultPoolID = "default"

// TxOption customises a transaction built by the New*Tx helpers below before signing.
type TxOption func(*types.Transaction)

// WithGasLimit overrides the default gas limit.
func WithGasLimit(limit uint64) TxOption {
	return func(tx *types.Transaction) {
		if tx != nil {
			tx.GasLimit = limit
		}
	}
}

// WithGasPrice overrides the default gas price.
func WithGasPrice(price *big.Int) TxOption {
	return func(tx *types.Transaction) {
		if tx != nil && price != nil {
			tx.GasPrice = new(big.Int).Set(price)
		}
	}
}

const (
	defaultLendingGasLimit = uint64(50_000)
	defaultLendingGasPrice = "1"
)

func defaultGasPrice() *big.Int {
	v, _ := new(big.Int).SetString(defaultLendingGasPrice, 10)
	return v
}

type lendingPayload struct {
	PoolID string `json:"poolId,omitempty"`
}

type liquidatePayload struct {
	PoolID   string `json:"poolId,omitempty"`
	Borrower string `json:"borrower"`
}

func encodePayload(poolID string) []byte {
	trimmed := strings.TrimSpace(poolID)
	if trimmed == "" || trimmed == defaultPoolID {
		return nil
	}
	data, _ := json.Marshal(lendingPayload{PoolID: trimmed})
	return data
}

func newLendingTx(txType types.TxType, chainID *big.Int, nonce uint64, amount *big.Int, data []byte, opts []TxOption) *types.Transaction {
	tx := &types.Transaction{
		ChainID:  new(big.Int).Set(chainID),
		Type:     txType,
		Nonce:    nonce,
		Value:    new(big.Int).Set(amount),
		Data:     data,
		GasLimit: defaultLendingGasLimit,
		GasPrice: defaultGasPrice(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(tx)
		}
	}
	return tx
}

func parseAmount(amount string) (*big.Int, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return nil, fmt.Errorf("lending: amount required")
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok || value.Sign() <= 0 {
		return nil, fmt.Errorf("lending: amount must be a positive integer")
	}
	return value, nil
}

// NewSupplyTx builds an unsigned TxTypeLendingSupplyNHB transaction.
func NewSupplyTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingSupplyNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewWithdrawTx builds an unsigned TxTypeLendingWithdrawNHB transaction.
func NewWithdrawTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingWithdrawNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewBorrowTx builds an unsigned TxTypeLendingBorrowNHB transaction.
func NewBorrowTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingBorrowNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewRepayTx builds an unsigned TxTypeLendingRepayNHB transaction.
func NewRepayTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingRepayNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewDepositCollateralTx builds an unsigned TxTypeLendingDepositZNHB transaction.
func NewDepositCollateralTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingDepositZNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewWithdrawCollateralTx builds an unsigned TxTypeLendingWithdrawZNHB transaction.
func NewWithdrawCollateralTx(chainID *big.Int, nonce uint64, poolID, amount string, opts ...TxOption) (*types.Transaction, error) {
	value, err := parseAmount(amount)
	if err != nil {
		return nil, err
	}
	return newLendingTx(types.TxTypeLendingWithdrawZNHB, chainID, nonce, value, encodePayload(poolID), opts), nil
}

// NewLiquidateTx builds an unsigned TxTypeLendingLiquidate transaction. It is
// signed by the liquidator, not the borrower -- see
// core/lending_native.go's applyLendingLiquidate.
func NewLiquidateTx(chainID *big.Int, nonce uint64, poolID, borrower string, opts ...TxOption) (*types.Transaction, error) {
	trimmedBorrower := strings.TrimSpace(borrower)
	if trimmedBorrower == "" {
		return nil, fmt.Errorf("lending: borrower address required")
	}
	trimmedPool := strings.TrimSpace(poolID)
	if trimmedPool == "" {
		trimmedPool = defaultPoolID
	}
	data, err := json.Marshal(liquidatePayload{PoolID: trimmedPool, Borrower: trimmedBorrower})
	if err != nil {
		return nil, fmt.Errorf("lending: encode liquidate payload: %w", err)
	}
	tx := newLendingTx(types.TxTypeLendingLiquidate, chainID, nonce, big.NewInt(0), data, opts)
	return tx, nil
}

// SignAndEncode signs tx with key and JSON-encodes it in the exact shape the
// node's nhb_sendTransaction RPC accepts (core/types.Transaction's own JSON
// tags) -- the same signed_tx_json passthrough SupplyAsset/WithdrawAsset/
// BorrowAsset/RepayAsset/DepositCollateral/WithdrawCollateral/Liquidate expect.
func SignAndEncode(tx *types.Transaction, key *ecdsa.PrivateKey) (string, error) {
	if tx == nil {
		return "", fmt.Errorf("lending: transaction required")
	}
	if key == nil {
		return "", fmt.Errorf("lending: signing key required")
	}
	if tx.GasLimit == 0 {
		return "", fmt.Errorf("lending: gas limit must be greater than zero")
	}
	if tx.GasPrice == nil || tx.GasPrice.Sign() <= 0 {
		return "", fmt.Errorf("lending: gas price must be greater than zero")
	}
	if err := tx.Sign(key); err != nil {
		return "", fmt.Errorf("lending: sign transaction: %w", err)
	}
	encoded, err := json.Marshal(tx)
	if err != nil {
		return "", fmt.Errorf("lending: encode signed transaction: %w", err)
	}
	return string(encoded), nil
}

// SenderAddress derives the bech32 nhb address for a private key, matching
// the account string mutation calls expect for validation/logging.
func SenderAddress(key *crypto.PrivateKey) (string, error) {
	if key == nil {
		return "", fmt.Errorf("lending: signing key required")
	}
	pub := key.PubKey()
	if pub == nil || pub.PublicKey == nil {
		return "", fmt.Errorf("lending: derive sender address: public key unavailable")
	}
	return pub.Address().String(), nil
}
