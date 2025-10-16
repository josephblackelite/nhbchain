package escrow

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/events"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
)

var (
	errNilState       = errors.New("escrow engine: state not configured")
	errNilTreasury    = errors.New("escrow engine: fee treasury not configured")
	errEscrowNotFound = errors.New("escrow engine: escrow not found")
	errRealmNotFound  = errors.New("escrow engine: realm not found")
	errRealmConfig    = errors.New("escrow engine: invalid realm configuration")
)

const moduleName = "escrow"

type engineState interface {
	EscrowPut(*Escrow) error
	EscrowGet(id [32]byte) (*Escrow, bool)
	EscrowCredit(id [32]byte, token string, amt *big.Int) error
	EscrowDebit(id [32]byte, token string, amt *big.Int) error
	EscrowBalance(id [32]byte, token string) (*big.Int, error)
	EscrowVaultAddress(token string) ([20]byte, error)
	GetAccount(addr []byte) (*types.Account, error)
	PutAccount(addr []byte, account *types.Account) error
	EscrowRealmPut(*EscrowRealm) error
	EscrowRealmGet(id string) (*EscrowRealm, bool, error)
	EscrowFrozenPolicyPut(id [32]byte, policy *FrozenArb) error
	EscrowFrozenPolicyGet(id [32]byte) (*FrozenArb, bool, error)
	ParamStoreGet(name string) ([]byte, bool, error)
}

type escrowEvent struct {
	evt *types.Event
}

func (e escrowEvent) EventType() string {
	if e.evt == nil {
		return ""
	}
	return e.evt.Type
}

func (e escrowEvent) Event() *types.Event { return e.evt }

// Engine wires the escrow business logic with external state and event
// emitters. The full transition logic will arrive in the NHBCHAIN NET-2
// milestone; for now the engine provides deterministic event emission
// helpers.
type Engine struct {
	state       engineState
	emitter     events.Emitter
	feeTreasury [20]byte
	nowFn       func() int64
	pauses      nativecommon.PauseView
}

type decisionEnvelope struct {
	EscrowID    string `json:"escrowId"`
	Outcome     string `json:"outcome"`
	Metadata    string `json:"metadata,omitempty"`
	PolicyNonce uint64 `json:"policyNonce"`
}

// NewEngine creates an escrow engine with a no-op emitter. Callers can override
// the emitter via SetEmitter.
func NewEngine() *Engine {
	return &Engine{
		emitter: events.NoopEmitter{},
		nowFn:   func() int64 { return time.Now().Unix() },
	}
}

// SetState configures the state backend used by the engine.
func (e *Engine) SetState(state engineState) { e.state = state }

// SetFeeTreasury configures the address that should receive escrow fees.
func (e *Engine) SetFeeTreasury(addr [20]byte) { e.feeTreasury = addr }

// SetPauses wires the pause view used to gate state transitions.
func (e *Engine) SetPauses(p nativecommon.PauseView) {
	if e == nil {
		return
	}
	e.pauses = p
}

// SetNowFunc overrides the time source used by the engine. Primarily intended
// for tests to provide deterministic timestamps.
func (e *Engine) SetNowFunc(now func() int64) {
	if now == nil {
		e.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	e.nowFn = now
}

// SetEmitter configures the event emitter used by the engine. Passing nil resets
// the emitter to a no-op implementation.
func (e *Engine) SetEmitter(emitter events.Emitter) {
	if emitter == nil {
		e.emitter = events.NoopEmitter{}
		return
	}
	e.emitter = emitter
}

func (e *Engine) emit(event *types.Event) {
	if e == nil || e.emitter == nil || event == nil {
		return
	}
	e.emitter.Emit(escrowEvent{evt: event})
}

func (e *Engine) now() int64 {
	if e == nil || e.nowFn == nil {
		return time.Now().Unix()
	}
	return e.nowFn()
}

func cloneBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func calculateFee(amount *big.Int, bps uint32) *big.Int {
	if amount == nil || amount.Sign() <= 0 || bps == 0 {
		return big.NewInt(0)
	}
	numerator := new(big.Int).Mul(amount, new(big.Int).SetUint64(uint64(bps)))
	numerator.Div(numerator, big.NewInt(10_000))
	return numerator
}

func ensureAccount(acc *types.Account) *types.Account {
	if acc == nil {
		return &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	}
	if acc.BalanceNHB == nil {
		acc.BalanceNHB = big.NewInt(0)
	}
	if acc.BalanceZNHB == nil {
		acc.BalanceZNHB = big.NewInt(0)
	}
	if acc.Stake == nil {
		acc.Stake = big.NewInt(0)
	}
	return acc
}

func (e *Engine) loadEscrow(id [32]byte) (*Escrow, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	escrow, ok := e.state.EscrowGet(id)
	if !ok {
		return nil, errEscrowNotFound
	}
	return escrow, nil
}

func (e *Engine) storeEscrow(esc *Escrow) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	return e.state.EscrowPut(esc)
}

