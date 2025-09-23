package state

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	"nhbchain/core/types"
)

var (
	accountMetadataPrefix = []byte("account-meta:")
	usernameIndexKey      = ethcrypto.Keccak256([]byte("username-index"))
	validatorSetKey       = ethcrypto.Keccak256([]byte("validator-set"))
)

type accountMetadata struct {
	BalanceZNHB     *big.Int
	Stake           *big.Int
	Username        string
	EngagementScore uint64
}

type validatorEntry struct {
	Address []byte
	Power   *big.Int
}

func accountStateKey(addr []byte) []byte {
	return ethcrypto.Keccak256(addr)
}

func accountMetadataKey(addr []byte) []byte {
	buf := make([]byte, len(accountMetadataPrefix)+len(addr))
	copy(buf, accountMetadataPrefix)
	copy(buf[len(accountMetadataPrefix):], addr)
	return ethcrypto.Keccak256(buf)
}

func ensureAccountDefaults(account *types.Account) {
	if account.BalanceNHB == nil {
		account.BalanceNHB = big.NewInt(0)
	}
	if account.BalanceZNHB == nil {
		account.BalanceZNHB = big.NewInt(0)
	}
	if account.Stake == nil {
		account.Stake = big.NewInt(0)
	}
	if len(account.StorageRoot) == 0 {
		account.StorageRoot = gethtypes.EmptyRootHash.Bytes()
	}
	if len(account.CodeHash) == 0 {
		account.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
}

// GetAccount reconstructs the high-level account structure stored under the
// provided address.
func (m *Manager) GetAccount(addr []byte) (*types.Account, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	stateAcc, err := m.loadStateAccount(addr)
	if err != nil {
		return nil, err
	}
	meta, err := m.loadAccountMetadata(addr)
	if err != nil {
		return nil, err
	}

	account := &types.Account{
		BalanceNHB:      big.NewInt(0),
		BalanceZNHB:     big.NewInt(0),
		Stake:           big.NewInt(0),
		EngagementScore: 0,
		StorageRoot:     gethtypes.EmptyRootHash.Bytes(),
		CodeHash:        gethtypes.EmptyCodeHash.Bytes(),
	}
	if stateAcc != nil {
		if stateAcc.Balance != nil {
			account.BalanceNHB = new(big.Int).Set(stateAcc.Balance.ToBig())
		}
		account.Nonce = stateAcc.Nonce
		account.StorageRoot = stateAcc.Root.Bytes()
		account.CodeHash = common.CopyBytes(stateAcc.CodeHash)
	}
	if meta != nil {
		if meta.BalanceZNHB != nil {
			account.BalanceZNHB = new(big.Int).Set(meta.BalanceZNHB)
		}
		if meta.Stake != nil {
			account.Stake = new(big.Int).Set(meta.Stake)
		}
		account.Username = meta.Username
		account.EngagementScore = meta.EngagementScore
	}
	ensureAccountDefaults(account)
	return account, nil
}

// PutAccount persists the provided account state under the supplied address.
func (m *Manager) PutAccount(addr []byte, account *types.Account) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if account == nil {
		return fmt.Errorf("nil account")
	}
	ensureAccountDefaults(account)

	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		return fmt.Errorf("balance overflow")
	}
	stateAcc := &gethtypes.StateAccount{
		Nonce:    account.Nonce,
		Balance:  balance,
		Root:     common.BytesToHash(account.StorageRoot),
		CodeHash: common.CopyBytes(account.CodeHash),
	}
	if len(stateAcc.CodeHash) == 0 {
		stateAcc.CodeHash = gethtypes.EmptyCodeHash.Bytes()
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := m.writeStateAccount(addr, stateAcc); err != nil {
		return err
	}

	meta := &accountMetadata{
		BalanceZNHB:     new(big.Int).Set(account.BalanceZNHB),
		Stake:           new(big.Int).Set(account.Stake),
		Username:        account.Username,
		EngagementScore: account.EngagementScore,
	}
	if err := m.writeAccountMetadata(addr, meta); err != nil {
		return err
	}
	return nil
}

// PutAccountMetadata persists only the account metadata (non-EVM balances,
// stake, usernames) for the supplied address.
func (m *Manager) PutAccountMetadata(addr []byte, account *types.Account) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if account == nil {
		account = &types.Account{}
	}
	ensureAccountDefaults(account)
	meta := &accountMetadata{
		BalanceZNHB:     new(big.Int).Set(account.BalanceZNHB),
		Stake:           new(big.Int).Set(account.Stake),
		Username:        account.Username,
		EngagementScore: account.EngagementScore,
	}
	return m.writeAccountMetadata(addr, meta)
}

