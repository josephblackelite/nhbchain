package types

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/crypto"
)

// TxType defines the purpose of a transaction.
type TxType byte

const (
	TxTypeTransfer         TxType = 0x01 // A standard transfer of NHB
	TxTypeRegisterIdentity TxType = 0x02 // A transaction to claim a username
	TxTypeCreateEscrow     TxType = 0x03 // Create escrow
	TxTypeReleaseEscrow    TxType = 0x04 // NEW: Buyer releases funds to seller
	TxTypeRefundEscrow     TxType = 0x05 // NEW: Seller refunds funds to buyer
	TxTypeStake            TxType = 0x06 // Implenting stake
	TxTypeUnstake          TxType = 0x07 // NEW: A transaction to un-stake ZapNHB
	TxTypeHeartbeat        TxType = 0x08 // Heartbeat from users device
)

// Transaction now has a Type field to distinguish its intent.
// Transaction now supports gas fees and a paymaster.
type Transaction struct {
	Type     TxType   `json:"type"`
	Nonce    uint64   `json:"nonce"`
	To       []byte   `json:"to"`
	Value    *big.Int `json:"value"`
	Data     []byte   `json:"data"`
	GasLimit uint64   `json:"gasLimit"` // The maximum gas the user is willing to pay
	GasPrice *big.Int `json:"gasPrice"` // The price per unit of gas

	Paymaster []byte `json:"paymaster,omitempty"` // NEW: Address of the gas fee sponsor

	// Signatures
	R, S, V                            *big.Int `json:"r"`                    // Sender's signature
	PaymasterR, PaymasterS, PaymasterV *big.Int `json:"paymasterR,omitempty"` // NEW: Paymaster's signature

	from []byte
}

// Hash logic must now include the new Type field.
func (tx *Transaction) Hash() ([]byte, error) {
	txData := struct {
		Type     TxType
		Nonce    uint64
		To       []byte
		Value    *big.Int
		Data     []byte
		GasLimit uint64
		GasPrice *big.Int
	}{tx.Type, tx.Nonce, tx.To, tx.Value, tx.Data, tx.GasLimit, tx.GasPrice}

	b, err := json.Marshal(txData)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	return hash[:], nil
}

// ... (Sign and From methods remain the same)
func (tx *Transaction) Sign(privKey *ecdsa.PrivateKey) error {
	hash, err := tx.Hash()
	if err != nil {
		return err
	}
	sig, err := crypto.Sign(hash, privKey)
	if err != nil {
		return err
	}
	tx.R = new(big.Int).SetBytes(sig[:32])
	tx.S = new(big.Int).SetBytes(sig[32:64])
	tx.V = new(big.Int).SetBytes([]byte{sig[64] + 27})
	return nil
}

func (tx *Transaction) From() ([]byte, error) {
	if tx.from != nil {
		return tx.from, nil
	}
	hash, err := tx.Hash()
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 65)
	copy(sig[32-len(tx.R.Bytes()):32], tx.R.Bytes())
	copy(sig[64-len(tx.S.Bytes()):64], tx.S.Bytes())
	sig[64] = byte(tx.V.Uint64() - 27)
	pubKey, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return nil, err
	}
	tx.from = crypto.PubkeyToAddress(*pubKey).Bytes()
	return tx.from, nil
}
