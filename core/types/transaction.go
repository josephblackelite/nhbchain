package types

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	TxTypeTransfer            TxType = 0x01 // A standard transfer of NHB
	TxTypeRegisterIdentity    TxType = 0x02 // A transaction to claim a username
	TxTypeCreateEscrow        TxType = 0x03 // Create escrow
	TxTypeReleaseEscrow       TxType = 0x04 // NEW: Buyer releases funds to seller
	TxTypeRefundEscrow        TxType = 0x05 // NEW: Seller refunds funds to buyer
	TxTypeStake               TxType = 0x06 // Implenting stake
	TxTypeUnstake             TxType = 0x07 // NEW: A transaction to un-stake ZapNHB
	TxTypeHeartbeat           TxType = 0x08 // Heartbeat from users device
	TxTypeLockEscrow          TxType = 0x09 // NEW: Buyer commits to a purchase
	TxTypeDisputeEscrow       TxType = 0x0A // NEW: Buyer raises a dispute
	TxTypeArbitrateRelease    TxType = 0x0B // NEW: Admin-only action to release to buyer
	TxTypeArbitrateRefund     TxType = 0x0C // NEW: Admin-only action to refund seller
	TxTypeStakeClaim          TxType = 0x0D // NEW: Claim matured unbonded ZapNHB
	TxTypeMint                TxType = 0x0E // NEW: Execute a signed mint voucher on-chain
	TxTypeSwapPayoutReceipt   TxType = 0x0F // NEW: Record a swap payout receipt attested by the treasury
	TxTypeTransferZNHB        TxType = 0x10 // A standard transfer of ZapNHB (ZNHB)
	TxTypeSwapMint            TxType = 0x11 // Native On-Chain Swap minting NHB
	TxTypeSwapBurn            TxType = 0x12 // Native On-Chain Swap burning NHB
	TxTypeLendingSupplyNHB    TxType = 0x13 // Supply NHB liquidity to a lending pool
	TxTypeLendingWithdrawNHB  TxType = 0x14 // Withdraw NHB liquidity from a lending pool
	TxTypeLendingDepositZNHB  TxType = 0x15 // Deposit ZNHB collateral into a lending pool
	TxTypeLendingWithdrawZNHB TxType = 0x16 // Withdraw ZNHB collateral from a lending pool
	TxTypeLendingBorrowNHB    TxType = 0x17 // Borrow NHB against ZNHB collateral
	TxTypeLendingRepayNHB     TxType = 0x18 // Repay NHB debt in a lending pool
	TxTypePOSAuthorize        TxType = 0x20 // Pre-authorize a merchant payment
	TxTypePOSCapture          TxType = 0x21 // Capture an authorized payment
	TxTypePOSVoid             TxType = 0x22 // Void authorized payment
	TxTypePOSRegistry         TxType = 0x23 // POS merchant/device registry update
)

