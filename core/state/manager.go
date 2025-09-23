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

	"nhbchain/core/identity"
	"nhbchain/native/escrow"
	"nhbchain/native/loyalty"
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
	tokenPrefix                = []byte("token:")
	tokenListKey               = ethcrypto.Keccak256([]byte("token-list"))
	balancePrefix              = []byte("balance:")
	rolePrefix                 = []byte("role:")
	loyaltyGlobalKeyBytes      = ethcrypto.Keccak256([]byte("loyalty:global"))
	loyaltyDailyPrefix         = []byte("loyalty-meter:base-daily:")
	loyaltyTotalPrefix         = []byte("loyalty-meter:base-total:")
	loyaltyProgramDailyPrefix  = []byte("loyalty-meter:program-daily:")
	loyaltyBusinessPrefix      = []byte("loyalty/business/")
	loyaltyBusinessOwnerPrefix = []byte("loyalty/business-owner/")
	loyaltyMerchantIndexPrefix = []byte("loyalty/merchant-index/")
	loyaltyBusinessCounterKey  = []byte("loyalty/business/counter")
	loyaltyOwnerPaymasterPref  = []byte("loyalty/owner-paymaster/")
	escrowRecordPrefix         = []byte("escrow/record/")
	escrowVaultPrefix          = []byte("escrow/vault/")
	escrowModuleSeedPrefix     = "module/escrow/vault/"
	tradeRecordPrefix          = []byte("trade/record/")
	tradeEscrowIndexPrefix     = []byte("trade/index/escrow/")
	identityAliasPrefix        = []byte("identity/alias/")
	identityReversePrefix      = []byte("identity/reverse/")
)

func LoyaltyGlobalStorageKey() []byte {
	return append([]byte(nil), loyaltyGlobalKeyBytes...)
}

