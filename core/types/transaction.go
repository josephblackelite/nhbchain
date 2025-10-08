package types

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

var nhbChainID = big.NewInt(0x4e4842) // ASCII "NHB"

// NHBChainID returns the canonical chain ID for the NHBCoin network.
func NHBChainID() *big.Int {
	return new(big.Int).Set(nhbChainID)
}

// IsValidChainID reports whether the provided chain ID matches the NHBCoin network.
func IsValidChainID(chainID *big.Int) bool {
	if chainID == nil {
		return false
	}
	return chainID.Cmp(nhbChainID) == 0
}

// TxType defines the purpose of a transaction.
type TxType byte

const (
	TxTypeTransfer          TxType = 0x01 // A standard transfer of NHB
	TxTypeTransferZNHB      TxType = 0x10 // A standard transfer of ZapNHB (ZNHB)
	TxTypeRegisterIdentity  TxType = 0x02 // A transaction to claim a username
	TxTypeCreateEscrow      TxType = 0x03 // Create escrow
	TxTypeReleaseEscrow     TxType = 0x04 // NEW: Buyer releases funds to seller
	TxTypeRefundEscrow      TxType = 0x05 // NEW: Seller refunds funds to buyer
	TxTypeStake             TxType = 0x06 // Implenting stake
	TxTypeUnstake           TxType = 0x07 // NEW: A transaction to un-stake ZapNHB
	TxTypeHeartbeat         TxType = 0x08 // Heartbeat from users device
	TxTypeLockEscrow        TxType = 0x09 // NEW: Buyer commits to a purchase
	TxTypeDisputeEscrow     TxType = 0x0A // NEW: Buyer raises a dispute
	TxTypeArbitrateRelease  TxType = 0x0B // NEW: Admin-only action to release to buyer
	TxTypeArbitrateRefund   TxType = 0x0C // NEW: Admin-only action to refund seller
	TxTypeStakeClaim        TxType = 0x0D // NEW: Claim matured unbonded ZapNHB
	TxTypeMint              TxType = 0x0E // NEW: Execute a signed mint voucher on-chain
	TxTypeSwapPayoutReceipt TxType = 0x0F // NEW: Record a swap payout receipt attested by the treasury
)

// RequiresSignature reports whether the transaction type must carry an
// originator signature that can be recovered via From(). Types that originate
// from module attestations rely on their envelope signatures instead.
func RequiresSignature(t TxType) bool {
	switch t {
	case TxTypeMint, TxTypeSwapPayoutReceipt:
		return false
	default:
		return true
	}
}

// Transaction now has a Type field to distinguish its intent.
// Transaction now supports gas fees and a paymaster.
type Transaction struct {
	ChainID  *big.Int `json:"chainId"`
	Type     TxType   `json:"type"`
	Nonce    uint64   `json:"nonce"`
	To       []byte   `json:"to"`
	Value    *big.Int `json:"value"`
	Data     []byte   `json:"data"`
	GasLimit uint64   `json:"gasLimit"` // The maximum gas the user is willing to pay
	GasPrice *big.Int `json:"gasPrice"` // The price per unit of gas

	Paymaster []byte `json:"paymaster,omitempty"` // NEW: Address of the gas fee sponsor

	IntentRef       []byte `json:"intentRef,omitempty"`
	IntentExpiry    uint64 `json:"intentExpiry,omitempty"`
	MerchantAddress string `json:"merchantAddr,omitempty"`
	DeviceID        string `json:"deviceId,omitempty"`
	RefundOf        string `json:"refundOf,omitempty"`

	// Signatures
	R          *big.Int `json:"r"` // Sender's signature
	S          *big.Int `json:"s"`
	V          *big.Int `json:"v"`
	PaymasterR *big.Int `json:"paymasterR,omitempty"` // NEW: Paymaster's signature
	PaymasterS *big.Int `json:"paymasterS,omitempty"`
	PaymasterV *big.Int `json:"paymasterV,omitempty"`

	from []byte
}

var (
	ErrPaymasterSignatureMissing = errors.New("transaction missing paymaster signature")
	ErrPaymasterSignatureInvalid = errors.New("invalid paymaster signature")
)

// Hash logic must now include the new Type field.
func (tx *Transaction) Hash() ([]byte, error) {
	txData := struct {
		ChainID      *big.Int
		Type         TxType
		Nonce        uint64
		To           []byte
		Value        *big.Int
		Data         []byte
		GasLimit     uint64
		GasPrice     *big.Int
		Paymaster    []byte `json:"paymaster,omitempty"`
		IntentRef    []byte `json:"intentRef,omitempty"`
		IntentExpiry uint64 `json:"intentExpiry,omitempty"`
		MerchantAddr string `json:"merchantAddr,omitempty"`
		DeviceID     string `json:"deviceId,omitempty"`
		RefundOf     string `json:"refundOf,omitempty"`
	}{ChainID: tx.ChainID, Type: tx.Type, Nonce: tx.Nonce, To: tx.To, Value: tx.Value, Data: tx.Data, GasLimit: tx.GasLimit, GasPrice: tx.GasPrice, IntentExpiry: tx.IntentExpiry, MerchantAddr: tx.MerchantAddress, DeviceID: tx.DeviceID, RefundOf: strings.TrimSpace(tx.RefundOf)}

	if len(tx.Paymaster) > 0 {
		txData.Paymaster = append([]byte(nil), tx.Paymaster...)
	}
	if len(tx.IntentRef) > 0 {
		txData.IntentRef = append([]byte(nil), tx.IntentRef...)
	}

	b, err := json.Marshal(txData)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	return hash[:], nil
}

// ... (Sign and From methods remain the same)
func (tx *Transaction) Sign(privKey *ecdsa.PrivateKey) error {
	if tx.ChainID == nil {
		return fmt.Errorf("chain id required")
	}
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
	tx.from = nil
	return nil
}

func (tx *Transaction) From() ([]byte, error) {
	if tx.from != nil {
		return tx.from, nil
	}
	if tx.R == nil || tx.S == nil || tx.V == nil {
		return nil, fmt.Errorf("transaction missing signature")
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

// PaymasterSponsor recovers the sponsoring paymaster address from the
// associated signature. A nil result with no error indicates the transaction
// does not request sponsorship.
func (tx *Transaction) PaymasterSponsor() ([]byte, error) {
	if len(tx.Paymaster) == 0 {
		return nil, nil
	}
	if tx.PaymasterR == nil || tx.PaymasterS == nil || tx.PaymasterV == nil {
		return nil, ErrPaymasterSignatureMissing
	}
	hash, err := tx.Hash()
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 65)
	copy(sig[32-len(tx.PaymasterR.Bytes()):32], tx.PaymasterR.Bytes())
	copy(sig[64-len(tx.PaymasterS.Bytes()):64], tx.PaymasterS.Bytes())
	sig[64] = byte(tx.PaymasterV.Uint64() - 27)
	pubKey, err := crypto.SigToPub(hash, sig)
	if err != nil {
		return nil, ErrPaymasterSignatureInvalid
	}
	recovered := crypto.PubkeyToAddress(*pubKey).Bytes()
	if !bytes.Equal(recovered, tx.Paymaster) {
		return nil, ErrPaymasterSignatureInvalid
	}
	sponsor := make([]byte, len(recovered))
	copy(sponsor, recovered)
	return sponsor, nil
}