func (e *Engine) transferToken(from, to [20]byte, token string, amount *big.Int) error {
	if e == nil || e.state == nil {
		return errNilState
	}
	amt := cloneBigInt(amount)
	if amt.Sign() == 0 {
		return nil
	}
	normalized, err := NormalizeToken(token)
	if err != nil {
		return err
	}
	if amt.Sign() < 0 {
		return fmt.Errorf("escrow: negative transfer amount")
	}
	fromAcc, err := e.state.GetAccount(from[:])
	if err != nil {
		return err
	}
	toAcc, err := e.state.GetAccount(to[:])
	if err != nil {
		return err
	}
	fromAcc = ensureAccount(fromAcc)
	toAcc = ensureAccount(toAcc)
	switch normalized {
	case "NHB":
		if fromAcc.BalanceNHB.Cmp(amt) < 0 {
			return fmt.Errorf("escrow: insufficient balance")
		}
		fromAcc.BalanceNHB = new(big.Int).Sub(fromAcc.BalanceNHB, amt)
		toAcc.BalanceNHB = new(big.Int).Add(toAcc.BalanceNHB, amt)
	case "ZNHB":
		if fromAcc.BalanceZNHB.Cmp(amt) < 0 {
			return fmt.Errorf("escrow: insufficient balance")
		}
		fromAcc.BalanceZNHB = new(big.Int).Sub(fromAcc.BalanceZNHB, amt)
		toAcc.BalanceZNHB = new(big.Int).Add(toAcc.BalanceZNHB, amt)
	default:
		return fmt.Errorf("escrow: unsupported token %s", token)
	}
	if err := e.state.PutAccount(from[:], fromAcc); err != nil {
		return err
	}
	if err := e.state.PutAccount(to[:], toAcc); err != nil {
		return err
	}
	return nil
}

func (e *Engine) ensureTreasuryConfigured() error {
	if e == nil {
		return errNilTreasury
	}
	if e.feeTreasury == ([20]byte{}) {
		return errNilTreasury
	}
	return nil
}

func (e *Engine) computeDisputePayouts(esc *Escrow) (*big.Int, *big.Int, *big.Int, [20]byte, error) {
	var zeroAddr [20]byte
	if esc == nil {
		return nil, nil, nil, zeroAddr, fmt.Errorf("escrow: nil escrow")
	}
	total := cloneBigInt(esc.Amount)
	if total.Sign() <= 0 {
		return nil, nil, nil, zeroAddr, fmt.Errorf("escrow: amount must be positive")
	}
	fee := calculateFee(total, esc.FeeBps)
	var realmFee *big.Int
	var recipient [20]byte
	if esc.FrozenArb != nil && esc.FrozenArb.FeeSchedule != nil {
		schedule := esc.FrozenArb.FeeSchedule
		realmFee = calculateFee(total, schedule.FeeBps)
		recipient = schedule.Recipient
		if realmFee.Sign() > 0 && recipient == ([20]byte{}) {
			return nil, nil, nil, zeroAddr, fmt.Errorf("escrow: realm fee recipient missing")
		}
	} else {
		realmFee = big.NewInt(0)
	}
	payout := new(big.Int).Sub(total, fee)
	payout.Sub(payout, realmFee)
	if payout.Sign() < 0 {
		return nil, nil, nil, zeroAddr, fmt.Errorf("escrow: dispute fees exceed escrow amount")
	}
	return payout, fee, realmFee, recipient, nil
}

func defaultRealmSchemeMap() map[ArbitrationScheme]struct{} {
	allowed := make(map[ArbitrationScheme]struct{}, len(DefaultRealmAllowedSchemes))
	for _, scheme := range DefaultRealmAllowedSchemes {
		allowed[scheme] = struct{}{}
	}
	return allowed
}

func parseUintParam(raw []byte) (uint64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return 0, fmt.Errorf("value must not be empty")
	}
	value, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func parseSchemeString(value string) (ArbitrationScheme, error) {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "single":
		return ArbitrationSchemeSingle, nil
	case "committee":
		return ArbitrationSchemeCommittee, nil
	}
	if trimmed == "" {
		return 0, fmt.Errorf("arbitration scheme must not be empty")
	}
	if num, err := strconv.ParseUint(trimmed, 10, 8); err == nil {
		scheme := ArbitrationScheme(num)
		if scheme.Valid() {
			return scheme, nil
		}
	}
	return 0, fmt.Errorf("unknown arbitration scheme %q", value)
}

