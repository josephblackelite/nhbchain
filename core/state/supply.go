package state

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"
)

var tokenSupplyPrefix = []byte("token/supply/")

func tokenSupplyKey(symbol string) []byte {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	key := make([]byte, len(tokenSupplyPrefix)+len(normalized))
	copy(key, tokenSupplyPrefix)
	copy(key[len(tokenSupplyPrefix):], normalized)
	return key
}

func (m *Manager) writeTokenSupply(symbol string, total *big.Int) error {
	if m == nil {
		return fmt.Errorf("state manager unavailable")
	}
	if total == nil {
		total = big.NewInt(0)
	}
	encoded, err := rlp.EncodeToBytes(total)
	if err != nil {
		return err
	}
	return m.trie.Update(tokenSupplyKey(symbol), encoded)
}

// TokenSupply returns the persisted total supply for the provided token. Missing
// entries default to zero.
func (m *Manager) TokenSupply(symbol string) (*big.Int, error) {
	if m == nil {
		return nil, fmt.Errorf("state manager unavailable")
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return nil, fmt.Errorf("token symbol required")
	}
	data, err := m.trie.Get(tokenSupplyKey(normalized))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	total := new(big.Int)
	if err := rlp.DecodeBytes(data, total); err != nil {
		return nil, err
	}
	return total, nil
}

// SetTokenSupply overwrites the stored total supply for the token.
func (m *Manager) SetTokenSupply(symbol string, amount *big.Int) error {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return fmt.Errorf("token symbol required")
	}
	if amount != nil && amount.Sign() < 0 {
		return fmt.Errorf("token %s supply cannot be negative", normalized)
	}
	return m.writeTokenSupply(normalized, amount)
}

// AdjustTokenSupply increments the stored total supply by the supplied delta and
// returns the updated total.
func (m *Manager) AdjustTokenSupply(symbol string, delta *big.Int) (*big.Int, error) {
	if m == nil {
		return nil, fmt.Errorf("state manager unavailable")
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	if normalized == "" {
		return nil, fmt.Errorf("token symbol required")
	}
	if delta == nil {
		delta = big.NewInt(0)
	}
	current, err := m.TokenSupply(normalized)
	if err != nil {
		return nil, err
	}
	updated := new(big.Int).Add(current, delta)
	if updated.Sign() < 0 {
		return nil, fmt.Errorf("token %s supply underflow", normalized)
	}
	if err := m.writeTokenSupply(normalized, updated); err != nil {
		return nil, err
	}
	return updated, nil
}
