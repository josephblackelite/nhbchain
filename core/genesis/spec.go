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

	"nhbchain/native/loyalty"
)

type GenesisSpec struct {
	GenesisTime   string                       `json:"genesisTime"`
	NativeTokens  []NativeTokenSpec            `json:"nativeTokens"`
	Validators    []ValidatorSpec              `json:"validators"`
	Alloc         map[string]map[string]string `json:"alloc"` // addr -> token -> amount
	Roles         map[string][]string          `json:"roles"` // role -> []addr
	ChainID       *uint64                      `json:"chainId,omitempty"`
	LoyaltyGlobal *LoyaltyGlobalSpec           `json:"loyaltyGlobal,omitempty"`

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
	Address           string `json:"address"`
	Power             uint64 `json:"power"`
	PubKey            string `json:"pubKey,omitempty"`
	Moniker           string `json:"moniker,omitempty"`
	AutoPopulateLocal bool   `json:"autoPopulateLocal,omitempty"`
}

type ValidatorAutoPopulateInfo struct {
	Address string
	PubKey  string
}

type LoyaltyGlobalSpec struct {
	Active       bool               `json:"active"`
	Treasury     string             `json:"treasury"`
	BaseBps      uint32             `json:"baseBps"`
	MinSpend     string             `json:"minSpend"`
	CapPerTx     string             `json:"capPerTx"`
	DailyCapUser string             `json:"dailyCapUser"`
	SeedZNHB     string             `json:"seedZNHB"`
	Dynamic      LoyaltyDynamicSpec `json:"dynamic"`

	treasuryAddr []byte
	minSpendAmt  *big.Int
	capPerTxAmt  *big.Int
	dailyCapAmt  *big.Int
	seedZNHB     *big.Int
}

type LoyaltyDynamicSpec struct {
	TargetBps          uint32                `json:"targetBps"`
	MinBps             uint32                `json:"minBps"`
	MaxBps             uint32                `json:"maxBps"`
	SmoothingStepBps   uint32                `json:"smoothingStepBps"`
	CoverageWindowDays uint32                `json:"coverageWindowDays"`
	DailyCapWei        string                `json:"dailyCapWei"`
	YearlyCapWei       string                `json:"yearlyCapWei"`
	PriceGuard         LoyaltyPriceGuardSpec `json:"priceGuard"`

	dailyCapAmt  *big.Int
	yearlyCapAmt *big.Int
}

type LoyaltyPriceGuardSpec struct {
	Enabled         bool   `json:"enabled"`
	MaxDeviationBps uint32 `json:"maxDeviationBps"`
}