func parseAllowedSchemesParam(raw []byte) (map[ArbitrationScheme]struct{}, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("allowed schemes payload empty")
	}
	allowed := make(map[ArbitrationScheme]struct{})
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		var single string
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("invalid allowed schemes payload: %w", err)
		}
		values = []string{single}
	}
	for _, entry := range values {
		scheme, err := parseSchemeString(entry)
		if err != nil {
			return nil, err
		}
		allowed[scheme] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("allowed schemes list empty")
	}
	return allowed, nil
}

func (e *Engine) realmBounds() (uint32, uint32, map[ArbitrationScheme]struct{}, error) {
	min := DefaultRealmMinThreshold
	max := DefaultRealmMaxThreshold
	allowed := defaultRealmSchemeMap()
	if e == nil || e.state == nil {
		return min, max, allowed, nil
	}
	if raw, ok, err := e.state.ParamStoreGet(ParamKeyRealmMinThreshold); err != nil {
		return 0, 0, nil, err
	} else if ok {
		parsed, err := parseUintParam(raw)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("realm min threshold: %w", err)
		}
		if parsed == 0 {
			return 0, 0, nil, fmt.Errorf("realm min threshold must be positive")
		}
		min = uint32(parsed)
	}
	if raw, ok, err := e.state.ParamStoreGet(ParamKeyRealmMaxThreshold); err != nil {
		return 0, 0, nil, err
	} else if ok {
		parsed, err := parseUintParam(raw)
		if err != nil {
			return 0, 0, nil, fmt.Errorf("realm max threshold: %w", err)
		}
		if parsed == 0 {
			return 0, 0, nil, fmt.Errorf("realm max threshold must be positive")
		}
		max = uint32(parsed)
	}
	if raw, ok, err := e.state.ParamStoreGet(ParamKeyRealmAllowedSchemes); err != nil {
		return 0, 0, nil, err
	} else if ok {
		parsed, err := parseAllowedSchemesParam(raw)
		if err != nil {
			return 0, 0, nil, err
		}
		if len(parsed) > 0 {
			allowed = parsed
		}
	}
	if min > max {
		return 0, 0, nil, fmt.Errorf("realm threshold bounds invalid: min %d > max %d", min, max)
	}
	return min, max, allowed, nil
}

func (e *Engine) validateArbitratorSetBounds(set *ArbitratorSet) (*ArbitratorSet, error) {
	sanitized, err := SanitizeArbitratorSet(set)
	if err != nil {
		return nil, err
	}
	min, max, allowed, err := e.realmBounds()
	if err != nil {
		return nil, err
	}
	if len(sanitized.Members) < int(min) {
		return nil, fmt.Errorf("escrow: arbitrator set too small for minimum threshold %d", min)
	}
	if sanitized.Threshold < min {
		return nil, fmt.Errorf("escrow: arbitrator threshold below minimum %d", min)
	}
	if sanitized.Threshold > max {
		return nil, fmt.Errorf("escrow: arbitrator threshold above maximum %d", max)
	}
	if _, ok := allowed[sanitized.Scheme]; !ok {
		return nil, fmt.Errorf("escrow: arbitration scheme %d not permitted", sanitized.Scheme)
	}
	return sanitized, nil
}

func (e *Engine) prepareFrozenPolicy(realmID string, now int64) (*EscrowRealm, *FrozenArb, error) {
	if e == nil || e.state == nil {
		return nil, nil, errNilState
	}
	trimmed := strings.TrimSpace(realmID)
	if trimmed == "" {
		return nil, nil, nil
	}
	realm, ok, err := e.state.EscrowRealmGet(trimmed)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errRealmNotFound
	}
	sanitizedRealm, err := SanitizeEscrowRealm(realm)
	if err != nil {
		return nil, nil, err
	}
	sanitizedSet, err := e.validateArbitratorSetBounds(sanitizedRealm.Arbitrators)
	if err != nil {
		return nil, nil, err
	}
	sanitizedRealm.Arbitrators = sanitizedSet
	if sanitizedRealm.NextPolicyNonce == 0 {
		return nil, nil, errRealmConfig
	}
	frozen := &FrozenArb{
		RealmID:      sanitizedRealm.ID,
		RealmVersion: sanitizedRealm.Version,
		PolicyNonce:  sanitizedRealm.NextPolicyNonce,
		Scheme:       sanitizedSet.Scheme,
		Threshold:    sanitizedSet.Threshold,
		Members:      append([][20]byte(nil), sanitizedSet.Members...),
		FrozenAt:     now,
		FeeSchedule:  sanitizedRealm.FeeSchedule.Clone(),
	}
	sanitizedRealm.NextPolicyNonce++
	sanitizedRealm.UpdatedAt = now
	return sanitizedRealm, frozen, nil
}

