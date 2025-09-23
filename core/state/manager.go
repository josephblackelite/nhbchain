package state

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/storage/trie"
)

// Manager provides a minimal interface for reading and writing state data during
// genesis initialisation.
type Manager struct {
	trie *trie.Trie
}

// NewManager creates a state manager operating on the provided trie.
func NewManager(tr *trie.Trie) *Manager {
	return &Manager{trie: tr}
}

type TokenMetadata struct {
	Symbol        string
	Name          string
	Decimals      uint8
	MintAuthority []byte
	MintPaused    bool
}

var (
	tokenPrefix   = []byte("token:")
	tokenListKey  = ethcrypto.Keccak256([]byte("token-list"))
	balancePrefix = []byte("balance:")
	rolePrefix    = []byte("role:")
)

func tokenMetadataKey(symbol string) []byte {
	buf := make([]byte, len(tokenPrefix)+len(symbol))
	copy(buf, tokenPrefix)
	copy(buf[len(tokenPrefix):], symbol)
	return ethcrypto.Keccak256(buf)
}

func balanceKey(addr []byte, symbol string) []byte {
	buf := make([]byte, len(balancePrefix)+len(symbol)+1+len(addr))
	copy(buf, balancePrefix)
	copy(buf[len(balancePrefix):], symbol)
	buf[len(balancePrefix)+len(symbol)] = ':'
	copy(buf[len(balancePrefix)+len(symbol)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func roleKey(role string) []byte {
	buf := make([]byte, len(rolePrefix)+len(role))
	copy(buf, rolePrefix)
	copy(buf[len(rolePrefix):], role)
	return ethcrypto.Keccak256(buf)
}

func kvKey(key []byte) []byte {
	return ethcrypto.Keccak256(key)
}

func (m *Manager) loadTokenList() ([]string, error) {
	data, err := m.trie.Get(tokenListKey)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	var list []string
	if err := rlp.DecodeBytes(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (m *Manager) writeTokenList(list []string) error {
	encoded, err := rlp.EncodeToBytes(list)
	if err != nil {
		return err
	}
	return m.trie.Update(tokenListKey, encoded)
}

func (m *Manager) loadTokenMetadata(symbol string) (*TokenMetadata, error) {
	key := tokenMetadataKey(symbol)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	meta := new(TokenMetadata)
	if err := rlp.DecodeBytes(data, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func (m *Manager) writeTokenMetadata(symbol string, meta *TokenMetadata) error {
	key := tokenMetadataKey(symbol)
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// RegisterToken stores the metadata for a native token and records it in the
// token index.
func (m *Manager) RegisterToken(symbol, name string, decimals uint8) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("token symbol must not be empty")
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("token %s: name must not be empty", normalized)
	}
	if existing, err := m.loadTokenMetadata(normalized); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("token %s already registered", normalized)
	}

	list, err := m.loadTokenList()
	if err != nil {
		return err
	}
	list = append(list, normalized)
	sort.Strings(list)
	if err := m.writeTokenList(list); err != nil {
		return err
	}

	meta := &TokenMetadata{
		Symbol:   normalized,
		Name:     name,
		Decimals: decimals,
	}
	return m.writeTokenMetadata(normalized, meta)
}

// SetTokenMintAuthority configures the mint authority for the given token.
func (m *Manager) SetTokenMintAuthority(symbol string, authority []byte) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}
	meta.MintAuthority = append([]byte(nil), authority...)
	return m.writeTokenMetadata(normalized, meta)
}

// SetTokenMintPaused stores the paused state for the given token.
func (m *Manager) SetTokenMintPaused(symbol string, paused bool) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}
	meta.MintPaused = paused
	return m.writeTokenMetadata(normalized, meta)
}

// Token retrieves metadata for a registered token.
func (m *Manager) Token(symbol string) (*TokenMetadata, error) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	return m.loadTokenMetadata(normalized)
}

// TokenList returns all registered token symbols in sorted order.
func (m *Manager) TokenList() ([]string, error) {
	return m.loadTokenList()
}