func (m *Manager) loadStateAccount(addr []byte) (*gethtypes.StateAccount, error) {
	key := accountStateKey(addr)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	stateAcc := new(gethtypes.StateAccount)
	if err := rlp.DecodeBytes(data, stateAcc); err != nil {
		slim := new(gethtypes.SlimAccount)
		if errSlim := rlp.DecodeBytes(data, slim); errSlim == nil {
			restored := &gethtypes.StateAccount{
				Nonce:   slim.Nonce,
				Balance: slim.Balance,
				Root:    gethtypes.EmptyRootHash,
				CodeHash: func() []byte {
					if len(slim.CodeHash) == 0 {
						return gethtypes.EmptyCodeHash.Bytes()
					}
					return append([]byte(nil), slim.CodeHash...)
				}(),
			}
			if len(slim.Root) != 0 {
				restored.Root = common.BytesToHash(slim.Root)
			}
			return restored, nil
		}
		return nil, err
	}
	return stateAcc, nil
}

func (m *Manager) writeStateAccount(addr []byte, stateAcc *gethtypes.StateAccount) error {
	key := accountStateKey(addr)
	encoded, err := rlp.EncodeToBytes(stateAcc)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
}

func (m *Manager) loadAccountMetadata(addr []byte) (*accountMetadata, error) {
	key := accountMetadataKey(addr)
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	meta := &accountMetadata{
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}
	if len(data) == 0 {
		return meta, nil
	}
	if err := rlp.DecodeBytes(data, meta); err != nil {
		return nil, err
	}
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	return meta, nil
}

func (m *Manager) writeAccountMetadata(addr []byte, meta *accountMetadata) error {
	if meta.BalanceZNHB == nil {
		meta.BalanceZNHB = big.NewInt(0)
	}
	if meta.Stake == nil {
		meta.Stake = big.NewInt(0)
	}
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return m.trie.Update(accountMetadataKey(addr), encoded)
}

// ResetUsernameIndex clears any stored username mappings.
func (m *Manager) ResetUsernameIndex() error {
	empty := make(map[string][]byte)
	encoded, err := rlp.EncodeToBytes(empty)
	if err != nil {
		return err
	}
	return m.trie.Update(usernameIndexKey, encoded)
}

// WriteValidatorSet persists the validator powers map.
// EncodeValidatorSet serializes the validator powers map into a deterministic
// RLP representation.
func EncodeValidatorSet(set map[string]*big.Int) ([]byte, error) {
	entries := make([]validatorEntry, 0, len(set))
	for key, power := range set {
		entry := validatorEntry{
			Address: append([]byte(nil), []byte(key)...),
			Power:   big.NewInt(0),
		}
		if power != nil {
			entry.Power = new(big.Int).Set(power)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].Address, entries[j].Address) < 0
	})
	return rlp.EncodeToBytes(entries)
}

// DecodeValidatorSet deserializes validator metadata produced by
// EncodeValidatorSet.
func DecodeValidatorSet(data []byte) (map[string]*big.Int, error) {
	if len(data) == 0 {
		return map[string]*big.Int{}, nil
	}
	var entries []validatorEntry
	if err := rlp.DecodeBytes(data, &entries); err != nil {
		return nil, err
	}
	result := make(map[string]*big.Int, len(entries))
	for _, entry := range entries {
		addrCopy := append([]byte(nil), entry.Address...)
		key := string(addrCopy)
		if entry.Power == nil {
			result[key] = big.NewInt(0)
			continue
		}
		result[key] = new(big.Int).Set(entry.Power)
	}
	return result, nil
}

func (m *Manager) WriteValidatorSet(set map[string]*big.Int) error {
	encoded, err := EncodeValidatorSet(set)
	if err != nil {
		return err
	}
	return m.trie.Update(validatorSetKey, encoded)
}

// LoadValidatorSet retrieves the validator set map stored in state.
func (m *Manager) LoadValidatorSet() (map[string]*big.Int, error) {
	data, err := m.trie.Get(validatorSetKey)
	if err != nil {
		return nil, err
	}
	return DecodeValidatorSet(data)
}