// CreateRealm persists a new arbitration realm using the configured governance
// bounds for validation.
func (e *Engine) CreateRealm(realm *EscrowRealm) (*EscrowRealm, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if realm == nil {
		return nil, fmt.Errorf("escrow: nil realm definition")
	}
	trimmed := strings.TrimSpace(realm.ID)
	if trimmed == "" {
		return nil, fmt.Errorf("escrow: realm id must not be empty")
	}
	if existing, ok, err := e.state.EscrowRealmGet(trimmed); err != nil {
		return nil, err
	} else if ok && existing != nil {
		return nil, fmt.Errorf("escrow: realm already exists")
	}
	sanitizedSet, err := e.validateArbitratorSetBounds(realm.Arbitrators)
	if err != nil {
		return nil, err
	}
	now := e.now()
	var schedule *RealmFeeSchedule
	if realm.FeeSchedule != nil {
		schedule = realm.FeeSchedule.Clone()
	}
	candidate := &EscrowRealm{
		ID:              trimmed,
		Version:         1,
		NextPolicyNonce: 1,
		CreatedAt:       now,
		UpdatedAt:       now,
		Arbitrators:     sanitizedSet,
		FeeSchedule:     schedule,
	}
	sanitizedRealm, err := SanitizeEscrowRealm(candidate)
	if err != nil {
		return nil, err
	}
	if err := e.state.EscrowRealmPut(sanitizedRealm); err != nil {
		return nil, err
	}
	e.emit(NewRealmCreatedEvent(sanitizedRealm))
	return sanitizedRealm.Clone(), nil
}

// UpdateRealm replaces the arbitrator policy of an existing realm, bumping the
// version while preserving the creation metadata.
func (e *Engine) UpdateRealm(realm *EscrowRealm) (*EscrowRealm, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if realm == nil {
		return nil, fmt.Errorf("escrow: nil realm definition")
	}
	trimmed := strings.TrimSpace(realm.ID)
	if trimmed == "" {
		return nil, fmt.Errorf("escrow: realm id must not be empty")
	}
	current, ok, err := e.state.EscrowRealmGet(trimmed)
	if err != nil {
		return nil, err
	}
	if !ok || current == nil {
		return nil, errRealmNotFound
	}
	sanitizedCurrent, err := SanitizeEscrowRealm(current)
	if err != nil {
		return nil, err
	}
	sanitizedSet, err := e.validateArbitratorSetBounds(realm.Arbitrators)
	if err != nil {
		return nil, err
	}
	sanitizedCurrent.Version++
	sanitizedCurrent.Arbitrators = sanitizedSet
	if realm.FeeSchedule != nil {
		sanitizedCurrent.FeeSchedule = realm.FeeSchedule.Clone()
	} else {
		sanitizedCurrent.FeeSchedule = nil
	}
	sanitizedCurrent.UpdatedAt = e.now()
	sanitizedRealm, err := SanitizeEscrowRealm(sanitizedCurrent)
	if err != nil {
		return nil, err
	}
	if err := e.state.EscrowRealmPut(sanitizedRealm); err != nil {
		return nil, err
	}
	e.emit(NewRealmUpdatedEvent(sanitizedRealm))
	return sanitizedRealm.Clone(), nil
}

// GetRealm resolves the latest definition for the provided realm identifier.
func (e *Engine) GetRealm(id string) (*EscrowRealm, bool, error) {
	if e == nil || e.state == nil {
		return nil, false, errNilState
	}
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, false, nil
	}
	realm, ok, err := e.state.EscrowRealmGet(trimmed)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	sanitized, err := SanitizeEscrowRealm(realm)
	if err != nil {
		return nil, false, err
	}
	return sanitized.Clone(), true, nil
}