func (d *LoyaltyDynamicSpec) validate() error {
	if d == nil {
		return nil
	}
	dailyCap, err := parseAmountString(d.DailyCapWei)
	if err != nil {
		return fmt.Errorf("dailyCapWei: %w", err)
	}
	yearlyCap, err := parseAmountString(d.YearlyCapWei)
	if err != nil {
		return fmt.Errorf("yearlyCapWei: %w", err)
	}
	if d.PriceGuard.MaxDeviationBps > loyalty.BaseRewardBpsDenominator {
		return fmt.Errorf("priceGuard.maxDeviationBps must be <= %d", loyalty.BaseRewardBpsDenominator)
	}
	d.dailyCapAmt = dailyCap
	d.yearlyCapAmt = yearlyCap
	return nil
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
	if s.hasChainID {
		return s.chainIDValue, true
	}
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

	if s.LoyaltyGlobal != nil {
		if err := s.LoyaltyGlobal.validate(tokenSymbols); err != nil {
			return fmt.Errorf("loyaltyGlobal: %w", err)
		}
	}

	// validators
	validatorAddresses := make(map[string]struct{}, len(s.Validators))
	autoPopulateCount := 0
	for i := range s.Validators {
		v := &s.Validators[i]
		if v.Power == 0 {
			return fmt.Errorf("validator[%d]: power must be greater than zero", i)
		}

		if v.AutoPopulateLocal {
			autoPopulateCount++
			if strings.TrimSpace(v.Address) != "" {
				return fmt.Errorf("validator[%d]: address must be omitted when autoPopulateLocal is set", i)
			}
			if strings.TrimSpace(v.PubKey) != "" {
				pk := strings.TrimSpace(v.PubKey)
				pk = strings.TrimPrefix(pk, "0x")
				if _, err := hex.DecodeString(pk); err != nil {
					return fmt.Errorf("validator[%d]: invalid pubKey: %w", i, err)
				}
			}
			continue
		}

		if strings.TrimSpace(v.Address) == "" {
			return fmt.Errorf("validator[%d]: address must be provided", i)
		}
		addr, err := ParseBech32Account(v.Address)
		if err != nil {
			return fmt.Errorf("validator[%d]: %w", i, err)
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
	if autoPopulateCount > 1 {
		return fmt.Errorf("validators: multiple entries marked for local auto-population")
	}

	// alloc
	if len(s.Alloc) > 0 {
		accounts := make([]string, 0, len(s.Alloc))
		for account := range s.Alloc {
			accounts = append(accounts, account)
		}
		sort.Strings(accounts)
		for _, account := range accounts {
			if _, err := ParseBech32Account(account); err != nil {
				return fmt.Errorf("alloc[%q]: %w", account, err)
			}
			tokenAlloc := s.Alloc[account]
			if len(tokenAlloc) == 0 {
				continue
			}
			symbols := make([]string, 0, len(tokenAlloc))
			for symbol := range tokenAlloc {
				symbols = append(symbols, symbol)
			}
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
	for role := range s.Roles {
		roleNames = append(roleNames, role)
	}
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

// ResolveValidatorAutoPopulate inspects the validator list and, if a validator
// is marked for local auto-population, fills it using the provided information.
// The spec is revalidated after mutation so downstream consumers observe a
// fully-resolved configuration.
func (s *GenesisSpec) ResolveValidatorAutoPopulate(info *ValidatorAutoPopulateInfo) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("genesis spec must not be nil")
	}

	if err := s.validate(); err != nil {
		return false, err
	}

	var target *ValidatorSpec
	for i := range s.Validators {
		if s.Validators[i].AutoPopulateLocal {
			target = &s.Validators[i]
			break
		}
	}
	if target == nil {
		return false, nil
	}

	if info == nil {
		return false, fmt.Errorf("validator auto-populate info required")
	}

	addr := strings.TrimSpace(info.Address)
	if addr == "" {
		return false, fmt.Errorf("validator auto-populate address must be provided")
	}
	if _, err := ParseBech32Account(addr); err != nil {
		return false, fmt.Errorf("validator auto-populate address invalid: %w", err)
	}

	originalAddress := target.Address
	originalPubKey := target.PubKey
	originalFlag := target.AutoPopulateLocal

	target.Address = addr

	if strings.TrimSpace(target.PubKey) == "" && strings.TrimSpace(info.PubKey) != "" {
		normalized := strings.TrimSpace(info.PubKey)
		normalized = strings.TrimPrefix(normalized, "0x")
		if _, err := hex.DecodeString(normalized); err != nil {
			target.Address = originalAddress
			return false, fmt.Errorf("validator auto-populate pubKey invalid: %w", err)
		}
		target.PubKey = strings.ToLower(normalized)
	}

	target.AutoPopulateLocal = false

	if err := s.validate(); err != nil {
		target.Address = originalAddress
		target.PubKey = originalPubKey
		target.AutoPopulateLocal = originalFlag
		return false, fmt.Errorf("validate resolved spec: %w", err)
	}

	return true, nil
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

func parseAmountString(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	amount, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount %q", value)
	}
	if amount.Sign() < 0 {
		return nil, fmt.Errorf("amount must not be negative")
	}
	return amount, nil
}

func (l *LoyaltyGlobalSpec) validate(tokenSymbols map[string]struct{}) error {
	if l == nil {
		return nil
	}
	trimmedTreasury := strings.TrimSpace(l.Treasury)
	if trimmedTreasury == "" {
		return fmt.Errorf("treasury must be provided")
	}
	addr, err := ParseBech32Account(trimmedTreasury)
	if err != nil {
		return fmt.Errorf("treasury: %w", err)
	}
	l.treasuryAddr = append([]byte(nil), addr[:]...)
	if l.BaseBps > 10_000 {
		return fmt.Errorf("baseBps must be 10_000 or fewer")
	}
	minSpend, err := parseAmountString(l.MinSpend)
	if err != nil {
		return fmt.Errorf("minSpend: %w", err)
	}
	capPerTx, err := parseAmountString(l.CapPerTx)
	if err != nil {
		return fmt.Errorf("capPerTx: %w", err)
	}
	dailyCap, err := parseAmountString(l.DailyCapUser)
	if err != nil {
		return fmt.Errorf("dailyCapUser: %w", err)
	}
	seed, err := parseAmountString(l.SeedZNHB)
	if err != nil {
		return fmt.Errorf("seedZNHB: %w", err)
	}
	if seed.Sign() > 0 {
		if _, ok := tokenSymbols["ZNHB"]; !ok {
			return fmt.Errorf("seedZNHB provided but token ZNHB not registered")
		}
	}
	if err := l.Dynamic.validate(); err != nil {
		return fmt.Errorf("dynamic: %w", err)
	}
	l.minSpendAmt = minSpend
	l.capPerTxAmt = capPerTx
	l.dailyCapAmt = dailyCap
	l.seedZNHB = seed
	return nil
}

func (l *LoyaltyGlobalSpec) Config() (*loyalty.GlobalConfig, *big.Int, error) {
	if l == nil {
		return nil, nil, nil
	}
	if l.treasuryAddr == nil {
		return nil, nil, fmt.Errorf("loyalty global spec not validated")
	}
	cfg := &loyalty.GlobalConfig{
		Active:       l.Active,
		Treasury:     append([]byte(nil), l.treasuryAddr...),
		BaseBps:      l.BaseBps,
		MinSpend:     new(big.Int).Set(l.minSpendAmt),
		CapPerTx:     new(big.Int).Set(l.capPerTxAmt),
		DailyCapUser: new(big.Int).Set(l.dailyCapAmt),
		Dynamic: loyalty.DynamicConfig{
			TargetBps:          l.Dynamic.TargetBps,
			MinBps:             l.Dynamic.MinBps,
			MaxBps:             l.Dynamic.MaxBps,
			SmoothingStepBps:   l.Dynamic.SmoothingStepBps,
			CoverageWindowDays: l.Dynamic.CoverageWindowDays,
			PriceGuard: loyalty.PriceGuardConfig{
				Enabled:         l.Dynamic.PriceGuard.Enabled,
				MaxDeviationBps: l.Dynamic.PriceGuard.MaxDeviationBps,
			},
		},
	}
	if l.Dynamic.dailyCapAmt != nil {
		cfg.Dynamic.DailyCap = new(big.Int).Set(l.Dynamic.dailyCapAmt)
	}
	if l.Dynamic.yearlyCapAmt != nil {
		cfg.Dynamic.YearlyCap = new(big.Int).Set(l.Dynamic.yearlyCapAmt)
	}
	cfg = cfg.Normalize()
	return cfg, new(big.Int).Set(l.seedZNHB), nil
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
