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
	// Staking / balances
	BalanceZNHB        *big.Int
	Stake              *big.Int
	StakeShares        *big.Int
	StakeLastIndex     *big.Int
	StakeLastPayoutTs  uint64
	LockedZNHB         *big.Int
	CollateralBalance  *big.Int
	DebtPrincipal      *big.Int
	SupplyShares       *big.Int
	LendingSupplyIndex *big.Int
	LendingBorrowIndex *big.Int
	DelegatedValidator []byte
	Unbonding          []stakeUnbond
	UnbondingSeq       uint64

	// Identity
	Username string

	// Engagement (EMA + daily meters)
	EngagementScore         uint64
	EngagementDay           string
	EngagementMinutes       uint64
	EngagementTxCount       uint64
	EngagementEscrowEvents  uint64
	EngagementGovEvents     uint64
	EngagementLastHeartbeat uint64

	// Lending breakers
	LendingCollateralDisabled bool
	LendingBorrowDisabled     bool
}

type validatorEntry struct {
	Address []byte
	Power   *big.Int
}

type usernameIndexEntry struct {
	Username string
	Address  []byte
}

type stakeUnbond struct {
	ID          uint64
	Validator   []byte
	Amount      *big.Int
	ReleaseTime uint64
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
	if account.StakeShares == nil {
		account.StakeShares = big.NewInt(0)
	}
	if account.StakeLastIndex == nil {
		account.StakeLastIndex = big.NewInt(0)
	}
	if account.LockedZNHB == nil {
		account.LockedZNHB = big.NewInt(0)
	}
	if account.CollateralBalance == nil {
		account.CollateralBalance = big.NewInt(0)
	}
	if account.DebtPrincipal == nil {
		account.DebtPrincipal = big.NewInt(0)
	}
	if account.SupplyShares == nil {
		account.SupplyShares = big.NewInt(0)
	}
	if account.LendingSnapshot.SupplyIndex == nil {
		account.LendingSnapshot.SupplyIndex = big.NewInt(0)
	}
	if account.LendingSnapshot.BorrowIndex == nil {
		account.LendingSnapshot.BorrowIndex = big.NewInt(0)
	}
	if account.PendingUnbonds == nil {
		account.PendingUnbonds = make([]types.StakeUnbond, 0)
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
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
		StakeShares:             big.NewInt(0),
		StakeLastIndex:          big.NewInt(0),
		EngagementScore:         0,
		EngagementDay:           "",
		EngagementMinutes:       0,
		EngagementTxCount:       0,
		EngagementEscrowEvents:  0,
		EngagementGovEvents:     0,
		EngagementLastHeartbeat: 0,
		StorageRoot:             gethtypes.EmptyRootHash.Bytes(),
		CodeHash:                gethtypes.EmptyCodeHash.Bytes(),
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
		if meta.StakeShares != nil {
			account.StakeShares = new(big.Int).Set(meta.StakeShares)
		}
		if meta.StakeLastIndex != nil {
			account.StakeLastIndex = new(big.Int).Set(meta.StakeLastIndex)
		}
		if meta.LockedZNHB != nil {
			account.LockedZNHB = new(big.Int).Set(meta.LockedZNHB)
		}
		if meta.CollateralBalance != nil {
			account.CollateralBalance = new(big.Int).Set(meta.CollateralBalance)
		}
		if meta.DebtPrincipal != nil {
			account.DebtPrincipal = new(big.Int).Set(meta.DebtPrincipal)
		}
		if meta.SupplyShares != nil {
			account.SupplyShares = new(big.Int).Set(meta.SupplyShares)
		}
		if meta.LendingSupplyIndex != nil {
			account.LendingSnapshot.SupplyIndex = new(big.Int).Set(meta.LendingSupplyIndex)
		}
		if meta.LendingBorrowIndex != nil {
			account.LendingSnapshot.BorrowIndex = new(big.Int).Set(meta.LendingBorrowIndex)
		}
		if len(meta.DelegatedValidator) > 0 {
			account.DelegatedValidator = append([]byte(nil), meta.DelegatedValidator...)
		}
		if len(meta.Unbonding) > 0 {
			account.PendingUnbonds = make([]types.StakeUnbond, len(meta.Unbonding))
			for i, entry := range meta.Unbonding {
				amount := big.NewInt(0)
				if entry.Amount != nil {
					amount = new(big.Int).Set(entry.Amount)
				}
				var validator []byte
				if len(entry.Validator) > 0 {
					validator = append([]byte(nil), entry.Validator...)
				}
				account.PendingUnbonds[i] = types.StakeUnbond{
					ID:          entry.ID,
					Validator:   validator,
					Amount:      amount,
					ReleaseTime: entry.ReleaseTime,
				}
			}
		}
		account.NextUnbondingID = meta.UnbondingSeq
		account.Username = meta.Username

		// Engagement
		account.EngagementScore = meta.EngagementScore
		account.EngagementDay = meta.EngagementDay
		account.EngagementMinutes = meta.EngagementMinutes
		account.EngagementTxCount = meta.EngagementTxCount
		account.EngagementEscrowEvents = meta.EngagementEscrowEvents
		account.EngagementGovEvents = meta.EngagementGovEvents
		account.EngagementLastHeartbeat = meta.EngagementLastHeartbeat
		account.StakeLastPayoutTs = meta.StakeLastPayoutTs
		account.LendingBreaker = types.LendingBreakerFlags{
			CollateralDisabled: meta.LendingCollateralDisabled,
			BorrowDisabled:     meta.LendingBorrowDisabled,
		}
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

	var delegated []byte
	if len(account.DelegatedValidator) > 0 {
		delegated = append([]byte(nil), account.DelegatedValidator...)
	}
	unbonding := make([]stakeUnbond, len(account.PendingUnbonds))
	for i, entry := range account.PendingUnbonds {
		amount := big.NewInt(0)
		if entry.Amount != nil {
			amount = new(big.Int).Set(entry.Amount)
		}
		var validator []byte
		if len(entry.Validator) > 0 {
			validator = append([]byte(nil), entry.Validator...)
		}
		unbonding[i] = stakeUnbond{
			ID:          entry.ID,
			Validator:   validator,
			Amount:      amount,
			ReleaseTime: entry.ReleaseTime,
		}
	}

	meta := &accountMetadata{
		// Staking / balances
		BalanceZNHB:        new(big.Int).Set(account.BalanceZNHB),
		Stake:              new(big.Int).Set(account.Stake),
		StakeShares:        new(big.Int).Set(account.StakeShares),
		StakeLastIndex:     new(big.Int).Set(account.StakeLastIndex),
		StakeLastPayoutTs:  account.StakeLastPayoutTs,
		LockedZNHB:         new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:  new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:      new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:       new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex: new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex: new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		DelegatedValidator: delegated,
		Unbonding:          unbonding,
		UnbondingSeq:       account.NextUnbondingID,

		// Identity
		Username: account.Username,

		// Engagement
		EngagementScore:         account.EngagementScore,
		EngagementDay:           account.EngagementDay,
		EngagementMinutes:       account.EngagementMinutes,
		EngagementTxCount:       account.EngagementTxCount,
		EngagementEscrowEvents:  account.EngagementEscrowEvents,
		EngagementGovEvents:     account.EngagementGovEvents,
		EngagementLastHeartbeat: account.EngagementLastHeartbeat,

		// Lending breakers
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
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

	var delegated []byte
	if len(account.DelegatedValidator) > 0 {
		delegated = append([]byte(nil), account.DelegatedValidator...)
	}
	unbonding := make([]stakeUnbond, len(account.PendingUnbonds))
	for i, entry := range account.PendingUnbonds {
		amount := big.NewInt(0)
		if entry.Amount != nil {
			amount = new(big.Int).Set(entry.Amount)
		}
		var validator []byte
		if len(entry.Validator) > 0 {
			validator = append([]byte(nil), entry.Validator...)
		}
		unbonding[i] = stakeUnbond{
			ID:          entry.ID,
			Validator:   validator,
			Amount:      amount,
			ReleaseTime: entry.ReleaseTime,
		}
	}

	meta := &accountMetadata{
		// Staking / balances
		BalanceZNHB:        new(big.Int).Set(account.BalanceZNHB),
		Stake:              new(big.Int).Set(account.Stake),
		StakeShares:        new(big.Int).Set(account.StakeShares),
		StakeLastIndex:     new(big.Int).Set(account.StakeLastIndex),
		StakeLastPayoutTs:  account.StakeLastPayoutTs,
		LockedZNHB:         new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:  new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:      new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:       new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex: new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex: new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		DelegatedValidator: delegated,
		Unbonding:          unbonding,
		UnbondingSeq:       account.NextUnbondingID,

		// Identity
		Username: account.Username,

		// Engagement
		EngagementScore:         account.EngagementScore,
		EngagementDay:           account.EngagementDay,
		EngagementMinutes:       account.EngagementMinutes,
		EngagementTxCount:       account.EngagementTxCount,
		EngagementEscrowEvents:  account.EngagementEscrowEvents,
		EngagementGovEvents:     account.EngagementGovEvents,
		EngagementLastHeartbeat: account.EngagementLastHeartbeat,

		// Lending breakers
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
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
		BalanceZNHB:    big.NewInt(0),
		Stake:          big.NewInt(0),
		StakeShares:    big.NewInt(0),
		StakeLastIndex: big.NewInt(0),
		LockedZNHB:     big.NewInt(0),
		Unbonding:      make([]stakeUnbond, 0),
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
	if meta.StakeShares == nil {
		meta.StakeShares = big.NewInt(0)
	}
	if meta.StakeLastIndex == nil {
		meta.StakeLastIndex = big.NewInt(0)
	}
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
	if meta.CollateralBalance == nil {
		meta.CollateralBalance = big.NewInt(0)
	}
	if meta.DebtPrincipal == nil {
		meta.DebtPrincipal = big.NewInt(0)
	}
	if meta.SupplyShares == nil {
		meta.SupplyShares = big.NewInt(0)
	}
	if meta.LendingSupplyIndex == nil {
		meta.LendingSupplyIndex = big.NewInt(0)
	}
	if meta.LendingBorrowIndex == nil {
		meta.LendingBorrowIndex = big.NewInt(0)
	}
	if meta.Unbonding == nil {
		meta.Unbonding = make([]stakeUnbond, 0)
	}
	if meta.StakeShares == nil {
		meta.StakeShares = big.NewInt(0)
	}
	if meta.StakeLastIndex == nil {
		meta.StakeLastIndex = big.NewInt(0)
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
	if meta.LockedZNHB == nil {
		meta.LockedZNHB = big.NewInt(0)
	}
	if meta.CollateralBalance == nil {
		meta.CollateralBalance = big.NewInt(0)
	}
	if meta.DebtPrincipal == nil {
		meta.DebtPrincipal = big.NewInt(0)
	}
	if meta.SupplyShares == nil {
		meta.SupplyShares = big.NewInt(0)
	}
	if meta.LendingSupplyIndex == nil {
		meta.LendingSupplyIndex = big.NewInt(0)
	}
	if meta.LendingBorrowIndex == nil {
		meta.LendingBorrowIndex = big.NewInt(0)
	}
	if meta.Unbonding == nil {
		meta.Unbonding = make([]stakeUnbond, 0)
	}
	if meta.StakeShares == nil {
		meta.StakeShares = big.NewInt(0)
	}
	if meta.StakeLastIndex == nil {
		meta.StakeLastIndex = big.NewInt(0)
	}
	encoded, err := rlp.EncodeToBytes(meta)
	if err != nil {
		return err
	}
	return m.trie.Update(accountMetadataKey(addr), encoded)
}

// ResetUsernameIndex clears any stored username mappings.
func (m *Manager) ResetUsernameIndex() error {
	encoded, err := EncodeUsernameIndex(nil)
	if err != nil {
		return err
	}
	return m.trie.Update(usernameIndexKey, encoded)
}

// EncodeUsernameIndex serializes the username->address mapping into a
// deterministic RLP representation.
func EncodeUsernameIndex(index map[string][]byte) ([]byte, error) {
	if index == nil {
		index = map[string][]byte{}
	}
	entries := make([]usernameIndexEntry, 0, len(index))
	for username, addr := range index {
		entry := usernameIndexEntry{Username: username}
		if len(addr) > 0 {
			entry.Address = append([]byte(nil), addr...)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Username < entries[j].Username
	})
	return rlp.EncodeToBytes(entries)
}

// DecodeUsernameIndex deserializes data produced by EncodeUsernameIndex. It
// also supports decoding legacy map encodings for backward compatibility.
func DecodeUsernameIndex(data []byte) (map[string][]byte, error) {
	if len(data) == 0 {
		return map[string][]byte{}, nil
	}
	var entries []usernameIndexEntry
	if err := rlp.DecodeBytes(data, &entries); err != nil {
		legacy := make(map[string][]byte)
		if errLegacy := rlp.DecodeBytes(data, &legacy); errLegacy == nil {
			result := make(map[string][]byte, len(legacy))
			for username, addr := range legacy {
				if len(addr) > 0 {
					result[username] = append([]byte(nil), addr...)
				} else {
					result[username] = nil
				}
			}
			return result, nil
		}
		return nil, err
	}
	index := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if len(entry.Address) > 0 {
			index[entry.Username] = append([]byte(nil), entry.Address...)
		} else {
			index[entry.Username] = nil
		}
	}
	return index, nil
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