// Create initialises and persists a new escrow definition. When a realm is
// provided the engine freezes the current arbitrator policy and associates it
// with the escrow.
func (e *Engine) Create(payer, payee [20]byte, token string, amount *big.Int, feeBps uint32, deadline int64, nonce uint64, mediatorOpt *[20]byte, metaHash [32]byte, realmID string) (*Escrow, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if nonce == 0 {
		return nil, fmt.Errorf("escrow: nonce must be positive")
	}
	normalizedToken, err := NormalizeToken(token)
	if err != nil {
		return nil, err
	}
	amt := cloneBigInt(amount)
	if amt.Sign() <= 0 {
		return nil, fmt.Errorf("escrow: amount must be positive")
	}
	if feeBps > 10_000 {
		return nil, fmt.Errorf("escrow: fee bps out of range")
	}
	now := e.now()
	if deadline < now {
		return nil, fmt.Errorf("escrow: deadline before creation time")
	}
	mediator := [20]byte{}
	if mediatorOpt != nil {
		mediator = *mediatorOpt
	}
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)
	id := ethcrypto.Keccak256Hash(payer[:], payee[:], metaHash[:], nonceBuf[:])
	existing, ok := e.state.EscrowGet(id)
	if ok {
		// Ensure idempotent behaviour: definitions must match
		if existing.Payer != payer || existing.Payee != payee || existing.Token != normalizedToken || existing.Amount.Cmp(amt) != 0 || existing.FeeBps != feeBps || existing.Deadline != deadline || existing.MetaHash != metaHash || existing.Mediator != mediator || existing.Nonce != nonce || strings.TrimSpace(existing.RealmID) != strings.TrimSpace(realmID) {
			return nil, fmt.Errorf("escrow: identifier already exists with different definition")
		}
		return existing, nil
	}
	trimmedRealm := strings.TrimSpace(realmID)
	var (
		realmUpdate *EscrowRealm
		frozen      *FrozenArb
	)
	if trimmedRealm != "" {
		realmUpdate, frozen, err = e.prepareFrozenPolicy(trimmedRealm, now)
		if err != nil {
			return nil, err
		}
	}
	esc := &Escrow{
		ID:        id,
		Payer:     payer,
		Payee:     payee,
		Mediator:  mediator,
		Token:     normalizedToken,
		Amount:    amt,
		FeeBps:    feeBps,
		Deadline:  deadline,
		CreatedAt: now,
		Nonce:     nonce,
		MetaHash:  metaHash,
		Status:    EscrowInit,
		RealmID:   trimmedRealm,
	}
	if frozen != nil {
		esc.FrozenArb = frozen.Clone()
	}
	if err := e.storeEscrow(esc); err != nil {
		return nil, err
	}
	if frozen != nil {
		if err := e.state.EscrowRealmPut(realmUpdate); err != nil {
			return nil, err
		}
		if err := e.state.EscrowFrozenPolicyPut(esc.ID, frozen); err != nil {
			return nil, err
		}
	}
	e.emit(NewCreatedEvent(esc))
	return esc.Clone(), nil
}

// Fund moves the escrow amount from the payer to the module vault and marks the
// escrow as funded. The operation is idempotent.
func (e *Engine) Fund(id [32]byte, from [20]byte) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowFunded {
		return nil
	}
	if esc.Status != EscrowInit {
		return fmt.Errorf("escrow: cannot fund in status %d", esc.Status)
	}
	if esc.Payer != from {
		return fmt.Errorf("escrow: unauthorized fund caller")
	}
	if esc.Amount == nil || esc.Amount.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	if err := e.transferToken(esc.Payer, vault, esc.Token, esc.Amount); err != nil {
		return err
	}
	if err := e.state.EscrowCredit(id, esc.Token, esc.Amount); err != nil {
		return err
	}
	esc.Status = EscrowFunded
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewFundedEvent(esc))
	return nil
}

// Release settles the escrow in favour of the payee, distributing any fees to
// the configured treasury. The operation is idempotent.
func (e *Engine) Release(id [32]byte, caller [20]byte) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowReleased {
		return nil
	}
	if esc.Status != EscrowFunded && esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot release in status %d", esc.Status)
	}
	if caller != esc.Payee {
		if esc.Mediator == ([20]byte{}) || caller != esc.Mediator {
			return fmt.Errorf("escrow: unauthorized release caller")
		}
	}
	if esc.Status == EscrowDisputed && caller != esc.Mediator {
		return fmt.Errorf("escrow: dispute requires mediator release")
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	total := cloneBigInt(esc.Amount)
	if total.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	var (
		payout     *big.Int
		fee        *big.Int
		realmFee   *big.Int
		realmPayee [20]byte
	)
	if esc.Status == EscrowDisputed {
		var err error
		payout, fee, realmFee, realmPayee, err = e.computeDisputePayouts(esc)
		if err != nil {
			return err
		}
	} else {
		fee = calculateFee(total, esc.FeeBps)
		payout = new(big.Int).Sub(total, fee)
		realmFee = big.NewInt(0)
	}
	if payout.Sign() > 0 {
		if err := e.transferToken(vault, esc.Payee, esc.Token, payout); err != nil {
			return err
		}
	}
	if fee.Sign() > 0 {
		if err := e.ensureTreasuryConfigured(); err != nil {
			return err
		}
		if err := e.transferToken(vault, e.feeTreasury, esc.Token, fee); err != nil {
			return err
		}
	}
	if realmFee != nil && realmFee.Sign() > 0 {
		if realmPayee == ([20]byte{}) {
			return fmt.Errorf("escrow: realm fee recipient missing")
		}
		if err := e.transferToken(vault, realmPayee, esc.Token, realmFee); err != nil {
			return err
		}
	}
	if err := e.state.EscrowDebit(id, esc.Token, total); err != nil {
		return err
	}
	esc.Status = EscrowReleased
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewReleasedEvent(esc))
	return nil
}