func LoyaltyBaseDailyMeterKey(addr []byte, day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(loyaltyDailyPrefix)+len(trimmed)+1+len(addr))
	copy(buf, loyaltyDailyPrefix)
	copy(buf[len(loyaltyDailyPrefix):], trimmed)
	buf[len(loyaltyDailyPrefix)+len(trimmed)] = ':'
	copy(buf[len(loyaltyDailyPrefix)+len(trimmed)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyBaseTotalMeterKey(addr []byte) []byte {
	buf := make([]byte, len(loyaltyTotalPrefix)+len(addr))
	copy(buf, loyaltyTotalPrefix)
	copy(buf[len(loyaltyTotalPrefix):], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyProgramDailyMeterKey(id loyalty.ProgramID, addr []byte, day string) []byte {
	trimmed := strings.TrimSpace(day)
	buf := make([]byte, len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)+1+len(addr))
	copy(buf, loyaltyProgramDailyPrefix)
	copy(buf[len(loyaltyProgramDailyPrefix):], id[:])
	buf[len(loyaltyProgramDailyPrefix)+len(id)] = ':'
	copy(buf[len(loyaltyProgramDailyPrefix)+len(id)+1:], trimmed)
	buf[len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)] = ':'
	copy(buf[len(loyaltyProgramDailyPrefix)+len(id)+1+len(trimmed)+1:], addr)
	return ethcrypto.Keccak256(buf)
}

func LoyaltyBusinessKey(id loyalty.BusinessID) []byte {
	key := make([]byte, len(loyaltyBusinessPrefix)+len(id))
	copy(key, loyaltyBusinessPrefix)
	copy(key[len(loyaltyBusinessPrefix):], id[:])
	return key
}

func LoyaltyBusinessOwnerKey(owner []byte) []byte {
	key := make([]byte, len(loyaltyBusinessOwnerPrefix)+len(owner))
	copy(key, loyaltyBusinessOwnerPrefix)
	copy(key[len(loyaltyBusinessOwnerPrefix):], owner)
	return key
}

func LoyaltyMerchantIndexKey(addr []byte) []byte {
	key := make([]byte, len(loyaltyMerchantIndexPrefix)+len(addr))
	copy(key, loyaltyMerchantIndexPrefix)
	copy(key[len(loyaltyMerchantIndexPrefix):], addr)
	return key
}

func LoyaltyBusinessCounterKey() []byte {
	return append([]byte(nil), loyaltyBusinessCounterKey...)
}

func LoyaltyOwnerPaymasterKey(owner []byte) []byte {
	key := make([]byte, len(loyaltyOwnerPaymasterPref)+len(owner))
	copy(key, loyaltyOwnerPaymasterPref)
	copy(key[len(loyaltyOwnerPaymasterPref):], owner)
	return key
}

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

func (m *Manager) loadBigInt(key []byte) (*big.Int, error) {
	data, err := m.trie.Get(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return big.NewInt(0), nil
	}
	value := new(big.Int)
	if err := rlp.DecodeBytes(data, value); err != nil {
		return nil, err
	}
	return value, nil
}

func (m *Manager) writeBigInt(key []byte, amount *big.Int) error {
	if amount == nil {
		amount = big.NewInt(0)
	}
	if amount.Sign() < 0 {
		return fmt.Errorf("negative value not allowed")
	}
	encoded, err := rlp.EncodeToBytes(amount)
	if err != nil {
		return err
	}
	return m.trie.Update(key, encoded)
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

func escrowStorageKey(id [32]byte) []byte {
	buf := make([]byte, len(escrowRecordPrefix)+len(id))
	copy(buf, escrowRecordPrefix)
	copy(buf[len(escrowRecordPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func escrowVaultKey(id [32]byte, token string) []byte {
	normalized := strings.ToUpper(strings.TrimSpace(token))
	buf := make([]byte, len(escrowVaultPrefix)+len(normalized)+1+len(id))
	copy(buf, escrowVaultPrefix)
	copy(buf[len(escrowVaultPrefix):], normalized)
	buf[len(escrowVaultPrefix)+len(normalized)] = ':'
	copy(buf[len(escrowVaultPrefix)+len(normalized)+1:], id[:])
	return ethcrypto.Keccak256(buf)
}

func tradeStorageKey(id [32]byte) []byte {
	buf := make([]byte, len(tradeRecordPrefix)+len(id))
	copy(buf, tradeRecordPrefix)
	copy(buf[len(tradeRecordPrefix):], id[:])
	return ethcrypto.Keccak256(buf)
}

func identityAliasKey(alias string) []byte {
	buf := make([]byte, len(identityAliasPrefix)+len(alias))
	copy(buf, identityAliasPrefix)
	copy(buf[len(identityAliasPrefix):], alias)
	return kvKey(buf)
}

func identityReverseKey(addr []byte) []byte {
	buf := make([]byte, len(identityReversePrefix)+len(addr))
	copy(buf, identityReversePrefix)
	copy(buf[len(identityReversePrefix):], addr)
	return kvKey(buf)
}

func tradeEscrowIndexKey(escrowID [32]byte) []byte {
	buf := make([]byte, len(tradeEscrowIndexPrefix)+len(escrowID))
	copy(buf, tradeEscrowIndexPrefix)
	copy(buf[len(tradeEscrowIndexPrefix):], escrowID[:])
	return ethcrypto.Keccak256(buf)
}

func escrowModuleAddress(token string) ([20]byte, error) {
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return [20]byte{}, err
	}
	seed := escrowModuleSeedPrefix + normalized
	hash := ethcrypto.Keccak256([]byte(seed))
	var addr [20]byte
	copy(addr[:], hash[len(hash)-20:])
	return addr, nil
}

type storedEscrow struct {
	ID        [32]byte
	Payer     [20]byte
	Payee     [20]byte
	Mediator  [20]byte
	Token     string
	Amount    *big.Int
	FeeBps    uint32
	Deadline  *big.Int
	CreatedAt *big.Int
	MetaHash  [32]byte
	Status    uint8
}

func newStoredEscrow(e *escrow.Escrow) *storedEscrow {
	if e == nil {
		return nil
	}
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	deadline := big.NewInt(e.Deadline)
	created := big.NewInt(e.CreatedAt)
	return &storedEscrow{
		ID:        e.ID,
		Payer:     e.Payer,
		Payee:     e.Payee,
		Mediator:  e.Mediator,
		Token:     e.Token,
		Amount:    amount,
		FeeBps:    e.FeeBps,
		Deadline:  deadline,
		CreatedAt: created,
		MetaHash:  e.MetaHash,
		Status:    uint8(e.Status),
	}
}

func (s *storedEscrow) toEscrow() (*escrow.Escrow, error) {
	if s == nil {
		return nil, fmt.Errorf("escrow: nil storage record")
	}
	out := &escrow.Escrow{
		ID:       s.ID,
		Payer:    s.Payer,
		Payee:    s.Payee,
		Mediator: s.Mediator,
		Token:    s.Token,
		Amount: func() *big.Int {
			if s.Amount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.Amount)
		}(),
		FeeBps:   s.FeeBps,
		MetaHash: s.MetaHash,
		Status:   escrow.EscrowStatus(s.Status),
	}
	if s.Deadline != nil {
		out.Deadline = s.Deadline.Int64()
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.Int64()
	}
	if !out.Status.Valid() {
		return nil, fmt.Errorf("escrow: invalid status in storage")
	}
	return out, nil
}

type storedTrade struct {
	ID          [32]byte
	OfferID     string
	Buyer       [20]byte
	Seller      [20]byte
	QuoteToken  string
	QuoteAmount *big.Int
	EscrowQuote [32]byte
	BaseToken   string
	BaseAmount  *big.Int
	EscrowBase  [32]byte
	Deadline    *big.Int
	CreatedAt   *big.Int
	Status      uint8
}

func newStoredTrade(t *escrow.Trade) *storedTrade {
	if t == nil {
		return nil
	}
	quote := big.NewInt(0)
	if t.QuoteAmount != nil {
		quote = new(big.Int).Set(t.QuoteAmount)
	}
	base := big.NewInt(0)
	if t.BaseAmount != nil {
		base = new(big.Int).Set(t.BaseAmount)
	}
	return &storedTrade{
		ID:          t.ID,
		OfferID:     t.OfferID,
		Buyer:       t.Buyer,
		Seller:      t.Seller,
		QuoteToken:  t.QuoteToken,
		QuoteAmount: quote,
		EscrowQuote: t.EscrowQuote,
		BaseToken:   t.BaseToken,
		BaseAmount:  base,
		EscrowBase:  t.EscrowBase,
		Deadline:    big.NewInt(t.Deadline),
		CreatedAt:   big.NewInt(t.CreatedAt),
		Status:      uint8(t.Status),
	}
}

func (s *storedTrade) toTrade() (*escrow.Trade, error) {
	if s == nil {
		return nil, fmt.Errorf("trade: nil storage record")
	}
	out := &escrow.Trade{
		ID:         s.ID,
		OfferID:    s.OfferID,
		Buyer:      s.Buyer,
		Seller:     s.Seller,
		QuoteToken: s.QuoteToken,
		QuoteAmount: func() *big.Int {
			if s.QuoteAmount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.QuoteAmount)
		}(),
		EscrowQuote: s.EscrowQuote,
		BaseToken:   s.BaseToken,
		BaseAmount: func() *big.Int {
			if s.BaseAmount == nil {
				return big.NewInt(0)
			}
			return new(big.Int).Set(s.BaseAmount)
		}(),
		EscrowBase: s.EscrowBase,
		Status:     escrow.TradeStatus(s.Status),
	}
	if s.Deadline != nil {
		out.Deadline = s.Deadline.Int64()
	}
	if s.CreatedAt != nil {
		out.CreatedAt = s.CreatedAt.Int64()
	}
	if !out.Status.Valid() {
		return nil, fmt.Errorf("trade: invalid status in storage")
	}
	return out, nil
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

// SetLoyaltyGlobalConfig stores the global configuration for the loyalty engine.
func (m *Manager) SetLoyaltyGlobalConfig(cfg *loyalty.GlobalConfig) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	normalized := cfg.Clone().Normalize()
	if err := normalized.Validate(); err != nil {
		return err
	}
	encoded, err := rlp.EncodeToBytes(normalized)
	if err != nil {
		return err
	}
	return m.trie.Update(loyaltyGlobalKeyBytes, encoded)
}

// LoyaltyGlobalConfig retrieves the stored global configuration, if any.
func (m *Manager) LoyaltyGlobalConfig() (*loyalty.GlobalConfig, error) {
	data, err := m.trie.Get(loyaltyGlobalKeyBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	cfg := new(loyalty.GlobalConfig)
	if err := rlp.DecodeBytes(data, cfg); err != nil {
		return nil, err
	}
	return cfg.Normalize(), nil
}

// SetLoyaltyBaseDailyAccrued stores the accrued base rewards for the provided
// address and UTC day string (YYYY-MM-DD).
func (m *Manager) SetLoyaltyBaseDailyAccrued(addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	return m.writeBigInt(LoyaltyBaseDailyMeterKey(addr, day), amount)
}

// LoyaltyBaseDailyAccrued returns the accrued base rewards for the supplied
// address and day.
func (m *Manager) LoyaltyBaseDailyAccrued(addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	return m.loadBigInt(LoyaltyBaseDailyMeterKey(addr, day))
}

// SetLoyaltyBaseTotalAccrued stores the lifetime accrued base rewards for the
// provided address.
func (m *Manager) SetLoyaltyBaseTotalAccrued(addr []byte, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	return m.writeBigInt(LoyaltyBaseTotalMeterKey(addr), amount)
}

// LoyaltyBaseTotalAccrued returns the lifetime accrued base rewards for the
// supplied address.
func (m *Manager) LoyaltyBaseTotalAccrued(addr []byte) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	return m.loadBigInt(LoyaltyBaseTotalMeterKey(addr))
}

// SetLoyaltyProgramDailyAccrued stores the accrued program rewards for the
// provided address and UTC day (YYYY-MM-DD).
func (m *Manager) SetLoyaltyProgramDailyAccrued(id loyalty.ProgramID, addr []byte, day string, amount *big.Int) error {
	if len(addr) == 0 {
		return fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return fmt.Errorf("day must not be empty")
	}
	return m.writeBigInt(LoyaltyProgramDailyMeterKey(id, addr, day), amount)
}

// LoyaltyProgramDailyAccrued returns the accrued program rewards for the
// supplied address and day.
func (m *Manager) LoyaltyProgramDailyAccrued(id loyalty.ProgramID, addr []byte, day string) (*big.Int, error) {
	if len(addr) == 0 {
		return nil, fmt.Errorf("address must not be empty")
	}
	if strings.TrimSpace(day) == "" {
		return nil, fmt.Errorf("day must not be empty")
	}
	return m.loadBigInt(LoyaltyProgramDailyMeterKey(id, addr, day))
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

// EscrowPut persists the provided escrow definition after validating and
// normalising its contents.
func (m *Manager) EscrowPut(e *escrow.Escrow) error {
	if e == nil {
		return fmt.Errorf("escrow: nil value")
	}
	sanitized, err := escrow.SanitizeEscrow(e)
	if err != nil {
		return err
	}
	record := newStoredEscrow(sanitized)
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(escrowStorageKey(sanitized.ID), encoded)
}

// EscrowGet retrieves an escrow definition by identifier. The returned boolean
// indicates whether the escrow exists in state.
func (m *Manager) EscrowGet(id [32]byte) (*escrow.Escrow, bool) {
	data, err := m.trie.Get(escrowStorageKey(id))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	stored := new(storedEscrow)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false
	}
	escrowValue, err := stored.toEscrow()
	if err != nil {
		return nil, false
	}
	sanitized, err := escrow.SanitizeEscrow(escrowValue)
	if err != nil {
		return nil, false
	}
	return sanitized, true
}

// EscrowVaultAddress returns the deterministic module address that holds funds
// for escrows denominated in the supplied token.
func (m *Manager) EscrowVaultAddress(token string) ([20]byte, error) {
	return escrowModuleAddress(token)
}

// EscrowCredit increases the tracked escrow balance for the supplied token.
// Attempts to operate on unknown escrows, unsupported tokens or negative
// amounts result in an error.
func (m *Manager) EscrowCredit(id [32]byte, token string, amt *big.Int) error {
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative credit")
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return err
	}
	exists, err := m.trie.Get(escrowStorageKey(id))
	if err != nil {
		return err
	}
	if len(exists) == 0 {
		return fmt.Errorf("escrow not found")
	}
	if amt.Sign() == 0 {
		return nil
	}
	key := escrowVaultKey(id, normalized)
	balance, err := m.loadBigInt(key)
	if err != nil {
		return err
	}
	updated := new(big.Int).Add(balance, amt)
	return m.writeBigInt(key, updated)
}

// EscrowDebit decreases the tracked escrow balance for the supplied token.
func (m *Manager) EscrowDebit(id [32]byte, token string, amt *big.Int) error {
	if amt == nil {
		amt = big.NewInt(0)
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative debit")
	}
	normalized, err := escrow.NormalizeToken(token)
	if err != nil {
		return err
	}
	key := escrowVaultKey(id, normalized)
	balance, err := m.loadBigInt(key)
	if err != nil {
		return err
	}
	if balance.Cmp(amt) < 0 {
		return fmt.Errorf("escrow: insufficient balance")
	}
	if amt.Sign() == 0 {
		return nil
	}
	updated := new(big.Int).Sub(balance, amt)
	return m.writeBigInt(key, updated)
}

// TradePut persists the provided trade definition after validation.
func (m *Manager) TradePut(t *escrow.Trade) error {
	if t == nil {
		return fmt.Errorf("trade: nil value")
	}
	sanitized, err := escrow.SanitizeTrade(t)
	if err != nil {
		return err
	}
	record := newStoredTrade(sanitized)
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return err
	}
	return m.trie.Update(tradeStorageKey(sanitized.ID), encoded)
}

// TradeGet retrieves a stored trade by identifier.
func (m *Manager) TradeGet(id [32]byte) (*escrow.Trade, bool) {
	data, err := m.trie.Get(tradeStorageKey(id))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	stored := new(storedTrade)
	if err := rlp.DecodeBytes(data, stored); err != nil {
		return nil, false
	}
	trade, err := stored.toTrade()
	if err != nil {
		return nil, false
	}
	sanitized, err := escrow.SanitizeTrade(trade)
	if err != nil {
		return nil, false
	}
	return sanitized, true
}

// TradeSetStatus updates the status of an existing trade.
func (m *Manager) TradeSetStatus(id [32]byte, status escrow.TradeStatus) error {
	if !status.Valid() {
		return fmt.Errorf("trade: invalid status %d", status)
	}
	trade, ok := m.TradeGet(id)
	if !ok {
		return fmt.Errorf("trade: not found")
	}
	if trade.Status == status {
		return nil
	}
	trade.Status = status
	return m.TradePut(trade)
}

// TradeIndexEscrow associates an escrow with a trade for quick lookups.
func (m *Manager) TradeIndexEscrow(escrowID [32]byte, tradeID [32]byte) error {
	key := tradeEscrowIndexKey(escrowID)
	return m.trie.Update(key, append([]byte(nil), tradeID[:]...))
}

// TradeLookupByEscrow resolves the trade identifier owning the provided escrow.
func (m *Manager) TradeLookupByEscrow(escrowID [32]byte) ([32]byte, bool, error) {
	key := tradeEscrowIndexKey(escrowID)
	data, err := m.trie.Get(key)
	if err != nil {
		return [32]byte{}, false, err
	}
	if len(data) != len([32]byte{}) {
		return [32]byte{}, false, nil
	}
	var id [32]byte
	copy(id[:], data)
	return id, true, nil
}

// TradeRemoveByEscrow removes the reverse index entry for the escrow.
func (m *Manager) TradeRemoveByEscrow(escrowID [32]byte) error {
	key := tradeEscrowIndexKey(escrowID)
	return m.trie.Update(key, nil)
}

// IsEscrowFunded reports whether the escrow currently holds funds.
func (m *Manager) IsEscrowFunded(id [32]byte) (bool, error) {
	esc, ok := m.EscrowGet(id)
	if !ok {
		return false, fmt.Errorf("escrow not found")
	}
	return esc.Status == escrow.EscrowFunded, nil
}

// IdentitySetAlias registers or updates the alias associated with the provided address.
func (m *Manager) IdentitySetAlias(addr []byte, alias string) error {
	if len(addr) != 20 {
		return fmt.Errorf("identity: address must be 20 bytes")
	}
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return err
	}
	aliasKey := identityAliasKey(normalized)
	existing, err := m.trie.Get(aliasKey)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if len(existing) != 20 {
			return fmt.Errorf("identity: corrupt alias mapping")
		}
		if !bytes.Equal(existing, addr) {
			return identity.ErrAliasTaken
		}
	}
	reverseKey := identityReverseKey(addr)
	currentAliasBytes, err := m.trie.Get(reverseKey)
	if err != nil {
		return err
	}
	currentAlias := string(currentAliasBytes)
	if currentAlias != "" && currentAlias != normalized {
		if err := m.trie.Update(identityAliasKey(currentAlias), nil); err != nil {
			return err
		}
	}
	var storedAddr [20]byte
	copy(storedAddr[:], addr)
	if err := m.trie.Update(aliasKey, storedAddr[:]); err != nil {
		return err
	}
	if err := m.trie.Update(reverseKey, []byte(normalized)); err != nil {
		return err
	}
	return nil
}

// IdentityResolve resolves an alias to its owning address.
func (m *Manager) IdentityResolve(alias string) ([20]byte, bool) {
	var zero [20]byte
	normalized, err := identity.NormalizeAlias(alias)
	if err != nil {
		return zero, false
	}
	data, err := m.trie.Get(identityAliasKey(normalized))
	if err != nil || len(data) != 20 {
		return zero, false
	}
	copy(zero[:], data)
	return zero, true
}

// IdentityReverse resolves an address to its registered alias.
func (m *Manager) IdentityReverse(addr []byte) (string, bool) {
	if len(addr) != 20 {
		return "", false
	}
	data, err := m.trie.Get(identityReverseKey(addr))
	if err != nil || len(data) == 0 {
		return "", false
	}
	return string(data), true
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
