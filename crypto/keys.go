package crypto

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"

	"github.com/btcsuite/btcutil/bech32"
	"github.com/ethereum/go-ethereum/crypto"
)

// AddressPrefix defines the different types of human-readable address prefixes.
type AddressPrefix string

const (
	NHBPrefix  AddressPrefix = "nhb"
	ZNHBPrefix AddressPrefix = "znhb"
)

// Address represents a 20-byte NHBCoin address with a specific prefix.
type Address struct {
	prefix AddressPrefix
	bytes  []byte
}

func NewAddress(prefix AddressPrefix, b []byte) Address {
	if len(b) != 20 {
		panic("address must be 20 bytes long")
	}
	return Address{prefix: prefix, bytes: b}
}

func (a Address) String() string {
	conv, err := bech32.ConvertBits(a.bytes, 8, 5, true)
	if err != nil {
		panic(err)
	}
	encoded, err := bech32.Encode(string(a.prefix), conv)
	if err != nil {
		panic(err)
	}
	return encoded
}

func (a Address) Bytes() []byte {
	return a.bytes
}

// Prefix returns the human-readable prefix associated with the address.
func (a Address) Prefix() AddressPrefix {
	return a.prefix
}

func DecodeAddress(addrStr string) (Address, error) {
	prefix, decoded, err := bech32.Decode(addrStr)
	if err != nil {
		return Address{}, fmt.Errorf("invalid bech32 string: %w", err)
	}
	conv, err := bech32.ConvertBits(decoded, 5, 8, false)
	if err != nil {
		return Address{}, fmt.Errorf("error converting bits: %w", err)
	}
	return NewAddress(AddressPrefix(prefix), conv), nil
}

// --- Key Management ---

type PrivateKey struct {
	*ecdsa.PrivateKey
}

type PublicKey struct {
	*ecdsa.PublicKey
}

func GeneratePrivateKey() (*PrivateKey, error) {
	key, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{key}, nil
}

// Bytes returns the byte representation of the private key.
func (k *PrivateKey) Bytes() []byte {
	return crypto.FromECDSA(k.PrivateKey)
}

func (k *PrivateKey) PubKey() *PublicKey {
	return &PublicKey{&k.PrivateKey.PublicKey}
}

func (k *PublicKey) Address() Address {
	addrBytes := crypto.PubkeyToAddress(*k.PublicKey).Bytes()
	return NewAddress(NHBPrefix, addrBytes)
}

func PrivateKeyFromBytes(b []byte) (*PrivateKey, error) {
	key, err := crypto.ToECDSA(b)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{key}, nil
}
