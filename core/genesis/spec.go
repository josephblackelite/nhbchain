package genesis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

type GenesisSpec struct {
	GenesisTime string              `json:"genesisTime"`
	Tokens      []TokenSpec         `json:"tokens"`
	Validators  []ValidatorSpec     `json:"validators"`
	Balances    map[string]string   `json:"balances"`
	Roles       map[string][]string `json:"roles"`

	genesisTimestamp time.Time
}

type TokenSpec struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals uint8  `json:"decimals"`
}

type ValidatorSpec struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Power   uint64 `json:"power"`
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

func (s *GenesisSpec) GenesisTimestamp() time.Time {
	return s.genesisTimestamp
}

func (s *GenesisSpec) validate() error {
	parsedTime, err := parseGenesisTime(s.GenesisTime)
	if err != nil {
		return err
	}
	s.genesisTimestamp = parsedTime

	tokenSymbols := make(map[string]struct{}, len(s.Tokens))
	for i := range s.Tokens {
		if err := s.Tokens[i].validate(); err != nil {
			return fmt.Errorf("token[%d]: %w", i, err)
		}
		symbolKey := strings.ToUpper(strings.TrimSpace(s.Tokens[i].Symbol))
		if _, exists := tokenSymbols[symbolKey]; exists {
			return fmt.Errorf("token[%d]: duplicate symbol %q", i, s.Tokens[i].Symbol)
		}
		tokenSymbols[symbolKey] = struct{}{}
	}

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
		addrKey := string(addr[:])
		if _, exists := validatorAddresses[addrKey]; exists {
			return fmt.Errorf("validator[%d]: duplicate address %q", i, v.Address)
		}
		validatorAddresses[addrKey] = struct{}{}
	}

	for account, amount := range s.Balances {
		if _, err := ParseBech32Account(account); err != nil {
			return fmt.Errorf("balances[%q]: %w", account, err)
		}
		if strings.TrimSpace(amount) == "" {
			return fmt.Errorf("balances[%q]: amount must be provided", account)
		}
		if _, ok := new(big.Int).SetString(amount, 10); !ok {
			return fmt.Errorf("balances[%q]: invalid amount %q", account, amount)
		}
	}

	for role, accounts := range s.Roles {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("roles: role name must be provided")
		}
		for i, account := range accounts {
			if _, err := ParseBech32Account(account); err != nil {
				return fmt.Errorf("roles[%q][%d]: %w", role, i, err)
			}
		}
	}

	return nil
}

func (t *TokenSpec) validate() error {
	if strings.TrimSpace(t.Symbol) == "" {
		return fmt.Errorf("symbol must be provided")
	}
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name must be provided")
	}
	if t.Decimals > 18 {
		return fmt.Errorf("decimals must be 18 or fewer")
	}
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
