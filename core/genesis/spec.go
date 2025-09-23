// core/genesis/spec.go
package genesis

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
)

type GenesisSpec struct {
	GenesisTime   string                       `json:"genesisTime"`
	NativeTokens  []NativeTokenSpec            `json:"nativeTokens"`
	Validators    []ValidatorSpec              `json:"validators"`
	Alloc         map[string]map[string]string `json:"alloc"`           // addr -> token -> amount
	Roles         map[string][]string          `json:"roles"`           // role -> []addr
	ChainID       *uint64                      `json:"chainId,omitempty"`

	genesisTimestamp time.Time
	chainIDValue     uint64
	hasChainID       bool
}

type NativeTokenSpec struct {
	Symbol            string `json:"symbol"`
	Name              string `json:"name"`
	Decimals          uint8  `json:"decimals"`
	MintAuthority     string `json:"mintAuthority,omitempty"`
	InitialMintPaused *bool  `json:"initialMintPaused,omitempty"`
}

type ValidatorSpec struct {
	Address string `json:"address"`
	Power   uint64 `json:"power"`
	PubKey  string `json:"pubKey,omitempty"`
	Moniker string `json:"moniker,omitempty"`
}

func LoadGenesisSpec(path string) (*GenesisSpec, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("genesis spec path must be provided")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read genesis spec %q: %w", path, err)
	}
	var spec GenesisSpec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("decode genesis spec %q: %w", path, err)
	}
	if err := spec.validate(); err != nil {
	 return nil, fmt.Errorf("invalid genesis spec %q: %w", path, err)
	}
	return &spec, nil
}

func (s *GenesisSpec) GenesisTimestamp() time.Time { return s.genesisTimestamp }
func (s *GenesisSpec) ChainIDValue() (uint64, bool) {
	if s.hasChainID { return s.chainIDValue, true }
	return 0, false
}

func (s *GenesisSpec) validate() error {
	parsedTime, err := parseGenesisTime(s.GenesisTime)
	if err != nil {
		return err
	}
	s.genesisTimestamp = parsedTime

	s.hasChainID = false
	s.chainIDValue = 0
	if s.ChainID != nil {
		s.hasChainID = true
		s.chainIDValue = *s.ChainID
	}

	// native tokens
	tokenSymbols := make(map[string]struct{}, len(s.NativeTokens))
	for i := range s.NativeTokens {
		if err := s.NativeTokens[i].validate(); err != nil {
			return fmt.Errorf("nativeToken[%d]: %w", i, err)
		}
		key := strings.ToUpper(strings.TrimSpace(s.NativeTokens[i].Symbol))
		if _, exists := tokenSymbols[key]; exists {
			return fmt.Errorf("nativeToken[%d]: duplicate symbol %q", i, s.NativeTokens[i].Symbol)
		}
		tokenSymbols[key] = struct{}{}
	}

	// validators
	validatorAddresses := make(map[string]struct{}, len(s.Validators))
	for i := range s.Validators {
		v := &s.Validators[i]
		if strings.TrimSpace(v.Address) == "" {
			return fmt.Errorf("validator[%d]: address must be provided", i)
		}
		addr, err := ParseBech32Account(v.Address)
		if err != nil {
			return fmt.Errorf("validator[%d]: %w", i, err)
		}
		if v.Power == 0 {
			return fmt.Errorf("validator[%d]: power must be greater than zero", i)
		}
		if strings.TrimSpace(v.PubKey) != "" {
			pk := strings.TrimSpace(v.PubKey)
			pk = strings.TrimPrefix(pk, "0x")
			if _, err := hex.DecodeString(pk); err != nil {
				return fmt.Errorf("validator[%d]: invalid pubKey: %w", i, err)
			}
		}
		addrKey := string(addr[:])
		if _, exists := validatorAddresses[addrKey]; exists {
			return fmt.Errorf("validator[%d]: duplicate address %q", i, v.Address)
		}
		validatorAddresses[addrKey] = struct{}{}
	}

	// alloc
	if len(s.Alloc) > 0 {
		accounts := make([]string, 0, len(s.Alloc))
		for account := range s.Alloc { accounts = append(accounts, account) }
		sort.Strings(accounts)
		for _, account := range accounts {
			if _, err := ParseBech32Account(account); err != nil {
				return fmt.Errorf("alloc[%q]: %w", account, err)
			}
			tokenAlloc := s.Alloc[account]
			if len(tokenAlloc) == 0 { continue }
			symbols := make([]string, 0, len(tokenAlloc))
			for symbol := range tokenAlloc { symbols = append(symbols, symbol) }
			sort.Strings(symbols)
			seen := make(map[string]struct{}, len(symbols))
			for _, symbol := range symbols {
				amount := tokenAlloc[symbol]
				if strings.TrimSpace(amount) == "" {
					return fmt.Errorf("alloc[%q][%q]: amount must be provided", account, symbol)
				}
				if _, ok := new(big.Int).SetString(amount, 10); !ok {
					return fmt.Errorf("alloc[%q][%q]: invalid amount %q", account, symbol, amount)
				}
				symKey := strings.ToUpper(strings.TrimSpace(symbol))
				if _, exists := tokenSymbols[symKey]; !exists {
					return fmt.Errorf("alloc[%q][%q]: undefined token", account, symbol)
				}
				if _, dup := seen[symKey]; dup {
					return fmt.Errorf("alloc[%q]: duplicate token %q", account, symbol)
				}
				seen[symKey] = struct{}{}
			}
		}
	}

	// roles
	roleNames := make([]string, 0, len(s.Roles))
	for role := range s.Roles { roleNames = append(roleNames, role) }
	sort.Strings(roleNames)
	for _, role := range roleNames {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("roles: role name must be provided")
		}
		accounts := s.Roles[role]
		for i, account := range accounts {
			if _, err := ParseBech32Account(account); err != nil {
				return fmt.Errorf("roles[%q][%d]: %w", role, i, err)
			}
		}
	}
	return nil
}

func (t *NativeTokenSpec) validate() error {
	if strings.TrimSpace(t.Symbol) == "" {
		return fmt.Errorf("symbol must be provided")
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name must be provided")
	}
	if t.Decimals > 18 {
		return fmt.Errorf("decimals must be 18 or fewer")
	}
	if strings.TrimSpace(t.MintAuthority) != "" {
		if _, err := ParseBech32Account(t.MintAuthority); err != nil {
			return fmt.Errorf("mintAuthority: %w", err)
		}
	}
	// InitialMintPaused is optional; no extra check needed.
	return nil
}

func parseGenesisTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, fmt.Errorf("genesisTime must be provided")
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts, nil
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, nil
	}
	return time.Time{}, fmt.Errorf("invalid genesisTime %q", value)
}