// Refund returns escrowed funds to the payer when invoked by the payer before
// the deadline. The operation is idempotent.
func (e *Engine) Refund(id [32]byte, caller [20]byte) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowRefunded {
		return nil
	}
	if esc.Status != EscrowFunded {
		return fmt.Errorf("escrow: cannot refund in status %d", esc.Status)
	}
	if caller != esc.Payer {
		return fmt.Errorf("escrow: unauthorized refund caller")
	}
	if e.now() >= esc.Deadline {
		return fmt.Errorf("escrow: refund deadline passed")
	}
	return e.refundEscrow(esc, esc.Payer, EscrowRefunded, NewRefundedEvent)
}

// Expire refunds the escrow to the payer once the deadline has elapsed. Anyone
// may invoke the expiry transition. The operation is idempotent.
func (e *Engine) Expire(id [32]byte, now int64) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowExpired {
		return nil
	}
	if now < esc.Deadline {
		return fmt.Errorf("escrow: deadline not reached")
	}
	if esc.Status != EscrowFunded {
		return nil
	}
	return e.refundEscrow(esc, esc.Payer, EscrowExpired, NewExpiredEvent)
}

// Dispute flags the escrow as disputed. Only the payer or payee may invoke the
// transition. The operation is idempotent.
func (e *Engine) Dispute(id [32]byte, caller [20]byte) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowDisputed {
		return nil
	}
	if esc.Status != EscrowFunded {
		return fmt.Errorf("escrow: cannot dispute in status %d", esc.Status)
	}
	if caller != esc.Payer && caller != esc.Payee {
		return fmt.Errorf("escrow: unauthorized dispute caller")
	}
	esc.Status = EscrowDisputed
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewDisputedEvent(esc))
	return nil
}

func parseDecisionPayload(id [32]byte, frozen *FrozenArb, payload []byte) (DecisionOutcome, [32]byte, [32]byte, error) {
	var zero [32]byte
	if len(payload) == 0 {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: decision payload required")
	}
	if frozen == nil {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: missing frozen arbitrator policy")
	}
	var envelope decisionEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: invalid decision payload: %w", err)
	}
	trimmedID := strings.TrimSpace(envelope.EscrowID)
	if trimmedID == "" {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: decision escrowId required")
	}
	decodedID, err := decodeFixedHex(trimmedID, len(id))
	if err != nil {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: invalid decision escrowId: %w", err)
	}
	var payloadID [32]byte
	copy(payloadID[:], decodedID)
	if payloadID != id {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: decision escrowId mismatch")
	}
	if envelope.PolicyNonce == 0 {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: decision policyNonce required")
	}
	if frozen.PolicyNonce != envelope.PolicyNonce {
		return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: decision policyNonce mismatch")
	}
	outcome, err := ParseDecisionOutcome(envelope.Outcome)
	if err != nil {
		return DecisionOutcomeUnknown, zero, zero, err
	}
	var meta [32]byte
	trimmedMeta := strings.TrimSpace(envelope.Metadata)
	if trimmedMeta != "" {
		decodedMeta, err := decodeFixedHex(trimmedMeta, len(meta))
		if err != nil {
			return DecisionOutcomeUnknown, zero, zero, fmt.Errorf("escrow: invalid decision metadata: %w", err)
		}
		copy(meta[:], decodedMeta)
	}
	hash := ethcrypto.Keccak256Hash(payload)
	var digest [32]byte
	copy(digest[:], hash[:])
	return outcome, meta, digest, nil
}

func decodeFixedHex(value string, length int) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("value must not be empty")
	}
	normalized := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return nil, err
	}
	if len(decoded) != length {
		return nil, fmt.Errorf("expected %d bytes, got %d", length, len(decoded))
	}
	return decoded, nil
}