// RequiresSignature reports whether the transaction type must carry an
// originator signature that can be recovered via From(). Types that originate
// from module attestations rely on their envelope signatures instead.
func RequiresSignature(t TxType) bool {
	switch t {
	case TxTypeMint:
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

	MaxBlockHeight uint64 `json:"maxBlockHeight,omitempty"`

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

	// secp256k1HalfN is half the secp256k1 curve order, used for signature malleability checks
	secp256k1HalfN = new(big.Int).Div(crypto.S256().Params().N, big.NewInt(2))
)

const maxIntentRefLength = 1024

func paddedAddress20(addr []byte, field string) ([]byte, error) {
	if len(addr) == 0 {
		return make([]byte, 20), nil
	}
	if len(addr) > 20 {
		return nil, fmt.Errorf("%s length must not exceed 20 bytes", field)
	}
	padded := make([]byte, 20)
	copy(padded[20-len(addr):], addr)
	return padded, nil
}

func writeBytes(buf *bytes.Buffer, data []byte) error {
	if len(data) > math.MaxUint16 {
		return fmt.Errorf("byte payload length %d exceeds max %d", len(data), math.MaxUint16)
	}
	binary.Write(buf, binary.BigEndian, uint16(len(data)))
	buf.Write(data)
	return nil
}

// ValidateBasic performs non-stateful transaction shape validation.
func (tx *Transaction) ValidateBasic() error {
	if tx == nil {
		return fmt.Errorf("transaction required")
	}
	if len(tx.To) > 20 {
		return fmt.Errorf("to length must not exceed 20 bytes")
	}
	if len(tx.Paymaster) > 20 {
		return fmt.Errorf("paymaster length must not exceed 20 bytes")
	}
	if len(tx.IntentRef) > maxIntentRefLength {
		return fmt.Errorf("intentRef length must not exceed %d bytes", maxIntentRefLength)
	}
	for name, value := range map[string]*big.Int{
		"value":      tx.Value,
		"gasPrice":   tx.GasPrice,
		"r":          tx.R,
		"s":          tx.S,
		"v":          tx.V,
		"paymasterR": tx.PaymasterR,
		"paymasterS": tx.PaymasterS,
		"paymasterV": tx.PaymasterV,
	} {
		if value != nil && value.Sign() < 0 {
			return fmt.Errorf("%s must not be negative", name)
		}
	}
	return nil
}

// writeString securely encodes strings for binary hashing.
func writeString(buf *bytes.Buffer, s string) {
	strBytes := []byte(strings.TrimSpace(s))
	binary.Write(buf, binary.BigEndian, uint32(len(strBytes)))
	buf.Write(strBytes)
}

// Hash logic must now include the new Type field.
func (tx *Transaction) Hash() ([]byte, error) {
	if err := tx.ValidateBasic(); err != nil {
		return nil, err
	}
	if tx.Type > 0 {
		// V3 Canonical Binary Encoding for Native Types
		buf := new(bytes.Buffer)
		buf.WriteString("NHB_TX_V3_MAINNET")

		if tx.ChainID != nil {
			binary.Write(buf, binary.BigEndian, tx.ChainID.Uint64())
		} else {
			binary.Write(buf, binary.BigEndian, uint64(0))
		}

		binary.Write(buf, binary.BigEndian, uint8(tx.Type))
		binary.Write(buf, binary.BigEndian, tx.Nonce)
		binary.Write(buf, binary.BigEndian, tx.MaxBlockHeight)
		binary.Write(buf, binary.BigEndian, tx.IntentExpiry)
		if err := writeBytes(buf, tx.IntentRef); err != nil {
			return nil, fmt.Errorf("intentRef: %w", err)
		}

		// To Address
		toPad, err := paddedAddress20(tx.To, "to")
		if err != nil {
			return nil, err
		}
		buf.Write(toPad)

		// Value
		if tx.Value != nil {
			valBytes := tx.Value.Bytes()
			binary.Write(buf, binary.BigEndian, uint16(len(valBytes)))
			buf.Write(valBytes)
		} else {
			binary.Write(buf, binary.BigEndian, uint16(0))
		}

		// Data Payload
		binary.Write(buf, binary.BigEndian, uint32(len(tx.Data)))
		buf.Write(tx.Data)

		// Fees
		binary.Write(buf, binary.BigEndian, tx.GasLimit)
		if tx.GasPrice != nil {
			gpBytes := tx.GasPrice.Bytes()
			binary.Write(buf, binary.BigEndian, uint16(len(gpBytes)))
			buf.Write(gpBytes)
		} else {
			binary.Write(buf, binary.BigEndian, uint16(0))
		}

		// Optional fields
		if len(tx.Paymaster) > 0 {
			buf.Write([]byte{1})
			pmPad, err := paddedAddress20(tx.Paymaster, "paymaster")
			if err != nil {
				return nil, err
			}
			buf.Write(pmPad)
		} else {
			buf.Write([]byte{0})
		}

		writeString(buf, tx.MerchantAddress)
		writeString(buf, tx.DeviceID)
		writeString(buf, tx.RefundOf)

		hash := sha256.Sum256(buf.Bytes())
		return hash[:], nil
	}

	// Legacy V2 / EVM JSON fallback
	txData := struct {
		ChainID        *big.Int
		Type           TxType
		Nonce          uint64
		MaxBlockHeight uint64
		To             []byte
		Value          *big.Int
		Data           []byte
		GasLimit       uint64
		GasPrice       *big.Int
		Paymaster      []byte `json:"paymaster,omitempty"`
		IntentRef      []byte `json:"intentRef,omitempty"`
		IntentExpiry   uint64 `json:"intentExpiry,omitempty"`
		MerchantAddr   string `json:"merchantAddr,omitempty"`
		DeviceID       string `json:"deviceId,omitempty"`
		RefundOf       string `json:"refundOf,omitempty"`
	}{ChainID: tx.ChainID, Type: tx.Type, Nonce: tx.Nonce, MaxBlockHeight: tx.MaxBlockHeight, To: tx.To, Value: tx.Value, Data: tx.Data, GasLimit: tx.GasLimit, GasPrice: tx.GasPrice, IntentExpiry: tx.IntentExpiry, MerchantAddr: tx.MerchantAddress, DeviceID: tx.DeviceID, RefundOf: strings.TrimSpace(tx.RefundOf)}

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
	if tx.S.Cmp(secp256k1HalfN) > 0 {
		return nil, fmt.Errorf("invalid signature: S > secp256k1n/2 (malleability protection)")
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
	if tx.PaymasterS.Cmp(secp256k1HalfN) > 0 {
		return nil, ErrPaymasterSignatureInvalid
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
