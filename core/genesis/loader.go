// core/genesis/loader.go
package genesis

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/consensus/store"
	"nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func BuildGenesisFromSpec(spec *GenesisSpec, db storage.Database) (*types.Block, error) {
	if spec == nil {
		return nil, fmt.Errorf("genesis spec must not be nil")
	}
	if db == nil {
		return nil, fmt.Errorf("database must not be nil")
	}

	ts := spec.GenesisTimestamp()
	if ts.IsZero() {
		parsed, err := parseGenesisTime(spec.GenesisTime)
		if err != nil {
			return nil, err
		}
		ts = parsed
	}

	header := &types.BlockHeader{
		Height:    0,
		Timestamp: ts.Unix(),
		PrevHash:  []byte{},
		StateRoot: gethtypes.EmptyRootHash.Bytes(),
		TxRoot:    gethtypes.EmptyRootHash.Bytes(),
	}

	// ---- Real state execution (deterministic) ----
	stateTrie, err := trie.NewTrie(db, nil)
	if err != nil {
		return nil, fmt.Errorf("init state trie: %w", err)
	}
	manager := state.NewManager(stateTrie)
	parentRoot := stateTrie.Root()

	// 1) Tokens (sorted)
	tokens := append([]NativeTokenSpec(nil), spec.NativeTokens...)
	sort.Slice(tokens, func(i, j int) bool {
		return strings.ToUpper(tokens[i].Symbol) < strings.ToUpper(tokens[j].Symbol)
	})
	for i := range tokens {
		token := &tokens[i]
		if err := manager.RegisterToken(token.Symbol, token.Name, token.Decimals); err != nil {
			return nil, fmt.Errorf("register token %q: %w", token.Symbol, err)
		}
		if strings.TrimSpace(token.MintAuthority) != "" {
			addr, err := ParseBech32Account(token.MintAuthority)
			if err != nil {
				return nil, fmt.Errorf("token %q mintAuthority: %w", token.Symbol, err)
			}
			if err := manager.SetTokenMintAuthority(token.Symbol, addr[:]); err != nil {
				return nil, fmt.Errorf("token %q: %w", token.Symbol, err)
			}
		}
		if token.InitialMintPaused != nil {
			if err := manager.SetTokenMintPaused(token.Symbol, *token.InitialMintPaused); err != nil {
				return nil, fmt.Errorf("token %q: %w", token.Symbol, err)
			}
		}
	}

	// 2) Allocations (outer: addresses sorted; inner: symbols sorted)
	allocAddresses := make([]string, 0, len(spec.Alloc))
	for addr := range spec.Alloc {
		allocAddresses = append(allocAddresses, addr)
	}
	sort.Strings(allocAddresses)
	for _, addrStr := range allocAddresses {
		parsed, err := ParseBech32Account(addrStr)
		if err != nil {
			return nil, fmt.Errorf("alloc[%q]: %w", addrStr, err)
		}
		addrBytes := parsed[:]
		balances := spec.Alloc[addrStr]
		if balances == nil {
			balances = map[string]string{}
		}

		account, err := manager.GetAccount(addrBytes)
		if err != nil {
			return nil, fmt.Errorf("load account %q: %w", addrStr, err)
		}

		symbolMap := make(map[string]*big.Int, len(balances))
		symbols := make([]string, 0, len(balances))
		for symbol, amountStr := range balances {
			normalized := strings.ToUpper(strings.TrimSpace(symbol))
			amount, ok := new(big.Int).SetString(strings.TrimSpace(amountStr), 10)
			if !ok {
				return nil, fmt.Errorf("alloc[%q][%q]: invalid amount %q", addrStr, symbol, amountStr)
			}
			symbolMap[normalized] = amount
			symbols = append(symbols, normalized)
		}
		sort.Strings(symbols)

		for _, symbol := range symbols {
			amount := new(big.Int).Set(symbolMap[symbol])
			if err := manager.SetBalance(addrBytes, symbol, amount); err != nil {
				return nil, fmt.Errorf("alloc[%q][%q]: %w", addrStr, symbol, err)
			}
			switch symbol {
			case "NHB":
				account.BalanceNHB = new(big.Int).Set(amount)
			case "ZNHB":
				account.BalanceZNHB = new(big.Int).Set(amount)
			}
		}

		if err := manager.PutAccount(addrBytes, account); err != nil {
			return nil, fmt.Errorf("persist account %q: %w", addrStr, err)
		}
	}

	// 3) Roles (role name sorted; addresses sorted)
	roleNames := make([]string, 0, len(spec.Roles))
	for role := range spec.Roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	for _, role := range roleNames {
		addresses := append([]string(nil), spec.Roles[role]...)
		sort.Strings(addresses)
		for _, addrStr := range addresses {
			parsed, err := ParseBech32Account(addrStr)
			if err != nil {
				return nil, fmt.Errorf("roles[%q]: %w", role, err)
			}
			if err := manager.SetRole(role, parsed[:]); err != nil {
				return nil, fmt.Errorf("roles[%q]: %w", role, err)
			}
		}
	}

	// 4) Validators (sorted by address)
	validators := append([]ValidatorSpec(nil), spec.Validators...)
	sort.Slice(validators, func(i, j int) bool {
		return strings.Compare(validators[i].Address, validators[j].Address) < 0
	})
	validatorPowers := make(map[string]*big.Int, len(validators))
	consensusValidators := make([]store.Validator, 0, len(validators))
	for _, v := range validators {
		parsed, err := ParseBech32Account(v.Address)
		if err != nil {
			return nil, fmt.Errorf("validator %q: %w", v.Address, err)
		}
		addrCopy := append([]byte(nil), parsed[:]...)
		validatorPowers[string(addrCopy)] = new(big.Int).SetUint64(v.Power)

		var pubKeyBytes []byte
		if strings.TrimSpace(v.PubKey) != "" {
			pk := strings.TrimSpace(v.PubKey)
			pk = strings.TrimPrefix(pk, "0x")
			decoded, err := hex.DecodeString(pk)
			if err != nil {
				return nil, fmt.Errorf("validator %q pubKey: %w", v.Address, err)
			}
			pubKeyBytes = decoded
		}
		consensusValidators = append(consensusValidators, store.Validator{
			Address: addrCopy,
			PubKey:  pubKeyBytes,
			Power:   v.Power,
			Moniker: v.Moniker,
		})
	}
	if err := manager.WriteValidatorSet(validatorPowers); err != nil {
		return nil, fmt.Errorf("persist validator set: %w", err)
	}

	consensusStore := store.New(db)
	if err := consensusStore.SaveValidators(consensusValidators); err != nil {
		return nil, fmt.Errorf("store consensus validators: %w", err)
	}

	// 5) Commit and set StateRoot
	newRoot, err := stateTrie.Commit(parentRoot, 0)
	if err != nil {
		return nil, fmt.Errorf("commit state: %w", err)
	}
	header.StateRoot = newRoot.Bytes()

	return types.NewBlock(header, nil), nil
}