func verifyDecisionSignatures(frozen *FrozenArb, digest [32]byte, signatures [][]byte) ([][20]byte, error) {
	if frozen == nil {
		return nil, fmt.Errorf("escrow: missing frozen arbitrator policy")
	}
	if len(signatures) == 0 {
		return nil, fmt.Errorf("escrow: signature bundle required")
	}
	allowed := make(map[[20]byte]struct{}, len(frozen.Members))
	for _, member := range frozen.Members {
		allowed[member] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("escrow: frozen policy has no members")
	}
	seen := make(map[[20]byte]struct{})
	unique := make([][20]byte, 0, len(signatures))
	for i, sig := range signatures {
		if len(sig) != 65 {
			return nil, fmt.Errorf("escrow: signature %d must be 65 bytes", i)
		}
		buf := make([]byte, len(sig))
		copy(buf, sig)
		if buf[64] >= 27 {
			buf[64] -= 27
		}
		if buf[64] != 0 && buf[64] != 1 {
			return nil, fmt.Errorf("escrow: signature %d has invalid recovery id", i)
		}
		pubKey, err := ethcrypto.SigToPub(digest[:], buf)
		if err != nil {
			return nil, fmt.Errorf("escrow: invalid signature %d: %w", i, err)
		}
		addr := ethcrypto.PubkeyToAddress(*pubKey)
		var signer [20]byte
		copy(signer[:], addr[:])
		if _, ok := allowed[signer]; !ok {
			return nil, fmt.Errorf("escrow: signature %d not from authorized arbitrator", i)
		}
		if _, dup := seen[signer]; dup {
			continue
		}
		seen[signer] = struct{}{}
		unique = append(unique, signer)
	}
	if len(unique) < int(frozen.Threshold) {
		return nil, fmt.Errorf("escrow: insufficient arbitrator quorum: have %d need %d", len(unique), frozen.Threshold)
	}
	return unique, nil
}

func (e *Engine) arbitratedRelease(esc *Escrow) error {
	if esc == nil {
		return fmt.Errorf("escrow: nil escrow")
	}
	if esc.Status == EscrowReleased {
		return nil
	}
	if esc.Status != EscrowFunded && esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot release in status %d", esc.Status)
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	total := cloneBigInt(esc.Amount)
	if total.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	var (
		payout     *big.Int
		fee        *big.Int
		realmFee   *big.Int
		realmPayee [20]byte
	)
	if esc.Status == EscrowDisputed {
		var err error
		payout, fee, realmFee, realmPayee, err = e.computeDisputePayouts(esc)
		if err != nil {
			return err
		}
	} else {
		fee = calculateFee(total, esc.FeeBps)
		payout = new(big.Int).Sub(total, fee)
		realmFee = big.NewInt(0)
	}
	if payout.Sign() > 0 {
		if err := e.transferToken(vault, esc.Payee, esc.Token, payout); err != nil {
			return err
		}
	}
	if fee.Sign() > 0 {
		if err := e.ensureTreasuryConfigured(); err != nil {
			return err
		}
		if err := e.transferToken(vault, e.feeTreasury, esc.Token, fee); err != nil {
			return err
		}
	}
	if realmFee != nil && realmFee.Sign() > 0 {
		if realmPayee == ([20]byte{}) {
			return fmt.Errorf("escrow: realm fee recipient missing")
		}
		if err := e.transferToken(vault, realmPayee, esc.Token, realmFee); err != nil {
			return err
		}
	}
	if err := e.state.EscrowDebit(esc.ID, esc.Token, total); err != nil {
		return err
	}
	esc.Status = EscrowReleased
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewReleasedEvent(esc))
	return nil
}

func (e *Engine) arbitratedRefund(esc *Escrow) error {
	if esc == nil {
		return fmt.Errorf("escrow: nil escrow")
	}
	if esc.Status == EscrowRefunded {
		return nil
	}
	if esc.Status != EscrowFunded && esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot refund in status %d", esc.Status)
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	total := cloneBigInt(esc.Amount)
	if total.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	var (
		payout     *big.Int
		fee        *big.Int
		realmFee   *big.Int
		realmPayee [20]byte
	)
	if esc.Status == EscrowDisputed {
		var computeErr error
		payout, fee, realmFee, realmPayee, computeErr = e.computeDisputePayouts(esc)
		if computeErr != nil {
			return computeErr
		}
	} else {
		payout = cloneBigInt(total)
		fee = big.NewInt(0)
		realmFee = big.NewInt(0)
	}
	if payout.Sign() > 0 {
		if err := e.transferToken(vault, esc.Payer, esc.Token, payout); err != nil {
			return err
		}
	}
	if fee != nil && fee.Sign() > 0 {
		if err := e.ensureTreasuryConfigured(); err != nil {
			return err
		}
		if err := e.transferToken(vault, e.feeTreasury, esc.Token, fee); err != nil {
			return err
		}
	}
	if realmFee != nil && realmFee.Sign() > 0 {
		if realmPayee == ([20]byte{}) {
			return fmt.Errorf("escrow: realm fee recipient missing")
		}
		if err := e.transferToken(vault, realmPayee, esc.Token, realmFee); err != nil {
			return err
		}
	}
	if err := e.state.EscrowDebit(esc.ID, esc.Token, total); err != nil {
		return err
	}
	esc.Status = EscrowRefunded
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(NewRefundedEvent(esc))
	return nil
}