// SetBalance stores an account balance for the provided token.
func (m *Manager) SetBalance(addr []byte, symbol string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if amount == nil {
		amount = big.NewInt(0)
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("negative balance not allowed")
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("token symbol must not be empty")
	}
	if meta, err := m.loadTokenMetadata(normalized); err != nil {
		return err
	} else if meta == nil {
		return fmt.Errorf("token %s not registered", normalized)
	}

	key := balanceKey(addr, normalized)
	encoded, err := rlp.EncodeToBytes(amount)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// Balance retrieves a token balance for the provided account and token.
func (m *Manager) Balance(addr []byte, symbol string) (*big.Int, error) {
	key := balanceKey(addr, strings.ToUpper(strings.TrimSpace(symbol)))
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	amount := new(big.Int)
	if err := rlp.DecodeBytes(data, amount); err != nil {
		return nil, err
	}
	return amount, nil
}

// SetRole associates an address with the specified role. Duplicate assignments
// are ignored while the stored list remains sorted for determinism.
func (m *Manager) SetRole(role string, addr []byte) error {
	trimmed := strings.TrimSpace(role)
	if trimmed == "" {
		return fmt.Errorf("role must not be empty")
	}
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	key := roleKey(trimmed)
	data, err := m.trie.Get(key)
	if err != nil {
		return err
	}
	var members [][]byte
	if len(data) > 0 {
		if err := rlp.DecodeBytes(data, &members); err != nil {
			return err
		}
	}
	found := false
	for _, existing := range members {
		if string(existing) == string(addr) {
			found = true
			break
		}
	}
	if !found {
		members = append(members, append([]byte(nil), addr...))
		sort.Slice(members, func(i, j int) bool {
			return hex.EncodeToString(members[i]) < hex.EncodeToString(members[j])
		})
	}
	encoded, err := rlp.EncodeToBytes(members)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

// RoleMembers returns all addresses assigned to the provided role.
func (m *Manager) RoleMembers(role string) ([][]byte, error) {
	key := roleKey(strings.TrimSpace(role))
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return [][]byte{}, nil
	}
	var members [][]byte
	if err := rlp.DecodeBytes(data, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// HasRole reports whether the provided address is associated with the
// specified role. Errors while reading the underlying state result in a false
// return, matching the best-effort semantics required by the callers.
func (m *Manager) HasRole(role string, addr []byte) bool {
	if len(addr) == 0 {
		return false
	}
	key := roleKey(strings.TrimSpace(role))
	data, err := m.trie.Get(key)
	if err != nil || len(data) == 0 {
		return false
	}
	var members [][]byte
	if err := rlp.DecodeBytes(data, &members); err != nil {
		return false
	}
	for _, member := range members {
		if bytes.Equal(member, addr) {
			return true
		}
	}
	return false
}

// TokenExists reports whether the provided token symbol is registered.
func (m *Manager) TokenExists(symbol string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return false
	}
	meta, err := m.loadTokenMetadata(normalized)
	if err != nil || meta == nil {
		return false
	}
	return true
}

// KVPut stores the provided value under the supplied key using RLP encoding.
// The key is automatically hashed with keccak256 to match the requirements of
// the underlying trie implementation.
func (m *Manager) KVPut(key []byte, value interface{}) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	return m.trie.Update(kvKey(key), encoded)
}

// KVGet retrieves the value stored under the supplied key and decodes it into
// the provided destination. The boolean return value indicates whether the key
// existed in state.
func (m *Manager) KVGet(key []byte, out interface{}) (bool, error) {
	if len(key) == 0 {
		return false, fmt.Errorf("kv: key must not be empty")
	}
	data, err := m.trie.Get(kvKey(key))
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(data, out); err != nil {
		return false, err
	}
	return true, nil
}

// KVAppend appends the provided value to the RLP-encoded byte slice list stored
// under the supplied key. Duplicate values are ignored to keep the index
// deterministic.
func (m *Manager) KVAppend(key []byte, value []byte) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	hashed := kvKey(key)
	data, err := m.trie.Get(hashed)
	if err != nil {
		return err
	}
	var list [][]byte
	if len(data) > 0 {
		if err := rlp.DecodeBytes(data, &list); err != nil {
			return err
		}
	}
	found := false
	for _, existing := range list {
		if bytes.Equal(existing, value) {
			found = true
			break
		}
	}
	if !found {
		list = append(list, append([]byte(nil), value...))
	}
	encoded, err := rlp.EncodeToBytes(list)
	if err != nil {
		return err
	}
	return m.trie.Update(hashed, encoded)
}

// KVGetList retrieves an RLP-encoded slice stored under the provided key and
// decodes it into the supplied destination slice pointer. When no value is
// present the destination is initialised with an empty slice to avoid nil
// surprises for callers.
func (m *Manager) KVGetList(key []byte, out interface{}) error {
	if len(key) == 0 {
		return fmt.Errorf("kv: key must not be empty")
	}
	hashed := kvKey(key)
	data, err := m.trie.Get(hashed)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		val := reflect.ValueOf(out)
		if val.Kind() != reflect.Ptr || val.IsNil() {
			return fmt.Errorf("kv: destination must be a non-nil pointer")
		}
		elem := val.Elem()
		if elem.Kind() != reflect.Slice {
			return fmt.Errorf("kv: destination must point to a slice")
		}
		elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
		return nil
	}
	return rlp.DecodeBytes(data, out)
}