// Resolve settles a disputed escrow according to the mediator-determined
// outcome. Valid outcomes are "release" and "refund".
func (e *Engine) Resolve(id [32]byte, caller [20]byte, outcome string) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowReleased || esc.Status == EscrowRefunded || esc.Status == EscrowExpired {
		return nil
	}
	if esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot resolve in status %d", esc.Status)
	}
	if esc.Mediator == ([20]byte{}) || caller != esc.Mediator {
		return fmt.Errorf("escrow: unauthorized resolver")
	}
	decision, err := ParseDecisionOutcome(outcome)
	if err != nil {
		return err
	}
	switch decision {
	case DecisionOutcomeRelease:
		if err := e.Release(id, caller); err != nil {
			return err
		}
	case DecisionOutcomeRefund:
		if err := e.arbitratedRefund(esc); err != nil {
			return err
		}
	default:
		return fmt.Errorf("escrow: invalid resolution outcome %s", outcome)
	}
	esc, err = e.loadEscrow(id)
	if err != nil {
		return err
	}
	e.emit(NewResolvedEvent(esc, decision, [32]byte{}, nil))
	return nil
}

// ResolveWithSignatures settles a disputed escrow after verifying a quorum of
// arbitrator signatures over the supplied decision payload.
func (e *Engine) ResolveWithSignatures(id [32]byte, decisionPayload []byte, signatures [][]byte) error {
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return err
	}
	esc, err := e.loadEscrow(id)
	if err != nil {
		return err
	}
	if esc.Status == EscrowReleased || esc.Status == EscrowRefunded || esc.Status == EscrowExpired {
		return nil
	}
	if esc.Status != EscrowDisputed {
		return fmt.Errorf("escrow: cannot resolve in status %d", esc.Status)
	}
	outcome, metaHash, digest, err := parseDecisionPayload(id, esc.FrozenArb, decisionPayload)
	if err != nil {
		return err
	}
	if esc.ResolutionHash == digest {
		return nil
	}
	if esc.ResolutionHash != ([32]byte{}) && esc.ResolutionHash != digest {
		return fmt.Errorf("escrow: conflicting decision payload")
	}
	signers, err := verifyDecisionSignatures(esc.FrozenArb, digest, signatures)
	if err != nil {
		return err
	}
	prevHash := esc.ResolutionHash
	esc.ResolutionHash = digest
	switch outcome {
	case DecisionOutcomeRelease:
		if err := e.arbitratedRelease(esc); err != nil {
			esc.ResolutionHash = prevHash
			return err
		}
	case DecisionOutcomeRefund:
		if err := e.arbitratedRefund(esc); err != nil {
			esc.ResolutionHash = prevHash
			return err
		}
	default:
		esc.ResolutionHash = prevHash
		return fmt.Errorf("escrow: unsupported decision outcome")
	}
	e.emit(NewResolvedEvent(esc, outcome, metaHash, signers))
	return nil
}

func (e *Engine) refundEscrow(esc *Escrow, recipient [20]byte, status EscrowStatus, eventFn func(*Escrow) *types.Event) error {
	if esc == nil {
		return fmt.Errorf("escrow: nil escrow")
	}
	if esc.Status == status {
		return nil
	}
	vault, err := e.state.EscrowVaultAddress(esc.Token)
	if err != nil {
		return err
	}
	amount := cloneBigInt(esc.Amount)
	if amount.Sign() <= 0 {
		return fmt.Errorf("escrow: amount must be positive")
	}
	if err := e.transferToken(vault, recipient, esc.Token, amount); err != nil {
		return err
	}
	if err := e.state.EscrowDebit(esc.ID, esc.Token, amount); err != nil {
		return err
	}
	esc.Status = status
	if err := e.storeEscrow(esc); err != nil {
		return err
	}
	e.emit(eventFn(esc))
	return nil
}
